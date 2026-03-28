package grpc

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/intellect4all/settla/gen/settla/v1"

	"github.com/intellect4all/settla/domain"
)

// ExtendedAnalyticsStore provides the new analytics query methods.
type ExtendedAnalyticsStore interface {
	GetFeeBreakdown(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.FeeBreakdownEntry, error)
	GetProviderPerformance(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.ProviderPerformance, error)
	GetCryptoDepositAnalytics(ctx context.Context, tenantID uuid.UUID, from, to time.Time) (*domain.DepositAnalytics, error)
	GetBankDepositAnalytics(ctx context.Context, tenantID uuid.UUID, from, to time.Time) (*domain.DepositAnalytics, error)
	GetReconciliationSummary(ctx context.Context, from, to time.Time) (*domain.ReconciliationSummary, error)
	GetDailySnapshots(ctx context.Context, tenantID uuid.UUID, metricType string, fromDate, toDate time.Time) ([]domain.DailySnapshot, error)
}

// ExportStore provides export job CRUD.
type ExportStore interface {
	CreateExportJob(ctx context.Context, tenantID uuid.UUID, exportType string, parameters map[string]any) (*domain.ExportJob, error)
	GetExportJob(ctx context.Context, id, tenantID uuid.UUID) (*domain.ExportJob, error)
	ListExportJobs(ctx context.Context, tenantID uuid.UUID, limit int32) ([]domain.ExportJob, error)
}

// WithExtendedAnalyticsStore sets the extended analytics store.
func WithExtendedAnalyticsStore(s ExtendedAnalyticsStore) ServerOption {
	return func(srv *Server) { srv.extAnalyticsStore = s }
}

// WithExportStore sets the export store.
func WithExportStore(s ExportStore) ServerOption {
	return func(srv *Server) { srv.exportStore = s }
}

// resolveTimeRange resolves period or from/to timestamps into a time range.
func resolveTimeRange(period string, from, to *timestamppb.Timestamp) (time.Time, time.Time) {
	if from != nil && to != nil && from.IsValid() && to.IsValid() {
		return from.AsTime(), to.AsTime()
	}
	return periodToTimeRange(period)
}

func (s *Server) GetTransferAnalytics(ctx context.Context, req *pb.GetTransferAnalyticsRequest) (*pb.GetTransferAnalyticsResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if s.analyticsStore == nil {
		return nil, status.Error(codes.Unimplemented, "analytics not configured")
	}

	from, to := resolveTimeRange(req.GetPeriod(), req.GetFrom(), req.GetTo())

	// Compose from existing analytics queries
	corridors, err := s.analyticsStore.GetCorridorMetrics(ctx, tenantID, from, to)
	if err != nil {
		return nil, mapDomainError(err)
	}

	statuses, err := s.analyticsStore.GetTransferStatusDistribution(ctx, tenantID, from, to)
	if err != nil {
		return nil, mapDomainError(err)
	}

	latency, err := s.analyticsStore.GetTransferLatencyPercentiles(ctx, tenantID, from, to)
	if err != nil {
		return nil, mapDomainError(err)
	}

	// Aggregate totals from corridors
	var totalCount int64
	totalVolume := decimal.Zero
	totalFees := decimal.Zero
	for _, c := range corridors {
		totalCount += c.TransferCount
		totalVolume = totalVolume.Add(c.VolumeUSD)
		totalFees = totalFees.Add(c.FeesUSD)
	}

	pbCorridors := make([]*pb.TransferAnalyticsCorridor, len(corridors))
	for i, c := range corridors {
		pbCorridors[i] = &pb.TransferAnalyticsCorridor{
			SourceCurrency: string(c.SourceCurrency),
			DestCurrency:   string(c.DestCurrency),
			TransferCount:  c.TransferCount,
			VolumeUsd:      c.VolumeUSD.String(),
			FeesUsd:        c.FeesUSD.String(),
			Completed:      c.Completed,
			Failed:         c.Failed,
			SuccessRate:    c.SuccessRate.String(),
			AvgLatencyMs:   c.AvgLatencyMs,
		}
	}

	pbStatuses := make([]*pb.TransferAnalyticsStatus, len(statuses))
	for i, s := range statuses {
		pbStatuses[i] = &pb.TransferAnalyticsStatus{
			Status: s.Status,
			Count:  s.Count,
		}
	}

	return &pb.GetTransferAnalyticsResponse{
		Corridors:     pbCorridors,
		Statuses:      pbStatuses,
		TotalCount:    totalCount,
		TotalVolumeUsd: totalVolume.String(),
		TotalFeesUsd:  totalFees.String(),
		SampleCount:   latency.SampleCount,
		P50Ms:         latency.P50Ms,
		P90Ms:         latency.P90Ms,
		P95Ms:         latency.P95Ms,
		P99Ms:         latency.P99Ms,
	}, nil
}

