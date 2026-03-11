package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/observability"
)

// Engine is the top-level settlement orchestrator. It coordinates the transfer
// lifecycle as a pure state machine: every method validates state, computes the
// next state plus outbox entries, and persists both atomically in a single
// database transaction. The engine makes ZERO network calls and has ZERO
// dependencies on ledger, treasury, rail, or node modules.
//
// Side effects (treasury reserve, ledger post, provider calls, blockchain sends,
// webhook delivery) are expressed as outbox intents. A dedicated relay/worker
// picks up intents from the outbox table and executes them, then calls back
// into the engine's Handle*Result methods with the outcome.
type Engine struct {
	transferStore TransferStore
	tenantStore   TenantStore
	router        Router // used ONLY for quote generation, NOT for provider execution
	logger        *slog.Logger
	metrics       *observability.Metrics
}

// NewEngine creates a settlement engine wired to the given dependencies.
// The router is used only for generating quotes in CreateTransfer and GetQuote;
// it does not execute any provider calls.
func NewEngine(
	transferStore TransferStore,
	tenantStore TenantStore,
	router Router,
	logger *slog.Logger,
	metrics *observability.Metrics,
) *Engine {
	return &Engine{
		transferStore: transferStore,
		tenantStore:   tenantStore,
		router:        router,
		logger:        logger.With("module", "core.engine"),
		metrics:       metrics,
	}
}

// CreateTransfer validates a settlement request, checks tenant limits, enforces
// idempotency, and persists the initial transfer record with an outbox event
// atomically in a single database transaction.
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
		// Persist inline quote so alternatives are retrievable during on-ramp/off-ramp
		if err := e.transferStore.CreateQuote(ctx, quote); err != nil {
			return nil, fmt.Errorf("settla-core: create transfer: persisting inline quote: %w", err)
		}
		req.QuoteID = &quote.ID
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
		StableAmount:   quote.StableAmount,
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

	// h. Build outbox event for transfer.created
	payload, err := json.Marshal(transfer)
	if err != nil {
		return nil, fmt.Errorf("settla-core: create transfer: marshalling event payload: %w", err)
	}
	entries := []domain.OutboxEntry{
		domain.NewOutboxEvent("transfer", transfer.ID, tenantID, domain.EventTransferCreated, payload),
	}

	// i. Persist transfer + outbox atomically
	if err := e.transferStore.CreateTransferWithOutbox(ctx, transfer, entries); err != nil {
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

// FundTransfer transitions CREATED → FUNDED and writes outbox intents for
// treasury reservation and a funded event. The treasury worker will pick up the
// IntentTreasuryReserve intent and actually reserve funds.
func (e *Engine) FundTransfer(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID) error {
	transfer, err := e.loadTransferForStep(ctx, tenantID, transferID, domain.TransferStatusCreated)
	if err != nil {
		return fmt.Errorf("settla-core: fund transfer %s: %w", transferID, err)
	}

	location := fmt.Sprintf("bank:%s", strings.ToLower(string(transfer.SourceCurrency)))

	reservePayload, err := json.Marshal(domain.TreasuryReservePayload{
		TransferID: transfer.ID,
		TenantID:   transfer.TenantID,
		Currency:   transfer.SourceCurrency,
		Amount:     transfer.SourceAmount,
		Location:   location,
	})
	if err != nil {
		return fmt.Errorf("settla-core: fund transfer %s: marshalling reserve payload: %w", transferID, err)
	}

	entries := []domain.OutboxEntry{
		domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentTreasuryReserve, reservePayload),
		domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID, domain.EventTransferFunded, transferEventPayload(transfer.ID, transfer.TenantID)),
	}

	if err := e.transferStore.TransitionWithOutbox(ctx, transfer.ID, domain.TransferStatusFunded, transfer.Version, entries); err != nil {
		return wrapTransitionError(err, "fund transfer", transferID)
	}

	e.logger.Info("settla-core: transfer funded",
		"transfer_id", transfer.ID,
		"tenant_id", transfer.TenantID,
	)

	return nil
}

