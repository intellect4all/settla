package messaging

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// Stream name constants for all Settla JetStream streams.
const (
	StreamTransfers        = "SETTLA_TRANSFERS"
	StreamProviders        = "SETTLA_PROVIDERS"
	StreamLedger           = "SETTLA_LEDGER"
	StreamTreasury         = "SETTLA_TREASURY"
	StreamBlockchain       = "SETTLA_BLOCKCHAIN"
	StreamWebhooks         = "SETTLA_WEBHOOKS"
	StreamProviderWebhooks = "SETTLA_PROVIDER_WEBHOOKS"
	StreamCryptoDeposits   = "SETTLA_CRYPTO_DEPOSITS"
	StreamEmails           = "SETTLA_EMAILS"
	StreamBankDeposits     = "SETTLA_BANK_DEPOSITS"
	StreamNameDLQ          = "SETTLA_DLQ"
)

// Stream settings shared across all streams.
const (
	// StreamMaxAge is the maximum age of messages in all streams.
	StreamMaxAge = 7 * 24 * time.Hour // 168 hours

	// StreamMaxMsgSize is the maximum message size (1 MB).
	StreamMaxMsgSize = 1_048_576

	// StreamDuplicateWindow is the deduplication window for message IDs.
	// 5 minutes covers relay crash-recovery scenarios where unpublished entries
	// are re-polled and re-published after restart.
	StreamDuplicateWindow = 5 * time.Minute
)

// StreamDefinition holds the configuration for a single stream.
type StreamDefinition struct {
	Name     string
	Subjects []string
}

// AllStreams returns the definitions for all Settla streams including the DLQ.
//
// Subject layout avoids overlap between SETTLA_PROVIDERS and SETTLA_PROVIDER_WEBHOOKS
// by using distinct subject prefixes:
//   - SETTLA_PROVIDERS:         settla.provider.command.>  (outbound: onramp, offramp, quote, etc.)
//   - SETTLA_PROVIDER_WEBHOOKS: settla.provider.inbound.>  (inbound webhook notifications from providers)
func AllStreams() []StreamDefinition {
	return []StreamDefinition{
		{
			Name:     StreamTransfers,
			Subjects: []string{"settla.transfer.partition.*.>"},
		},
		{
			Name:     StreamProviders,
			Subjects: []string{"settla.provider.command.partition.*.>"},
		},
		{
			Name:     StreamLedger,
			Subjects: []string{"settla.ledger.partition.*.>"},
		},
		{
			Name:     StreamTreasury,
			Subjects: []string{"settla.treasury.>"},
		},
		{
			Name:     StreamBlockchain,
			Subjects: []string{"settla.blockchain.partition.*.>"},
		},
		{
			Name:     StreamWebhooks,
			Subjects: []string{"settla.webhook.partition.*.>"},
		},
		{
			Name:     StreamProviderWebhooks,
			Subjects: []string{"settla.provider.inbound.partition.*.>"},
		},
		{
			Name:     StreamCryptoDeposits,
			Subjects: []string{"settla.deposit.partition.*.>"},
		},
		{
			Name:     StreamEmails,
			Subjects: []string{"settla.email.partition.*.>"},
		},
		{
			Name:     StreamBankDeposits,
			Subjects: []string{"settla.bank_deposit.partition.*.>", "settla.inbound.bank.>"},
		},
		{
			// WorkQueue retention — dead letter queue for unprocessable messages.
			// Keep for 7 days to allow investigation and reprocessing.
			Name:     StreamNameDLQ,
			Subjects: []string{"settla.dlq.>"},
		},
	}
}

// CreateStreams creates or updates all Settla JetStream streams idempotently.
// Set replicas to 1 for dev, 3 for production (requires a multi-node NATS cluster).
func CreateStreams(ctx context.Context, js jetstream.JetStream, replicas int) error {
	if replicas < 1 {
		replicas = 1
	}

	for _, def := range AllStreams() {
		cfg := jetstream.StreamConfig{
			Name:       def.Name,
			Subjects:   def.Subjects,
			Retention:  jetstream.WorkQueuePolicy,
			Storage:    jetstream.FileStorage,
			MaxAge:     StreamMaxAge,
			MaxMsgSize: StreamMaxMsgSize,
			Duplicates: StreamDuplicateWindow,
			Discard:    jetstream.DiscardOld,
			Replicas:   replicas,
		}

		if def.Name == StreamNameDLQ {
			// LimitsPolicy ensures DLQ messages persist for 30 days even
			// when no consumer is attached. InterestPolicy would drop
			// messages immediately if the DLQ monitor is down — unacceptable
			// for a payment system's dead letter queue.
			cfg.Retention = jetstream.LimitsPolicy
			cfg.MaxAge = 30 * 24 * time.Hour
		}

		if _, err := js.CreateOrUpdateStream(ctx, cfg); err != nil {
			return fmt.Errorf("settla-messaging: ensuring stream %s: %w", def.Name, err)
		}
	}

	return nil
}

// DLQSubject builds the dead letter queue subject for a failed message.
// Format: settla.dlq.{streamName}.{eventType}
func DLQSubject(streamName string, eventType string) string {
	return fmt.Sprintf("settla.dlq.%s.%s", streamName, eventType)
}

// StreamForSubject returns the stream name that owns a given subject prefix.
// This is useful for looking up which stream to subscribe to.
func StreamForSubject(subject string) string {
	// Match by prefix in priority order (more specific first).
	switch {
	case matchPrefix(subject, "settla.transfer.partition."):
		return StreamTransfers
	case matchPrefix(subject, "settla.provider.inbound.partition."):
		return StreamProviderWebhooks
	case matchPrefix(subject, "settla.provider.command.partition."):
		return StreamProviders
	case matchPrefix(subject, "settla.ledger.partition."):
		return StreamLedger
	case matchPrefix(subject, "settla.treasury."):
		return StreamTreasury
	case matchPrefix(subject, "settla.blockchain.partition."):
		return StreamBlockchain
	case matchPrefix(subject, "settla.webhook.partition."):
		return StreamWebhooks
	case matchPrefix(subject, "settla.deposit.partition."):
		return StreamCryptoDeposits
	case matchPrefix(subject, "settla.email.partition."):
		return StreamEmails
	case matchPrefix(subject, "settla.bank_deposit.partition."):
		return StreamBankDeposits
	case matchPrefix(subject, "settla.inbound.bank."):
		return StreamBankDeposits
	case matchPrefix(subject, "settla.dlq."):
		return StreamNameDLQ
	default:
		return ""
	}
}

func matchPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