func (s *Server) GetFeeAnalytics(ctx context.Context, req *pb.GetFeeAnalyticsRequest) (*pb.GetFeeAnalyticsResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if s.extAnalyticsStore == nil {
		return nil, status.Error(codes.Unimplemented, "analytics not configured")
	}

	from, to := resolveTimeRange(req.GetPeriod(), req.GetFrom(), req.GetTo())
	entries, err := s.extAnalyticsStore.GetFeeBreakdown(ctx, tenantID, from, to)
	if err != nil {
		return nil, mapDomainError(err)
	}

	totalFees := decimal.Zero
	pbEntries := make([]*pb.FeeAnalyticsEntry, len(entries))
	for i, e := range entries {
		totalFees = totalFees.Add(e.TotalFeesUSD)
		pbEntries[i] = &pb.FeeAnalyticsEntry{
			SourceCurrency: e.SourceCurrency,
			DestCurrency:   e.DestCurrency,
			TransferCount:  e.TransferCount,
			VolumeUsd:      e.VolumeUSD.String(),
			OnRampFeesUsd:  e.OnRampFeesUSD.String(),
			OffRampFeesUsd: e.OffRampFeesUSD.String(),
			NetworkFeesUsd: e.NetworkFeesUSD.String(),
			TotalFeesUsd:   e.TotalFeesUSD.String(),
		}
	}

	return &pb.GetFeeAnalyticsResponse{
		Entries:      pbEntries,
		TotalFeesUsd: totalFees.String(),
	}, nil
}

func (s *Server) GetProviderAnalytics(ctx context.Context, req *pb.GetProviderAnalyticsRequest) (*pb.GetProviderAnalyticsResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if s.extAnalyticsStore == nil {
		return nil, status.Error(codes.Unimplemented, "analytics not configured")
	}

	from, to := resolveTimeRange(req.GetPeriod(), req.GetFrom(), req.GetTo())
	providers, err := s.extAnalyticsStore.GetProviderPerformance(ctx, tenantID, from, to)
	if err != nil {
		return nil, mapDomainError(err)
	}

	pbProviders := make([]*pb.ProviderAnalyticsEntry, len(providers))
	for i, p := range providers {
		pbProviders[i] = &pb.ProviderAnalyticsEntry{
			Provider:         p.Provider,
			SourceCurrency:   p.SourceCurrency,
			DestCurrency:     p.DestCurrency,
			TransactionCount: p.TransactionCount,
			Completed:        p.Completed,
			Failed:           p.Failed,
			SuccessRate:      p.SuccessRate.String(),
			AvgSettlementMs:  p.AvgSettlementMs,
			TotalVolume:      p.TotalVolume.String(),
		}
	}

	return &pb.GetProviderAnalyticsResponse{Providers: pbProviders}, nil
}

func (s *Server) GetReconciliationAnalytics(ctx context.Context, req *pb.GetReconciliationAnalyticsRequest) (*pb.GetReconciliationAnalyticsResponse, error) {
	_, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if s.extAnalyticsStore == nil {
		return nil, status.Error(codes.Unimplemented, "analytics not configured")
	}

	// Use last 30 days for reconciliation summary
	to := time.Now().UTC()
	from := to.AddDate(0, 0, -30)

	summary, err := s.extAnalyticsStore.GetReconciliationSummary(ctx, from, to)
	if err != nil {
		return nil, mapDomainError(err)
	}

	resp := &pb.GetReconciliationAnalyticsResponse{
		TotalRuns:        summary.TotalRuns,
		ChecksPassed:     summary.ChecksPassed,
		ChecksFailed:     summary.ChecksFailed,
		PassRate:         summary.PassRate.String(),
		NeedsReviewCount: summary.NeedsReviewCount,
	}
	if summary.LastRunAt != nil {
		resp.LastRunAt = timestamppb.New(*summary.LastRunAt)
	}

	return resp, nil
}