// InitiateOnRamp transitions FUNDED → ON_RAMPING and writes an outbox intent
// for the on-ramp provider to convert fiat to stablecoin.
func (e *Engine) InitiateOnRamp(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID) error {
	transfer, err := e.loadTransferForStep(ctx, tenantID, transferID, domain.TransferStatusFunded)
	if err != nil {
		return fmt.Errorf("settla-core: on-ramp transfer %s: %w", transferID, err)
	}

	// Load quote to get fallback alternatives
	var alternatives []domain.OnRampFallback
	if transfer.QuoteID != nil {
		quote, qErr := e.transferStore.GetQuote(ctx, transfer.TenantID, *transfer.QuoteID)
		if qErr == nil && quote != nil {
			for _, alt := range quote.Route.AlternativeRoutes {
				alternatives = append(alternatives, domain.OnRampFallback{
					ProviderID:      alt.OnRampProvider,
					OffRampProvider: alt.OffRampProvider,
					Chain:           alt.Chain,
					StableCoin:      alt.StableCoin,
					Fee:             alt.Fee,
					Rate:            alt.Rate,
					StableAmount:    alt.StableAmount,
				})
			}
		}
	}

	onRampPayload, err := json.Marshal(domain.ProviderOnRampPayload{
		TransferID:   transfer.ID,
		TenantID:     transfer.TenantID,
		ProviderID:   transfer.OnRampProviderID,
		Amount:       transfer.SourceAmount,
		FromCurrency: transfer.SourceCurrency,
		ToCurrency:   transfer.StableCoin,
		Reference:    transfer.ID.String(),
		Alternatives: alternatives,
		QuotedRate:   transfer.FXRate,
	})
	if err != nil {
		return fmt.Errorf("settla-core: on-ramp transfer %s: marshalling on-ramp payload: %w", transferID, err)
	}

	entries := []domain.OutboxEntry{
		domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentProviderOnRamp, onRampPayload),
	}

	if err := e.transferStore.TransitionWithOutbox(ctx, transfer.ID, domain.TransferStatusOnRamping, transfer.Version, entries); err != nil {
		return wrapTransitionError(err, "on-ramp transfer", transferID)
	}

	e.logger.Info("settla-core: on-ramp initiated",
		"transfer_id", transfer.ID,
		"tenant_id", transfer.TenantID,
		"provider_id", transfer.OnRampProviderID,
	)

	return nil
}

