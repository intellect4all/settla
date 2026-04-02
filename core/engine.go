package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/observability"
)

var engineTracer = otel.Tracer("settla.core.engine")

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
	transferStore             TransferStore
	tenantStore               TenantStore
	router                    Router // used ONLY for quote generation, NOT for provider execution
	providerRegistry          domain.ProviderRegistry
	logger                    *slog.Logger
	metrics                   *observability.Metrics
	dailyVolumeCache          sync.Map           // map[string]dailyVolumeEntry — fallback when volumeCounter is nil
	dailyVolumeCounter        DailyVolumeCounter // optional: atomic counter (Redis). Nil = use sync.Map fallback.
	dailyVolumeWarnOnce       sync.Once
	requireDailyVolumeCounter bool
}

type dailyVolumeEntry struct {
	volume    decimal.Decimal
	expiresAt time.Time
}

// dailyVolumeCacheTTL is how long daily volume entries are cached in memory
// when the sync.Map fallback is active (no DailyVolumeCounter configured).
const dailyVolumeCacheTTL = 5 * time.Second

// EngineOption configures optional Engine dependencies.
type EngineOption func(*Engine)

// WithDailyVolumeCounter sets an atomic daily volume counter (e.g. Redis-backed)
// for race-free daily limit enforcement. When set, the in-memory sync.Map cache
// is bypassed.
func WithDailyVolumeCounter(counter DailyVolumeCounter) EngineOption {
	return func(e *Engine) { e.dailyVolumeCounter = counter }
}

// WithRequireDailyVolumeCounter rejects transfer creation when no atomic
// DailyVolumeCounter is configured and the tenant has a daily limit. Use this
// in production to prevent the non-atomic sync.Map fallback from being used.
func WithRequireDailyVolumeCounter() EngineOption {
	return func(e *Engine) { e.requireDailyVolumeCounter = true }
}

