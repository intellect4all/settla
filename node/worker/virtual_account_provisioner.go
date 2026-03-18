package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/intellect4all/settla/domain"
)

// VirtualAccountProvisionerStore abstracts the store operations for provisioning.
type VirtualAccountProvisionerStore interface {
	ListVirtualAccountsByTenant(ctx context.Context, tenantID uuid.UUID) ([]domain.VirtualAccountPool, error)
}

// VirtualAccountProvisioner periodically checks the virtual account pool levels
// and provisions new accounts when below the low watermark.
type VirtualAccountProvisioner struct {
	store        VirtualAccountProvisionerStore
	partnerReg   BankingPartnerRegistry
	tenantLister func(ctx context.Context) ([]uuid.UUID, error)
	logger       *slog.Logger
	lowWatermark int
	pollInterval time.Duration
}

// NewVirtualAccountProvisioner creates a provisioner with default settings.
func NewVirtualAccountProvisioner(
	store VirtualAccountProvisionerStore,
	partnerReg BankingPartnerRegistry,
	tenantLister func(ctx context.Context) ([]uuid.UUID, error),
	logger *slog.Logger,
) *VirtualAccountProvisioner {
	return &VirtualAccountProvisioner{
		store:        store,
		partnerReg:   partnerReg,
		tenantLister: tenantLister,
		logger:       logger.With("module", "virtual-account-provisioner"),
		lowWatermark: 10,
		pollInterval: 60 * time.Second,
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

	tenants, err := p.tenantLister(ctx)
	if err != nil {
		p.logger.Error("settla-provisioner: failed to list tenants", "error", err)
		return
	}

	for _, tenantID := range tenants {
		accounts, err := p.store.ListVirtualAccountsByTenant(ctx, tenantID)
		if err != nil {
			p.logger.Error("settla-provisioner: failed to list accounts",
				"tenant_id", tenantID, "error", err)
			continue
		}

		// Count available accounts by currency
		availableByCurrency := make(map[string]int)
		currencies := make(map[string]bool)
		for _, a := range accounts {
			currencies[string(a.Currency)] = true
			if a.Available {
				availableByCurrency[string(a.Currency)]++
			}
		}

		for currency := range currencies {
			avail := availableByCurrency[currency]
			if avail >= p.lowWatermark {
				continue
			}

			needed := p.lowWatermark - avail
			p.logger.Info("settla-provisioner: pool below watermark",
				"tenant_id", tenantID,
				"currency", currency,
				"available", avail,
				"needed", needed,
			)

			// Provision via banking partner and insert into pool once tenant+currency mapping is configured.
		}
	}
}