// HandleOnRampResult processes the result of an on-ramp provider execution.
// On success: transitions ON_RAMPING → SETTLING with intents for ledger post
// and blockchain send.
// On failure: transitions ON_RAMPING → REFUNDING with intent for treasury release.
func (e *Engine) HandleOnRampResult(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID, result domain.IntentResult) error {
	transfer, err := e.loadTransferForStep(ctx, tenantID, transferID, domain.TransferStatusOnRamping)
	if err != nil {
		return fmt.Errorf("settla-core: handle on-ramp result %s: %w", transferID, err)
	}

	if result.Success {
		// Build ledger post intent for on-ramp accounting
		tenant, err := e.tenantStore.GetTenant(ctx, transfer.TenantID)
		if err != nil {
			return fmt.Errorf("settla-core: handle on-ramp result %s: loading tenant: %w", transferID, err)
		}
		slug := tenant.Slug
		onRampFee, err := tenant.FeeSchedule.CalculateFee(transfer.SourceAmount, "onramp")
		if err != nil {
			return fmt.Errorf("settla-core: fee calculation for transfer %s: %w", transferID, err)
		}

		onRampLines := []domain.LedgerLineEntry{
			{
				AccountCode: fmt.Sprintf("assets:crypto:%s:%s", strings.ToLower(string(transfer.StableCoin)), strings.ToLower(transfer.Chain)),
				EntryType:   string(domain.EntryTypeDebit),
				Amount:      transfer.SourceAmount.Sub(onRampFee),
				Currency:    string(transfer.SourceCurrency),
				Description: "Debit crypto asset",
			},
			{
				AccountCode: "expenses:provider:onramp",
				EntryType:   string(domain.EntryTypeDebit),
				Amount:      onRampFee,
				Currency:    string(transfer.SourceCurrency),
				Description: "Debit on-ramp fee",
			},
			{
				AccountCode: domain.TenantAccountCode(slug, fmt.Sprintf("assets:bank:%s:clearing", strings.ToLower(string(transfer.SourceCurrency)))),
				EntryType:   string(domain.EntryTypeCredit),
				Amount:      transfer.SourceAmount,
				Currency:    string(transfer.SourceCurrency),
				Description: "Credit clearing account",
			},
		}

		if err := validateLedgerLines(onRampLines); err != nil {
			return fmt.Errorf("settla-core: handle on-ramp result %s: ledger entries imbalanced: %w", transferID, err)
		}

		ledgerPayload, err := json.Marshal(domain.LedgerPostPayload{
			TransferID:     transfer.ID,
			TenantID:       transfer.TenantID,
			IdempotencyKey: fmt.Sprintf("onramp:%s", transfer.ID),
			Description:    fmt.Sprintf("On-ramp transfer %s", transfer.ID),
			ReferenceType:  "transfer",
			Lines:          onRampLines,
		})
		if err != nil {
			return fmt.Errorf("settla-core: handle on-ramp result %s: marshalling ledger payload: %w", transferID, err)
		}

		// Build blockchain send intent
		blockchainPayload, err := json.Marshal(domain.BlockchainSendPayload{
			TransferID: transfer.ID,
			TenantID:   transfer.TenantID,
			Chain:      transfer.Chain,
			Token:      string(transfer.StableCoin),
			Amount:     transfer.StableAmount,
			Memo:       fmt.Sprintf("settlement:%s", transfer.ID),
		})
		if err != nil {
			return fmt.Errorf("settla-core: handle on-ramp result %s: marshalling blockchain payload: %w", transferID, err)
		}

		entries := []domain.OutboxEntry{
			domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentLedgerPost, ledgerPayload),
			domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentBlockchainSend, blockchainPayload),
			domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID, domain.EventOnRampCompleted, transferEventPayload(transfer.ID, transfer.TenantID)),
		}

		if err := e.transferStore.TransitionWithOutbox(ctx, transfer.ID, domain.TransferStatusSettling, transfer.Version, entries); err != nil {
			return wrapTransitionError(err, "handle on-ramp result", transferID)
		}

		e.logger.Info("settla-core: on-ramp completed, settling",
			"transfer_id", transfer.ID,
			"tenant_id", transfer.TenantID,
		)
	} else {
		// On-ramp failed — release treasury and transition to refunding
		location := fmt.Sprintf("bank:%s", strings.ToLower(string(transfer.SourceCurrency)))
		releasePayload, err := json.Marshal(domain.TreasuryReleasePayload{
			TransferID: transfer.ID,
			TenantID:   transfer.TenantID,
			Currency:   transfer.SourceCurrency,
			Amount:     transfer.SourceAmount,
			Location:   location,
			Reason:     "onramp_failure",
		})
		if err != nil {
			return fmt.Errorf("settla-core: handle on-ramp result %s: marshalling release payload: %w", transferID, err)
		}

		entries := []domain.OutboxEntry{
			domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentTreasuryRelease, releasePayload),
			domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID, domain.EventProviderOnRampFailed, transferEventPayload(transfer.ID, transfer.TenantID)),
		}

		if err := e.transferStore.TransitionWithOutbox(ctx, transfer.ID, domain.TransferStatusRefunding, transfer.Version, entries); err != nil {
			return wrapTransitionError(err, "handle on-ramp result", transferID)
		}

		e.logger.Warn("settla-core: on-ramp failed, refunding",
			"transfer_id", transfer.ID,
			"tenant_id", transfer.TenantID,
			"error", result.Error,
		)
	}

	return nil
}

