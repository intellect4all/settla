package grpc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/intellect4all/settla/gen/settla/v1"

	"github.com/intellect4all/settla/domain"
	"github.com/shopspring/decimal"
)

// PortalAuthStore provides data access for portal authentication.
type PortalAuthStore interface {
	CreateTenantWithUser(ctx context.Context, tenant *domain.Tenant, user *domain.PortalUser) error
	GetPortalUserByEmail(ctx context.Context, email string) (user *domain.PortalUser, tenantName, tenantSlug, tenantStatus, kybStatus string, err error)
	GetPortalUserByID(ctx context.Context, id uuid.UUID) (user *domain.PortalUser, tenantName, tenantSlug, tenantStatus, kybStatus string, err error)
	VerifyEmail(ctx context.Context, tokenHash string) error
	UpdateLastLogin(ctx context.Context, userID uuid.UUID) error
	GetTenantBySlug(ctx context.Context, slug string) (*domain.Tenant, error)
	GetTenant(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error)
	UpdateTenantKYB(ctx context.Context, tenantID uuid.UUID, kybStatus string) error
	UpdateTenantStatus(ctx context.Context, tenantID uuid.UUID, status string) error
	UpdateTenantMetadata(ctx context.Context, tenantID uuid.UUID, metadata []byte) error
}

// WithPortalAuthStore sets the portal auth store for the PortalAuthService.
func WithPortalAuthStore(s PortalAuthStore) ServerOption {
	return func(srv *Server) { srv.portalAuthStore = s }
}

// WithJWTSecret sets the JWT signing secret.
func WithJWTSecret(secret string) ServerOption {
	return func(srv *Server) { srv.jwtSecret = []byte(secret) }
}

const (
	accessTokenExpiry  = 15 * time.Minute
	refreshTokenExpiry = 7 * 24 * time.Hour
	emailTokenExpiry   = 24 * time.Hour
	bcryptCost         = 12
)

// JWT claims for portal access tokens.
type portalClaims struct {
	jwt.RegisteredClaims
	TenantID string `json:"tid"`
	Role     string `json:"role"`
	TokenType string `json:"type"`
}

var slugRegexp = regexp.MustCompile(`[^a-z0-9]+`)

// ──────────────────────────────────────────────────────────────────────────────
// PortalAuthService RPCs
// ──────────────────────────────────────────────────────────────────────────────

