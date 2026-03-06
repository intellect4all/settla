package core

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/observability"
)

// providerTimeout is the maximum time allowed for provider calls (on-ramp, off-ramp).
const providerTimeout = 30 * time.Second

// Engine is the top-level settlement orchestrator. It coordinates the transfer
// lifecycle across the ledger, treasury, rail, and provider modules.
//
// Engine depends only on domain interfaces and core-local port interfaces,
// not concrete implementations. In a single-binary deployment these are local
// structs; if a module is extracted to its own service, the corresponding
// field becomes a gRPC client that satisfies the same interface.
type Engine struct {
	transferStore TransferStore
	tenantStore   TenantStore
	ledger        domain.Ledger
	treasury      domain.TreasuryManager
	router        Router
	providers     ProviderRegistry
	publisher     domain.EventPublisher
	logger        *slog.Logger
	metrics       *observability.Metrics
}

// NewEngine creates a settlement engine wired to the given dependencies.
func NewEngine(
	transferStore TransferStore,
	tenantStore TenantStore,
	ledger domain.Ledger,
	treasury domain.TreasuryManager,
	router Router,
	providers ProviderRegistry,
	publisher domain.EventPublisher,
	logger *slog.Logger,
	metrics *observability.Metrics,
) *Engine {
	return &Engine{
		transferStore: transferStore,
		tenantStore:   tenantStore,
		ledger:        ledger,
		treasury:      treasury,
		router:        router,
		providers:     providers,
		publisher:     publisher,
		logger:        logger.With("module", "core.engine"),
		metrics:       metrics,
	}
}