// HandleSettlementResult processes the result of on-chain settlement.
// On success: transitions SETTLING → OFF_RAMPING with intent for off-ramp provider.
// On failure: transitions SETTLING → FAILED with intents for treasury release
// and ledger reversal.
func (e *Engine) HandleSettlementResult(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID, result domain.IntentResult) error {
	transfer, err := e.loadTransferForStep(ctx, tenantID, transferID, domain.TransferStatusSettling)
	if err != nil {
		return fmt.Errorf("settla-core: handle settlement result %s: %w", transferID, err)
	}

	if result.Success {
		// Load quote to get fallback alternatives for off-ramp.
		// Only alternatives with the same chain+stablecoin qualify (on-ramp already delivered).
		var offRampAlts []domain.OffRampFallback
		if transfer.QuoteID != nil {
			quote, qErr := e.transferStore.GetQuote(ctx, transfer.TenantID, *transfer.QuoteID)
			if qErr == nil && quote != nil {
				for _, alt := range quote.Route.AlternativeRoutes {
					if alt.Chain == transfer.Chain && alt.StableCoin == transfer.StableCoin {
						offRampAlts = append(offRampAlts, domain.OffRampFallback{
							ProviderID: alt.OffRampProvider,
							Fee:        alt.Fee,
							Rate:       alt.Rate,
						})
					}
				}
			}
		}

		// Build off-ramp intent
		offRampPayload, err := json.Marshal(domain.ProviderOffRampPayload{
			TransferID:   transfer.ID,
			TenantID:     transfer.TenantID,
			ProviderID:   transfer.OffRampProviderID,
			Amount:       transfer.StableAmount,
			FromCurrency: transfer.StableCoin,
			ToCurrency:   transfer.DestCurrency,
			Recipient:    transfer.Recipient,
			Reference:    transfer.ID.String(),
			Alternatives: offRampAlts,
			QuotedRate:   transfer.FXRate,
		})
		if err != nil {
			return fmt.Errorf("settla-core: handle settlement result %s: marshalling off-ramp payload: %w", transferID, err)
		}

		entries := []domain.OutboxEntry{
			domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentProviderOffRamp, offRampPayload),
			domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID, domain.EventSettlementCompleted, transferEventPayload(transfer.ID, transfer.TenantID)),
		}

		if err := e.transferStore.TransitionWithOutbox(ctx, transfer.ID, domain.TransferStatusOffRamping, transfer.Version, entries); err != nil {
			return wrapTransitionError(err, "handle settlement result", transferID)
		}

		e.logger.Info("settla-core: settlement confirmed, off-ramping",
			"transfer_id", transfer.ID,
			"tenant_id", transfer.TenantID,
			"tx_hash", result.TxHash,
		)
	} else {
		// Settlement failed — release treasury + reverse ledger
		location := fmt.Sprintf("bank:%s", strings.ToLower(string(transfer.SourceCurrency)))

		releasePayload, err := json.Marshal(domain.TreasuryReleasePayload{
			TransferID: transfer.ID,
			TenantID:   transfer.TenantID,
			Currency:   transfer.SourceCurrency,
			Amount:     transfer.SourceAmount,
			Location:   location,
			Reason:     "settlement_failure",
		})
		if err != nil {
			return fmt.Errorf("settla-core: handle settlement result %s: marshalling release payload: %w", transferID, err)
		}

		reversePayload, err := json.Marshal(domain.LedgerPostPayload{
			TransferID:     transfer.ID,
			TenantID:       transfer.TenantID,
			IdempotencyKey: fmt.Sprintf("reverse-settle:%s", transfer.ID),
			Description:    fmt.Sprintf("Reverse settlement for transfer %s: %s", transfer.ID, result.Error),
			ReferenceType:  "reversal",
		})
		if err != nil {
			return fmt.Errorf("settla-core: handle settlement result %s: marshalling reverse payload: %w", transferID, err)
		}

		entries := []domain.OutboxEntry{
			domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentTreasuryRelease, releasePayload),
			domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentLedgerReverse, reversePayload),
			domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID, domain.EventBlockchainFailed, transferEventPayload(transfer.ID, transfer.TenantID)),
		}

		if err := e.transferStore.TransitionWithOutbox(ctx, transfer.ID, domain.TransferStatusFailed, transfer.Version, entries); err != nil {
			return wrapTransitionError(err, "handle settlement result", transferID)
		}

		e.logger.Warn("settla-core: settlement failed",
			"transfer_id", transfer.ID,
			"tenant_id", transfer.TenantID,
			"error", result.Error,
		)
	}

	return nil
}

