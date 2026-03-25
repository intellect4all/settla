package paymentlink

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	depositcore "github.com/intellect4all/settla/core/deposit"
	"github.com/intellect4all/settla/domain"
)

// NanoID alphabet: URL-safe, unambiguous characters (no lookalikes like 0/O, l/1).
const shortCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
const shortCodeLength = 12
const maxShortCodeAttempts = 3


// PaymentLinkStore defines the persistence interface for payment links.
type PaymentLinkStore interface {
	Create(ctx context.Context, link *domain.PaymentLink) error
	GetByID(ctx context.Context, tenantID, linkID uuid.UUID) (*domain.PaymentLink, error)
	GetByShortCode(ctx context.Context, shortCode string) (*domain.PaymentLink, error)
	List(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]domain.PaymentLink, int64, error)
	IncrementUseCount(ctx context.Context, linkID uuid.UUID) error
	UpdateStatus(ctx context.Context, tenantID, linkID uuid.UUID, status domain.PaymentLinkStatus) error
}

// TenantStore is the subset of tenant operations needed by the service.
type TenantStore interface {
	GetTenant(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error)
}

// Service manages payment link CRUD and redemption.
type Service struct {
	store         PaymentLinkStore
	depositEngine *depositcore.Engine
	tenantStore   TenantStore
	logger        *slog.Logger
	baseURL       string // e.g. "https://pay.settla.io/p"
}

// NewService creates a payment link service.
func NewService(
	store PaymentLinkStore,
	depositEngine *depositcore.Engine,
	tenantStore TenantStore,
	logger *slog.Logger,
	baseURL string,
) *Service {
	return &Service{
		store:         store,
		depositEngine: depositEngine,
		tenantStore:   tenantStore,
		logger:        logger.With("module", "core.paymentlink"),
		baseURL:       baseURL,
	}
}

// CreateRequest is the input for creating a new payment link.
type CreateRequest struct {
	Description string
	RedirectURL string
	UseLimit    *int
	ExpiresAt   *int64 // Unix timestamp, optional

	// Session template
	Amount         decimal.Decimal
	Currency       domain.Currency
	Chain          domain.CryptoChain
	Token          string
	SettlementPref domain.SettlementPreference
	TTLSeconds     int32
}

// CreateResult is the output of creating a payment link.
type CreateResult struct {
	Link *domain.PaymentLink
	URL  string
}

// Create validates the tenant, generates a short code, and persists the link.
func (s *Service) Create(ctx context.Context, tenantID uuid.UUID, req CreateRequest) (*CreateResult, error) {
	// Validate tenant is active and crypto-enabled
	tenant, err := s.tenantStore.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("settla-paymentlink: create: loading tenant %s: %w", tenantID, err)
	}
	if !tenant.IsActive() {
		return nil, domain.ErrTenantSuspended(tenantID.String())
	}
	if !tenant.CryptoConfig.CryptoEnabled {
		return nil, domain.ErrCryptoDisabled(tenantID.String())
	}

	// Validate amount
	if !req.Amount.IsPositive() {
		return nil, domain.ErrAmountTooLow(req.Amount.String(), "0")
	}

	var link *domain.PaymentLink
	var lastErr error

	for attempt := range maxShortCodeAttempts {
		shortCode, err := generateShortCode()
		if err != nil {
			return nil, fmt.Errorf("settla-paymentlink: create: generating short code: %w", err)
		}

		link = &domain.PaymentLink{
			TenantID:    tenantID,
			ShortCode:   shortCode,
			Description: req.Description,
			RedirectURL: req.RedirectURL,
			Status:      domain.PaymentLinkStatusActive,
			UseLimit:    req.UseLimit,
			SessionConfig: domain.PaymentLinkSessionConfig{
				Amount:         req.Amount,
				Currency:       req.Currency,
				Chain:          req.Chain,
				Token:          req.Token,
				SettlementPref: req.SettlementPref,
				TTLSeconds:     req.TTLSeconds,
			},
		}

		if req.ExpiresAt != nil {
			t := time.Unix(*req.ExpiresAt, 0).UTC()
			link.ExpiresAt = &t
		}

		lastErr = s.store.Create(ctx, link)
		if lastErr == nil {
			break
		}

		if !domain.IsShortCodeCollision(lastErr) {
			return nil, fmt.Errorf("settla-paymentlink: create: persisting: %w", lastErr)
		}

		s.logger.Warn("settla-paymentlink: short code collision, retrying",
			"attempt", attempt+1,
			"tenant_id", tenantID,
		)
	}

	if lastErr != nil {
		return nil, fmt.Errorf("settla-paymentlink: create: failed to generate unique short code after %d attempts", maxShortCodeAttempts)
	}

	url := fmt.Sprintf("%s/%s", s.baseURL, link.ShortCode)

	s.logger.Info("settla-paymentlink: link created",
		"link_id", link.ID,
		"tenant_id", tenantID,
		"short_code", link.ShortCode,
	)

	return &CreateResult{Link: link, URL: url}, nil
}