// CreateTransfer validates a settlement request, checks tenant limits, enforces
// idempotency, and persists the initial transfer record.
func (e *Engine) CreateTransfer(ctx context.Context, tenantID uuid.UUID, req CreateTransferRequest) (*domain.Transfer, error) {
	// a. Load tenant, verify active
	tenant, err := e.tenantStore.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("settla-core: create transfer: loading tenant %s: %w", tenantID, err)
	}
	if !tenant.IsActive() {
		return nil, domain.ErrTenantSuspended(tenantID.String())
	}

	// b. Validate source amount > 0, currencies supported
	if !req.SourceAmount.IsPositive() {
		return nil, domain.ErrAmountTooLow(req.SourceAmount.String(), "0")
	}
	if err := domain.ValidateCurrency(req.SourceCurrency); err != nil {
		return nil, fmt.Errorf("settla-core: create transfer: %w", err)
	}
	if err := domain.ValidateCurrency(req.DestCurrency); err != nil {
		return nil, fmt.Errorf("settla-core: create transfer: %w", err)
	}
	if req.Recipient.Name == "" || req.Recipient.Country == "" {
		return nil, fmt.Errorf("settla-core: create transfer: recipient name and country are required")
	}

	// c. Check per-transfer limit
	if !tenant.PerTransferLimit.IsZero() && req.SourceAmount.GreaterThan(tenant.PerTransferLimit) {
		return nil, domain.ErrAmountTooHigh(req.SourceAmount.String(), tenant.PerTransferLimit.String())
	}

	// d. Check daily volume limit
	if !tenant.DailyLimitUSD.IsZero() {
		today := time.Now().UTC().Truncate(24 * time.Hour)
		dailyVolume, err := e.transferStore.GetDailyVolume(ctx, tenantID, today)
		if err != nil {
			return nil, fmt.Errorf("settla-core: create transfer: checking daily volume: %w", err)
		}
		if dailyVolume.Add(req.SourceAmount).GreaterThan(tenant.DailyLimitUSD) {
			return nil, domain.ErrDailyLimitExceeded(tenantID.String())
		}
	}

	// e. Check idempotency key
	if req.IdempotencyKey != "" {
		existing, err := e.transferStore.GetTransferByIdempotencyKey(ctx, tenantID, req.IdempotencyKey)
		if err == nil && existing != nil {
			return existing, nil
		}
	}

	// f. Fetch and validate quote
	var quote *domain.Quote
	if req.QuoteID != nil {
		quote, err = e.transferStore.GetQuote(ctx, tenantID, *req.QuoteID)
		if err != nil {
			return nil, fmt.Errorf("settla-core: create transfer: fetching quote %s: %w", req.QuoteID, err)
		}
		if quote.TenantID != tenantID {
			return nil, fmt.Errorf("settla-core: create transfer: quote %s belongs to different tenant", req.QuoteID)
		}
		if quote.IsExpired() {
			return nil, domain.ErrQuoteExpired(req.QuoteID.String())
		}
	} else {
		// Get a fresh quote from the router
		quote, err = e.router.GetQuote(ctx, tenantID, domain.QuoteRequest{
			SourceCurrency: req.SourceCurrency,
			SourceAmount:   req.SourceAmount,
			DestCurrency:   req.DestCurrency,
			DestCountry:    req.Recipient.Country,
		})
		if err != nil {
			return nil, fmt.Errorf("settla-core: create transfer: getting quote: %w", err)
		}
	}

	// g. Create transfer record
	now := time.Now().UTC()
	transfer := &domain.Transfer{
		ID:             uuid.New(),
		TenantID:       tenantID,
		ExternalRef:    req.ExternalRef,
		IdempotencyKey: req.IdempotencyKey,
		Status:         domain.TransferStatusCreated,
		Version:        1,
		SourceCurrency: req.SourceCurrency,
		SourceAmount:   req.SourceAmount,
		DestCurrency:   req.DestCurrency,
		DestAmount:     quote.DestAmount,
		StableCoin:     quote.Route.StableCoin,
		StableAmount:   quote.StableAmount, // intermediate stablecoin amount from router
		Chain:          quote.Route.Chain,
		FXRate:            quote.FXRate,
		Fees:              quote.Fees,
		OnRampProviderID:  quote.Route.OnRampProvider,
		OffRampProviderID: quote.Route.OffRampProvider,
		Sender:            req.Sender,
		Recipient:      req.Recipient,
		QuoteID:        req.QuoteID,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := e.transferStore.CreateTransfer(ctx, transfer); err != nil {
		return nil, fmt.Errorf("settla-core: create transfer: persisting: %w", err)
	}

	corridor := observability.FormatCorridor(string(req.SourceCurrency), string(req.DestCurrency))
	if e.metrics != nil {
		e.metrics.TransfersTotal.WithLabelValues(tenantID.String(), string(domain.TransferStatusCreated), corridor).Inc()
	}

	e.logger.Info("settla-core: transfer created",
		"transfer_id", transfer.ID,
		"tenant_id", tenantID,
		"corridor", corridor,
		"source_amount", req.SourceAmount.String(),
	)

	// h. Create initial transfer event
	event := &domain.TransferEvent{
		ID:         uuid.New(),
		TransferID: transfer.ID,
		TenantID:   tenantID,
		FromStatus: "",
		ToStatus:   domain.TransferStatusCreated,
		OccurredAt: now,
	}
	if err := e.transferStore.CreateTransferEvent(ctx, event); err != nil {
		return nil, fmt.Errorf("settla-core: create transfer: persisting event: %w", err)
	}

	// i. Publish EventTransferCreated
	if err := e.publisher.Publish(ctx, domain.Event{
		ID:        uuid.New(),
		TenantID:  tenantID,
		Type:      domain.EventTransferCreated,
		Timestamp: now,
		Data:      transfer,
	}); err != nil {
		e.logger.Error("settla-core: failed to publish transfer.created", "transfer_id", transfer.ID, "error", err)
	}

	return transfer, nil
}

// GetQuote generates an FX rate quote for a settlement corridor.
func (e *Engine) GetQuote(ctx context.Context, tenantID uuid.UUID, req domain.QuoteRequest) (*domain.Quote, error) {
	tenant, err := e.tenantStore.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("settla-core: get quote: loading tenant %s: %w", tenantID, err)
	}
	if !tenant.IsActive() {
		return nil, domain.ErrTenantSuspended(tenantID.String())
	}

	quote, err := e.router.GetQuote(ctx, tenantID, req)
	if err != nil {
		return nil, fmt.Errorf("settla-core: get quote: %w", err)
	}

	// Persist quote so it can be referenced by transfer creation
	if err := e.transferStore.CreateQuote(ctx, quote); err != nil {
		return nil, fmt.Errorf("settla-core: get quote: persisting: %w", err)
	}

	return quote, nil
}

// GetQuoteByID retrieves a previously created quote by ID.
func (e *Engine) GetQuoteByID(ctx context.Context, tenantID uuid.UUID, quoteID uuid.UUID) (*domain.Quote, error) {
	quote, err := e.transferStore.GetQuote(ctx, tenantID, quoteID)
	if err != nil {
		return nil, fmt.Errorf("settla-core: get quote %s: %w", quoteID, err)
	}
	return quote, nil
}

// GetTransfer retrieves a transfer by tenant and ID.
func (e *Engine) GetTransfer(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID) (*domain.Transfer, error) {
	transfer, err := e.transferStore.GetTransfer(ctx, tenantID, transferID)
	if err != nil {
		return nil, fmt.Errorf("settla-core: get transfer %s: %w", transferID, err)
	}
	return transfer, nil
}