// HandleOffRampResult processes the result of an off-ramp provider execution.
// On success: calls CompleteTransfer to finalize.
// On failure: transitions OFF_RAMPING → FAILED with compensation intents.
func (e *Engine) HandleOffRampResult(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID, result domain.IntentResult) error {
	if result.Success {
		return e.CompleteTransfer(ctx, tenantID, transferID)
	}

	transfer, err := e.loadTransferForStep(ctx, tenantID, transferID, domain.TransferStatusOffRamping)
	if err != nil {
		return fmt.Errorf("settla-core: handle off-ramp result %s: %w", transferID, err)
	}

	// Off-ramp failed — release treasury + reverse ledger + notify tenant
	location := fmt.Sprintf("bank:%s", strings.ToLower(string(transfer.SourceCurrency)))

	releasePayload, err := json.Marshal(domain.TreasuryReleasePayload{
		TransferID: transfer.ID,
		TenantID:   transfer.TenantID,
		Currency:   transfer.SourceCurrency,
		Amount:     transfer.SourceAmount,
		Location:   location,
		Reason:     "offramp_failure",
	})
	if err != nil {
		return fmt.Errorf("settla-core: handle off-ramp result %s: marshalling release payload: %w", transferID, err)
	}

	reversePayload, err := json.Marshal(domain.LedgerPostPayload{
		TransferID:     transfer.ID,
		TenantID:       transfer.TenantID,
		IdempotencyKey: fmt.Sprintf("reverse-offramp:%s", transfer.ID),
		Description:    fmt.Sprintf("Reverse off-ramp for transfer %s: %s", transfer.ID, result.Error),
		ReferenceType:  "reversal",
	})
	if err != nil {
		return fmt.Errorf("settla-core: handle off-ramp result %s: marshalling reverse payload: %w", transferID, err)
	}

	webhookPayload, err := json.Marshal(domain.WebhookDeliverPayload{
		TransferID: transfer.ID,
		TenantID:   transfer.TenantID,
		EventType:  domain.EventTransferFailed,
	})
	if err != nil {
		return fmt.Errorf("settla-core: handle off-ramp result %s: marshalling webhook payload: %w", transferID, err)
	}

	entries := []domain.OutboxEntry{
		domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentTreasuryRelease, releasePayload),
		domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentLedgerReverse, reversePayload),
		domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentWebhookDeliver, webhookPayload),
		domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID, domain.EventProviderOffRampFailed, transferEventPayload(transfer.ID, transfer.TenantID)),
	}

	if err := e.transferStore.TransitionWithOutbox(ctx, transfer.ID, domain.TransferStatusFailed, transfer.Version, entries); err != nil {
		return wrapTransitionError(err, "handle off-ramp result", transferID)
	}

	e.logger.Warn("settla-core: off-ramp failed",
		"transfer_id", transfer.ID,
		"tenant_id", transfer.TenantID,
		"error", result.Error,
	)

	return nil
}

