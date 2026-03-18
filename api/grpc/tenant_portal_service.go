package grpc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/intellect4all/settla/gen/settla/v1"

	"github.com/intellect4all/settla/domain"
)

// TenantPortalStore provides tenant self-service data access.
type TenantPortalStore interface {
	GetTenant(ctx context.Context, tenantID uuid.UUID) (*domain.Tenant, error)
	UpdateWebhookConfig(ctx context.Context, tenantID uuid.UUID, webhookURL, webhookSecret string) error
	ListAPIKeys(ctx context.Context, tenantID uuid.UUID) ([]domain.APIKey, error)
	CreateAPIKey(ctx context.Context, key *domain.APIKey) error
	DeactivateAPIKeyByTenant(ctx context.Context, tenantID, keyID uuid.UUID) error
	GetAPIKeyByIDAndTenant(ctx context.Context, tenantID, keyID uuid.UUID) (*domain.APIKey, error)

	// Dashboard metrics
	GetDashboardMetrics(ctx context.Context, tenantID uuid.UUID, todayStart, sevenDaysAgo, thirtyDaysAgo time.Time) (*domain.DashboardMetrics, error)
	GetTransferStatsHourly(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.TransferStatsBucket, error)
	GetTransferStatsDaily(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.TransferStatsBucket, error)
	GetFeeReportByCorridor(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.FeeReportEntry, error)
}

// WithTenantPortalStore sets the tenant portal store for the TenantPortalService.
func WithTenantPortalStore(s TenantPortalStore) ServerOption {
	return func(srv *Server) { srv.portalStore = s }
}

// ──────────────────────────────────────────────────────────────────────────────
// TenantPortalService implementation
// ──────────────────────────────────────────────────────────────────────────────

func (s *Server) GetMyTenant(ctx context.Context, req *pb.GetMyTenantRequest) (*pb.GetMyTenantResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if s.portalStore == nil {
		return nil, status.Error(codes.Unimplemented, "tenant portal not configured")
	}

	tenant, err := s.portalStore.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, mapDomainError(err)
	}

	return &pb.GetMyTenantResponse{
		Tenant: tenantProfileToProto(tenant),
	}, nil
}

func (s *Server) UpdateWebhookConfig(ctx context.Context, req *pb.UpdateWebhookConfigRequest) (*pb.UpdateWebhookConfigResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if s.portalStore == nil {
		return nil, status.Error(codes.Unimplemented, "tenant portal not configured")
	}

	if err := validateNonEmpty("webhook_url", req.GetWebhookUrl()); err != nil {
		return nil, err
	}

	// Generate new HMAC secret
	secret, err := generateWebhookSecret()
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to generate webhook secret")
	}

	if err := s.portalStore.UpdateWebhookConfig(ctx, tenantID, req.GetWebhookUrl(), secret); err != nil {
		return nil, mapDomainError(err)
	}

	return &pb.UpdateWebhookConfigResponse{
		WebhookUrl:    req.GetWebhookUrl(),
		WebhookSecret: secret,
	}, nil
}

func (s *Server) ListAPIKeys(ctx context.Context, req *pb.ListAPIKeysRequest) (*pb.ListAPIKeysResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if s.portalStore == nil {
		return nil, status.Error(codes.Unimplemented, "tenant portal not configured")
	}

	keys, err := s.portalStore.ListAPIKeys(ctx, tenantID)
	if err != nil {
		return nil, mapDomainError(err)
	}

	pbKeys := make([]*pb.APIKeyInfo, len(keys))
	for i := range keys {
		pbKeys[i] = apiKeyToProto(&keys[i])
	}

	return &pb.ListAPIKeysResponse{Keys: pbKeys}, nil
}