func (s *Server) Register(ctx context.Context, req *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	if s.portalAuthStore == nil {
		return nil, status.Error(codes.Unimplemented, "portal auth not configured")
	}

	if err := validateNonEmpty("company_name", req.GetCompanyName()); err != nil {
		return nil, err
	}
	if err := validateNonEmpty("email", req.GetEmail()); err != nil {
		return nil, err
	}
	if len(req.GetPassword()) < 8 {
		return nil, status.Error(codes.InvalidArgument, "password must be at least 8 characters")
	}
	displayName := req.GetDisplayName()
	if displayName == "" {
		displayName = req.GetEmail()
	}

	// Check for existing email
	existing, _, _, _, _, err := s.portalAuthStore.GetPortalUserByEmail(ctx, req.GetEmail())
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to check existing email")
	}
	if existing != nil {
		return nil, mapDomainError(domain.ErrEmailAlreadyExists(req.GetEmail()))
	}

	// Generate slug from company name
	slug := generateSlug(req.GetCompanyName())

	// Check slug conflict, append random suffix if needed
	for i := 0; i < 5; i++ {
		existingTenant, err := s.portalAuthStore.GetTenantBySlug(ctx, slug)
		if err != nil {
			return nil, status.Error(codes.Internal, "failed to check slug availability")
		}
		if existingTenant == nil {
			break
		}
		suffix, _ := randomHex(3)
		slug = slug + "-" + suffix
		if i == 4 {
			return nil, mapDomainError(domain.ErrSlugConflict(slug))
		}
	}

	// Hash password
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(req.GetPassword()), bcryptCost)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to hash password")
	}

	// Generate email verification token
	rawToken, err := randomHex(32)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to generate verification token")
	}
	tokenHash := sha256Hex(rawToken)
	tokenExpiry := time.Now().UTC().Add(emailTokenExpiry)

	// Default fee schedule for self-onboarded tenants: 50/35 bps, min $0.50, max $50
	tenant := &domain.Tenant{
		Name:            req.GetCompanyName(),
		Slug:            slug,
		Status:          domain.TenantStatusOnboarding,
		KYBStatus:       domain.KYBStatusPending,
		SettlementModel: domain.SettlementModelPrefunded,
		FeeSchedule: domain.FeeSchedule{
			OnRampBPS:  50,
			OffRampBPS: 35,
			MinFeeUSD:  decimal.NewFromFloat(0.50),
			MaxFeeUSD:  decimal.NewFromFloat(50.00),
		},
		DailyLimitUSD:    decimal.NewFromFloat(10000),
		PerTransferLimit: decimal.NewFromFloat(5000),
	}

	user := &domain.PortalUser{
		Email:               req.GetEmail(),
		PasswordHash:        string(passwordHash),
		DisplayName:         displayName,
		Role:                domain.PortalUserRoleOwner,
		EmailTokenHash:      tokenHash,
		EmailTokenExpiresAt: &tokenExpiry,
	}

	if err := tenant.Validate(); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid tenant: %v", err)
	}

	if err := s.portalAuthStore.CreateTenantWithUser(ctx, tenant, user); err != nil {
		if strings.Contains(err.Error(), "unique constraint") || strings.Contains(err.Error(), "duplicate key") {
			return nil, mapDomainError(domain.ErrEmailAlreadyExists(req.GetEmail()))
		}
		s.logger.Error("settla-auth: failed to create tenant with user", "error", err)
		return nil, status.Error(codes.Internal, "failed to create account")
	}

	// Dev mode: log the verification token to stdout
	s.logger.Info("settla-auth: email verification token (dev mode)",
		"email", req.GetEmail(),
		"token", rawToken,
		"tenant_id", tenant.ID.String(),
		slog.String("verify_url", fmt.Sprintf("/auth/verify-email?token=%s", rawToken)),
	)

	auditLog(ctx, s.auditLogger, s.logger, domain.AuditEntry{
		TenantID:   tenant.ID,
		ActorType:  "system",
		ActorID:    "portal_auth",
		Action:     "tenant.registered",
		EntityType: "tenant",
		EntityID:   uuidPtr(tenant.ID),
		NewValue:   mustJSON(map[string]string{"company_name": req.GetCompanyName(), "email": req.GetEmail(), "slug": slug}),
	})

	return &pb.RegisterResponse{
		TenantId: tenant.ID.String(),
		UserId:   user.ID.String(),
		Email:    user.Email,
		Message:  "Check your email for verification",
	}, nil
}