// CompleteTransfer transitions OFF_RAMPING → COMPLETED and writes
// outbox intents for treasury release, final ledger posting, and webhook delivery.
func (e *Engine) CompleteTransfer(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID) error {
	transfer, err := e.loadTransfer(ctx, tenantID, transferID)
	if err != nil {
		return fmt.Errorf("settla-core: complete transfer %s: %w", transferID, err)
	}

	if transfer.Status != domain.TransferStatusOffRamping {
		return fmt.Errorf("settla-core: complete transfer %s: %w",
			transferID, domain.ErrInvalidTransition(string(transfer.Status), string(domain.TransferStatusCompleted)))
	}

	location := fmt.Sprintf("bank:%s", strings.ToLower(string(transfer.SourceCurrency)))

	// Treasury release intent — unlock the reservation
	releasePayload, err := json.Marshal(domain.TreasuryReleasePayload{
		TransferID: transfer.ID,
		TenantID:   transfer.TenantID,
		Currency:   transfer.SourceCurrency,
		Amount:     transfer.SourceAmount,
		Location:   location,
		Reason:     "transfer_complete",
	})
	if err != nil {
		return fmt.Errorf("settla-core: complete transfer %s: marshalling release payload: %w", transferID, err)
	}

	// Ledger post intent — final completion entries
	tenant, err := e.tenantStore.GetTenant(ctx, transfer.TenantID)
	if err != nil {
		return fmt.Errorf("settla-core: complete transfer %s: loading tenant: %w", transferID, err)
	}
	slug := tenant.Slug
	totalFees := transfer.Fees.TotalFeeUSD
	netAmount := transfer.SourceAmount.Sub(totalFees)

	completionLines := []domain.LedgerLineEntry{
		{
			AccountCode: domain.TenantAccountCode(slug, "liabilities:customer:pending"),
			EntryType:   string(domain.EntryTypeDebit),
			Amount:      transfer.SourceAmount,
			Currency:    string(transfer.SourceCurrency),
			Description: "Debit customer pending",
		},
		{
			AccountCode: domain.TenantAccountCode(slug, "liabilities:payable:recipient"),
			EntryType:   string(domain.EntryTypeCredit),
			Amount:      netAmount,
			Currency:    string(transfer.SourceCurrency),
			Description: "Credit recipient payable (net of fees)",
		},
		{
			AccountCode: domain.TenantAccountCode(slug, "revenue:fees:settlement"),
			EntryType:   string(domain.EntryTypeCredit),
			Amount:      totalFees,
			Currency:    string(transfer.SourceCurrency),
			Description: "Credit settlement fee revenue",
		},
	}

	if err := validateLedgerLines(completionLines); err != nil {
		return fmt.Errorf("settla-core: complete transfer %s: ledger entries imbalanced: %w", transferID, err)
	}

	ledgerPayload, err := json.Marshal(domain.LedgerPostPayload{
		TransferID:     transfer.ID,
		TenantID:       transfer.TenantID,
		IdempotencyKey: fmt.Sprintf("complete:%s", transfer.ID),
		Description:    fmt.Sprintf("Complete transfer %s", transfer.ID),
		ReferenceType:  "transfer",
		Lines:          completionLines,
	})
	if err != nil {
		return fmt.Errorf("settla-core: complete transfer %s: marshalling ledger payload: %w", transferID, err)
	}

	// Webhook delivery intent — notify tenant
	webhookPayload, err := json.Marshal(domain.WebhookDeliverPayload{
		TransferID: transfer.ID,
		TenantID:   transfer.TenantID,
		EventType:  domain.EventTransferCompleted,
	})
	if err != nil {
		return fmt.Errorf("settla-core: complete transfer %s: marshalling webhook payload: %w", transferID, err)
	}

	entries := []domain.OutboxEntry{
		domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentTreasuryRelease, releasePayload),
		domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentLedgerPost, ledgerPayload),
		domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentWebhookDeliver, webhookPayload),
		domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID, domain.EventTransferCompleted, transferEventPayload(transfer.ID, transfer.TenantID)),
	}

	if err := e.transferStore.TransitionWithOutbox(ctx, transfer.ID, domain.TransferStatusCompleted, transfer.Version, entries); err != nil {
		return wrapTransitionError(err, "complete transfer", transferID)
	}

	corridor := observability.FormatCorridor(string(transfer.SourceCurrency), string(transfer.DestCurrency))
	if e.metrics != nil {
		e.metrics.TransfersTotal.WithLabelValues(transfer.TenantID.String(), string(domain.TransferStatusCompleted), corridor).Inc()
	}

	e.logger.Info("settla-core: transfer completed",
		"transfer_id", transfer.ID,
		"tenant_id", transfer.TenantID,
		"corridor", corridor,
	)

	return nil
}

// FailTransfer transitions a transfer to FAILED with a reason and code, and writes
// outbox intents for treasury release and webhook notification.
func (e *Engine) FailTransfer(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID, reason string, code string) error {
	transfer, err := e.loadTransfer(ctx, tenantID, transferID)
	if err != nil {
		return fmt.Errorf("settla-core: fail transfer %s: %w", transferID, err)
	}

	if !transfer.CanTransitionTo(domain.TransferStatusFailed) {
		return fmt.Errorf("settla-core: fail transfer %s: %w",
			transferID, domain.ErrInvalidTransition(string(transfer.Status), string(domain.TransferStatusFailed)))
	}

	location := fmt.Sprintf("bank:%s", strings.ToLower(string(transfer.SourceCurrency)))

	releasePayload, err := json.Marshal(domain.TreasuryReleasePayload{
		TransferID: transfer.ID,
		TenantID:   transfer.TenantID,
		Currency:   transfer.SourceCurrency,
		Amount:     transfer.SourceAmount,
		Location:   location,
		Reason:     "transfer_failed",
	})
	if err != nil {
		return fmt.Errorf("settla-core: fail transfer %s: marshalling release payload: %w", transferID, err)
	}

	webhookPayload, err := json.Marshal(domain.WebhookDeliverPayload{
		TransferID: transfer.ID,
		TenantID:   transfer.TenantID,
		EventType:  domain.EventTransferFailed,
		Data:       []byte(fmt.Sprintf(`{"reason":%q,"code":%q}`, reason, code)),
	})
	if err != nil {
		return fmt.Errorf("settla-core: fail transfer %s: marshalling webhook payload: %w", transferID, err)
	}

	entries := []domain.OutboxEntry{
		domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentTreasuryRelease, releasePayload),
		domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentWebhookDeliver, webhookPayload),
		domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID, domain.EventTransferFailed, transferEventPayload(transfer.ID, transfer.TenantID)),
	}

	corridor := observability.FormatCorridor(string(transfer.SourceCurrency), string(transfer.DestCurrency))
	if e.metrics != nil {
		e.metrics.TransfersTotal.WithLabelValues(transfer.TenantID.String(), string(domain.TransferStatusFailed), corridor).Inc()
	}

	if err := e.transferStore.TransitionWithOutbox(ctx, transfer.ID, domain.TransferStatusFailed, transfer.Version, entries); err != nil {
		return wrapTransitionError(err, "fail transfer", transferID)
	}

	e.logger.Warn("settla-core: transfer failed",
		"transfer_id", transfer.ID,
		"tenant_id", transfer.TenantID,
		"reason", reason,
		"code", code,
	)

	return nil
}

