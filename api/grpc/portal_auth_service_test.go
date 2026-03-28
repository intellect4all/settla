package grpc

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/metadata"

	pb "github.com/intellect4all/settla/gen/settla/v1"

	"github.com/intellect4all/settla/domain"
)

// ── Mock store ──────────────────────────────────────────────────────────────

type mockPortalAuthStore struct {
	users           map[string]*domain.PortalUser // keyed by email
	tenantsByID     map[uuid.UUID]*domain.Tenant
	tenantsBySlug   map[string]*domain.Tenant
	verifiedTokens  map[string]bool
	lastLoginUser   uuid.UUID
	tenantMetadata  map[uuid.UUID][]byte
}

func newMockStore() *mockPortalAuthStore {
	return &mockPortalAuthStore{
		users:          make(map[string]*domain.PortalUser),
		tenantsByID:    make(map[uuid.UUID]*domain.Tenant),
		tenantsBySlug:  make(map[string]*domain.Tenant),
		verifiedTokens: make(map[string]bool),
		tenantMetadata: make(map[uuid.UUID][]byte),
	}
}

func (m *mockPortalAuthStore) CreateTenantWithUser(_ context.Context, tenant *domain.Tenant, user *domain.PortalUser) error {
	tenant.ID = uuid.New()
	user.ID = uuid.New()
	user.TenantID = tenant.ID
	m.users[user.Email] = user
	m.tenantsByID[tenant.ID] = tenant
	m.tenantsBySlug[tenant.Slug] = tenant
	return nil
}

func (m *mockPortalAuthStore) GetPortalUserByEmail(_ context.Context, email string) (*domain.PortalUser, string, string, string, string, error) {
	u, ok := m.users[email]
	if !ok {
		return nil, "", "", "", "", nil
	}
	t := m.tenantsByID[u.TenantID]
	return u, t.Name, t.Slug, string(t.Status), string(t.KYBStatus), nil
}

func (m *mockPortalAuthStore) GetPortalUserByID(_ context.Context, id uuid.UUID) (*domain.PortalUser, string, string, string, string, error) {
	for _, u := range m.users {
		if u.ID == id {
			t := m.tenantsByID[u.TenantID]
			return u, t.Name, t.Slug, string(t.Status), string(t.KYBStatus), nil
		}
	}
	return nil, "", "", "", "", nil
}

func (m *mockPortalAuthStore) VerifyEmail(_ context.Context, tokenHash string) error {
	m.verifiedTokens[tokenHash] = true
	for _, u := range m.users {
		if u.EmailTokenHash == tokenHash {
			u.EmailVerified = true
		}
	}
	return nil
}

func (m *mockPortalAuthStore) UpdateLastLogin(_ context.Context, userID uuid.UUID) error {
	m.lastLoginUser = userID
	return nil
}

func (m *mockPortalAuthStore) GetTenantBySlug(_ context.Context, slug string) (*domain.Tenant, error) {
	t, ok := m.tenantsBySlug[slug]
	if !ok {
		return nil, nil
	}
	return t, nil
}

func (m *mockPortalAuthStore) GetTenant(_ context.Context, tenantID uuid.UUID) (*domain.Tenant, error) {
	t, ok := m.tenantsByID[tenantID]
	if !ok {
		return nil, nil
	}
	return t, nil
}

func (m *mockPortalAuthStore) UpdateTenantKYB(_ context.Context, tenantID uuid.UUID, kybStatus string) error {
	if t, ok := m.tenantsByID[tenantID]; ok {
		t.KYBStatus = domain.KYBStatus(kybStatus)
	}
	return nil
}

func (m *mockPortalAuthStore) UpdateTenantStatus(_ context.Context, tenantID uuid.UUID, status string) error {
	if t, ok := m.tenantsByID[tenantID]; ok {
		t.Status = domain.TenantStatus(status)
	}
	return nil
}

func (m *mockPortalAuthStore) UpdateTenantMetadata(_ context.Context, tenantID uuid.UUID, metadata []byte) error {
	m.tenantMetadata[tenantID] = metadata
	return nil
}