// ListTransfers returns transfers for a tenant with pagination.
func (e *Engine) ListTransfers(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]domain.Transfer, error) {
	transfers, err := e.transferStore.ListTransfers(ctx, tenantID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("settla-core: list transfers for tenant %s: %w", tenantID, err)
	}
	return transfers, nil
}

// ProcessTransfer runs the full settlement pipeline synchronously.
// Used for testing/demo; in production, each step is event-driven.
func (e *Engine) ProcessTransfer(ctx context.Context, transferID uuid.UUID) error {
	steps := []func(context.Context, uuid.UUID) error{
		e.FundTransfer,
		e.InitiateOnRamp,
		e.SettleOnChain,
		e.InitiateOffRamp,
		e.CompleteTransfer,
	}
	for _, step := range steps {
		if err := step(ctx, transferID); err != nil {
			return err
		}
	}
	return nil
}

// FundTransfer reserves treasury funds and posts the initial ledger entries.
func (e *Engine) FundTransfer(ctx context.Context, transferID uuid.UUID) error {
	transfer, err := e.loadTransferForStep(ctx, transferID, domain.TransferStatusCreated)
	if err != nil {
		return fmt.Errorf("settla-core: fund transfer %s: %w", transferID, err)
	}

	tenant, err := e.tenantStore.GetTenant(ctx, transfer.TenantID)
	if err != nil {
		return fmt.Errorf("settla-core: fund transfer %s: loading tenant: %w", transferID, err)
	}

	// Reserve funds in treasury (in-memory, nanosecond latency)
	location := fmt.Sprintf("bank:%s", strings.ToLower(string(transfer.SourceCurrency)))
	if err := e.treasury.Reserve(ctx, transfer.TenantID, transfer.SourceCurrency, location, transfer.SourceAmount, transfer.ID); err != nil {
		return fmt.Errorf("settla-core: fund transfer %s: reserving treasury: %w", transferID, err)
	}

	// Post ledger entries
	slug := tenant.Slug
	entry := domain.JournalEntry{
		ID:             uuid.New(),
		TenantID:       &transfer.TenantID,
		IdempotencyKey: fmt.Sprintf("fund:%s", transfer.ID),
		PostedAt:       time.Now().UTC(),
		EffectiveDate:  time.Now().UTC(),
		Description:    fmt.Sprintf("Fund transfer %s", transfer.ID),
		ReferenceType:  "transfer",
		ReferenceID:    &transfer.ID,
		Lines: []domain.EntryLine{
			{
				ID:          uuid.New(),
				AccountCode: domain.TenantAccountCode(slug, fmt.Sprintf("assets:bank:%s:clearing", strings.ToLower(string(transfer.SourceCurrency)))),
				EntryType:   domain.EntryTypeDebit,
				Amount:      transfer.SourceAmount,
				Currency:    transfer.SourceCurrency,
				Description: "Debit clearing account",
			},
			{
				ID:          uuid.New(),
				AccountCode: domain.TenantAccountCode(slug, "liabilities:customer:pending"),
				EntryType:   domain.EntryTypeCredit,
				Amount:      transfer.SourceAmount,
				Currency:    transfer.SourceCurrency,
				Description: "Credit customer pending",
			},
		},
	}
	if _, err := e.ledger.PostEntries(ctx, entry); err != nil {
		return fmt.Errorf("settla-core: fund transfer %s: posting ledger entries: %w", transferID, err)
	}

	// Transition to FUNDED
	if err := e.transitionAndPersist(ctx, transfer, domain.TransferStatusFunded); err != nil {
		return fmt.Errorf("settla-core: fund transfer %s: %w", transferID, err)
	}

	now := time.Now().UTC()
	transfer.FundedAt = &now

	// Publish event
	e.publishEvent(ctx, transfer.TenantID, domain.EventTransferFunded, transfer)

	return nil
}

