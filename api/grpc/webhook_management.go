package grpc

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/intellect4all/settla/gen/settla/v1"

	"github.com/intellect4all/settla/domain"
)

// WebhookManagementStore extends TenantPortalStore with webhook management queries.
type WebhookManagementStore interface {
	// Delivery logs
	InsertWebhookDelivery(ctx context.Context, d *domain.WebhookDelivery) error
	ListWebhookDeliveries(ctx context.Context, tenantID uuid.UUID, eventType, status string, pageSize, pageOffset int32) ([]domain.WebhookDelivery, int64, error)
	GetWebhookDelivery(ctx context.Context, tenantID, deliveryID uuid.UUID) (*domain.WebhookDelivery, error)
	GetWebhookDeliveryStats(ctx context.Context, tenantID uuid.UUID, since time.Time) (*domain.WebhookDeliveryStats, error)

	// Event subscriptions
	ListWebhookEventSubscriptions(ctx context.Context, tenantID uuid.UUID) ([]domain.WebhookEventSubscription, error)
	UpdateWebhookEventSubscriptions(ctx context.Context, tenantID uuid.UUID, eventTypes []string) ([]domain.WebhookEventSubscription, error)
}

// WithWebhookManagementStore sets the webhook management store.
func WithWebhookManagementStore(s WebhookManagementStore) ServerOption {
	return func(srv *Server) { srv.webhookStore = s }
}

// ──────────────────────────────────────────────────────────────────────────────
// Webhook delivery log RPCs
// ──────────────────────────────────────────────────────────────────────────────

func (s *Server) ListWebhookDeliveries(ctx context.Context, req *pb.ListWebhookDeliveriesRequest) (*pb.ListWebhookDeliveriesResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if s.webhookStore == nil {
		return nil, status.Error(codes.Unimplemented, "webhook management not configured")
	}

	pageSize := req.GetPageSize()
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 50
	}

	deliveries, total, err := s.webhookStore.ListWebhookDeliveries(
		ctx, tenantID, req.GetEventType(), req.GetStatus(), pageSize, req.GetPageOffset(),
	)
	if err != nil {
		return nil, mapDomainError(err)
	}

	pbDeliveries := make([]*pb.WebhookDeliveryInfo, len(deliveries))
	for i := range deliveries {
		pbDeliveries[i] = webhookDeliveryToProto(&deliveries[i])
	}

	return &pb.ListWebhookDeliveriesResponse{
		Deliveries: pbDeliveries,
		TotalCount: total,
	}, nil
}

func (s *Server) GetWebhookDelivery(ctx context.Context, req *pb.GetWebhookDeliveryRequest) (*pb.GetWebhookDeliveryResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	deliveryID, err := parseUUID(req.GetDeliveryId(), "delivery_id")
	if err != nil {
		return nil, err
	}

	if s.webhookStore == nil {
		return nil, status.Error(codes.Unimplemented, "webhook management not configured")
	}

	d, err := s.webhookStore.GetWebhookDelivery(ctx, tenantID, deliveryID)
	if err != nil {
		return nil, mapDomainError(err)
	}

	return &pb.GetWebhookDeliveryResponse{
		Delivery:    webhookDeliveryToProto(d),
		RequestBody: d.RequestBody,
	}, nil
}

