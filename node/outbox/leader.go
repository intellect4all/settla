package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	// leaderKVBucket is the NATS KV bucket used for leader election.
	leaderKVBucket = "settla-outbox-leader"
	// leaderKey is the key within the bucket that holds the current leader ID.
	leaderKey = "leader"
	// leaderTTL is how long a leader lease lasts before it must be renewed.
	leaderTTL = 10 * time.Second
	// leaderRenewInterval is how often the leader renews its lease.
	leaderRenewInterval = 3 * time.Second
	// leaderAcquireInterval is how often non-leaders try to acquire the lease.
	leaderAcquireInterval = 5 * time.Second
)

var relayActiveGauge = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "settla_outbox_relay_active",
	Help: "1 if this instance is the active outbox relay leader, 0 otherwise.",
})

// LeaderElector manages leader election for outbox relay redundancy using NATS KV.
// Only the leader instance actively polls the outbox; standby instances wait.
type LeaderElector struct {
	js       jetstream.JetStream
	kv       jetstream.KeyValue
	identity string // unique identity of this instance (e.g., pod name)
	logger   *slog.Logger
	isLeader atomic.Bool
}

// NewLeaderElector creates a leader elector. Identity should be unique per instance
// (e.g., hostname, pod name).
func NewLeaderElector(js jetstream.JetStream, logger *slog.Logger) *LeaderElector {
	identity, _ := os.Hostname()
	if identity == "" {
		identity = fmt.Sprintf("relay-%d", time.Now().UnixNano())
	}
	return &LeaderElector{
		js:       js,
		identity: identity,
		logger:   logger.With("component", "outbox-leader", "identity", identity),
	}
}

// IsLeader returns true if this instance currently holds the leader lease.
func (le *LeaderElector) IsLeader() bool {
	return le.isLeader.Load()
}

// Run starts the leader election loop. It blocks until ctx is cancelled.
// The caller should check IsLeader() to determine if this instance should poll.
func (le *LeaderElector) Run(ctx context.Context) error {
	// Create or get the KV bucket.
	kv, err := le.js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: leaderKVBucket,
		TTL:    leaderTTL,
	})
	if err != nil {
		return fmt.Errorf("settla-outbox-leader: creating KV bucket: %w", err)
	}
	le.kv = kv

	le.logger.Info("settla-outbox-leader: election loop started")

	for {
		select {
		case <-ctx.Done():
			le.isLeader.Store(false)
			relayActiveGauge.Set(0)
			return ctx.Err()
		default:
		}

		if le.isLeader.Load() {
			// Renew the lease.
			if err := le.renew(ctx); err != nil {
				le.logger.Warn("settla-outbox-leader: lost leadership", "error", err)
				le.isLeader.Store(false)
				relayActiveGauge.Set(0)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(leaderRenewInterval):
			}
		} else {
			// Try to acquire the lease.
			if err := le.acquire(ctx); err == nil {
				le.logger.Info("settla-outbox-leader: acquired leadership")
				le.isLeader.Store(true)
				relayActiveGauge.Set(1)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(leaderAcquireInterval):
			}
		}
	}
}

// acquire attempts to create the leader key. Succeeds only if the key doesn't
// exist (or has expired via TTL).
func (le *LeaderElector) acquire(ctx context.Context) error {
	_, err := le.kv.Create(ctx, leaderKey, []byte(le.identity))
	return err
}

// renew updates the leader key to reset the TTL. Fails if another instance took over.
func (le *LeaderElector) renew(ctx context.Context) error {
	entry, err := le.kv.Get(ctx, leaderKey)
	if err != nil {
		return fmt.Errorf("getting leader key: %w", err)
	}
	if string(entry.Value()) != le.identity {
		return fmt.Errorf("leadership taken by %s", string(entry.Value()))
	}
	_, err = le.kv.Update(ctx, leaderKey, []byte(le.identity), entry.Revision())
	return err
}