// InitiateOnRamp converts fiat to stablecoin via the on-ramp provider.
func (e *Engine) InitiateOnRamp(ctx context.Context, transferID uuid.UUID) error {
	transfer, err := e.loadTransferForStep(ctx, transferID, domain.TransferStatusFunded)
	if err != nil {
		return fmt.Errorf("settla-core: on-ramp transfer %s: %w", transferID, err)
	}

	tenant, err := e.tenantStore.GetTenant(ctx, transfer.TenantID)
	if err != nil {
		return fmt.Errorf("settla-core: on-ramp transfer %s: loading tenant: %w", transferID, err)
	}

	// Get on-ramp provider from route stored on transfer
	onRamp := e.providers.GetOnRampProvider(transfer.OnRampProviderID)
	if onRamp == nil {
		return fmt.Errorf("settla-core: on-ramp transfer %s: no on-ramp provider available", transferID)
	}

	// Execute with timeout
	provCtx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()

	provTx, err := onRamp.Execute(provCtx, domain.OnRampRequest{
		Amount:       transfer.SourceAmount,
		FromCurrency: transfer.SourceCurrency,
		ToCurrency:   transfer.StableCoin,
		Reference:    transfer.ID.String(),
	})
	if err != nil {
		return fmt.Errorf("settla-core: on-ramp transfer %s: provider execute: %w", transferID, err)
	}

	// Calculate on-ramp fee
	onRampFee := tenant.FeeSchedule.CalculateFee(transfer.SourceAmount, "onramp")

	// Post ledger entries
	slug := tenant.Slug
	entry := domain.JournalEntry{
		ID:             uuid.New(),
		TenantID:       &transfer.TenantID,
		IdempotencyKey: fmt.Sprintf("onramp:%s", transfer.ID),
		PostedAt:       time.Now().UTC(),
		EffectiveDate:  time.Now().UTC(),
		Description:    fmt.Sprintf("On-ramp transfer %s via %s", transfer.ID, onRamp.ID()),
		ReferenceType:  "transfer",
		ReferenceID:    &transfer.ID,
		Lines: []domain.EntryLine{
			{
				ID:          uuid.New(),
				AccountCode: fmt.Sprintf("assets:crypto:%s:%s", strings.ToLower(string(transfer.StableCoin)), strings.ToLower(transfer.Chain)),
				EntryType:   domain.EntryTypeDebit,
				Amount:      transfer.SourceAmount.Sub(onRampFee),
				Currency:    transfer.SourceCurrency,
				Description: "Debit crypto asset",
			},
			{
				ID:          uuid.New(),
				AccountCode: "expenses:provider:onramp",
				EntryType:   domain.EntryTypeDebit,
				Amount:      onRampFee,
				Currency:    transfer.SourceCurrency,
				Description: "Debit on-ramp fee",
			},
			{
				ID:          uuid.New(),
				AccountCode: domain.TenantAccountCode(slug, fmt.Sprintf("assets:bank:%s:clearing", strings.ToLower(string(transfer.SourceCurrency)))),
				EntryType:   domain.EntryTypeCredit,
				Amount:      transfer.SourceAmount,
				Currency:    transfer.SourceCurrency,
				Description: "Credit clearing account",
			},
		},
	}
	if _, err := e.ledger.PostEntries(ctx, entry); err != nil {
		return fmt.Errorf("settla-core: on-ramp transfer %s: posting ledger entries: %w", transferID, err)
	}

	// Transition ON_RAMPING → SETTLING
	if err := e.transitionAndPersist(ctx, transfer, domain.TransferStatusOnRamping); err != nil {
		return fmt.Errorf("settla-core: on-ramp transfer %s: %w", transferID, err)
	}
	if err := e.transitionAndPersist(ctx, transfer, domain.TransferStatusSettling); err != nil {
		return fmt.Errorf("settla-core: on-ramp transfer %s: %w", transferID, err)
	}

	// Publish events
	e.publishEvent(ctx, transfer.TenantID, domain.EventOnRampInitiated, map[string]any{
		"transfer_id": transfer.ID,
		"provider_id": onRamp.ID(),
		"provider_tx": provTx.ID,
	})
	e.publishEvent(ctx, transfer.TenantID, domain.EventOnRampCompleted, map[string]any{
		"transfer_id": transfer.ID,
		"provider_id": onRamp.ID(),
		"provider_tx": provTx.ID,
	})

	return nil
}

