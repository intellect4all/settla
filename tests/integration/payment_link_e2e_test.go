//go:build integration

package integration

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	depositcore "github.com/intellect4all/settla/core/deposit"
	paymentlinkcore "github.com/intellect4all/settla/core/paymentlink"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/observability"
)


type memPaymentLinkStore struct {
	mu            sync.RWMutex
	links         map[uuid.UUID]*domain.PaymentLink
	linksByCode   map[string]uuid.UUID
	linksByTenant map[uuid.UUID][]uuid.UUID // tenantID → linkIDs (ordered by creation)
}

var _ paymentlinkcore.PaymentLinkStore = (*memPaymentLinkStore)(nil)

func newMemPaymentLinkStore() *memPaymentLinkStore {
	return &memPaymentLinkStore{
		links:         make(map[uuid.UUID]*domain.PaymentLink),
		linksByCode:   make(map[string]uuid.UUID),
		linksByTenant: make(map[uuid.UUID][]uuid.UUID),
	}
}

func (s *memPaymentLinkStore) Create(ctx context.Context, link *domain.PaymentLink) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if link.ID == uuid.Nil {
		link.ID = uuid.New()
	}
	now := time.Now().UTC()
	link.CreatedAt = now
	link.UpdatedAt = now

	s.links[link.ID] = link
	s.linksByCode[link.ShortCode] = link.ID
	s.linksByTenant[link.TenantID] = append(s.linksByTenant[link.TenantID], link.ID)
	return nil
}

func (s *memPaymentLinkStore) GetByID(ctx context.Context, tenantID, linkID uuid.UUID) (*domain.PaymentLink, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	link, ok := s.links[linkID]
	if !ok || link.TenantID != tenantID {
		return nil, nil
	}
	cp := *link
	return &cp, nil
}

func (s *memPaymentLinkStore) GetByShortCode(ctx context.Context, shortCode string) (*domain.PaymentLink, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	linkID, ok := s.linksByCode[shortCode]
	if !ok {
		return nil, nil
	}
	link := s.links[linkID]
	cp := *link
	return &cp, nil
}

func (s *memPaymentLinkStore) List(ctx context.Context, tenantID uuid.UUID, limit, offset int) ([]domain.PaymentLink, int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ids := s.linksByTenant[tenantID]
	total := int64(len(ids))

	if offset >= len(ids) {
		return nil, total, nil
	}
	end := offset + limit
	if end > len(ids) {
		end = len(ids)
	}

	var result []domain.PaymentLink
	for _, id := range ids[offset:end] {
		result = append(result, *s.links[id])
	}
	return result, total, nil
}

func (s *memPaymentLinkStore) ListCursor(ctx context.Context, tenantID uuid.UUID, pageSize int, cursor time.Time) ([]domain.PaymentLink, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []domain.PaymentLink
	for _, id := range s.linksByTenant[tenantID] {
		link := s.links[id]
		if link.CreatedAt.Before(cursor) {
			result = append(result, *link)
		}
	}
	if len(result) > pageSize {
		result = result[:pageSize]
	}
	return result, nil
}

func (s *memPaymentLinkStore) IncrementUseCount(ctx context.Context, linkID uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	link, ok := s.links[linkID]
	if !ok {
		return fmt.Errorf("payment link %s not found", linkID)
	}
	link.UseCount++
	link.UpdatedAt = time.Now().UTC()
	return nil
}

func (s *memPaymentLinkStore) UpdateStatus(ctx context.Context, tenantID, linkID uuid.UUID, status domain.PaymentLinkStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	link, ok := s.links[linkID]
	if !ok || link.TenantID != tenantID {
		return fmt.Errorf("payment link %s not found for tenant %s", linkID, tenantID)
	}
	link.Status = status
	link.UpdatedAt = time.Now().UTC()
	return nil
}

// getLinkDirect returns the link without copying (for assertions).
func (s *memPaymentLinkStore) getLinkDirect(linkID uuid.UUID) *domain.PaymentLink {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.links[linkID]
}


