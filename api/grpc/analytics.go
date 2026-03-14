package grpc

import (
	"context"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/intellect4all/settla/gen/settla/v1"

	"github.com/intellect4all/settla/domain"
)

// AnalyticsStore provides enhanced analytics data access.
type AnalyticsStore interface {
	GetTransferStatusDistribution(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.StatusCount, error)
	GetCorridorMetrics(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.CorridorMetrics, error)
	GetTransferLatencyPercentiles(ctx context.Context, tenantID uuid.UUID, from, to time.Time) (*domain.LatencyPercentiles, error)
	GetVolumeComparison(ctx context.Context, tenantID uuid.UUID, previousStart, currentStart, currentEnd time.Time) (*domain.VolumeComparison, error)
	GetRecentActivity(ctx context.Context, tenantID uuid.UUID, limit int32) ([]domain.ActivityItem, error)
}

// WithAnalyticsStore sets the analytics store.
func WithAnalyticsStore(s AnalyticsStore) ServerOption {
	return func(srv *Server) { srv.analyticsStore = s }
}

// periodToTimeRange converts a period string to from/to time range.
func periodToTimeRange(period string) (from, to time.Time) {
	to = time.Now().UTC()
	switch period {
	case "24h":
		from = to.Add(-24 * time.Hour)
	case "7d":
		from = to.AddDate(0, 0, -7)
	case "30d":
		from = to.AddDate(0, 0, -30)
	default:
		from = to.Add(-24 * time.Hour)
	}
	return from, to
}

func (s *Server) GetTransferStatusDistribution(ctx context.Context, req *pb.GetTransferStatusDistributionRequest) (*pb.GetTransferStatusDistributionResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if s.analyticsStore == nil {
		return nil, status.Error(codes.Unimplemented, "analytics not configured")
	}

	from, to := periodToTimeRange(req.GetPeriod())
	statuses, err := s.analyticsStore.GetTransferStatusDistribution(ctx, tenantID, from, to)
	if err != nil {
		return nil, mapDomainError(err)
	}

	pbStatuses := make([]*pb.StatusCount, len(statuses))
	for i := range statuses {
		pbStatuses[i] = &pb.StatusCount{
			Status: statuses[i].Status,
			Count:  statuses[i].Count,
		}
	}

	return &pb.GetTransferStatusDistributionResponse{Statuses: pbStatuses}, nil
}

func (s *Server) GetCorridorMetrics(ctx context.Context, req *pb.GetCorridorMetricsRequest) (*pb.GetCorridorMetricsResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if s.analyticsStore == nil {
		return nil, status.Error(codes.Unimplemented, "analytics not configured")
	}

	from, to := periodToTimeRange(req.GetPeriod())
	corridors, err := s.analyticsStore.GetCorridorMetrics(ctx, tenantID, from, to)
	if err != nil {
		return nil, mapDomainError(err)
	}

	pbCorridors := make([]*pb.CorridorMetric, len(corridors))
	for i := range corridors {
		pbCorridors[i] = &pb.CorridorMetric{
			SourceCurrency: corridors[i].SourceCurrency,
			DestCurrency:   corridors[i].DestCurrency,
			TransferCount:  corridors[i].TransferCount,
			VolumeUsd:      corridors[i].VolumeUSD.String(),
			FeesUsd:        corridors[i].FeesUSD.String(),
			Completed:      corridors[i].Completed,
			Failed:         corridors[i].Failed,
			SuccessRate:    corridors[i].SuccessRate.String(),
			AvgLatencyMs:   corridors[i].AvgLatencyMs,
		}
	}

	return &pb.GetCorridorMetricsResponse{Corridors: pbCorridors}, nil
}

func (s *Server) GetTransferLatencyPercentiles(ctx context.Context, req *pb.GetTransferLatencyPercentilesRequest) (*pb.GetTransferLatencyPercentilesResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if s.analyticsStore == nil {
		return nil, status.Error(codes.Unimplemented, "analytics not configured")
	}

	from, to := periodToTimeRange(req.GetPeriod())
	latency, err := s.analyticsStore.GetTransferLatencyPercentiles(ctx, tenantID, from, to)
	if err != nil {
		return nil, mapDomainError(err)
	}

	return &pb.GetTransferLatencyPercentilesResponse{
		SampleCount: latency.SampleCount,
		P50Ms:       latency.P50Ms,
		P90Ms:       latency.P90Ms,
		P95Ms:       latency.P95Ms,
		P99Ms:       latency.P99Ms,
	}, nil
}

func (s *Server) GetVolumeComparison(ctx context.Context, req *pb.GetVolumeComparisonRequest) (*pb.GetVolumeComparisonResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if s.analyticsStore == nil {
		return nil, status.Error(codes.Unimplemented, "analytics not configured")
	}

	now := time.Now().UTC()
	var currentStart, previousStart, currentEnd time.Time
	switch req.GetPeriod() {
	case "30d":
		currentStart = now.AddDate(0, 0, -30)
		previousStart = now.AddDate(0, 0, -60)
	default: // "7d"
		currentStart = now.AddDate(0, 0, -7)
		previousStart = now.AddDate(0, 0, -14)
	}
	currentEnd = now

	comparison, err := s.analyticsStore.GetVolumeComparison(ctx, tenantID, previousStart, currentStart, currentEnd)
	if err != nil {
		return nil, mapDomainError(err)
	}

	return &pb.GetVolumeComparisonResponse{
		CurrentCount:      comparison.CurrentCount,
		CurrentVolumeUsd:  comparison.CurrentVolumeUSD.String(),
		CurrentFeesUsd:    comparison.CurrentFeesUSD.String(),
		PreviousCount:     comparison.PreviousCount,
		PreviousVolumeUsd: comparison.PreviousVolumeUSD.String(),
		PreviousFeesUsd:   comparison.PreviousFeesUSD.String(),
	}, nil
}

func (s *Server) GetRecentActivity(ctx context.Context, req *pb.GetRecentActivityRequest) (*pb.GetRecentActivityResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if s.analyticsStore == nil {
		return nil, status.Error(codes.Unimplemented, "analytics not configured")
	}

	limit := req.GetLimit()
	if limit <= 0 || limit > 50 {
		limit = 20
	}

	items, err := s.analyticsStore.GetRecentActivity(ctx, tenantID, limit)
	if err != nil {
		return nil, mapDomainError(err)
	}

	pbItems := make([]*pb.ActivityItem, len(items))
	for i := range items {
		pbItems[i] = &pb.ActivityItem{
			TransferId:     items[i].TransferID,
			ExternalRef:    items[i].ExternalRef,
			Status:         items[i].Status,
			SourceCurrency: items[i].SourceCurrency,
			SourceAmount:   items[i].SourceAmount.String(),
			DestCurrency:   items[i].DestCurrency,
			DestAmount:     items[i].DestAmount.String(),
			UpdatedAt:      timestamppb.New(items[i].UpdatedAt),
			FailureReason:  items[i].FailureReason,
		}
	}

	return &pb.GetRecentActivityResponse{Items: pbItems}, nil
}