// SettleOnChain sends the stablecoin transaction on the blockchain.
func (e *Engine) SettleOnChain(ctx context.Context, transferID uuid.UUID) error {
	transfer, err := e.loadTransferForStep(ctx, transferID, domain.TransferStatusSettling)
	if err != nil {
		return fmt.Errorf("settla-core: settle on-chain %s: %w", transferID, err)
	}

	// Get blockchain client
	chain := e.providers.GetBlockchainClient(transfer.Chain)
	if chain == nil {
		return fmt.Errorf("settla-core: settle on-chain %s: no blockchain client for chain %s", transferID, transfer.Chain)
	}

	// Estimate gas
	txReq := domain.TxRequest{
		Token:  string(transfer.StableCoin),
		Amount: transfer.StableAmount,
	}
	gasFee, err := chain.EstimateGas(ctx, txReq)
	if err != nil {
		return fmt.Errorf("settla-core: settle on-chain %s: estimating gas: %w", transferID, err)
	}

	// Send transaction
	chainTx, err := chain.SendTransaction(ctx, txReq)
	if err != nil {
		return fmt.Errorf("settla-core: settle on-chain %s: sending transaction: %w", transferID, err)
	}

	// Post ledger entries
	entry := domain.JournalEntry{
		ID:             uuid.New(),
		TenantID:       &transfer.TenantID,
		IdempotencyKey: fmt.Sprintf("settle:%s", transfer.ID),
		PostedAt:       time.Now().UTC(),
		EffectiveDate:  time.Now().UTC(),
		Description:    fmt.Sprintf("On-chain settlement %s tx %s", transfer.ID, chainTx.Hash),
		ReferenceType:  "transfer",
		ReferenceID:    &transfer.ID,
		Lines: []domain.EntryLine{
			{
				ID:          uuid.New(),
				AccountCode: "assets:settlement:in_transit",
				EntryType:   domain.EntryTypeDebit,
				Amount:      transfer.StableAmount,
				Currency:    transfer.StableCoin,
				Description: "Debit settlement in transit",
			},
			{
				ID:          uuid.New(),
				AccountCode: fmt.Sprintf("expenses:network:%s:gas", strings.ToLower(transfer.Chain)),
				EntryType:   domain.EntryTypeDebit,
				Amount:      gasFee,
				Currency:    transfer.StableCoin,
				Description: "Debit gas fee",
			},
			{
				ID:          uuid.New(),
				AccountCode: fmt.Sprintf("assets:crypto:%s:%s", strings.ToLower(string(transfer.StableCoin)), strings.ToLower(transfer.Chain)),
				EntryType:   domain.EntryTypeCredit,
				Amount:      transfer.StableAmount.Add(gasFee),
				Currency:    transfer.StableCoin,
				Description: "Credit crypto asset",
			},
		},
	}
	if _, err := e.ledger.PostEntries(ctx, entry); err != nil {
		return fmt.Errorf("settla-core: settle on-chain %s: posting ledger entries: %w", transferID, err)
	}

	// Publish settlement completed
	e.publishEvent(ctx, transfer.TenantID, domain.EventSettlementCompleted, map[string]any{
		"transfer_id": transfer.ID,
		"chain":       transfer.Chain,
		"tx_hash":     chainTx.Hash,
		"gas_fee":     gasFee.String(),
	})

	return nil
}