func (s *Server) CreateAPIKey(ctx context.Context, req *pb.CreateAPIKeyRequest) (*pb.CreateAPIKeyResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if s.portalStore == nil {
		return nil, status.Error(codes.Unimplemented, "tenant portal not configured")
	}

	env := req.GetEnvironment()
	if env != "LIVE" && env != "TEST" {
		return nil, status.Error(codes.InvalidArgument, "environment must be LIVE or TEST")
	}

	// Generate raw key and its hash
	rawKey, keyHash, keyPrefix, err := generateAPIKey(env)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to generate API key")
	}

	key := &domain.APIKey{
		TenantID:    tenantID,
		KeyHash:     keyHash,
		KeyPrefix:   keyPrefix,
		Environment: env,
		Name:        req.GetName(),
		IsActive:    true,
	}

	if err := s.portalStore.CreateAPIKey(ctx, key); err != nil {
		return nil, mapDomainError(err)
	}

	auditLog(ctx, s.auditLogger, s.logger, domain.AuditEntry{
		TenantID:   tenantID,
		ActorType:  "api_key",
		ActorID:    tenantID.String(),
		Action:     "api_key.created",
		EntityType: "api_key",
		EntityID:   &key.ID,
		NewValue:   mustJSON(map[string]string{"key_prefix": keyPrefix, "environment": env, "name": req.GetName()}),
	})

	return &pb.CreateAPIKeyResponse{
		Key:    apiKeyToProto(key),
		RawKey: rawKey,
	}, nil
}

func (s *Server) RevokeAPIKey(ctx context.Context, req *pb.RevokeAPIKeyRequest) (*pb.RevokeAPIKeyResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	keyID, err := parseUUID(req.GetKeyId(), "key_id")
	if err != nil {
		return nil, err
	}

	if s.portalStore == nil {
		return nil, status.Error(codes.Unimplemented, "tenant portal not configured")
	}

	// Fetch the key hash before deactivating so we can return it to the gateway
	// for immediate L1/L2 auth cache invalidation.
	existingKey, err := s.portalStore.GetAPIKeyByIDAndTenant(ctx, tenantID, keyID)
	if err != nil {
		return nil, mapDomainError(err)
	}

	if err := s.portalStore.DeactivateAPIKeyByTenant(ctx, tenantID, keyID); err != nil {
		return nil, mapDomainError(err)
	}

	auditLog(ctx, s.auditLogger, s.logger, domain.AuditEntry{
		TenantID:   tenantID,
		ActorType:  "api_key",
		ActorID:    tenantID.String(),
		Action:     "api_key.revoked",
		EntityType: "api_key",
		EntityID:   &keyID,
		NewValue:   mustJSON(map[string]string{"key_prefix": existingKey.KeyPrefix}),
	})

	return &pb.RevokeAPIKeyResponse{KeyHash: existingKey.KeyHash}, nil
}

func (s *Server) RotateAPIKey(ctx context.Context, req *pb.RotateAPIKeyRequest) (*pb.RotateAPIKeyResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	oldKeyID, err := parseUUID(req.GetOldKeyId(), "old_key_id")
	if err != nil {
		return nil, err
	}

	if s.portalStore == nil {
		return nil, status.Error(codes.Unimplemented, "tenant portal not configured")
	}

	// Verify old key belongs to this tenant
	oldKey, err := s.portalStore.GetAPIKeyByIDAndTenant(ctx, tenantID, oldKeyID)
	if err != nil {
		return nil, mapDomainError(err)
	}

	// Deactivate old key
	if err := s.portalStore.DeactivateAPIKeyByTenant(ctx, tenantID, oldKeyID); err != nil {
		return nil, mapDomainError(err)
	}

	// Create new key with same environment
	rawKey, keyHash, keyPrefix, err := generateAPIKey(oldKey.Environment)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to generate replacement API key")
	}

	name := req.GetName()
	if name == "" {
		name = oldKey.Name
	}

	newKey := &domain.APIKey{
		TenantID:    tenantID,
		KeyHash:     keyHash,
		KeyPrefix:   keyPrefix,
		Environment: oldKey.Environment,
		Name:        name,
		IsActive:    true,
	}

	if err := s.portalStore.CreateAPIKey(ctx, newKey); err != nil {
		return nil, mapDomainError(err)
	}

	auditLog(ctx, s.auditLogger, s.logger, domain.AuditEntry{
		TenantID:   tenantID,
		ActorType:  "api_key",
		ActorID:    tenantID.String(),
		Action:     "api_key.rotated",
		EntityType: "api_key",
		EntityID:   &newKey.ID,
		OldValue:   mustJSON(map[string]string{"old_key_id": oldKeyID.String(), "old_key_prefix": oldKey.KeyPrefix}),
		NewValue:   mustJSON(map[string]string{"new_key_prefix": keyPrefix, "environment": oldKey.Environment, "name": name}),
	})

	return &pb.RotateAPIKeyResponse{
		Key:    apiKeyToProto(newKey),
		RawKey: rawKey,
	}, nil
}