// InitiateRefund transitions a transfer through REFUNDING and writes outbox
// intents for ledger reversal and treasury release.
func (e *Engine) InitiateRefund(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID) error {
	transfer, err := e.loadTransfer(ctx, tenantID, transferID)
	if err != nil {
		return fmt.Errorf("settla-core: refund transfer %s: %w", transferID, err)
	}

	if !transfer.CanTransitionTo(domain.TransferStatusRefunding) {
		return fmt.Errorf("settla-core: refund transfer %s: %w",
			transferID, domain.ErrInvalidTransition(string(transfer.Status), string(domain.TransferStatusRefunding)))
	}

	location := fmt.Sprintf("bank:%s", strings.ToLower(string(transfer.SourceCurrency)))

	reversePayload, err := json.Marshal(domain.LedgerPostPayload{
		TransferID:     transfer.ID,
		TenantID:       transfer.TenantID,
		IdempotencyKey: fmt.Sprintf("refund:%s", transfer.ID),
		Description:    fmt.Sprintf("Refund for transfer %s", transfer.ID),
		ReferenceType:  "reversal",
	})
	if err != nil {
		return fmt.Errorf("settla-core: refund transfer %s: marshalling reverse payload: %w", transferID, err)
	}

	releasePayload, err := json.Marshal(domain.TreasuryReleasePayload{
		TransferID: transfer.ID,
		TenantID:   transfer.TenantID,
		Currency:   transfer.SourceCurrency,
		Amount:     transfer.SourceAmount,
		Location:   location,
		Reason:     "refund",
	})
	if err != nil {
		return fmt.Errorf("settla-core: refund transfer %s: marshalling release payload: %w", transferID, err)
	}

	entries := []domain.OutboxEntry{
		domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentLedgerReverse, reversePayload),
		domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentTreasuryRelease, releasePayload),
		domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID, domain.EventRefundInitiated, transferEventPayload(transfer.ID, transfer.TenantID)),
	}

	if err := e.transferStore.TransitionWithOutbox(ctx, transfer.ID, domain.TransferStatusRefunding, transfer.Version, entries); err != nil {
		return wrapTransitionError(err, "refund transfer", transferID)
	}

	e.logger.Info("settla-core: refund initiated",
		"transfer_id", transfer.ID,
		"tenant_id", transfer.TenantID,
	)

	return nil
}

// HandleRefundResult processes the result of a refund operation.
// On success: transitions REFUNDING → FAILED (refund completed, transfer is terminal).
// On failure: logs the error — the recovery detector will escalate to manual review.
func (e *Engine) HandleRefundResult(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID, result domain.IntentResult) error {
	transfer, err := e.loadTransferForStep(ctx, tenantID, transferID, domain.TransferStatusRefunding)
	if err != nil {
		return fmt.Errorf("settla-core: handle refund result %s: %w", transferID, err)
	}

	if result.Success {
		webhookPayload, err := json.Marshal(domain.WebhookDeliverPayload{
			TransferID: transfer.ID,
			TenantID:   transfer.TenantID,
			EventType:  domain.EventTransferFailed,
			Data:       []byte(fmt.Sprintf(`{"reason":"refund_completed","transfer_id":"%s"}`, transfer.ID)),
		})
		if err != nil {
			return fmt.Errorf("settla-core: handle refund result %s: marshalling webhook payload: %w", transferID, err)
		}

		entries := []domain.OutboxEntry{
			domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentWebhookDeliver, webhookPayload),
			domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID, domain.EventRefundCompleted, transferEventPayload(transfer.ID, transfer.TenantID)),
		}

		if err := e.transferStore.TransitionWithOutbox(ctx, transfer.ID, domain.TransferStatusFailed, transfer.Version, entries); err != nil {
			return wrapTransitionError(err, "handle refund result", transferID)
		}

		e.logger.Info("settla-core: refund completed",
			"transfer_id", transfer.ID,
			"tenant_id", transfer.TenantID,
		)
	} else {
		e.logger.Warn("settla-core: refund failed, awaiting recovery escalation",
			"transfer_id", transfer.ID,
			"tenant_id", transfer.TenantID,
			"error", result.Error,
		)
	}

	return nil
}