// InitiateOffRamp converts stablecoin to destination fiat via the off-ramp provider.
func (e *Engine) InitiateOffRamp(ctx context.Context, transferID uuid.UUID) error {
	transfer, err := e.loadTransferForStep(ctx, transferID, domain.TransferStatusSettling)
	if err != nil {
		return fmt.Errorf("settla-core: off-ramp transfer %s: %w", transferID, err)
	}

	tenant, err := e.tenantStore.GetTenant(ctx, transfer.TenantID)
	if err != nil {
		return fmt.Errorf("settla-core: off-ramp transfer %s: loading tenant: %w", transferID, err)
	}

	// Get off-ramp provider from route stored on transfer
	offRamp := e.providers.GetOffRampProvider(transfer.OffRampProviderID)
	if offRamp == nil {
		return fmt.Errorf("settla-core: off-ramp transfer %s: no off-ramp provider available", transferID)
	}

	// Execute with timeout
	provCtx, cancel := context.WithTimeout(ctx, providerTimeout)
	defer cancel()

	_, err = offRamp.Execute(provCtx, domain.OffRampRequest{
		Amount:       transfer.StableAmount,
		FromCurrency: transfer.StableCoin,
		ToCurrency:   transfer.DestCurrency,
		Recipient:    transfer.Recipient,
		Reference:    transfer.ID.String(),
	})
	if err != nil {
		return fmt.Errorf("settla-core: off-ramp transfer %s: provider execute: %w", transferID, err)
	}

	// Calculate off-ramp fee
	offRampFee := tenant.FeeSchedule.CalculateFee(transfer.DestAmount, "offramp")

	// Post ledger entries
	slug := tenant.Slug
	entry := domain.JournalEntry{
		ID:             uuid.New(),
		TenantID:       &transfer.TenantID,
		IdempotencyKey: fmt.Sprintf("offramp:%s", transfer.ID),
		PostedAt:       time.Now().UTC(),
		EffectiveDate:  time.Now().UTC(),
		Description:    fmt.Sprintf("Off-ramp transfer %s via %s", transfer.ID, offRamp.ID()),
		ReferenceType:  "transfer",
		ReferenceID:    &transfer.ID,
		Lines: []domain.EntryLine{
			{
				ID:          uuid.New(),
				AccountCode: domain.TenantAccountCode(slug, "liabilities:payable:recipient"),
				EntryType:   domain.EntryTypeDebit,
				Amount:      transfer.DestAmount,
				Currency:    transfer.StableCoin,
				Description: "Debit recipient payable",
			},
			{
				ID:          uuid.New(),
				AccountCode: "expenses:provider:offramp",
				EntryType:   domain.EntryTypeDebit,
				Amount:      offRampFee,
				Currency:    transfer.StableCoin,
				Description: "Debit off-ramp fee",
			},
			{
				ID:          uuid.New(),
				AccountCode: "assets:settlement:in_transit",
				EntryType:   domain.EntryTypeCredit,
				Amount:      transfer.DestAmount.Add(offRampFee),
				Currency:    transfer.StableCoin,
				Description: "Credit settlement in transit",
			},
		},
	}
	if _, err := e.ledger.PostEntries(ctx, entry); err != nil {
		return fmt.Errorf("settla-core: off-ramp transfer %s: posting ledger entries: %w", transferID, err)
	}

	// Transition to OFF_RAMPING
	if err := e.transitionAndPersist(ctx, transfer, domain.TransferStatusOffRamping); err != nil {
		return fmt.Errorf("settla-core: off-ramp transfer %s: %w", transferID, err)
	}

	e.publishEvent(ctx, transfer.TenantID, domain.EventOffRampInitiated, map[string]any{
		"transfer_id": transfer.ID,
		"provider_id": offRamp.ID(),
	})

	// Mock/synchronous providers complete immediately — publish completion event.
	// In production with async providers, this event comes from a webhook callback.
	e.publishEvent(ctx, transfer.TenantID, domain.EventOffRampCompleted, map[string]any{
		"transfer_id": transfer.ID,
		"provider_id": offRamp.ID(),
	})

	return nil
}