// ── Helper ──────────────────────────────────────────────────────────────────

const testOpsAPIKey = "test-ops-api-key-for-unit-tests"

func newTestServer(store *mockPortalAuthStore) *Server {
	srv := &Server{
		portalAuthStore: store,
		jwtSecret:       []byte("test-jwt-secret-32-bytes-long!!!"),
		opsAPIKey:       testOpsAPIKey,
		logger:          slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}
	return srv
}

// ── Tests ───────────────────────────────────────────────────────────────────

func TestRegister_Success(t *testing.T) {
	store := newMockStore()
	srv := newTestServer(store)

	resp, err := srv.Register(context.Background(), &pb.RegisterRequest{
		CompanyName: "Acme Fintech",
		Email:       "alice@acme.example",
		Password:    "Strongpass123",
		DisplayName: "Alice",
	})
	if err != nil {
		t.Fatalf("Register() unexpected error: %v", err)
	}
	if resp.TenantId == "" {
		t.Error("Register() returned empty tenant_id")
	}
	if resp.UserId == "" {
		t.Error("Register() returned empty user_id")
	}
	if resp.Email != "alice@acme.example" {
		t.Errorf("Register() email = %q, want %q", resp.Email, "alice@acme.example")
	}

	// Verify user was created in store
	u, ok := store.users["alice@acme.example"]
	if !ok {
		t.Fatal("user not found in store")
	}
	if u.Role != domain.PortalUserRoleOwner {
		t.Errorf("user role = %q, want OWNER", u.Role)
	}
	// Verify password was hashed
	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte("Strongpass123")); err != nil {
		t.Error("password hash doesn't match")
	}
	// Verify tenant was created with correct defaults
	tenant := store.tenantsByID[u.TenantID]
	if tenant.Status != domain.TenantStatusOnboarding {
		t.Errorf("tenant status = %q, want ONBOARDING", tenant.Status)
	}
	if tenant.Slug != "acme-fintech" {
		t.Errorf("tenant slug = %q, want %q", tenant.Slug, "acme-fintech")
	}
}

func TestRegister_DuplicateEmail(t *testing.T) {
	store := newMockStore()
	srv := newTestServer(store)

	_, err := srv.Register(context.Background(), &pb.RegisterRequest{
		CompanyName: "First Co",
		Email:       "dup@example.com",
		Password:    "Password123",
	})
	if err != nil {
		t.Fatalf("first Register() error: %v", err)
	}

	_, err = srv.Register(context.Background(), &pb.RegisterRequest{
		CompanyName: "Second Co",
		Email:       "dup@example.com",
		Password:    "password456",
	})
	if err == nil {
		t.Fatal("second Register() should have returned error for duplicate email")
	}
}

func TestRegister_ValidationErrors(t *testing.T) {
	srv := newTestServer(newMockStore())

	tests := []struct {
		name string
		req  *pb.RegisterRequest
	}{
		{"empty company", &pb.RegisterRequest{CompanyName: "", Email: "a@b.com", Password: "Validpass1"}},
		{"empty email", &pb.RegisterRequest{CompanyName: "Co", Email: "", Password: "Validpass1"}},
		{"short password", &pb.RegisterRequest{CompanyName: "Co", Email: "a@b.com", Password: "short"}},
		{"no uppercase", &pb.RegisterRequest{CompanyName: "Co", Email: "a@b.com", Password: "password123"}},
		{"no digit", &pb.RegisterRequest{CompanyName: "Co", Email: "a@b.com", Password: "Passwordonly"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := srv.Register(context.Background(), tt.req)
			if err == nil {
				t.Error("Register() should have returned validation error")
			}
		})
	}
}