// ProcessTransfer runs the settlement pipeline for testing/demo by stepping
// through FundTransfer → InitiateOnRamp → HandleOnRampResult(success) →
// HandleSettlementResult(success) → HandleOffRampResult(success).
// In production, each step is triggered by workers processing outbox intents.
func (e *Engine) ProcessTransfer(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID) error {
	if err := e.FundTransfer(ctx, tenantID, transferID); err != nil {
		return err
	}
	if err := e.InitiateOnRamp(ctx, tenantID, transferID); err != nil {
		return err
	}
	if err := e.HandleOnRampResult(ctx, tenantID, transferID, domain.IntentResult{Success: true}); err != nil {
		return err
	}
	if err := e.HandleSettlementResult(ctx, tenantID, transferID, domain.IntentResult{Success: true, TxHash: "0xdemo"}); err != nil {
		return err
	}
	return e.HandleOffRampResult(ctx, tenantID, transferID, domain.IntentResult{Success: true})
}

// transferEventPayload returns a minimal JSON payload for transfer lifecycle events.
// This ensures all events carry the transfer_id so downstream workers can route them.
func transferEventPayload(transferID, tenantID uuid.UUID) []byte {
	data, _ := json.Marshal(struct {
		TransferID uuid.UUID `json:"transfer_id"`
		TenantID   uuid.UUID `json:"tenant_id"`
	}{TransferID: transferID, TenantID: tenantID})
	return data
}

// loadTransfer fetches a transfer by tenant and ID. The tenantID is passed
// through from the outbox entry payload to enforce tenant isolation in all
// store lookups.
func (e *Engine) loadTransfer(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID) (*domain.Transfer, error) {
	transfer, err := e.transferStore.GetTransfer(ctx, tenantID, transferID)
	if err != nil {
		return nil, fmt.Errorf("loading transfer %s: %w", transferID, err)
	}
	return transfer, nil
}

// loadTransferForStep loads a transfer and verifies it's in the expected status.
func (e *Engine) loadTransferForStep(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID, expectedStatus domain.TransferStatus) (*domain.Transfer, error) {
	transfer, err := e.loadTransfer(ctx, tenantID, transferID)
	if err != nil {
		return nil, err
	}
	if transfer.Status != expectedStatus {
		return nil, domain.ErrInvalidTransition(string(transfer.Status), "next")
	}
	return transfer, nil
}

// validateLedgerLines converts outbox LedgerLineEntry items to domain EntryLine
// and validates that debits equal credits before the entries are queued.
func validateLedgerLines(lines []domain.LedgerLineEntry) error {
	entryLines := make([]domain.EntryLine, len(lines))
	for i, l := range lines {
		entryLines[i] = domain.EntryLine{
			ID: uuid.New(),
			Posting: domain.Posting{
				AccountCode: l.AccountCode,
				EntryType:   domain.EntryType(l.EntryType),
				Amount:      l.Amount,
				Currency:    domain.Currency(l.Currency),
			},
		}
	}
	return domain.ValidateEntries(entryLines)
}

// wrapTransitionError adds context to TransitionWithOutbox errors. Optimistic lock
// conflicts get a "concurrent modification" message so callers (workers) can
// distinguish retryable conflicts from permanent failures via errors.Is.
func wrapTransitionError(err error, step string, transferID uuid.UUID) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrOptimisticLock) {
		return fmt.Errorf("settla-core: %s: concurrent modification of transfer %s: %w", step, transferID, ErrOptimisticLock)
	}
	return fmt.Errorf("settla-core: %s %s: %w", step, transferID, err)
}