func (s *Server) GetWebhookDeliveryStats(ctx context.Context, req *pb.GetWebhookDeliveryStatsRequest) (*pb.GetWebhookDeliveryStatsResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if s.webhookStore == nil {
		return nil, status.Error(codes.Unimplemented, "webhook management not configured")
	}

	now := time.Now().UTC()
	var since time.Time
	switch req.GetPeriod() {
	case "24h":
		since = now.Add(-24 * time.Hour)
	case "7d":
		since = now.AddDate(0, 0, -7)
	case "30d":
		since = now.AddDate(0, 0, -30)
	default:
		since = now.Add(-24 * time.Hour)
	}

	stats, err := s.webhookStore.GetWebhookDeliveryStats(ctx, tenantID, since)
	if err != nil {
		return nil, mapDomainError(err)
	}

	return &pb.GetWebhookDeliveryStatsResponse{
		TotalDeliveries: stats.TotalDeliveries,
		Successful:      stats.Successful,
		Failed:          stats.Failed,
		DeadLettered:    stats.DeadLettered,
		Pending:         stats.Pending,
		AvgLatencyMs:    stats.AvgLatencyMs,
		P95LatencyMs:    stats.P95LatencyMs,
	}, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Webhook event subscription RPCs
// ──────────────────────────────────────────────────────────────────────────────

func (s *Server) ListWebhookEventSubscriptions(ctx context.Context, req *pb.ListWebhookEventSubscriptionsRequest) (*pb.ListWebhookEventSubscriptionsResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if s.webhookStore == nil {
		return nil, status.Error(codes.Unimplemented, "webhook management not configured")
	}

	subs, err := s.webhookStore.ListWebhookEventSubscriptions(ctx, tenantID)
	if err != nil {
		return nil, mapDomainError(err)
	}

	pbSubs := make([]*pb.WebhookEventSubscriptionInfo, len(subs))
	for i := range subs {
		pbSubs[i] = &pb.WebhookEventSubscriptionInfo{
			Id:        subs[i].ID.String(),
			EventType: subs[i].EventType,
			CreatedAt: timestamppb.New(subs[i].CreatedAt),
		}
	}

	return &pb.ListWebhookEventSubscriptionsResponse{
		Subscriptions:       pbSubs,
		AvailableEventTypes: domain.WebhookEventTypes,
	}, nil
}

func (s *Server) UpdateWebhookEventSubscriptions(ctx context.Context, req *pb.UpdateWebhookEventSubscriptionsRequest) (*pb.UpdateWebhookEventSubscriptionsResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if s.webhookStore == nil {
		return nil, status.Error(codes.Unimplemented, "webhook management not configured")
	}

	// Validate event types
	validTypes := make(map[string]bool)
	for _, t := range domain.WebhookEventTypes {
		validTypes[t] = true
	}
	for _, et := range req.GetEventTypes() {
		if !validTypes[et] {
			return nil, status.Errorf(codes.InvalidArgument, "unknown event type: %s", et)
		}
	}

	subs, err := s.webhookStore.UpdateWebhookEventSubscriptions(ctx, tenantID, req.GetEventTypes())
	if err != nil {
		return nil, mapDomainError(err)
	}

	pbSubs := make([]*pb.WebhookEventSubscriptionInfo, len(subs))
	for i := range subs {
		pbSubs[i] = &pb.WebhookEventSubscriptionInfo{
			Id:        subs[i].ID.String(),
			EventType: subs[i].EventType,
			CreatedAt: timestamppb.New(subs[i].CreatedAt),
		}
	}

	return &pb.UpdateWebhookEventSubscriptionsResponse{
		Subscriptions: pbSubs,
	}, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Test webhook RPC
// ──────────────────────────────────────────────────────────────────────────────

func (s *Server) TestWebhook(ctx context.Context, req *pb.TestWebhookRequest) (*pb.TestWebhookResponse, error) {
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

	if tenant.WebhookURL == "" {
		return nil, status.Error(codes.FailedPrecondition, "no webhook URL configured")
	}

	// Build test payload
	testPayload := map[string]any{
		"id":          uuid.New().String(),
		"event_type":  "webhook.test",
		"transfer_id": uuid.Nil.String(),
		"tenant_id":   tenantID.String(),
		"data": map[string]string{
			"message": "This is a test webhook from Settla",
		},
		"created_at": time.Now().UTC().Format(time.RFC3339),
	}

	body, err := json.Marshal(testPayload)
	if err != nil {
		return nil, status.Error(codes.Internal, "failed to marshal test payload")
	}

	// Sign with HMAC-SHA256
	mac := hmac.New(sha256.New, []byte(tenant.WebhookSecret))
	mac.Write(body)
	signature := hex.EncodeToString(mac.Sum(nil))

	// Send HTTP POST
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, tenant.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return &pb.TestWebhookResponse{
			Success: false,
			Error:   fmt.Sprintf("invalid webhook URL: %v", err),
		}, nil
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Settla-Signature", signature)
	httpReq.Header.Set("X-Settla-Event", "webhook.test")
	httpReq.Header.Set("X-Settla-Delivery", testPayload["id"].(string))

	client := &http.Client{Timeout: 10 * time.Second}
	start := time.Now()
	resp, err := client.Do(httpReq)
	durationMs := int32(time.Since(start).Milliseconds())

	if err != nil {
		return &pb.TestWebhookResponse{
			Success:    false,
			DurationMs: durationMs,
			Error:      fmt.Sprintf("delivery failed: %v", err),
		}, nil
	}
	defer resp.Body.Close()

	success := resp.StatusCode >= 200 && resp.StatusCode < 300
	errMsg := ""
	if !success {
		errMsg = fmt.Sprintf("received HTTP %d", resp.StatusCode)
	}

	return &pb.TestWebhookResponse{
		Success:    success,
		StatusCode: int32(resp.StatusCode),
		DurationMs: durationMs,
		Error:      errMsg,
	}, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Proto mapping helpers
// ──────────────────────────────────────────────────────────────────────────────

func webhookDeliveryToProto(d *domain.WebhookDelivery) *pb.WebhookDeliveryInfo {
	if d == nil {
		return nil
	}

	info := &pb.WebhookDeliveryInfo{
		Id:           d.ID.String(),
		TenantId:     d.TenantID.String(),
		EventType:    d.EventType,
		DeliveryId:   d.DeliveryID,
		WebhookUrl:   d.WebhookURL,
		Status:       d.Status,
		Attempt:      d.Attempt,
		MaxAttempts:  d.MaxAttempts,
		ErrorMessage: d.ErrorMessage,
		CreatedAt:    timestamppb.New(d.CreatedAt),
	}
	if d.TransferID != nil {
		info.TransferId = d.TransferID.String()
	}
	if d.StatusCode != nil {
		info.StatusCode = *d.StatusCode
	}
	if d.DurationMs != nil {
		info.DurationMs = *d.DurationMs
	}
	if d.DeliveredAt != nil {
		info.DeliveredAt = timestamppb.New(*d.DeliveredAt)
	}
	return info
}
