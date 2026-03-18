package chainmonitor

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/intellect4all/settla/domain"
)

// AddressStore abstracts address retrieval for syncing the watched address set.
type AddressStore interface {
	// ListActiveAddresses returns all addresses with active (non-terminal) sessions.
	ListActiveAddresses(ctx context.Context, chain string) ([]AddressInfo, error)
}

// TokenStore abstracts token retrieval for reloading the token registry.
type TokenStore interface {
	// ListTokensByChain returns all active tokens for a chain.
	ListTokensByChain(ctx context.Context, chain string) ([]domain.Token, error)
}

// ChainPoller is the interface that chain-specific pollers implement.
type ChainPoller interface {
	// Poll executes one poll cycle for the chain.
	Poll(ctx context.Context) error
	// PollInterval returns the configured interval between polls.
	PollInterval() time.Duration
	// Chain returns the chain identifier.
	Chain() string
}

// Monitor is the top-level orchestrator for chain monitoring.
// It manages per-chain pollers, address sync, and token registry reloads.
type Monitor struct {
	cfg          MonitorConfig
	pollers      []ChainPoller
	addresses    *AddressSet
	tokens       *TokenRegistry
	addrStore    AddressStore
	tokenStore   TokenStore
	logger       *slog.Logger
	cancelFunc   context.CancelFunc
	wg           sync.WaitGroup
}

// NewMonitor creates the chain monitor orchestrator.
func NewMonitor(
	cfg MonitorConfig,
	pollers []ChainPoller,
	addresses *AddressSet,
	tokens *TokenRegistry,
	addrStore AddressStore,
	tokenStore TokenStore,
	logger *slog.Logger,
) *Monitor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Monitor{
		cfg:        cfg,
		pollers:    pollers,
		addresses:  addresses,
		tokens:     tokens,
		addrStore:  addrStore,
		tokenStore: tokenStore,
		logger:     logger.With("module", "chain-monitor"),
	}
}

// RegisterPoller adds a chain poller to the monitor.
// Must be called before Start.
func (m *Monitor) RegisterPoller(poller ChainPoller) {
	m.pollers = append(m.pollers, poller)
}

// Start begins all pollers and background sync goroutines.
// It blocks until ctx is cancelled.
func (m *Monitor) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	m.cancelFunc = cancel
	defer cancel()
	defer m.wg.Wait()

	// Initial sync before starting pollers
	if err := m.syncTokens(ctx); err != nil {
		m.logger.Warn("settla-chainmonitor: initial token sync failed", "error", err)
	}
	if err := m.syncAddresses(ctx); err != nil {
		m.logger.Warn("settla-chainmonitor: initial address sync failed", "error", err)
	}

	// Start per-chain pollers
	for _, poller := range m.pollers {
		p := poller
		m.wg.Add(1)
		go func() {
			defer m.wg.Done()
			m.runPoller(ctx, p)
		}()
	}

	// Start address sync goroutine (incremental)
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.runAddressSync(ctx)
	}()

	// Start token reload goroutine
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.runTokenReload(ctx)
	}()

	m.logger.Info("settla-chainmonitor: started",
		"pollers", len(m.pollers),
		"addresses", m.addresses.Len(),
	)

	// Block until context is cancelled
	<-ctx.Done()
	return nil
}

// Stop cancels all goroutines and waits for them to finish.
func (m *Monitor) Stop() {
	if m.cancelFunc != nil {
		m.cancelFunc()
	}
	m.wg.Wait()
	m.logger.Info("settla-chainmonitor: stopped")
}

// runPoller runs a single chain poller on its configured interval.
func (m *Monitor) runPoller(ctx context.Context, p ChainPoller) {
	ticker := time.NewTicker(p.PollInterval())
	defer ticker.Stop()

	chain := p.Chain()
	m.logger.Info("settla-chainmonitor: poller started",
		"chain", chain,
		"interval", p.PollInterval(),
	)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := p.Poll(ctx); err != nil {
				m.logger.Warn("settla-chainmonitor: poll cycle failed",
					"chain", chain,
					"error", err,
				)
			}
		}
	}
}

// runAddressSync periodically syncs the address set from the database.
func (m *Monitor) runAddressSync(ctx context.Context) {
	ticker := time.NewTicker(m.cfg.AddressSyncInterval)
	defer ticker.Stop()

	reconcileTicker := time.NewTicker(m.cfg.FullReconcileInterval)
	defer reconcileTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.syncAddresses(ctx); err != nil {
				m.logger.Warn("settla-chainmonitor: address sync failed", "error", err)
			}
		case <-reconcileTicker.C:
			if err := m.reconcileAddresses(ctx); err != nil {
				m.logger.Warn("settla-chainmonitor: full address reconciliation failed", "error", err)
			}
		}
	}
}

// runTokenReload periodically reloads the token registry from the database.
func (m *Monitor) runTokenReload(ctx context.Context) {
	ticker := time.NewTicker(m.cfg.TokenReloadInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.syncTokens(ctx); err != nil {
				m.logger.Warn("settla-chainmonitor: token reload failed", "error", err)
			}
		}
	}
}

// syncAddresses does an incremental address sync — loads active addresses
// from the DB and adds any new ones to the set.
func (m *Monitor) syncAddresses(ctx context.Context) error {
	for _, p := range m.pollers {
		addresses, err := m.addrStore.ListActiveAddresses(ctx, p.Chain())
		if err != nil {
			return fmt.Errorf("listing addresses for %s: %w", p.Chain(), err)
		}
		for _, addr := range addresses {
			m.addresses.Add(addr)
		}
	}
	m.addresses.Publish()
	return nil
}

// reconcileAddresses does a full reconciliation — replaces the entire address
// set with the current DB state.
func (m *Monitor) reconcileAddresses(ctx context.Context) error {
	var allAddrs []AddressInfo
	for _, p := range m.pollers {
		addresses, err := m.addrStore.ListActiveAddresses(ctx, p.Chain())
		if err != nil {
			return fmt.Errorf("listing addresses for %s: %w", p.Chain(), err)
		}
		allAddrs = append(allAddrs, addresses...)
	}
	m.addresses.Replace(allAddrs)
	m.logger.Info("settla-chainmonitor: full address reconciliation complete",
		"total_addresses", len(allAddrs),
	)
	return nil
}

// syncTokens reloads all active tokens from the database.
func (m *Monitor) syncTokens(ctx context.Context) error {
	var allTokens []domain.Token
	for _, p := range m.pollers {
		tokens, err := m.tokenStore.ListTokensByChain(ctx, p.Chain())
		if err != nil {
			return fmt.Errorf("listing tokens for %s: %w", p.Chain(), err)
		}
		allTokens = append(allTokens, tokens...)
	}
	m.tokens.Reload(allTokens)
	return nil
}