// NewEngine creates a settlement engine wired to the given dependencies.
// The router is used only for generating quotes in CreateTransfer and GetQuote;
// it does not execute any provider calls. The providerRegistry is used to
// validate that quoted providers are still available before creating a transfer.
func NewEngine(
	transferStore TransferStore,
	tenantStore TenantStore,
	router Router,
	providerRegistry domain.ProviderRegistry,
	logger *slog.Logger,
	metrics *observability.Metrics,
	opts ...EngineOption,
) *Engine {
	e := &Engine{
		transferStore:    transferStore,
		tenantStore:      tenantStore,
		router:           router,
		providerRegistry: providerRegistry,
		logger:           logger.With("module", "core.engine"),
		metrics:          metrics,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// CreateTransfer validates a settlement request, checks tenant limits, enforces
// idempotency, and persists the initial transfer record with an outbox event
// atomically in a single database transaction.
func (e *Engine) CreateTransfer(ctx context.Context, tenantID uuid.UUID, req CreateTransferRequest) (_ *domain.Transfer, retErr error) {
	ctx, span := engineTracer.Start(ctx, "Engine.CreateTransfer")
	defer func() {
		if retErr != nil {
			span.RecordError(retErr)
			span.SetStatus(codes.Error, retErr.Error())
		}
		span.End()
	}()
	span.SetAttributes(attribute.String("tenant_id", tenantID.String()))

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
	if err := req.Sender.Validate(); err != nil {
		return nil, fmt.Errorf("settla-core: create transfer: %w", err)
	}
	if req.Recipient.Name == "" || req.Recipient.Country == "" {
		return nil, fmt.Errorf("settla-core: create transfer: recipient name and country are required")
	}
	if req.Recipient.AccountNumber != "" {
		// Validate account number format: must be alphanumeric, 4-34 chars (covers IBAN length)
		acctNum := req.Recipient.AccountNumber
		if len(acctNum) < 4 || len(acctNum) > 34 {
			return nil, fmt.Errorf("settla-core: create transfer: recipient account_number must be 4-34 characters, got %d", len(acctNum))
		}
		for _, c := range acctNum {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '-') {
				return nil, fmt.Errorf("settla-core: create transfer: recipient account_number contains invalid character %q", string(c))
			}
		}
	}
	if req.Recipient.AccountNumber != "" && req.Recipient.BankName == "" {
		return nil, fmt.Errorf("settla-core: create transfer: recipient bank_name is required when account_number is provided")
	}

	// c. Check idempotency key
	if req.IdempotencyKey != "" {
		existing, err := e.transferStore.GetTransferByIdempotencyKey(ctx, tenantID, req.IdempotencyKey)
		if err == nil && existing != nil {
			return existing, nil
		}
	}

	// c2. Check per-tenant pending transfer limit
	if tenant.MaxPendingTransfers > 0 {
		count, err := e.transferStore.CountPendingTransfers(ctx, tenantID)
		if err != nil {
			return nil, fmt.Errorf("settla-core: create transfer: counting pending transfers: %w", err)
		}
		if count >= tenant.MaxPendingTransfers {
			return nil, fmt.Errorf("settla-core: create transfer: tenant %s exceeded max pending transfers (%d)", tenantID, tenant.MaxPendingTransfers)
		}
	}

	// d. Fetch and validate quote — resolved before limit checks so that
	// quote.StableAmount (USDT, pegged 1:1 to USD) provides the USD-equivalent
	// for comparing against USD-denominated tenant limits.
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

	// d2. Cross-validate currency pair for pre-existing quotes
	if req.QuoteID != nil {
		if quote.SourceCurrency != req.SourceCurrency {
			return nil, fmt.Errorf("settla-core: create transfer: quote source currency %s does not match request source currency %s", quote.SourceCurrency, req.SourceCurrency)
		}
		if quote.DestCurrency != req.DestCurrency {
			return nil, fmt.Errorf("settla-core: create transfer: quote dest currency %s does not match request dest currency %s", quote.DestCurrency, req.DestCurrency)
		}
	}

	if err := quote.Validate(); err != nil {
		return nil, fmt.Errorf("settla-core: create transfer: quote validation failed: %w", err)
	}
	// d5. Validate quoted providers are still available in the registry
	if e.providerRegistry != nil {
		if _, err := e.providerRegistry.GetOnRamp(quote.Route.OnRampProvider); err != nil {
			return nil, domain.ErrProviderUnavailable(quote.Route.OnRampProvider)
		}
		if _, err := e.providerRegistry.GetOffRamp(quote.Route.OffRampProvider); err != nil {
			return nil, domain.ErrProviderUnavailable(quote.Route.OffRampProvider)
		}
	}

	// d6. Validate chain is a supported blockchain
	if err := domain.ValidateChain(quote.Route.Chain); err != nil {
		return nil, fmt.Errorf("settla-core: create transfer: %w", err)
	}

	// e. Check per-transfer limit (USD-equivalent via quote.StableAmount)
	// StableAmount is the intermediate stablecoin (USDT, pegged 1:1 to USD),
	// making it the correct USD-equivalent for limit comparison regardless of source currency.
	if !tenant.PerTransferLimit.IsZero() && quote.StableAmount.GreaterThan(tenant.PerTransferLimit) {
		return nil, domain.ErrAmountTooHigh(quote.StableAmount.String(), tenant.PerTransferLimit.String())
	}

	// f. Check daily volume limit (USD-equivalent)
	// The DB query sums stable_amount — the USD-equivalent set from the quote at creation.
	if !tenant.DailyLimitUSD.IsZero() {
		today := time.Now().UTC().Truncate(24 * time.Hour)

		dailyVolume, err := e.getDailyVolume(ctx, tenantID, today)
		if err != nil {
			return nil, fmt.Errorf("settla-core: create transfer: checking daily volume: %w", err)
		}

		if dailyVolume.Add(quote.StableAmount).GreaterThan(tenant.DailyLimitUSD) {
			return nil, domain.ErrDailyLimitExceeded(tenantID.String())
		}
	}

	// g. Create transfer record
	now := time.Now().UTC()
	transfer := &domain.Transfer{
		ID:                uuid.Must(uuid.NewV7()),
		TenantID:          tenantID,
		ExternalRef:       req.ExternalRef,
		IdempotencyKey:    req.IdempotencyKey,
		Status:            domain.TransferStatusCreated,
		Version:           1,
		SourceCurrency:    req.SourceCurrency,
		SourceAmount:      req.SourceAmount,
		DestCurrency:      req.DestCurrency,
		DestAmount:        quote.DestAmount,
		StableCoin:        quote.Route.StableCoin,
		StableAmount:      quote.StableAmount,
		Chain:             quote.Route.Chain,
		FXRate:            quote.FXRate,
		Fees:              quote.Fees,
		OnRampProviderID:  quote.Route.OnRampProvider,
		OffRampProviderID: quote.Route.OffRampProvider,
		Sender:            req.Sender,
		Recipient:         req.Recipient,
		QuoteID:           req.QuoteID,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	transfer.FeeScheduleSnapshot = &tenant.FeeSchedule

	// g2. Validate fee breakdown consistency
	if err := transfer.Fees.Validate(); err != nil {
		return nil, fmt.Errorf("settla-core: create transfer: %w", err)
	}

	// g3. Validate total fees are less than source amount
	if transfer.Fees.TotalFeeUSD.GreaterThanOrEqual(transfer.SourceAmount) {
		return nil, fmt.Errorf("settla-core: create transfer: total fees (%s) must be less than source amount (%s)", transfer.Fees.TotalFeeUSD, transfer.SourceAmount)
	}

	// g4. Reject zero fees — indicates misconfigured fee schedule or quote
	if transfer.Fees.TotalFeeUSD.IsZero() {
		return nil, fmt.Errorf("settla-core: create transfer: zero fees not permitted for tenant %s — check fee schedule configuration", tenantID)
	}

	// h. Build outbox event for transfer.created
	payload, err := json.Marshal(transfer)
	if err != nil {
		return nil, fmt.Errorf("settla-core: create transfer: marshalling event payload: %w", err)
	}
	entries, err := buildOutboxEntries(
		outboxResult(domain.NewOutboxEvent("transfer", transfer.ID, tenantID, domain.EventTransferCreated, payload)),
	)
	if err != nil {
		return nil, fmt.Errorf("settla-core: create transfer: building outbox entries: %w", err)
	}

	entries = setCorrelationID(entries, transfer.ID)

	// i. Persist transfer + outbox atomically
	if err := e.transferStore.CreateTransferWithOutbox(ctx, transfer, entries); err != nil {
		return nil, fmt.Errorf("settla-core: create transfer: persisting: %w", err)
	}

	// Update daily volume counter with the new transfer amount
	if !tenant.DailyLimitUSD.IsZero() {
		today := time.Now().UTC().Truncate(24 * time.Hour)
		e.incrDailyVolume(ctx, tenantID, today, quote.StableAmount)
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

// GetRoutingOptions returns ranked provider routes for a corridor/amount
// without creating a transfer or persisting a quote. Read-only operation.
func (e *Engine) GetRoutingOptions(ctx context.Context, tenantID uuid.UUID, req domain.QuoteRequest) (*domain.RouteResult, error) {
	tenant, err := e.tenantStore.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("settla-core: get routing options: loading tenant %s: %w", tenantID, err)
	}
	if !tenant.IsActive() {
		return nil, domain.ErrTenantSuspended(tenantID.String())
	}

	result, err := e.router.GetRoutingOptions(ctx, tenantID, req)
	if err != nil {
		return nil, fmt.Errorf("settla-core: get routing options: %w", err)
	}

	return result, nil
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
// Returns ErrQuoteExpired if the quote has passed its expiry time.
func (e *Engine) GetQuoteByID(ctx context.Context, tenantID uuid.UUID, quoteID uuid.UUID) (*domain.Quote, error) {
	quote, err := e.transferStore.GetQuote(ctx, tenantID, quoteID)
	if err != nil {
		return nil, fmt.Errorf("settla-core: get quote %s: %w", quoteID, err)
	}
	if quote.IsExpired() {
		return nil, domain.ErrQuoteExpired(quoteID.String())
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

// GetTransferByExternalRef retrieves a transfer by tenant and external reference.
func (e *Engine) GetTransferByExternalRef(ctx context.Context, tenantID uuid.UUID, externalRef string) (*domain.Transfer, error) {
	transfer, err := e.transferStore.GetTransferByExternalRef(ctx, tenantID, externalRef)
	if err != nil {
		return nil, fmt.Errorf("settla-core: get transfer by external ref %s: %w", externalRef, err)
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

// ListTransfersFiltered returns transfers with optional server-side filtering.
func (e *Engine) ListTransfersFiltered(ctx context.Context, tenantID uuid.UUID, statusFilter, searchQuery string, limit int) ([]domain.Transfer, error) {
	transfers, err := e.transferStore.ListTransfersFiltered(ctx, tenantID, statusFilter, searchQuery, limit)
	if err != nil {
		return nil, fmt.Errorf("settla-core: list transfers filtered for tenant %s: %w", tenantID, err)
	}
	return transfers, nil
}

// GetTransferEvents returns all state-change events for a transfer.
func (e *Engine) GetTransferEvents(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID) ([]domain.TransferEvent, error) {
	events, err := e.transferStore.GetTransferEvents(ctx, tenantID, transferID)
	if err != nil {
		return nil, fmt.Errorf("settla-core: list transfer events for %s: %w", transferID, err)
	}
	return events, nil
}

// FundTransfer transitions CREATED → FUNDED and writes outbox intents for
// treasury reservation and a funded event. The treasury worker will pick up the
// IntentTreasuryReserve intent and actually reserve funds.
func (e *Engine) FundTransfer(ctx context.Context, tenantID uuid.UUID, transferID uuid.UUID) error {
	ctx, span := engineTracer.Start(ctx, "Engine.FundTransfer")
	defer span.End()
	span.SetAttributes(attribute.String("tenant_id", tenantID.String()), attribute.String("transfer_id", transferID.String()))

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

	entries, err := buildOutboxEntries(
		outboxResult(domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentTreasuryReserve, reservePayload)),
		outboxResult(domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID, domain.EventTransferFunded, transferEventPayload(transfer.ID, transfer.TenantID))),
	)
	if err != nil {
		return fmt.Errorf("settla-core: fund transfer %s: building outbox entries: %w", transferID, err)
	}

	entries = setCorrelationID(entries, transfer.ID)

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

	// Load quote to get fallback alternatives (deduplicated by provider+chain key)
	var alternatives []domain.OnRampFallback
	if transfer.QuoteID != nil {
		quote, qErr := e.transferStore.GetQuote(ctx, transfer.TenantID, *transfer.QuoteID)
		if qErr == nil && quote != nil {
			seen := make(map[string]bool)
			for _, alt := range quote.Route.AlternativeRoutes {
				dedupKey := alt.OnRampProvider + ":" + alt.OffRampProvider + ":" + string(alt.Chain)
				if seen[dedupKey] {
					continue
				}
				seen[dedupKey] = true
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

	entries, err := buildOutboxEntries(
		outboxResult(domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentProviderOnRamp, onRampPayload)),
	)
	if err != nil {
		return fmt.Errorf("settla-core: on-ramp transfer %s: building outbox entries: %w", transferID, err)
	}

	entries = setCorrelationID(entries, transfer.ID)

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
	ctx, span := engineTracer.Start(ctx, "Engine.HandleOnRampResult")
	defer span.End()
	span.SetAttributes(attribute.String("tenant_id", tenantID.String()), attribute.String("transfer_id", transferID.String()))

	transfer, err := e.loadTransfer(ctx, tenantID, transferID)
	if err != nil {
		return fmt.Errorf("settla-core: handle on-ramp result %s: %w", transferID, err)
	}
	if transfer.Status != domain.TransferStatusOnRamping {
		// If the transfer has advanced past ON_RAMPING, this is a NATS replay — skip.
		// Otherwise (terminal, failed, or earlier state), reject.
		if !isAdvancedPast(transfer.Status, domain.TransferStatusOnRamping) {
			return domain.ErrInvalidTransition(string(transfer.Status), string(domain.TransferStatusOnRamping))
		}
		e.logger.Info("settla-core: skipping on-ramp result (NATS replay): transfer already advanced",
			"transfer_id", transferID, "current_status", transfer.Status, "expected_status", domain.TransferStatusOnRamping)
		return nil
	}

	if result.Success {
		// Build ledger post intent for on-ramp accounting
		tenant, err := e.tenantStore.GetTenant(ctx, transfer.TenantID)
		if err != nil {
			return fmt.Errorf("settla-core: handle on-ramp result %s: loading tenant: %w", transferID, err)
		}
		slug := tenant.Slug
		onRampFee := transfer.Fees.OnRampFee

		// Validate net amount after fee subtraction is positive
		netAmount := transfer.SourceAmount.Sub(onRampFee)
		if !netAmount.IsPositive() {
			return fmt.Errorf("settla-core: net amount after fee must be positive, got %s (source %s - fee %s) for transfer %s",
				netAmount.String(), transfer.SourceAmount.String(), onRampFee.String(), transferID)
		}

		onRampLines := []domain.LedgerLineEntry{
			{
				AccountCode: fmt.Sprintf("assets:crypto:%s:%s", strings.ToLower(string(transfer.StableCoin)), strings.ToLower(string(transfer.Chain))),
				EntryType:   string(domain.EntryTypeDebit),
				Amount:      transfer.StableAmount,
				Currency:    string(transfer.StableCoin),
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

		// Note: on-ramp is a cross-currency entry (fiat credit → crypto debit),
		// so per-currency balance validation does not apply. TigerBeetle enforces
		// the actual balance constraints at write time.

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

		entries, err := buildOutboxEntries(
			outboxResult(domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentLedgerPost, ledgerPayload)),
			outboxResult(domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentBlockchainSend, blockchainPayload)),
			outboxResult(domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID, domain.EventOnRampCompleted, transferEventPayload(transfer.ID, transfer.TenantID))),
		)
		if err != nil {
			return fmt.Errorf("settla-core: handle on-ramp result %s: building outbox entries: %w", transferID, err)
		}

		entries = setCorrelationID(entries, transfer.ID)

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

		entries, err := buildOutboxEntries(
			outboxResult(domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentTreasuryRelease, releasePayload)),
			outboxResult(domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID, domain.EventProviderOnRampFailed, transferEventPayload(transfer.ID, transfer.TenantID))),
		)
		if err != nil {
			return fmt.Errorf("settla-core: handle on-ramp result %s: building outbox entries: %w", transferID, err)
		}

		entries = setCorrelationID(entries, transfer.ID)

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
	ctx, span := engineTracer.Start(ctx, "Engine.HandleSettlementResult")
	defer span.End()
	span.SetAttributes(attribute.String("tenant_id", tenantID.String()), attribute.String("transfer_id", transferID.String()))

	transfer, err := e.loadTransfer(ctx, tenantID, transferID)
	if err != nil {
		return fmt.Errorf("settla-core: handle settlement result %s: %w", transferID, err)
	}
	if transfer.Status != domain.TransferStatusSettling {
		if !isAdvancedPast(transfer.Status, domain.TransferStatusSettling) {
			return domain.ErrInvalidTransition(string(transfer.Status), string(domain.TransferStatusSettling))
		}
		e.logger.Info("settla-core: skipping settlement result (NATS replay): transfer already advanced",
			"transfer_id", transferID, "current_status", transfer.Status, "expected_status", domain.TransferStatusSettling)
		return nil
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
			SourceTxHash: result.TxHash,
		})
		if err != nil {
			return fmt.Errorf("settla-core: handle settlement result %s: marshalling off-ramp payload: %w", transferID, err)
		}

		entries, err := buildOutboxEntries(
			outboxResult(domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentProviderOffRamp, offRampPayload)),
			outboxResult(domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID, domain.EventSettlementCompleted, transferEventPayload(transfer.ID, transfer.TenantID))),
		)
		if err != nil {
			return fmt.Errorf("settla-core: handle settlement result %s: building outbox entries: %w", transferID, err)
		}

		entries = setCorrelationID(entries, transfer.ID)

		if err := e.transferStore.TransitionWithOutbox(ctx, transfer.ID, domain.TransferStatusOffRamping, transfer.Version, entries); err != nil {
			return wrapTransitionError(err, "handle settlement result", transferID)
		}

		// Track blockchain tx in transfer for recovery detector
		if result.TxHash != "" {
			transfer.BlockchainTxs = append(transfer.BlockchainTxs, domain.BlockchainTx{
				Chain:  transfer.Chain,
				Type:   "settlement",
				TxHash: result.TxHash,
				Status: "confirmed",
			})
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

		// Build reversed ledger lines: swap debits and credits from the on-ramp posting
		tenant, tErr := e.tenantStore.GetTenant(ctx, transfer.TenantID)
		if tErr != nil {
			return fmt.Errorf("settla-core: handle settlement result %s: loading tenant for reversal: %w", transferID, tErr)
		}
		slug := tenant.Slug
		onRampFee := transfer.Fees.OnRampFee
		reversalLines := []domain.LedgerLineEntry{
			{
				AccountCode: fmt.Sprintf("assets:crypto:%s:%s", strings.ToLower(string(transfer.StableCoin)), strings.ToLower(string(transfer.Chain))),
				EntryType:   string(domain.EntryTypeCredit),
				Amount:      transfer.StableAmount,
				Currency:    string(transfer.StableCoin),
				Description: "Reverse: credit crypto asset",
			},
			{
				AccountCode: "expenses:provider:onramp",
				EntryType:   string(domain.EntryTypeCredit),
				Amount:      onRampFee,
				Currency:    string(transfer.SourceCurrency),
				Description: "Reverse: credit on-ramp fee",
			},
			{
				AccountCode: domain.TenantAccountCode(slug, fmt.Sprintf("assets:bank:%s:clearing", strings.ToLower(string(transfer.SourceCurrency)))),
				EntryType:   string(domain.EntryTypeDebit),
				Amount:      transfer.SourceAmount,
				Currency:    string(transfer.SourceCurrency),
				Description: "Reverse: debit clearing account",
			},
		}
		reversePayload, err := json.Marshal(domain.LedgerPostPayload{
			TransferID:     transfer.ID,
			TenantID:       transfer.TenantID,
			IdempotencyKey: fmt.Sprintf("reverse-settle:%s", transfer.ID),
			Description:    fmt.Sprintf("Reverse settlement for transfer %s: %s", transfer.ID, result.Error),
			ReferenceType:  "reversal",
			Lines:          reversalLines,
		})
		if err != nil {
			return fmt.Errorf("settla-core: handle settlement result %s: marshalling reverse payload: %w", transferID, err)
		}

		entries, err := buildOutboxEntries(
			outboxResult(domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentTreasuryRelease, releasePayload)),
			outboxResult(domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentLedgerReverse, reversePayload)),
			outboxResult(domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID, domain.EventBlockchainFailed, transferEventPayload(transfer.ID, transfer.TenantID))),
		)
		if err != nil {
			return fmt.Errorf("settla-core: handle settlement result %s: building outbox entries: %w", transferID, err)
		}

		entries = setCorrelationID(entries, transfer.ID)

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
	ctx, span := engineTracer.Start(ctx, "Engine.HandleOffRampResult")
	defer span.End()
	span.SetAttributes(attribute.String("tenant_id", tenantID.String()), attribute.String("transfer_id", transferID.String()))
	// Idempotency: if transfer already advanced past OFF_RAMPING, this is a NATS replay — skip.
	precheck, pErr := e.loadTransfer(ctx, tenantID, transferID)
	if pErr != nil {
		return fmt.Errorf("settla-core: handle off-ramp result %s: %w", transferID, pErr)
	}
	if precheck.Status != domain.TransferStatusOffRamping {
		if !isAdvancedPast(precheck.Status, domain.TransferStatusOffRamping) {
			return domain.ErrInvalidTransition(string(precheck.Status), string(domain.TransferStatusOffRamping))
		}
		e.logger.Info("settla-core: skipping off-ramp result (NATS replay): transfer already advanced",
			"transfer_id", transferID, "current_status", precheck.Status, "expected_status", domain.TransferStatusOffRamping)
		return nil
	}

	if result.Success {
		return e.CompleteTransfer(ctx, tenantID, transferID)
	}

	transfer, err := e.loadTransferForStep(ctx, tenantID, transferID, domain.TransferStatusOffRamping)
	if err != nil {
		return fmt.Errorf("settla-core: handle off-ramp result %s: %w", transferID, err)
	}

	// Off-ramp failed — release treasury + reverse ledger + notify tenant
	tenant, tErr := e.tenantStore.GetTenant(ctx, transfer.TenantID)
	if tErr != nil {
		return fmt.Errorf("settla-core: handle off-ramp result %s: loading tenant for reversal: %w", transferID, tErr)
	}
	slug := tenant.Slug

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

	reversalLines := []domain.LedgerLineEntry{
		{
			AccountCode: fmt.Sprintf("assets:crypto:%s:%s", strings.ToLower(string(transfer.StableCoin)), strings.ToLower(string(transfer.Chain))),
			EntryType:   string(domain.EntryTypeCredit),
			Amount:      transfer.StableAmount,
			Currency:    string(transfer.StableCoin),
			Description: "Reverse: credit crypto asset",
		},
		{
			AccountCode: "expenses:provider:onramp",
			EntryType:   string(domain.EntryTypeCredit),
			Amount:      transfer.Fees.OnRampFee,
			Currency:    string(transfer.SourceCurrency),
			Description: "Reverse: credit on-ramp fee",
		},
		{
			AccountCode: domain.TenantAccountCode(slug, fmt.Sprintf("assets:bank:%s:clearing", strings.ToLower(string(transfer.SourceCurrency)))),
			EntryType:   string(domain.EntryTypeDebit),
			Amount:      transfer.SourceAmount,
			Currency:    string(transfer.SourceCurrency),
			Description: "Reverse: debit clearing account",
		},
	}

	reversePayload, err := json.Marshal(domain.LedgerPostPayload{
		TransferID:     transfer.ID,
		TenantID:       transfer.TenantID,
		IdempotencyKey: fmt.Sprintf("reverse-offramp:%s", transfer.ID),
		Description:    fmt.Sprintf("Reverse off-ramp for transfer %s: %s", transfer.ID, result.Error),
		ReferenceType:  "reversal",
		Lines:          reversalLines,
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

	entries, err := buildOutboxEntries(
		outboxResult(domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentTreasuryRelease, releasePayload)),
		outboxResult(domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentLedgerReverse, reversePayload)),
		outboxResult(domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentWebhookDeliver, webhookPayload)),
		outboxResult(domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID, domain.EventProviderOffRampFailed, transferEventPayload(transfer.ID, transfer.TenantID))),
	)
	if err != nil {
		return fmt.Errorf("settla-core: handle off-ramp result %s: building outbox entries: %w", transferID, err)
	}

	entries = setCorrelationID(entries, transfer.ID)

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
	transfer, err := e.loadTransferForStep(ctx, tenantID, transferID, domain.TransferStatusOffRamping)
	if err != nil {
		return fmt.Errorf("settla-core: complete transfer %s: %w", transferID, err)
	}

	location := fmt.Sprintf("bank:%s", strings.ToLower(string(transfer.SourceCurrency)))

	// Treasury consume intent — consume the reservation and debit the balance.
	// This atomically decrements both reservedMicro and balanceMicro because
	// money physically left the tenant's position (sent to recipient).
	consumePayload, err := json.Marshal(domain.TreasuryConsumePayload{
		TransferID: transfer.ID,
		TenantID:   transfer.TenantID,
		Currency:   transfer.SourceCurrency,
		Amount:     transfer.SourceAmount,
		Location:   location,
	})
	if err != nil {
		return fmt.Errorf("settla-core: complete transfer %s: marshalling consume payload: %w", transferID, err)
	}

	// Ledger post intent — final completion entries
	tenant, err := e.tenantStore.GetTenant(ctx, transfer.TenantID)
	if err != nil {
		return fmt.Errorf("settla-core: complete transfer %s: loading tenant: %w", transferID, err)
	}
	slug := tenant.Slug
	totalFees := transfer.Fees.TotalFeeUSD
	netAmount := transfer.SourceAmount.Sub(totalFees)

	// Validate net amount is positive before building ledger lines
	if !netAmount.IsPositive() {
		return fmt.Errorf("settla-core: complete transfer %s: net amount after fees must be positive, got %s (source %s - fees %s)",
			transferID, netAmount.String(), transfer.SourceAmount.String(), totalFees.String())
	}

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

	entries, err := buildOutboxEntries(
		outboxResult(domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentTreasuryConsume, consumePayload)),
		outboxResult(domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentLedgerPost, ledgerPayload)),
		outboxResult(domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentWebhookDeliver, webhookPayload)),
		outboxResult(domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID, domain.EventTransferCompleted, transferEventPayload(transfer.ID, transfer.TenantID))),
	)
	if err != nil {
		return fmt.Errorf("settla-core: complete transfer %s: building outbox entries: %w", transferID, err)
	}

	entries = setCorrelationID(entries, transfer.ID)

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

	entries, err := buildOutboxEntries(
		outboxResult(domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentTreasuryRelease, releasePayload)),
		outboxResult(domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentWebhookDeliver, webhookPayload)),
		outboxResult(domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID, domain.EventTransferFailed, transferEventPayload(transfer.ID, transfer.TenantID))),
	)
	if err != nil {
		return fmt.Errorf("settla-core: fail transfer %s: building outbox entries: %w", transferID, err)
	}

	entries = setCorrelationID(entries, transfer.ID)

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

	// Only allow refund from FUNDED or FAILED states
	if transfer.Status != domain.TransferStatusFunded && transfer.Status != domain.TransferStatusFailed {
		return fmt.Errorf("settla-core: refund transfer %s: %w",
			transferID, domain.ErrInvalidTransition(string(transfer.Status), string(domain.TransferStatusRefunding)))
	}

	if !transfer.CanTransitionTo(domain.TransferStatusRefunding) {
		return fmt.Errorf("settla-core: refund transfer %s: %w",
			transferID, domain.ErrInvalidTransition(string(transfer.Status), string(domain.TransferStatusRefunding)))
	}

	location := fmt.Sprintf("bank:%s", strings.ToLower(string(transfer.SourceCurrency)))

	// Build reversal ledger lines to undo any on-ramp posting that may exist.
	// If the transfer was only FUNDED (no on-ramp), the ledger worker handles
	// the empty-lines case gracefully (no-op reversal).
	var refundLines []domain.LedgerLineEntry
	if transfer.Status == domain.TransferStatusFailed && transfer.StableAmount.IsPositive() {
		// On-ramp was completed — reverse it
		tenant, tErr := e.tenantStore.GetTenant(ctx, transfer.TenantID)
		if tErr != nil {
			return fmt.Errorf("settla-core: refund transfer %s: loading tenant for reversal: %w", transferID, tErr)
		}
		slug := tenant.Slug
		refundLines = []domain.LedgerLineEntry{
			{
				AccountCode: fmt.Sprintf("assets:crypto:%s:%s", strings.ToLower(string(transfer.StableCoin)), strings.ToLower(string(transfer.Chain))),
				EntryType:   string(domain.EntryTypeCredit),
				Amount:      transfer.StableAmount,
				Currency:    string(transfer.StableCoin),
				Description: "Refund: reverse crypto asset",
			},
			{
				AccountCode: "expenses:provider:onramp",
				EntryType:   string(domain.EntryTypeCredit),
				Amount:      transfer.Fees.OnRampFee,
				Currency:    string(transfer.SourceCurrency),
				Description: "Refund: reverse on-ramp fee",
			},
			{
				AccountCode: domain.TenantAccountCode(slug, fmt.Sprintf("assets:bank:%s:clearing", strings.ToLower(string(transfer.SourceCurrency)))),
				EntryType:   string(domain.EntryTypeDebit),
				Amount:      transfer.SourceAmount,
				Currency:    string(transfer.SourceCurrency),
				Description: "Refund: debit clearing account",
			},
		}
	}

	reversePayload, err := json.Marshal(domain.LedgerPostPayload{
		TransferID:     transfer.ID,
		TenantID:       transfer.TenantID,
		IdempotencyKey: fmt.Sprintf("refund:%s", transfer.ID),
		Description:    fmt.Sprintf("Refund for transfer %s", transfer.ID),
		ReferenceType:  "reversal",
		Lines:          refundLines,
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

	entries, err := buildOutboxEntries(
		outboxResult(domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentLedgerReverse, reversePayload)),
		outboxResult(domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentTreasuryRelease, releasePayload)),
		outboxResult(domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID, domain.EventRefundInitiated, transferEventPayload(transfer.ID, transfer.TenantID))),
	)
	if err != nil {
		return fmt.Errorf("settla-core: refund transfer %s: building outbox entries: %w", transferID, err)
	}

	entries = setCorrelationID(entries, transfer.ID)

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
// On success: transitions REFUNDING → REFUNDED (terminal state).
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
			EventType:  domain.EventRefundCompleted,
			Data:       []byte(fmt.Sprintf(`{"reason":"refund_completed","transfer_id":"%s"}`, transfer.ID)),
		})
		if err != nil {
			return fmt.Errorf("settla-core: handle refund result %s: marshalling webhook payload: %w", transferID, err)
		}

		entries, err := buildOutboxEntries(
			outboxResult(domain.NewOutboxIntent("transfer", transfer.ID, transfer.TenantID, domain.IntentWebhookDeliver, webhookPayload)),
			outboxResult(domain.NewOutboxEvent("transfer", transfer.ID, transfer.TenantID, domain.EventRefundCompleted, transferEventPayload(transfer.ID, transfer.TenantID))),
		)
		if err != nil {
			return fmt.Errorf("settla-core: handle refund result %s: building outbox entries: %w", transferID, err)
		}

		entries = setCorrelationID(entries, transfer.ID)

		if err := e.transferStore.TransitionWithOutbox(ctx, transfer.ID, domain.TransferStatusRefunded, transfer.Version, entries); err != nil {
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

// buildOutboxEntries collects outbox entry results and returns the first error encountered.
func buildOutboxEntries(results ...struct {
	entry domain.OutboxEntry
	err   error
}) ([]domain.OutboxEntry, error) {
	entries := make([]domain.OutboxEntry, 0, len(results))
	for _, r := range results {
		if r.err != nil {
			return nil, r.err
		}
		entries = append(entries, r.entry)
	}
	return entries, nil
}

// outboxResult wraps a (OutboxEntry, error) return into a struct for buildOutboxEntries.
func outboxResult(entry domain.OutboxEntry, err error) struct {
	entry domain.OutboxEntry
	err   error
} {
	return struct {
		entry domain.OutboxEntry
		err   error
	}{entry: entry, err: err}
}

// validateLedgerLines converts outbox LedgerLineEntry items to domain EntryLine
// and validates that debits equal credits before the entries are queued.
func validateLedgerLines(lines []domain.LedgerLineEntry) error {
	entryLines := make([]domain.EntryLine, len(lines))
	for i, l := range lines {
		entryLines[i] = domain.EntryLine{
			ID: uuid.Must(uuid.NewV7()),
			Posting: domain.Posting{
				AccountCode: domain.AccountCode(l.AccountCode),
				EntryType:   domain.EntryType(l.EntryType),
				Amount:      l.Amount,
				Currency:    domain.Currency(l.Currency),
			},
		}
	}
	return domain.ValidateEntries(entryLines)
}

// setCorrelationID sets the CorrelationID on all outbox entries to the transfer ID,
// enabling end-to-end tracing of multi-step flows across partition boundaries.
func setCorrelationID(entries []domain.OutboxEntry, id uuid.UUID) []domain.OutboxEntry {
	for i := range entries {
		entries[i] = entries[i].WithCorrelationID(id)
	}
	return entries
}

// getDailyVolume returns the current daily volume for a tenant.
// If a DailyVolumeCounter is configured (e.g. Redis), it uses the atomic counter.
// Otherwise falls back to the in-memory sync.Map cache with DB refresh.
func (e *Engine) getDailyVolume(ctx context.Context, tenantID uuid.UUID, today time.Time) (decimal.Decimal, error) {
	// Atomic counter path (Redis-backed)
	if e.dailyVolumeCounter != nil {
		vol, err := e.dailyVolumeCounter.GetDailyVolume(ctx, tenantID, today)
		if err != nil {
			e.logger.Warn("settla-core: daily volume counter unavailable, falling back to DB",
				"tenant_id", tenantID, "error", err)
			// Fall through to DB query below
		} else if !vol.IsZero() {
			return vol, nil
		}
		// Key doesn't exist yet — seed from DB
		dbVol, dbErr := e.transferStore.GetDailyVolume(ctx, tenantID, today)
		if dbErr != nil {
			return decimal.Zero, dbErr
		}
		// Seed atomically (only if key still doesn't exist)
		if _, seedErr := e.dailyVolumeCounter.SeedDailyVolume(ctx, tenantID, today, dbVol); seedErr != nil {
			e.logger.Warn("settla-core: failed to seed daily volume counter",
				"tenant_id", tenantID, "error", seedErr)
		}
		return dbVol, nil
	}

	// Fallback: approximate in-memory cache. WARNING: this path is NOT race-free.
	// Two concurrent CreateTransfer calls may both read stale volume and both
	// proceed, under-counting the daily total. Configure a DailyVolumeCounter
	// (Redis-backed) via WithDailyVolumeCounter() for production enforcement.
	if e.requireDailyVolumeCounter {
		return decimal.Zero, fmt.Errorf("settla-core: daily volume counter required but not configured")
	}
	e.dailyVolumeWarnOnce.Do(func() {
		e.logger.Warn("settla-core: using non-atomic sync.Map fallback for daily volume tracking; configure DailyVolumeCounter for production")
	})
	cacheKey := tenantID.String() + ":" + today.Format("2006-01-02")
	if cached, ok := e.dailyVolumeCache.Load(cacheKey); ok {
		entry := cached.(dailyVolumeEntry)
		if time.Now().Before(entry.expiresAt) {
			return entry.volume, nil
		}
		e.dailyVolumeCache.Delete(cacheKey)
	}
	vol, err := e.transferStore.GetDailyVolume(ctx, tenantID, today)
	if err != nil {
		return decimal.Zero, err
	}
	e.dailyVolumeCache.Store(cacheKey, dailyVolumeEntry{volume: vol, expiresAt: time.Now().Add(dailyVolumeCacheTTL)})
	return vol, nil
}

// incrDailyVolume atomically increments the daily volume counter after a
// successful transfer creation. Best-effort: errors are logged but do not
// fail the transfer.
func (e *Engine) incrDailyVolume(ctx context.Context, tenantID uuid.UUID, today time.Time, amount decimal.Decimal) {
	if e.dailyVolumeCounter != nil {
		if _, err := e.dailyVolumeCounter.IncrDailyVolume(ctx, tenantID, today, amount); err != nil {
			e.logger.Warn("settla-core: failed to increment daily volume counter",
				"tenant_id", tenantID, "error", err)
		}
		return
	}
	// Fallback: update in-memory cache.
	// NOTE: This Load+Store is NOT atomic — two concurrent goroutines may both
	// read the same volume and each add their own amount, effectively losing one
	// increment. This is acceptable for the approximate fallback; use
	// DailyVolumeCounter (Redis INCRBYFLOAT) for exact enforcement.
	cacheKey := tenantID.String() + ":" + today.Format("2006-01-02")
	if cached, ok := e.dailyVolumeCache.Load(cacheKey); ok {
		entry := cached.(dailyVolumeEntry)
		e.dailyVolumeCache.Store(cacheKey, dailyVolumeEntry{
			volume:    entry.volume.Add(amount),
			expiresAt: entry.expiresAt,
		})
	}
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

// advancedPastStates maps each expected status to the set of statuses that
// indicate the transfer has already moved forward in the happy path. These
// are the only statuses for which a NATS replay should be silently skipped.
var advancedPastStates = map[domain.TransferStatus]map[domain.TransferStatus]bool{
	domain.TransferStatusOnRamping: {
		domain.TransferStatusSettling:   true,
		domain.TransferStatusOffRamping: true,
	},
	domain.TransferStatusSettling: {
		domain.TransferStatusOffRamping: true,
	},
}

// isAdvancedPast returns true if currentStatus indicates the transfer has
// already moved past expectedStatus in the happy-path lifecycle. This is
// used to distinguish legitimate NATS replays (skip silently) from invalid
// operations on terminal or unrelated states (return error).
func isAdvancedPast(currentStatus, expectedStatus domain.TransferStatus) bool {
	past, ok := advancedPastStates[expectedStatus]
	if !ok {
		return false
	}
	return past[currentStatus]
}