// CompleteTransfer finalises the settlement: posts closing ledger entries,
// releases treasury reservation, and transitions to COMPLETED.
func (e *Engine) CompleteTransfer(ctx context.Context, transferID uuid.UUID) error {
	transfer, err := e.loadTransfer(ctx, transferID)
	if err != nil {
		return fmt.Errorf("settla-core: complete transfer %s: %w", transferID, err)
	}

	if transfer.Status != domain.TransferStatusOffRamping && transfer.Status != domain.TransferStatusCompleting {
		return fmt.Errorf("settla-core: complete transfer %s: %w",
			transferID, domain.ErrInvalidTransition(string(transfer.Status), string(domain.TransferStatusCompleted)))
	}

	tenant, err := e.tenantStore.GetTenant(ctx, transfer.TenantID)
	if err != nil {
		return fmt.Errorf("settla-core: complete transfer %s: loading tenant: %w", transferID, err)
	}

	// If OFF_RAMPING, transition to COMPLETING first
	if transfer.Status == domain.TransferStatusOffRamping {
		if err := e.transitionAndPersist(ctx, transfer, domain.TransferStatusCompleting); err != nil {
			return fmt.Errorf("settla-core: complete transfer %s: %w", transferID, err)
		}
	}

	// Post closing ledger entries (all in source currency, balanced)
	slug := tenant.Slug
	totalFees := transfer.Fees.TotalFeeUSD
	netAmount := transfer.SourceAmount.Sub(totalFees)
	entry := domain.JournalEntry{
		ID:             uuid.New(),
		TenantID:       &transfer.TenantID,
		IdempotencyKey: fmt.Sprintf("complete:%s", transfer.ID),
		PostedAt:       time.Now().UTC(),
		EffectiveDate:  time.Now().UTC(),
		Description:    fmt.Sprintf("Complete transfer %s", transfer.ID),
		ReferenceType:  "transfer",
		ReferenceID:    &transfer.ID,
		Lines: []domain.EntryLine{
			{
				ID:          uuid.New(),
				AccountCode: domain.TenantAccountCode(slug, "liabilities:customer:pending"),
				EntryType:   domain.EntryTypeDebit,
				Amount:      transfer.SourceAmount,
				Currency:    transfer.SourceCurrency,
				Description: "Debit customer pending",
			},
			{
				ID:          uuid.New(),
				AccountCode: domain.TenantAccountCode(slug, "liabilities:payable:recipient"),
				EntryType:   domain.EntryTypeCredit,
				Amount:      netAmount,
				Currency:    transfer.SourceCurrency,
				Description: "Credit recipient payable (net of fees)",
			},
			{
				ID:          uuid.New(),
				AccountCode: domain.TenantAccountCode(slug, "revenue:fees:settlement"),
				EntryType:   domain.EntryTypeCredit,
				Amount:      totalFees,
				Currency:    transfer.SourceCurrency,
				Description: "Credit settlement fee revenue",
			},
		},
	}
	if _, err := e.ledger.PostEntries(ctx, entry); err != nil {
		return fmt.Errorf("settla-core: complete transfer %s: posting ledger entries: %w", transferID, err)
	}

	// Release treasury reservation
	location := fmt.Sprintf("bank:%s", strings.ToLower(string(transfer.SourceCurrency)))
	if err := e.treasury.Release(ctx, transfer.TenantID, transfer.SourceCurrency, location, transfer.SourceAmount, transfer.ID); err != nil {
		e.logger.Error("settla-core: failed to release treasury reservation",
			"transfer_id", transfer.ID, "error", err)
	}

	// Transition to COMPLETED
	if err := e.transitionAndPersist(ctx, transfer, domain.TransferStatusCompleted); err != nil {
		return fmt.Errorf("settla-core: complete transfer %s: %w", transferID, err)
	}

	now := time.Now().UTC()
	transfer.CompletedAt = &now

	// Record completion metrics.
	corridor := observability.FormatCorridor(string(transfer.SourceCurrency), string(transfer.DestCurrency))
	if e.metrics != nil {
		e.metrics.TransfersTotal.WithLabelValues(transfer.TenantID.String(), string(domain.TransferStatusCompleted), corridor).Inc()
		duration := now.Sub(transfer.CreatedAt).Seconds()
		e.metrics.TransferDuration.WithLabelValues(transfer.TenantID.String(), corridor, transfer.Chain).Observe(duration)
	}

	e.logger.Info("settla-core: transfer completed",
		"transfer_id", transfer.ID,
		"tenant_id", transfer.TenantID,
		"corridor", corridor,
		"duration_s", now.Sub(transfer.CreatedAt).Seconds(),
	)

	e.publishEvent(ctx, transfer.TenantID, domain.EventTransferCompleted, transfer)

	return nil
}

// FailTransfer transitions a transfer to FAILED with a reason and code.
func (e *Engine) FailTransfer(ctx context.Context, transferID uuid.UUID, reason string, code string) error {
	transfer, err := e.loadTransfer(ctx, transferID)
	if err != nil {
		return fmt.Errorf("settla-core: fail transfer %s: %w", transferID, err)
	}

	if !transfer.CanTransitionTo(domain.TransferStatusFailed) {
		return fmt.Errorf("settla-core: fail transfer %s: %w",
			transferID, domain.ErrInvalidTransition(string(transfer.Status), string(domain.TransferStatusFailed)))
	}

	transfer.FailureReason = reason
	transfer.FailureCode = code
	now := time.Now().UTC()
	transfer.FailedAt = &now

	if err := e.transitionAndPersist(ctx, transfer, domain.TransferStatusFailed); err != nil {
		return fmt.Errorf("settla-core: fail transfer %s: %w", transferID, err)
	}

	corridor := observability.FormatCorridor(string(transfer.SourceCurrency), string(transfer.DestCurrency))
	if e.metrics != nil {
		e.metrics.TransfersTotal.WithLabelValues(transfer.TenantID.String(), string(domain.TransferStatusFailed), corridor).Inc()
	}

	e.publishEvent(ctx, transfer.TenantID, domain.EventTransferFailed, map[string]any{
		"transfer_id": transfer.ID,
		"reason":      reason,
		"code":        code,
	})

	return nil
}