func (s *Server) GetDepositAnalytics(ctx context.Context, req *pb.GetDepositAnalyticsRequest) (*pb.GetDepositAnalyticsResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if s.extAnalyticsStore == nil {
		return nil, status.Error(codes.Unimplemented, "analytics not configured")
	}

	from, to := resolveTimeRange(req.GetPeriod(), req.GetFrom(), req.GetTo())

	crypto, err := s.extAnalyticsStore.GetCryptoDepositAnalytics(ctx, tenantID, from, to)
	if err != nil {
		return nil, mapDomainError(err)
	}

	bank, err := s.extAnalyticsStore.GetBankDepositAnalytics(ctx, tenantID, from, to)
	if err != nil {
		return nil, mapDomainError(err)
	}

	return &pb.GetDepositAnalyticsResponse{
		Crypto: depositAnalyticsToProto(crypto),
		Bank:   depositAnalyticsToProto(bank),
	}, nil
}

func (s *Server) CreateExportJob(ctx context.Context, req *pb.CreateExportJobRequest) (*pb.CreateExportJobResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	if err := validateNonEmpty("export_type", req.GetExportType()); err != nil {
		return nil, err
	}

	if s.exportStore == nil {
		return nil, status.Error(codes.Unimplemented, "export not configured")
	}

	params := map[string]any{
		"period": req.GetPeriod(),
		"format": req.GetFormat(),
	}
	if req.GetFrom() != nil {
		params["from"] = req.GetFrom().AsTime().Format(time.RFC3339)
	}
	if req.GetTo() != nil {
		params["to"] = req.GetTo().AsTime().Format(time.RFC3339)
	}

	job, err := s.exportStore.CreateExportJob(ctx, tenantID, req.GetExportType(), params)
	if err != nil {
		return nil, mapDomainError(err)
	}

	return &pb.CreateExportJobResponse{Job: exportJobToProto(job)}, nil
}

func (s *Server) GetExportJob(ctx context.Context, req *pb.GetExportJobRequest) (*pb.GetExportJobResponse, error) {
	tenantID, err := parseUUID(req.GetTenantId(), "tenant_id")
	if err != nil {
		return nil, err
	}

	jobID, err := parseUUID(req.GetJobId(), "job_id")
	if err != nil {
		return nil, err
	}

	if s.exportStore == nil {
		return nil, status.Error(codes.Unimplemented, "export not configured")
	}

	job, err := s.exportStore.GetExportJob(ctx, jobID, tenantID)
	if err != nil {
		return nil, mapDomainError(err)
	}

	return &pb.GetExportJobResponse{Job: exportJobToProto(job)}, nil
}

func depositAnalyticsToProto(d *domain.DepositAnalytics) *pb.DepositAnalyticsData {
	if d == nil {
		return &pb.DepositAnalyticsData{}
	}
	return &pb.DepositAnalyticsData{
		TotalSessions:     d.TotalSessions,
		CompletedSessions: d.CompletedSessions,
		ExpiredSessions:   d.ExpiredSessions,
		FailedSessions:    d.FailedSessions,
		ConversionRate:    d.ConversionRate.String(),
		TotalReceived:     d.TotalReceived.String(),
		TotalFees:         d.TotalFees.String(),
		TotalNet:          d.TotalNet.String(),
	}
}

func exportJobToProto(j *domain.ExportJob) *pb.AnalyticsExportJob {
	if j == nil {
		return nil
	}
	pj := &pb.AnalyticsExportJob{
		Id:           j.ID.String(),
		TenantId:     j.TenantID.String(),
		Status:       j.Status,
		ExportType:   j.ExportType,
		RowCount:     j.RowCount,
		DownloadUrl:  j.DownloadURL,
		ErrorMessage: j.ErrorMessage,
		CreatedAt:    timestamppb.New(j.CreatedAt),
	}
	if j.DownloadExpiresAt != nil {
		pj.DownloadExpiresAt = timestamppb.New(*j.DownloadExpiresAt)
	}
	if j.CompletedAt != nil {
		pj.CompletedAt = timestamppb.New(*j.CompletedAt)
	}
	return pj
}