type paymentLinkTestHarness struct {
	Service          *paymentlinkcore.Service
	PaymentLinkStore *memPaymentLinkStore
	DepositStore     *memDepositStore
	TenantStore      *memTenantStore
	DepositEngine    *depositcore.Engine
}

func newPaymentLinkTestHarness(t *testing.T) *paymentLinkTestHarness {
	t.Helper()

	logger := observability.NewLogger("settla-paymentlink-integration-test", "test")

	// Pre-seed address pool
	addresses := make([]domain.CryptoAddressPool, 20)
	for i := range addresses {
		addresses[i] = domain.CryptoAddressPool{
			ID:              uuid.New(),
			Chain:           "tron",
			Address:         fmt.Sprintf("TPayLinkAddr%04d", i+1),
			DerivationIndex: int64(i),
			Dispensed:       false,
			CreatedAt:       time.Now().UTC(),
		}
	}

	depositStore := newMemDepositStore(addresses)
	tenantStore := newMemTenantStore()
	paymentLinkStore := newMemPaymentLinkStore()

	// Seed tenant with crypto config
	now := time.Now().UTC()
	kybVerified := now.Add(-30 * 24 * time.Hour)
	tenantStore.addTenant(&domain.Tenant{
		ID:              LemfiTenantID,
		Name:            "Lemfi",
		Slug:            "lemfi",
		Status:          domain.TenantStatusActive,
		KYBStatus:       domain.KYBStatusVerified,
		KYBVerifiedAt:   &kybVerified,
		SettlementModel: domain.SettlementModelPrefunded,
		FeeSchedule: domain.FeeSchedule{
			OnRampBPS:                 40,
			OffRampBPS:                35,
			CryptoCollectionBPS:       30,
			CryptoCollectionMaxFeeUSD: decimal.NewFromInt(50),
		},
		DailyLimitUSD:    decimal.NewFromInt(10_000_000),
		PerTransferLimit: decimal.NewFromInt(1_000_000),
		CryptoConfig: domain.TenantCryptoConfig{
			CryptoEnabled:         true,
			DefaultSettlementPref: domain.SettlementPreferenceAutoConvert,
			SupportedChains:       []string{"tron"},
			MinConfirmationsTron:  19,
			PaymentToleranceBPS:   50,
			DefaultSessionTTLSecs: 3600,
		},
		CreatedAt: now,
		UpdatedAt: now,
	})

	// Seed a second tenant (Fincra) with crypto disabled for negative tests
	tenantStore.addTenant(&domain.Tenant{
		ID:              FincraTenantID,
		Name:            "Fincra",
		Slug:            "fincra",
		Status:          domain.TenantStatusActive,
		KYBStatus:       domain.KYBStatusVerified,
		KYBVerifiedAt:   &kybVerified,
		SettlementModel: domain.SettlementModelPrefunded,
		FeeSchedule: domain.FeeSchedule{
			OnRampBPS:  25,
			OffRampBPS: 20,
		},
		DailyLimitUSD:    decimal.NewFromInt(5_000_000),
		PerTransferLimit: decimal.NewFromInt(500_000),
		CryptoConfig: domain.TenantCryptoConfig{
			CryptoEnabled: false,
		},
		CreatedAt: now,
		UpdatedAt: now,
	})

	depositEngine := depositcore.NewEngine(depositStore, tenantStore, logger)

	svc := paymentlinkcore.NewService(
		paymentLinkStore,
		depositEngine,
		tenantStore,
		logger,
		"https://pay.settla.io/p",
	)

	return &paymentLinkTestHarness{
		Service:          svc,
		PaymentLinkStore: paymentLinkStore,
		DepositStore:     depositStore,
		TenantStore:      tenantStore,
		DepositEngine:    depositEngine,
	}
}