// Resolve loads a payment link by short code and validates it can be redeemed.
func (s *Service) Resolve(ctx context.Context, shortCode string) (*domain.PaymentLink, error) {
	link, err := s.store.GetByShortCode(ctx, shortCode)
	if err != nil {
		return nil, fmt.Errorf("settla-paymentlink: resolve: %w", err)
	}
	if link == nil {
		return nil, domain.ErrPaymentLinkNotFound(shortCode)
	}

	if err := link.CanRedeem(); err != nil {
		return nil, err
	}

	return link, nil
}

// RedeemResult is the output of redeeming a payment link.
type RedeemResult struct {
	Session *domain.DepositSession
	Link    *domain.PaymentLink
}

// Redeem resolves the link, creates a deposit session from the template, and increments use_count.
func (s *Service) Redeem(ctx context.Context, shortCode string) (*RedeemResult, error) {
	link, err := s.Resolve(ctx, shortCode)
	if err != nil {
		return nil, err
	}

	// Create deposit session from link template
	idempotencyKey, err := domain.NewIdempotencyKey(fmt.Sprintf("plink:%s:%d", link.ID, link.UseCount+1))
	if err != nil {
		return nil, fmt.Errorf("settla-paymentlink: redeem: generating idempotency key: %w", err)
	}

	session, err := s.depositEngine.CreateSession(ctx, link.TenantID, depositcore.CreateSessionRequest{
		IdempotencyKey: idempotencyKey,
		Chain:          link.SessionConfig.Chain,
		Token:          link.SessionConfig.Token,
		ExpectedAmount: link.SessionConfig.Amount,
		SettlementPref: link.SessionConfig.SettlementPref,
		TTLSeconds:     link.SessionConfig.TTLSeconds,
	})
	if err != nil {
		return nil, fmt.Errorf("settla-paymentlink: redeem: creating deposit session: %w", err)
	}

	// Increment use count
	if err := s.store.IncrementUseCount(ctx, link.ID); err != nil {
		s.logger.Warn("settla-paymentlink: redeem: failed to increment use count",
			"link_id", link.ID,
			"error", err,
		)
	}

	s.logger.Info("settla-paymentlink: link redeemed",
		"link_id", link.ID,
		"session_id", session.ID,
		"short_code", shortCode,
	)

	return &RedeemResult{Session: session, Link: link}, nil
}

// Get retrieves a payment link by tenant and ID.
func (s *Service) Get(ctx context.Context, tenantID, linkID uuid.UUID) (*domain.PaymentLink, error) {
	link, err := s.store.GetByID(ctx, tenantID, linkID)
	if err != nil {
		return nil, fmt.Errorf("settla-paymentlink: get %s: %w", linkID, err)
	}
	if link == nil {
		return nil, domain.ErrPaymentLinkNotFound(linkID.String())
	}
	return link, nil
}

// ListResult contains paginated payment links.
type ListResult struct {
	Links []domain.PaymentLink
	Total int64
}

// List retrieves payment links for a tenant with pagination.
func (s *Service) List(ctx context.Context, tenantID uuid.UUID, limit, offset int) (*ListResult, error) {
	links, total, err := s.store.List(ctx, tenantID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("settla-paymentlink: list: %w", err)
	}
	return &ListResult{Links: links, Total: total}, nil
}

// Disable sets a payment link to DISABLED status.
func (s *Service) Disable(ctx context.Context, tenantID, linkID uuid.UUID) error {
	link, err := s.store.GetByID(ctx, tenantID, linkID)
	if err != nil {
		return fmt.Errorf("settla-paymentlink: disable %s: %w", linkID, err)
	}
	if link == nil {
		return domain.ErrPaymentLinkNotFound(linkID.String())
	}

	if err := s.store.UpdateStatus(ctx, tenantID, linkID, domain.PaymentLinkStatusDisabled); err != nil {
		return fmt.Errorf("settla-paymentlink: disable %s: %w", linkID, err)
	}

	s.logger.Info("settla-paymentlink: link disabled",
		"link_id", linkID,
		"tenant_id", tenantID,
	)
	return nil
}

// generateShortCode generates a cryptographically random short code.
func generateShortCode() (string, error) {
	alphabetLen := big.NewInt(int64(len(shortCodeAlphabet)))
	code := make([]byte, shortCodeLength)
	for i := range code {
		n, err := rand.Int(rand.Reader, alphabetLen)
		if err != nil {
			return "", fmt.Errorf("generating random index: %w", err)
		}
		code[i] = shortCodeAlphabet[n.Int64()]
	}
	return string(code), nil
}