func (s *Server) GetDashboardMetrics(ctx context.Context, req *pb.GetDashboardMetricsRequest) (*pb.GetDashboardMetricsResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if s.portalStore == nil {
		return nil, status.Error(codes.Unimplemented, "tenant portal not configured")
	}

	now := time.Now().UTC()
	todayStart := now.Truncate(24 * time.Hour)
	sevenDaysAgo := todayStart.AddDate(0, 0, -7)
	thirtyDaysAgo := todayStart.AddDate(0, 0, -30)

	metrics, err := s.portalStore.GetDashboardMetrics(ctx, tenantID, todayStart, sevenDaysAgo, thirtyDaysAgo)
	if err != nil {
		return nil, mapDomainError(err)
	}

	// Get daily limit from tenant
	tenant, err := s.portalStore.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, mapDomainError(err)
	}

	return &pb.GetDashboardMetricsResponse{
		TransfersToday: metrics.TransfersToday,
		VolumeTodayUsd: metrics.VolumeTodayUSD.String(),
		CompletedToday: metrics.CompletedToday,
		FailedToday:    metrics.FailedToday,
		Transfers_7D:   metrics.Transfers7D,
		Volume_7DUsd:   metrics.Volume7DUSD.String(),
		Fees_7DUsd:     metrics.Fees7DUSD.String(),
		Transfers_30D:  metrics.Transfers30D,
		Volume_30DUsd:  metrics.Volume30DUSD.String(),
		Fees_30DUsd:    metrics.Fees30DUSD.String(),
		SuccessRate_30D: metrics.SuccessRate30D.String(),
		DailyLimitUsd:  tenant.DailyLimitUSD.String(),
		DailyUsageUsd:  metrics.VolumeTodayUSD.String(),
	}, nil
}

func (s *Server) GetTransferStats(ctx context.Context, req *pb.GetTransferStatsRequest) (*pb.GetTransferStatsResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if s.portalStore == nil {
		return nil, status.Error(codes.Unimplemented, "tenant portal not configured")
	}

	now := time.Now().UTC()
	var from time.Time
	switch req.GetPeriod() {
	case "24h":
		from = now.Add(-24 * time.Hour)
	case "7d":
		from = now.AddDate(0, 0, -7)
	case "30d":
		from = now.AddDate(0, 0, -30)
	default:
		from = now.Add(-24 * time.Hour)
	}

	var buckets []domain.TransferStatsBucket
	switch req.GetGranularity() {
	case "day":
		buckets, err = s.portalStore.GetTransferStatsDaily(ctx, tenantID, from, now)
	default: // "hour" is default
		buckets, err = s.portalStore.GetTransferStatsHourly(ctx, tenantID, from, now)
	}
	if err != nil {
		return nil, mapDomainError(err)
	}

	pbBuckets := make([]*pb.TransferStatsBucket, len(buckets))
	for i := range buckets {
		pbBuckets[i] = &pb.TransferStatsBucket{
			Timestamp: timestamppb.New(buckets[i].Timestamp),
			Total:     buckets[i].Total,
			Completed: buckets[i].Completed,
			Failed:    buckets[i].Failed,
			VolumeUsd: buckets[i].VolumeUSD.String(),
			FeesUsd:   buckets[i].FeesUSD.String(),
		}
	}

	return &pb.GetTransferStatsResponse{Buckets: pbBuckets}, nil
}