// InitiateRefund reverses ledger entries, releases treasury, and transitions
// through REFUNDING → REFUNDED.
func (e *Engine) InitiateRefund(ctx context.Context, transferID uuid.UUID) error {
	transfer, err := e.loadTransfer(ctx, transferID)
	if err != nil {
		return fmt.Errorf("settla-core: refund transfer %s: %w", transferID, err)
	}

	if !transfer.CanTransitionTo(domain.TransferStatusRefunding) {
		return fmt.Errorf("settla-core: refund transfer %s: %w",
			transferID, domain.ErrInvalidTransition(string(transfer.Status), string(domain.TransferStatusRefunding)))
	}

	// Transition to REFUNDING
	if err := e.transitionAndPersist(ctx, transfer, domain.TransferStatusRefunding); err != nil {
		return fmt.Errorf("settla-core: refund transfer %s: %w", transferID, err)
	}

	// Reverse ledger entries for this transfer
	events, err := e.transferStore.GetTransferEvents(ctx, transfer.TenantID, transfer.ID)
	if err != nil {
		e.logger.Error("settla-core: failed to get transfer events for reversal",
			"transfer_id", transfer.ID, "error", err)
	}
	_ = events // Events are informational; we reverse by reference ID

	// Reverse all journal entries referencing this transfer
	// The ledger.ReverseEntry handles creating mirror entries
	_, err = e.ledger.ReverseEntry(ctx, transfer.ID, fmt.Sprintf("Refund for transfer %s", transfer.ID))
	if err != nil {
		e.logger.Warn("settla-core: ledger reversal may have partially failed",
			"transfer_id", transfer.ID, "error", err)
	}

	// Release treasury reservation if still held
	location := fmt.Sprintf("bank:%s", strings.ToLower(string(transfer.SourceCurrency)))
	if err := e.treasury.Release(ctx, transfer.TenantID, transfer.SourceCurrency, location, transfer.SourceAmount, transfer.ID); err != nil {
		e.logger.Warn("settla-core: treasury release during refund failed (may already be released)",
			"transfer_id", transfer.ID, "error", err)
	}

	// Transition to REFUNDED
	if err := e.transitionAndPersist(ctx, transfer, domain.TransferStatusRefunded); err != nil {
		return fmt.Errorf("settla-core: refund transfer %s: %w", transferID, err)
	}

	e.publishEvent(ctx, transfer.TenantID, domain.EventRefundCompleted, map[string]any{
		"transfer_id": transfer.ID,
	})

	return nil
}

// loadTransfer fetches a transfer by ID across all tenants by iterating.
// In practice the caller should provide the tenantID, but for the step-based
// pipeline we look up the transfer by the ID stored in the event.
func (e *Engine) loadTransfer(ctx context.Context, transferID uuid.UUID) (*domain.Transfer, error) {
	// The transfer store requires tenant_id for isolation. In the step pipeline,
	// we use uuid.Nil as a sentinel that the store implementation resolves internally.
	transfer, err := e.transferStore.GetTransfer(ctx, uuid.Nil, transferID)
	if err != nil {
		return nil, fmt.Errorf("loading transfer %s: %w", transferID, err)
	}
	return transfer, nil
}

// loadTransferForStep loads a transfer and verifies it's in the expected status.
func (e *Engine) loadTransferForStep(ctx context.Context, transferID uuid.UUID, expectedStatus domain.TransferStatus) (*domain.Transfer, error) {
	transfer, err := e.loadTransfer(ctx, transferID)
	if err != nil {
		return nil, err
	}
	if transfer.Status != expectedStatus {
		return nil, domain.ErrInvalidTransition(string(transfer.Status), "next")
	}
	return transfer, nil
}

// transitionAndPersist applies a state transition and persists both the transfer and event.
func (e *Engine) transitionAndPersist(ctx context.Context, transfer *domain.Transfer, target domain.TransferStatus) error {
	fromStatus := transfer.Status
	event, err := transfer.TransitionTo(target)
	if err != nil {
		return err
	}

	if err := e.transferStore.UpdateTransfer(ctx, transfer); err != nil {
		return fmt.Errorf("persisting transition to %s: %w", target, err)
	}

	if err := e.transferStore.CreateTransferEvent(ctx, event); err != nil {
		e.logger.Error("settla-core: failed to persist transfer event",
			"transfer_id", transfer.ID, "to_status", target, "error", err)
	}

	e.logger.Info("settla-core: state transition",
		"transfer_id", transfer.ID,
		"tenant_id", transfer.TenantID,
		"from_status", fromStatus,
		"to_status", target,
	)

	return nil
}

// publishEvent is a non-blocking event publish helper. Errors are logged but
// do not fail the operation — events are eventually consistent via NATS replay.
func (e *Engine) publishEvent(ctx context.Context, tenantID uuid.UUID, eventType string, data any) {
	if err := e.publisher.Publish(ctx, domain.Event{
		ID:        uuid.New(),
		TenantID:  tenantID,
		Type:      eventType,
		Timestamp: time.Now().UTC(),
		Data:      data,
	}); err != nil {
		e.logger.Error("settla-core: failed to publish event",
			"event_type", eventType, "tenant_id", tenantID, "error", err)
	}
}