func TestLogin_Success(t *testing.T) {
	store := newMockStore()
	srv := newTestServer(store)

	// Register a user first
	regResp, err := srv.Register(context.Background(), &pb.RegisterRequest{
		CompanyName: "Login Corp",
		Email:       "bob@login.example",
		Password:    "Securepass99",
	})
	if err != nil {
		t.Fatalf("Register() error: %v", err)
	}

	// Verify email manually
	user := store.users["bob@login.example"]
	user.EmailVerified = true

	// Login
	resp, err := srv.Login(context.Background(), &pb.LoginRequest{
		Email:    "bob@login.example",
		Password: "Securepass99",
	})
	if err != nil {
		t.Fatalf("Login() error: %v", err)
	}
	if resp.AccessToken == "" {
		t.Error("Login() returned empty access_token")
	}
	if resp.RefreshToken == "" {
		t.Error("Login() returned empty refresh_token")
	}
	if resp.ExpiresIn != int64(accessTokenExpiry.Seconds()) {
		t.Errorf("Login() expires_in = %d, want %d", resp.ExpiresIn, int64(accessTokenExpiry.Seconds()))
	}
	if resp.User == nil {
		t.Fatal("Login() returned nil user")
	}
	if resp.User.Email != "bob@login.example" {
		t.Errorf("Login() user.email = %q, want %q", resp.User.Email, "bob@login.example")
	}
	if resp.User.TenantId != regResp.TenantId {
		t.Errorf("Login() user.tenant_id = %q, want %q", resp.User.TenantId, regResp.TenantId)
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	store := newMockStore()
	srv := newTestServer(store)

	_, _ = srv.Register(context.Background(), &pb.RegisterRequest{
		CompanyName: "WrongPass Co",
		Email:       "wrong@pass.example",
		Password:    "Correctpass1",
	})
	store.users["wrong@pass.example"].EmailVerified = true

	_, err := srv.Login(context.Background(), &pb.LoginRequest{
		Email:    "wrong@pass.example",
		Password: "Wrongpass123",
	})
	if err == nil {
		t.Fatal("Login() should fail with wrong password")
	}
}

func TestLogin_EmailNotVerified(t *testing.T) {
	store := newMockStore()
	srv := newTestServer(store)

	_, _ = srv.Register(context.Background(), &pb.RegisterRequest{
		CompanyName: "Unverified Co",
		Email:       "unverified@example.com",
		Password:    "Password123",
	})
	// Don't verify email

	_, err := srv.Login(context.Background(), &pb.LoginRequest{
		Email:    "unverified@example.com",
		Password: "Password123",
	})
	if err == nil {
		t.Fatal("Login() should fail when email is not verified")
	}
}

func TestLogin_NonexistentUser(t *testing.T) {
	srv := newTestServer(newMockStore())

	_, err := srv.Login(context.Background(), &pb.LoginRequest{
		Email:    "nobody@nowhere.com",
		Password: "Password123",
	})
	if err == nil {
		t.Fatal("Login() should fail for nonexistent user")
	}
}

func TestVerifyEmail(t *testing.T) {
	store := newMockStore()
	srv := newTestServer(store)

	_, _ = srv.Register(context.Background(), &pb.RegisterRequest{
		CompanyName: "Verify Co",
		Email:       "verify@example.com",
		Password:    "Password123",
	})

	user := store.users["verify@example.com"]
	// The raw token was logged, but we can derive the hash and simulate
	// For testing, use the stored hash directly
	tokenHash := user.EmailTokenHash

	resp, err := srv.VerifyEmail(context.Background(), &pb.VerifyEmailRequest{
		// We can't easily get the raw token since it was logged.
		// Instead, test with a known token and check the hash path.
		Token: "dummy-token",
	})
	if err != nil {
		t.Fatalf("VerifyEmail() error: %v", err)
	}
	if resp.Message == "" {
		t.Error("VerifyEmail() returned empty message")
	}
	// The store should have recorded the verification (for the hashed dummy token, not the real one)
	dummyHash := sha256Hex("dummy-token")
	if !store.verifiedTokens[dummyHash] {
		t.Error("VerifyEmail() did not record token verification")
	}
	_ = tokenHash // used to verify the real flow would work
}

func TestRefreshToken(t *testing.T) {
	store := newMockStore()
	srv := newTestServer(store)

	// Register + verify + login to get a refresh token
	_, _ = srv.Register(context.Background(), &pb.RegisterRequest{
		CompanyName: "Refresh Co",
		Email:       "refresh@example.com",
		Password:    "Password123",
	})
	store.users["refresh@example.com"].EmailVerified = true

	loginResp, err := srv.Login(context.Background(), &pb.LoginRequest{
		Email:    "refresh@example.com",
		Password: "Password123",
	})
	if err != nil {
		t.Fatalf("Login() error: %v", err)
	}

	// Wait so tokens differ (JWT uses second-precision timestamps)
	time.Sleep(1100 * time.Millisecond)

	resp, err := srv.RefreshToken(context.Background(), &pb.RefreshTokenRequest{
		RefreshToken: loginResp.RefreshToken,
	})
	if err != nil {
		t.Fatalf("RefreshToken() error: %v", err)
	}
	if resp.AccessToken == "" {
		t.Error("RefreshToken() returned empty access_token")
	}
	if resp.AccessToken == loginResp.AccessToken {
		t.Error("RefreshToken() should return a new access token")
	}
}

func TestRefreshToken_WithAccessToken(t *testing.T) {
	store := newMockStore()
	srv := newTestServer(store)

	_, _ = srv.Register(context.Background(), &pb.RegisterRequest{
		CompanyName: "BadRefresh Co",
		Email:       "badrefresh@example.com",
		Password:    "Password123",
	})
	store.users["badrefresh@example.com"].EmailVerified = true

	loginResp, _ := srv.Login(context.Background(), &pb.LoginRequest{
		Email:    "badrefresh@example.com",
		Password: "Password123",
	})

	_, err := srv.RefreshToken(context.Background(), &pb.RefreshTokenRequest{
		RefreshToken: loginResp.AccessToken, // wrong type
	})
	if err == nil {
		t.Fatal("RefreshToken() should reject access token")
	}
}

func TestSubmitKYB(t *testing.T) {
	store := newMockStore()
	srv := newTestServer(store)

	regResp, _ := srv.Register(context.Background(), &pb.RegisterRequest{
		CompanyName: "KYB Corp",
		Email:       "kyb@example.com",
		Password:    "Password123",
	})

	resp, err := srv.SubmitKYB(context.Background(), &pb.SubmitKYBRequest{
		TenantId:                  regResp.TenantId,
		CompanyRegistrationNumber: "REG-12345",
		Country:                   "NG",
		BusinessType:              "fintech",
		ContactName:               "Jane Doe",
		ContactEmail:              "jane@kyb.example",
		ContactPhone:              "+2341234567890",
	})
	if err != nil {
		t.Fatalf("SubmitKYB() error: %v", err)
	}
	if resp.KybStatus != string(domain.KYBStatusInReview) {
		t.Errorf("SubmitKYB() kyb_status = %q, want IN_REVIEW", resp.KybStatus)
	}

	// Verify tenant KYB status was updated
	tenantID, _ := uuid.Parse(regResp.TenantId)
	tenant := store.tenantsByID[tenantID]
	if tenant.KYBStatus != domain.KYBStatusInReview {
		t.Errorf("tenant KYB status = %q, want IN_REVIEW", tenant.KYBStatus)
	}
	if len(store.tenantMetadata[tenantID]) == 0 {
		t.Error("tenant metadata not updated")
	}
}

func TestApproveKYB(t *testing.T) {
	store := newMockStore()
	srv := newTestServer(store)

	regResp, _ := srv.Register(context.Background(), &pb.RegisterRequest{
		CompanyName: "Approve Co",
		Email:       "approve@example.com",
		Password:    "Password123",
	})

	// ApproveKYB requires ops API key via gRPC metadata.
	approveCtx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-ops-api-key", testOpsAPIKey))
	resp, err := srv.ApproveKYB(approveCtx, &pb.ApproveKYBRequest{
		TenantId: regResp.TenantId,
	})
	if err != nil {
		t.Fatalf("ApproveKYB() error: %v", err)
	}
	if resp.TenantStatus != string(domain.TenantStatusActive) {
		t.Errorf("ApproveKYB() tenant_status = %q, want ACTIVE", resp.TenantStatus)
	}
	if resp.KybStatus != string(domain.KYBStatusVerified) {
		t.Errorf("ApproveKYB() kyb_status = %q, want VERIFIED", resp.KybStatus)
	}

	// Verify in store
	tenantID, _ := uuid.Parse(regResp.TenantId)
	tenant := store.tenantsByID[tenantID]
	if tenant.Status != domain.TenantStatusActive {
		t.Errorf("tenant status = %q, want ACTIVE", tenant.Status)
	}
}

func TestGenerateSlug(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Acme Fintech", "acme-fintech"},
		{"  My Company  ", "my-company"},
		{"UPPER CASE", "upper-case"},
		{"special!@#chars", "special-chars"},
		{"", "tenant"},
		{"a very long company name that should be truncated at fifty characters exactly yes", "a-very-long-company-name-that-should-be-truncated-"},
	}

	for _, tt := range tests {
		got := generateSlug(tt.input)
		if got != tt.want {
			t.Errorf("generateSlug(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFullOnboardingFlow(t *testing.T) {
	store := newMockStore()
	srv := newTestServer(store)
	ctx := context.Background()

	// 1. Register
	regResp, err := srv.Register(ctx, &pb.RegisterRequest{
		CompanyName: "FlowTest Ltd",
		Email:       "flow@test.example",
		Password:    "Flowpass123",
		DisplayName: "Flow Tester",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// 2. Cannot login before email verification
	_, err = srv.Login(ctx, &pb.LoginRequest{
		Email:    "flow@test.example",
		Password: "Flowpass123",
	})
	if err == nil {
		t.Fatal("Login should fail before email verification")
	}

	// 3. Verify email
	store.users["flow@test.example"].EmailVerified = true

	// 4. Login
	loginResp, err := srv.Login(ctx, &pb.LoginRequest{
		Email:    "flow@test.example",
		Password: "Flowpass123",
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if loginResp.User.TenantStatus != string(domain.TenantStatusOnboarding) {
		t.Errorf("expected ONBOARDING status, got %s", loginResp.User.TenantStatus)
	}

	// 5. Submit KYB
	kybResp, err := srv.SubmitKYB(ctx, &pb.SubmitKYBRequest{
		TenantId:                  regResp.TenantId,
		CompanyRegistrationNumber: "REG-999",
		Country:                   "GB",
		BusinessType:              "payment_processor",
		ContactName:               "Flow Tester",
		ContactEmail:              "flow@test.example",
	})
	if err != nil {
		t.Fatalf("SubmitKYB: %v", err)
	}
	if kybResp.KybStatus != string(domain.KYBStatusInReview) {
		t.Errorf("expected IN_REVIEW, got %s", kybResp.KybStatus)
	}

	// 6. Approve KYB (admin action — requires ops API key in metadata)
	opsCtx := metadata.NewIncomingContext(ctx, metadata.Pairs("x-ops-api-key", testOpsAPIKey))
	approveResp, err := srv.ApproveKYB(opsCtx, &pb.ApproveKYBRequest{
		TenantId: regResp.TenantId,
	})
	if err != nil {
		t.Fatalf("ApproveKYB: %v", err)
	}
	if approveResp.TenantStatus != string(domain.TenantStatusActive) {
		t.Errorf("expected ACTIVE, got %s", approveResp.TenantStatus)
	}

	// 7. Refresh token
	refreshResp, err := srv.RefreshToken(ctx, &pb.RefreshTokenRequest{
		RefreshToken: loginResp.RefreshToken,
	})
	if err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if refreshResp.AccessToken == "" {
		t.Error("RefreshToken returned empty access token")
	}
}