func (s *Server) GetFeeReport(ctx context.Context, req *pb.GetFeeReportRequest) (*pb.GetFeeReportResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if s.portalStore == nil {
		return nil, status.Error(codes.Unimplemented, "tenant portal not configured")
	}

	from := time.Now().UTC().AddDate(0, 0, -30) // Default: last 30 days
	to := time.Now().UTC()

	if req.GetFrom() != nil {
		from = req.GetFrom().AsTime()
	}
	if req.GetTo() != nil {
		to = req.GetTo().AsTime()
	}

	entries, err := s.portalStore.GetFeeReportByCorridor(ctx, tenantID, from, to)
	if err != nil {
		return nil, mapDomainError(err)
	}

	totalFees := "0"
	pbEntries := make([]*pb.FeeReportEntry, len(entries))
	for i := range entries {
		pbEntries[i] = &pb.FeeReportEntry{
			SourceCurrency:  entries[i].SourceCurrency,
			DestCurrency:    entries[i].DestCurrency,
			TransferCount:   entries[i].TransferCount,
			TotalVolumeUsd:  entries[i].TotalVolumeUSD.String(),
			OnRampFeesUsd:   entries[i].OnRampFeesUSD.String(),
			OffRampFeesUsd:  entries[i].OffRampFeesUSD.String(),
			NetworkFeesUsd:  entries[i].NetworkFeesUSD.String(),
			TotalFeesUsd:    entries[i].TotalFeesUSD.String(),
		}
	}

	// Sum total fees
	if len(entries) > 0 {
		sum := entries[0].TotalFeesUSD
		for i := 1; i < len(entries); i++ {
			sum = sum.Add(entries[i].TotalFeesUSD)
		}
		totalFees = sum.String()
	}

	return &pb.GetFeeReportResponse{
		Entries:      pbEntries,
		TotalFeesUsd: totalFees,
	}, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Proto mapping helpers
// ──────────────────────────────────────────────────────────────────────────────

func tenantProfileToProto(t *domain.Tenant) *pb.TenantProfile {
	if t == nil {
		return nil
	}
	p := &pb.TenantProfile{
		Id:               t.ID.String(),
		Name:             t.Name,
		Slug:             t.Slug,
		Status:           string(t.Status),
		SettlementModel:  string(t.SettlementModel),
		KybStatus:        string(t.KYBStatus),
		FeeSchedule: &pb.TenantFeeSchedule{
			OnRampBps:  int32(t.FeeSchedule.OnRampBPS),
			OffRampBps: int32(t.FeeSchedule.OffRampBPS),
			MinFeeUsd:  t.FeeSchedule.MinFeeUSD.String(),
			MaxFeeUsd:  t.FeeSchedule.MaxFeeUSD.String(),
		},
		DailyLimitUsd:    t.DailyLimitUSD.String(),
		PerTransferLimit: t.PerTransferLimit.String(),
		WebhookUrl:       t.WebhookURL,
		CreatedAt:        timestamppb.New(t.CreatedAt),
		UpdatedAt:        timestamppb.New(t.UpdatedAt),
	}
	if t.KYBVerifiedAt != nil {
		p.KybVerifiedAt = timestamppb.New(*t.KYBVerifiedAt)
	}
	return p
}

func apiKeyToProto(k *domain.APIKey) *pb.APIKeyInfo {
	if k == nil {
		return nil
	}
	info := &pb.APIKeyInfo{
		Id:          k.ID.String(),
		KeyPrefix:   k.KeyPrefix,
		Environment: k.Environment,
		Name:        k.Name,
		IsActive:    k.IsActive,
		CreatedAt:   timestamppb.New(k.CreatedAt),
	}
	if k.ExpiresAt != nil {
		info.ExpiresAt = timestamppb.New(*k.ExpiresAt)
	}
	return info
}

// ──────────────────────────────────────────────────────────────────────────────
// Key generation helpers
// ──────────────────────────────────────────────────────────────────────────────

// generateAPIKey creates a raw API key, its SHA-256 hash, and the display prefix.
// Format: sk_live_<32 random hex chars> or sk_test_<32 random hex chars>
func generateAPIKey(environment string) (rawKey, keyHash, keyPrefix string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", "", fmt.Errorf("generating random bytes: %w", err)
	}

	prefix := "sk_live_"
	if environment == "TEST" {
		prefix = "sk_test_"
	}

	rawKey = prefix + hex.EncodeToString(buf)
	hash := sha256.Sum256([]byte(rawKey))
	keyHash = hex.EncodeToString(hash[:])
	keyPrefix = rawKey[:12] // e.g. "sk_live_ab3c"

	return rawKey, keyHash, keyPrefix, nil
}

// generateWebhookSecret creates a random 32-byte hex string for HMAC-SHA256 signing.
func generateWebhookSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "whsec_" + hex.EncodeToString(buf), nil
}