func (s *Server) Login(ctx context.Context, req *pb.LoginRequest) (*pb.LoginResponse, error) {
	if s.portalAuthStore == nil {
		return nil, status.Error(codes.Unimplemented, "portal auth not configured")
	}

	if err := validateNonEmpty("email", req.GetEmail()); err != nil {
		return nil, err
	}
	if err := validateNonEmpty("password", req.GetPassword()); err != nil {
		return nil, err
	}

	user, tenantName, tenantSlug, tenantStatus, kybStatus, err := s.portalAuthStore.GetPortalUserByEmail(ctx, req.GetEmail())
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to look up user")
	}
	if user == nil {
		return nil, mapDomainError(domain.ErrInvalidCredentials())
	}

	// Verify password
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.GetPassword())); err != nil {
		return nil, mapDomainError(domain.ErrInvalidCredentials())
	}

	// Check email verified
	if !user.EmailVerified {
		return nil, mapDomainError(domain.ErrEmailNotVerified(user.Email))
	}

	// Check tenant not suspended
	if tenantStatus == string(domain.TenantStatusSuspended) {
		return nil, mapDomainError(domain.ErrTenantSuspended(user.TenantID.String()))
	}

	// Sign JWT access token
	now := time.Now().UTC()
	accessToken, err := s.signToken(user.ID.String(), user.TenantID.String(), string(user.Role), "access", now, accessTokenExpiry)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to sign access token")
	}

	// Sign JWT refresh token
	refreshToken, err := s.signToken(user.ID.String(), user.TenantID.String(), string(user.Role), "refresh", now, refreshTokenExpiry)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to sign refresh token")
	}

	// Update last login (fire-and-forget)
	go func() {
		if err := s.portalAuthStore.UpdateLastLogin(context.Background(), user.ID); err != nil {
			s.logger.Warn("settla-auth: failed to update last login", "user_id", user.ID, "error", err)
		}
	}()

	return &pb.LoginResponse{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresIn:    int64(accessTokenExpiry.Seconds()),
		User: &pb.PortalUserProfile{
			Id:           user.ID.String(),
			Email:        user.Email,
			DisplayName:  user.DisplayName,
			Role:         string(user.Role),
			TenantId:     user.TenantID.String(),
			TenantName:   tenantName,
			TenantSlug:   tenantSlug,
			TenantStatus: tenantStatus,
			KybStatus:    kybStatus,
		},
	}, nil
}

func (s *Server) VerifyEmail(ctx context.Context, req *pb.VerifyEmailRequest) (*pb.VerifyEmailResponse, error) {
	if s.portalAuthStore == nil {
		return nil, status.Error(codes.Unimplemented, "portal auth not configured")
	}

	if err := validateNonEmpty("token", req.GetToken()); err != nil {
		return nil, err
	}

	tokenHash := sha256Hex(req.GetToken())

	if err := s.portalAuthStore.VerifyEmail(ctx, tokenHash); err != nil {
		return nil, status.Error(codes.Internal, "failed to verify email")
	}

	return &pb.VerifyEmailResponse{
		Message: "Email verified successfully",
	}, nil
}

func (s *Server) RefreshToken(ctx context.Context, req *pb.RefreshTokenRequest) (*pb.RefreshTokenResponse, error) {
	if s.portalAuthStore == nil {
		return nil, status.Error(codes.Unimplemented, "portal auth not configured")
	}

	if err := validateNonEmpty("refresh_token", req.GetRefreshToken()); err != nil {
		return nil, err
	}

	// Parse and validate the refresh token
	claims, err := s.parseToken(req.GetRefreshToken())
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "invalid refresh token")
	}

	if claims.TokenType != "refresh" {
		return nil, status.Error(codes.InvalidArgument, "token is not a refresh token")
	}

	// Issue new access token
	now := time.Now().UTC()
	accessToken, err := s.signToken(claims.Subject, claims.TenantID, claims.Role, "access", now, accessTokenExpiry)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to sign access token")
	}

	return &pb.RefreshTokenResponse{
		AccessToken: accessToken,
		ExpiresIn:   int64(accessTokenExpiry.Seconds()),
	}, nil
}