func TestPaymentLinkE2E_HappyPath(t *testing.T) {
	h := newPaymentLinkTestHarness(t)
	ctx := context.Background()

	// 1. Create payment link
	result, err := h.Service.Create(ctx, LemfiTenantID, paymentlinkcore.CreateRequest{
		Description: "Invoice #1001",
		RedirectURL: "https://merchant.com/thanks",
		Amount:      decimal.NewFromInt(100),
		Currency:    "USDT",
		Chain:       "tron",
		Token:       "USDT",
		TTLSeconds:  3600,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if result.Link == nil {
		t.Fatal("expected link to be non-nil")
	}
	if result.Link.Status != domain.PaymentLinkStatusActive {
		t.Fatalf("expected ACTIVE, got %s", result.Link.Status)
	}
	if result.Link.ShortCode == "" {
		t.Fatal("expected short code to be generated")
	}
	if len(result.Link.ShortCode) != 12 {
		t.Fatalf("expected 12-char short code, got %d", len(result.Link.ShortCode))
	}
	if result.URL == "" {
		t.Fatal("expected URL to be set")
	}
	expectedURL := fmt.Sprintf("https://pay.settla.io/p/%s", result.Link.ShortCode)
	if result.URL != expectedURL {
		t.Fatalf("expected URL %s, got %s", expectedURL, result.URL)
	}
	if result.Link.Description != "Invoice #1001" {
		t.Fatalf("expected description 'Invoice #1001', got %q", result.Link.Description)
	}
	if result.Link.UseCount != 0 {
		t.Fatalf("expected use_count 0, got %d", result.Link.UseCount)
	}

	shortCode := result.Link.ShortCode
	linkID := result.Link.ID

	// 2. Resolve payment link (public)
	link, err := h.Service.Resolve(ctx, shortCode)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if link.ID != linkID {
		t.Fatalf("resolved link ID mismatch: %s != %s", link.ID, linkID)
	}

	// 3. Redeem payment link → creates deposit session
	redeemResult, err := h.Service.Redeem(ctx, shortCode)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if redeemResult.Session == nil {
		t.Fatal("expected deposit session to be created")
	}
	if redeemResult.Session.Status != domain.DepositSessionStatusPendingPayment {
		t.Fatalf("expected session status PENDING_PAYMENT, got %s", redeemResult.Session.Status)
	}
	if redeemResult.Session.DepositAddress == "" {
		t.Fatal("expected deposit address to be assigned")
	}
	if !redeemResult.Session.ExpectedAmount.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("expected session amount 100, got %s", redeemResult.Session.ExpectedAmount)
	}
	if redeemResult.Session.Chain != "tron" {
		t.Fatalf("expected chain tron, got %s", redeemResult.Session.Chain)
	}
	if redeemResult.Session.Token != "USDT" {
		t.Fatalf("expected token USDT, got %s", redeemResult.Session.Token)
	}

	// 4. Verify use_count was incremented
	stored := h.PaymentLinkStore.getLinkDirect(linkID)
	if stored.UseCount != 1 {
		t.Fatalf("expected use_count 1 after redeem, got %d", stored.UseCount)
	}

	// 5. Verify deposit session outbox entries
	entries := h.DepositStore.drainOutbox()
	if !outboxContains(entries, domain.IntentMonitorAddress) {
		t.Error("expected IntentMonitorAddress in outbox after redeem")
	}
	if !outboxContains(entries, domain.EventDepositSessionCreated) {
		t.Error("expected EventDepositSessionCreated in outbox after redeem")
	}

	// 6. Verify the deposit session can progress through the full lifecycle
	sessionID := redeemResult.Session.ID
	depositAddr := redeemResult.Session.DepositAddress

	// Detect transaction
	err = h.DepositEngine.HandleTransactionDetected(ctx, LemfiTenantID, sessionID, domain.IncomingTransaction{
		Chain:       "tron",
		TxHash:      "0xplink_tx_1",
		FromAddress: "TCustomerAddr",
		ToAddress:   depositAddr,
		Amount:      decimal.NewFromInt(100),
		BlockNumber: 60_000_000,
		BlockHash:   "0xblock_plink_1",
		Timestamp:   time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("HandleTransactionDetected: %v", err)
	}

	session, _ := h.DepositEngine.GetSession(ctx, LemfiTenantID, sessionID)
	if session.Status != domain.DepositSessionStatusDetected {
		t.Fatalf("expected DETECTED, got %s", session.Status)
	}

	// Confirm transaction
	err = h.DepositEngine.HandleTransactionConfirmed(ctx, LemfiTenantID, sessionID, "0xplink_tx_1", 19)
	if err != nil {
		t.Fatalf("HandleTransactionConfirmed: %v", err)
	}

	session, _ = h.DepositEngine.GetSession(ctx, LemfiTenantID, sessionID)
	if session.Status != domain.DepositSessionStatusCrediting {
		t.Fatalf("expected CREDITING, got %s", session.Status)
	}

	// Credit
	err = h.DepositEngine.HandleCreditResult(ctx, LemfiTenantID, sessionID, domain.IntentResult{Success: true})
	if err != nil {
		t.Fatalf("HandleCreditResult: %v", err)
	}

	session, _ = h.DepositEngine.GetSession(ctx, LemfiTenantID, sessionID)
	if session.Status != domain.DepositSessionStatusSettling {
		t.Fatalf("expected SETTLING, got %s", session.Status)
	}

	// Settle
	err = h.DepositEngine.HandleSettlementResult(ctx, LemfiTenantID, sessionID, domain.IntentResult{Success: true})
	if err != nil {
		t.Fatalf("HandleSettlementResult: %v", err)
	}

	session, _ = h.DepositEngine.GetSession(ctx, LemfiTenantID, sessionID)
	if session.Status != domain.DepositSessionStatusSettled {
		t.Fatalf("expected SETTLED, got %s", session.Status)
	}
}


func TestPaymentLinkE2E_UseLimit(t *testing.T) {
	h := newPaymentLinkTestHarness(t)
	ctx := context.Background()

	useLimit := 2
	result, err := h.Service.Create(ctx, LemfiTenantID, paymentlinkcore.CreateRequest{
		Amount:   decimal.NewFromInt(50),
		Currency: "USDT",
		Chain:    "tron",
		Token:    "USDT",
		UseLimit: &useLimit,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	shortCode := result.Link.ShortCode

	// First redeem — should succeed
	_, err = h.Service.Redeem(ctx, shortCode)
	if err != nil {
		t.Fatalf("Redeem 1: %v", err)
	}
	h.DepositStore.drainOutbox()

	// Second redeem — should succeed (use_count=1, limit=2)
	_, err = h.Service.Redeem(ctx, shortCode)
	if err != nil {
		t.Fatalf("Redeem 2: %v", err)
	}
	h.DepositStore.drainOutbox()

	// Third redeem — should fail (use_count=2, limit=2)
	_, err = h.Service.Redeem(ctx, shortCode)
	if err == nil {
		t.Fatal("expected error on third redeem (use limit exhausted)")
	}

	// Verify it's the right error
	domainErr, ok := err.(*domain.DomainError)
	if !ok {
		t.Fatalf("expected domain.DomainError, got %T: %v", err, err)
	}
	if domainErr.Code() != domain.CodePaymentLinkExhausted {
		t.Fatalf("expected CodePaymentLinkExhausted, got %s", domainErr.Code())
	}

	// Verify use_count
	stored := h.PaymentLinkStore.getLinkDirect(result.Link.ID)
	if stored.UseCount != 2 {
		t.Fatalf("expected use_count 2, got %d", stored.UseCount)
	}
}


func TestPaymentLinkE2E_Expired(t *testing.T) {
	h := newPaymentLinkTestHarness(t)
	ctx := context.Background()

	// Create link with expiry in the past
	pastUnix := time.Now().Add(-1 * time.Hour).Unix()
	result, err := h.Service.Create(ctx, LemfiTenantID, paymentlinkcore.CreateRequest{
		Amount:    decimal.NewFromInt(100),
		Currency:  "USDT",
		Chain:     "tron",
		Token:     "USDT",
		ExpiresAt: &pastUnix,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Resolve should fail — link expired
	_, err = h.Service.Resolve(ctx, result.Link.ShortCode)
	if err == nil {
		t.Fatal("expected error resolving expired link")
	}
	domainErr, ok := err.(*domain.DomainError)
	if !ok {
		t.Fatalf("expected domain.DomainError, got %T: %v", err, err)
	}
	if domainErr.Code() != domain.CodePaymentLinkExpired {
		t.Fatalf("expected CodePaymentLinkExpired, got %s", domainErr.Code())
	}

	// Redeem should also fail
	_, err = h.Service.Redeem(ctx, result.Link.ShortCode)
	if err == nil {
		t.Fatal("expected error redeeming expired link")
	}
}


func TestPaymentLinkE2E_Disabled(t *testing.T) {
	h := newPaymentLinkTestHarness(t)
	ctx := context.Background()

	result, err := h.Service.Create(ctx, LemfiTenantID, paymentlinkcore.CreateRequest{
		Amount:   decimal.NewFromInt(100),
		Currency: "USDT",
		Chain:    "tron",
		Token:    "USDT",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Disable the link
	err = h.Service.Disable(ctx, LemfiTenantID, result.Link.ID)
	if err != nil {
		t.Fatalf("Disable: %v", err)
	}

	// Verify status changed
	stored := h.PaymentLinkStore.getLinkDirect(result.Link.ID)
	if stored.Status != domain.PaymentLinkStatusDisabled {
		t.Fatalf("expected DISABLED, got %s", stored.Status)
	}

	// Resolve should fail
	_, err = h.Service.Resolve(ctx, result.Link.ShortCode)
	if err == nil {
		t.Fatal("expected error resolving disabled link")
	}
	domainErr, ok := err.(*domain.DomainError)
	if !ok {
		t.Fatalf("expected domain.DomainError, got %T: %v", err, err)
	}
	if domainErr.Code() != domain.CodePaymentLinkDisabled {
		t.Fatalf("expected CodePaymentLinkDisabled, got %s", domainErr.Code())
	}

	// Redeem should also fail
	_, err = h.Service.Redeem(ctx, result.Link.ShortCode)
	if err == nil {
		t.Fatal("expected error redeeming disabled link")
	}
}


func TestPaymentLinkE2E_NotFound(t *testing.T) {
	h := newPaymentLinkTestHarness(t)
	ctx := context.Background()

	_, err := h.Service.Resolve(ctx, "nonexistent12")
	if err == nil {
		t.Fatal("expected error resolving nonexistent link")
	}
	domainErr, ok := err.(*domain.DomainError)
	if !ok {
		t.Fatalf("expected domain.DomainError, got %T: %v", err, err)
	}
	if domainErr.Code() != domain.CodePaymentLinkNotFound {
		t.Fatalf("expected CodePaymentLinkNotFound, got %s", domainErr.Code())
	}
}


func TestPaymentLinkE2E_TenantIsolation(t *testing.T) {
	h := newPaymentLinkTestHarness(t)
	ctx := context.Background()

	// Create link for Lemfi
	result, err := h.Service.Create(ctx, LemfiTenantID, paymentlinkcore.CreateRequest{
		Amount:   decimal.NewFromInt(100),
		Currency: "USDT",
		Chain:    "tron",
		Token:    "USDT",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Get by ID with correct tenant — should succeed
	link, err := h.Service.Get(ctx, LemfiTenantID, result.Link.ID)
	if err != nil {
		t.Fatalf("Get with correct tenant: %v", err)
	}
	if link.ID != result.Link.ID {
		t.Fatal("expected to get the correct link")
	}

	// Get by ID with wrong tenant — should fail
	_, err = h.Service.Get(ctx, FincraTenantID, result.Link.ID)
	if err == nil {
		t.Fatal("expected error getting link with wrong tenant")
	}

	// Disable with wrong tenant — should fail
	err = h.Service.Disable(ctx, FincraTenantID, result.Link.ID)
	if err != nil {
		// This is expected — the store won't find it for wrong tenant
	}

	// Link should still be active
	stored := h.PaymentLinkStore.getLinkDirect(result.Link.ID)
	if stored.Status != domain.PaymentLinkStatusActive {
		t.Fatal("link should still be ACTIVE after disable attempt with wrong tenant")
	}

	// List for Lemfi — should return 1
	listResult, err := h.Service.List(ctx, LemfiTenantID, 20, 0)
	if err != nil {
		t.Fatalf("List Lemfi: %v", err)
	}
	if listResult.Total != 1 {
		t.Fatalf("expected 1 link for Lemfi, got %d", listResult.Total)
	}

	// List for Fincra — should return 0
	listResult, err = h.Service.List(ctx, FincraTenantID, 20, 0)
	if err != nil {
		t.Fatalf("List Fincra: %v", err)
	}
	if listResult.Total != 0 {
		t.Fatalf("expected 0 links for Fincra, got %d", listResult.Total)
	}
}


func TestPaymentLinkE2E_CryptoDisabledTenant(t *testing.T) {
	h := newPaymentLinkTestHarness(t)
	ctx := context.Background()

	// Fincra has crypto disabled — create should fail
	_, err := h.Service.Create(ctx, FincraTenantID, paymentlinkcore.CreateRequest{
		Amount:   decimal.NewFromInt(100),
		Currency: "USDT",
		Chain:    "tron",
		Token:    "USDT",
	})
	if err == nil {
		t.Fatal("expected error creating payment link for crypto-disabled tenant")
	}

	domainErr, ok := err.(*domain.DomainError)
	if !ok {
		t.Fatalf("expected domain.DomainError, got %T: %v", err, err)
	}
	if domainErr.Code() != domain.CodeCryptoDisabled {
		t.Fatalf("expected CodeCryptoDisabled, got %s", domainErr.Code())
	}
}


func TestPaymentLinkE2E_InvalidAmount(t *testing.T) {
	h := newPaymentLinkTestHarness(t)
	ctx := context.Background()

	// Zero amount
	_, err := h.Service.Create(ctx, LemfiTenantID, paymentlinkcore.CreateRequest{
		Amount:   decimal.Zero,
		Currency: "USDT",
		Chain:    "tron",
		Token:    "USDT",
	})
	if err == nil {
		t.Fatal("expected error creating payment link with zero amount")
	}

	// Negative amount
	_, err = h.Service.Create(ctx, LemfiTenantID, paymentlinkcore.CreateRequest{
		Amount:   decimal.NewFromInt(-50),
		Currency: "USDT",
		Chain:    "tron",
		Token:    "USDT",
	})
	if err == nil {
		t.Fatal("expected error creating payment link with negative amount")
	}
}


func TestPaymentLinkE2E_ListPagination(t *testing.T) {
	h := newPaymentLinkTestHarness(t)
	ctx := context.Background()

	// Create 5 links
	for i := 0; i < 5; i++ {
		_, err := h.Service.Create(ctx, LemfiTenantID, paymentlinkcore.CreateRequest{
			Description: fmt.Sprintf("Link %d", i+1),
			Amount:      decimal.NewFromInt(int64(10 * (i + 1))),
			Currency:    "USDT",
			Chain:       "tron",
			Token:       "USDT",
		})
		if err != nil {
			t.Fatalf("Create link %d: %v", i+1, err)
		}
	}

	// List first page (limit=2)
	page1, err := h.Service.List(ctx, LemfiTenantID, 2, 0)
	if err != nil {
		t.Fatalf("List page 1: %v", err)
	}
	if page1.Total != 5 {
		t.Fatalf("expected total 5, got %d", page1.Total)
	}
	if len(page1.Links) != 2 {
		t.Fatalf("expected 2 links on page 1, got %d", len(page1.Links))
	}

	// List second page (limit=2, offset=2)
	page2, err := h.Service.List(ctx, LemfiTenantID, 2, 2)
	if err != nil {
		t.Fatalf("List page 2: %v", err)
	}
	if len(page2.Links) != 2 {
		t.Fatalf("expected 2 links on page 2, got %d", len(page2.Links))
	}

	// List third page (limit=2, offset=4) — should return 1
	page3, err := h.Service.List(ctx, LemfiTenantID, 2, 4)
	if err != nil {
		t.Fatalf("List page 3: %v", err)
	}
	if len(page3.Links) != 1 {
		t.Fatalf("expected 1 link on page 3, got %d", len(page3.Links))
	}

	// Beyond range — should return 0
	page4, err := h.Service.List(ctx, LemfiTenantID, 2, 10)
	if err != nil {
		t.Fatalf("List beyond range: %v", err)
	}
	if len(page4.Links) != 0 {
		t.Fatalf("expected 0 links beyond range, got %d", len(page4.Links))
	}
}


func TestPaymentLinkE2E_RedeemCreatesUniqueSessions(t *testing.T) {
	h := newPaymentLinkTestHarness(t)
	ctx := context.Background()

	result, err := h.Service.Create(ctx, LemfiTenantID, paymentlinkcore.CreateRequest{
		Amount:   decimal.NewFromInt(75),
		Currency: "USDT",
		Chain:    "tron",
		Token:    "USDT",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	shortCode := result.Link.ShortCode

	// Redeem twice — should get different sessions with different addresses
	redeem1, err := h.Service.Redeem(ctx, shortCode)
	if err != nil {
		t.Fatalf("Redeem 1: %v", err)
	}
	h.DepositStore.drainOutbox()

	redeem2, err := h.Service.Redeem(ctx, shortCode)
	if err != nil {
		t.Fatalf("Redeem 2: %v", err)
	}
	h.DepositStore.drainOutbox()

	if redeem1.Session.ID == redeem2.Session.ID {
		t.Fatal("expected different session IDs for separate redemptions")
	}
	if redeem1.Session.DepositAddress == redeem2.Session.DepositAddress {
		t.Fatal("expected different deposit addresses for separate redemptions")
	}

	// Both should have the same expected amount
	if !redeem1.Session.ExpectedAmount.Equal(redeem2.Session.ExpectedAmount) {
		t.Fatalf("expected same amount, got %s vs %s",
			redeem1.Session.ExpectedAmount, redeem2.Session.ExpectedAmount)
	}
}


func TestPaymentLinkE2E_UnlimitedUses(t *testing.T) {
	h := newPaymentLinkTestHarness(t)
	ctx := context.Background()

	// Create link without use limit
	result, err := h.Service.Create(ctx, LemfiTenantID, paymentlinkcore.CreateRequest{
		Amount:   decimal.NewFromInt(25),
		Currency: "USDT",
		Chain:    "tron",
		Token:    "USDT",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	shortCode := result.Link.ShortCode

	// Redeem 10 times — all should succeed (unlimited)
	for i := 0; i < 10; i++ {
		_, err := h.Service.Redeem(ctx, shortCode)
		if err != nil {
			t.Fatalf("Redeem %d: %v", i+1, err)
		}
		h.DepositStore.drainOutbox()
	}

	// Verify use_count
	stored := h.PaymentLinkStore.getLinkDirect(result.Link.ID)
	if stored.UseCount != 10 {
		t.Fatalf("expected use_count 10, got %d", stored.UseCount)
	}
}


func TestPaymentLinkE2E_SessionConfigPropagation(t *testing.T) {
	h := newPaymentLinkTestHarness(t)
	ctx := context.Background()

	result, err := h.Service.Create(ctx, LemfiTenantID, paymentlinkcore.CreateRequest{
		Description:    "Premium Plan",
		Amount:         decimal.NewFromFloat(249.99),
		Currency:       "USDT",
		Chain:          "tron",
		Token:          "USDT",
		SettlementPref: domain.SettlementPreferenceHold,
		TTLSeconds:     1800,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	redeemResult, err := h.Service.Redeem(ctx, result.Link.ShortCode)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}

	session := redeemResult.Session

	// Verify all config fields are correctly propagated to the deposit session
	if !session.ExpectedAmount.Equal(decimal.NewFromFloat(249.99)) {
		t.Fatalf("expected amount 249.99, got %s", session.ExpectedAmount)
	}
	if session.Chain != "tron" {
		t.Fatalf("expected chain tron, got %s", session.Chain)
	}
	if session.Token != "USDT" {
		t.Fatalf("expected token USDT, got %s", session.Token)
	}
	if session.SettlementPref != domain.SettlementPreferenceHold {
		t.Fatalf("expected settlement pref HOLD, got %s", session.SettlementPref)
	}
	if session.TenantID != LemfiTenantID {
		t.Fatalf("expected tenant ID %s, got %s", LemfiTenantID, session.TenantID)
	}
}
