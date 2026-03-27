package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// VirtualAccountProvisionerStore abstracts the store operations for provisioning.
type VirtualAccountProvisionerStore interface {
	CountAvailableVirtualAccountsByCurrency(ctx context.Context, tenantID uuid.UUID) (map[string]int64, error)
}

// TenantIterator iterates over active tenants in batches.
// Satisfied by *cache.TenantIndex.ForEach or domain.ForEachTenantBatch.
type TenantIterator func(ctx context.Context, batchSize int32, fn func(ids []uuid.UUID) error) error

// VirtualAccountProvisioner periodically checks the virtual account pool levels
// and provisions new accounts when below the low watermark.
type VirtualAccountProvisioner struct {
	store         VirtualAccountProvisionerStore
	partnerReg    BankingPartnerRegistry
	tenantForEach TenantIterator
	logger        *slog.Logger
	lowWatermark  int
	pollInterval  time.Duration
}

// NewVirtualAccountProvisioner creates a provisioner with default settings.
func NewVirtualAccountProvisioner(
	store VirtualAccountProvisionerStore,
	partnerReg BankingPartnerRegistry,
	tenantForEach TenantIterator,
	logger *slog.Logger,
) *VirtualAccountProvisioner {
	return &VirtualAccountProvisioner{
		store:         store,
		partnerReg:    partnerReg,
		tenantForEach: tenantForEach,
		logger:        logger.With("module", "virtual-account-provisioner"),
		lowWatermark:  10,
		pollInterval:  60 * time.Second,
	}
}

// Run starts the provisioner loop. Blocks until ctx is cancelled.
func (p *VirtualAccountProvisioner) Run(ctx context.Context) {
	p.logger.Info("settla-provisioner: starting virtual account provisioner",
		"low_watermark", p.lowWatermark,
		"poll_interval", p.pollInterval,
	)

	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.logger.Info("settla-provisioner: stopping")
			return
		case <-ticker.C:
			p.poll(ctx)
		}
	}
}

func (p *VirtualAccountProvisioner) poll(ctx context.Context) {
	if p.partnerReg == nil {
		return // no banking partners configured
	}

	err := p.tenantForEach(ctx, 500, func(ids []uuid.UUID) error {
		for _, tenantID := range ids {
			availableByCurrency, err := p.store.CountAvailableVirtualAccountsByCurrency(ctx, tenantID)
			if err != nil {
				p.logger.Error("settla-provisioner: failed to count available accounts",
					"tenant_id", tenantID, "error", err)
				continue
			}

			for currency, avail := range availableByCurrency {
				if avail >= int64(p.lowWatermark) {
					continue
				}

				needed := int64(p.lowWatermark) - avail
				p.logger.Info("settla-provisioner: pool below watermark",
					"tenant_id", tenantID,
					"currency", currency,
					"available", avail,
					"needed", needed,
				)

				// Provision via banking partner and insert into pool once tenant+currency mapping is configured.
			}
		}
		return nil
	})
	if err != nil {
		p.logger.Error("settla-provisioner: tenant iteration failed", "error", err)
	}
}