func (s *Server) SubmitKYB(ctx context.Context, req *pb.SubmitKYBRequest) (*pb.SubmitKYBResponse, error) {
	if s.portalAuthStore == nil {
		return nil, status.Error(codes.Unimplemented, "portal auth not configured")
	}

	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if err := validateNonEmpty("company_registration_number", req.GetCompanyRegistrationNumber()); err != nil {
		return nil, err
	}
	if err := validateNonEmpty("country", req.GetCountry()); err != nil {
		return nil, err
	}
	if err := validateCountryCode(req.GetCountry()); err != nil {
		return nil, err
	}
	if err := validateNonEmpty("business_type", req.GetBusinessType()); err != nil {
		return nil, err
	}
	if err := validateNonEmpty("contact_name", req.GetContactName()); err != nil {
		return nil, err
	}
	if err := validateNonEmpty("contact_email", req.GetContactEmail()); err != nil {
		return nil, err
	}

	// Validate tenant is in ONBOARDING status
	tenant, err := s.portalAuthStore.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to get tenant")
	}
	if tenant == nil {
		return nil, mapDomainError(domain.ErrTenantNotFound(tenantID.String()))
	}
	if tenant.Status != domain.TenantStatusOnboarding {
		return nil, status.Errorf(codes.FailedPrecondition, "tenant must be in ONBOARDING status, current: %s", tenant.Status)
	}

	// Store KYB data in metadata JSONB
	kybMetadata := fmt.Sprintf(
		`{"kyb_registration_number":"%s","kyb_country":"%s","kyb_business_type":"%s","kyb_contact_name":"%s","kyb_contact_email":"%s","kyb_contact_phone":"%s","kyb_submitted_at":"%s"}`,
		req.GetCompanyRegistrationNumber(),
		req.GetCountry(),
		req.GetBusinessType(),
		req.GetContactName(),
		req.GetContactEmail(),
		req.GetContactPhone(),
		time.Now().UTC().Format(time.RFC3339),
	)

	if err := s.portalAuthStore.UpdateTenantMetadata(ctx, tenantID, []byte(kybMetadata)); err != nil {
		return nil, status.Error(codes.Internal, "failed to store KYB data")
	}

	if err := s.portalAuthStore.UpdateTenantKYB(ctx, tenantID, string(domain.KYBStatusInReview)); err != nil {
		return nil, status.Error(codes.Internal, "failed to update KYB status")
	}

	return &pb.SubmitKYBResponse{
		Message:   "KYB submitted for review",
		KybStatus: string(domain.KYBStatusInReview),
	}, nil
}

func (s *Server) ApproveKYB(ctx context.Context, req *pb.ApproveKYBRequest) (*pb.ApproveKYBResponse, error) {
	if s.portalAuthStore == nil {
		return nil, status.Error(codes.Unimplemented, "portal auth not configured")
	}

	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	// Update KYB to VERIFIED
	if err := s.portalAuthStore.UpdateTenantKYB(ctx, tenantID, string(domain.KYBStatusVerified)); err != nil {
		return nil, status.Error(codes.Internal, "failed to update KYB status")
	}

	// Set tenant to ACTIVE
	if err := s.portalAuthStore.UpdateTenantStatus(ctx, tenantID, string(domain.TenantStatusActive)); err != nil {
		return nil, status.Error(codes.Internal, "failed to activate tenant")
	}

	auditLog(ctx, s.auditLogger, s.logger, domain.AuditEntry{
		TenantID:   tenantID,
		ActorType:  "system",
		ActorID:    "portal_auth",
		Action:     "tenant.kyb_approved",
		EntityType: "tenant",
		EntityID:   uuidPtr(tenantID),
		NewValue:   mustJSON(map[string]string{"kyb_status": string(domain.KYBStatusVerified), "tenant_status": string(domain.TenantStatusActive)}),
	})

	return &pb.ApproveKYBResponse{
		Message:      "KYB approved, tenant is now ACTIVE",
		TenantStatus: string(domain.TenantStatusActive),
		KybStatus:    string(domain.KYBStatusVerified),
	}, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// JWT helpers
// ──────────────────────────────────────────────────────────────────────────────

func (s *Server) signToken(userID, tenantID, role, tokenType string, now time.Time, expiry time.Duration) (string, error) {
	claims := portalClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(expiry)),
			Issuer:    "settla",
		},
		TenantID:  tenantID,
		Role:      role,
		TokenType: tokenType,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.jwtSecret)
}

func (s *Server) parseToken(tokenString string) (*portalClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &portalClaims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return s.jwtSecret, nil
	})
	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*portalClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}

	return claims, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Utility helpers
// ──────────────────────────────────────────────────────────────────────────────

func generateSlug(name string) string {
	slug := strings.ToLower(strings.TrimSpace(name))
	slug = slugRegexp.ReplaceAllString(slug, "-")
	slug = strings.Trim(slug, "-")
	if len(slug) > 50 {
		slug = slug[:50]
	}
	if slug == "" {
		slug = "tenant"
	}
	return slug
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
