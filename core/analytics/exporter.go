package analytics

import (
	"context"
	"encoding/csv"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/intellect4all/settla/domain"
)

// ExportDataSource provides data for export jobs.
type ExportDataSource interface {
	GetFeeBreakdown(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.FeeBreakdownEntry, error)
	GetProviderPerformance(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.ProviderPerformance, error)
	GetCorridorMetrics(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.CorridorMetrics, error)
}

// ExportJobStore provides export job persistence.
type ExportJobStore interface {
	ListPendingExportJobs(ctx context.Context, batchSize int32) ([]domain.ExportJob, error)
	UpdateExportJobStatus(ctx context.Context, id uuid.UUID, status, filePath, downloadURL string, downloadExpiresAt *time.Time, rowCount int64, errorMessage string, completedAt *time.Time) error
}

// Exporter polls for pending export jobs and generates CSV/JSON files.
type Exporter struct {
	source    ExportDataSource
	jobStore  ExportJobStore
	logger    *slog.Logger
	storagePath string
	pollInterval time.Duration
}

// NewExporter creates a new Exporter.
func NewExporter(
	source ExportDataSource,
	jobStore ExportJobStore,
	storagePath string,
	logger *slog.Logger,
) *Exporter {
	return &Exporter{
		source:      source,
		jobStore:    jobStore,
		logger:      logger.With("module", "core.analytics.exporter"),
		storagePath: storagePath,
		pollInterval: 5 * time.Second,
	}
}

// Start begins the exporter polling loop.
func (e *Exporter) Start(ctx context.Context) error {
	e.logger.Info("settla-analytics: exporter starting", "storage_path", e.storagePath)

	if err := os.MkdirAll(e.storagePath, 0o750); err != nil {
		return fmt.Errorf("settla-analytics: creating storage directory: %w", err)
	}

	ticker := time.NewTicker(e.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			e.logger.Info("settla-analytics: exporter stopped")
			return ctx.Err()
		case <-ticker.C:
			if err := e.processPending(ctx); err != nil {
				e.logger.Error("settla-analytics: export processing failed", "error", err)
			}
		}
	}
}

func (e *Exporter) processPending(ctx context.Context) error {
	jobs, err := e.jobStore.ListPendingExportJobs(ctx, 10)
	if err != nil {
		return fmt.Errorf("listing pending jobs: %w", err)
	}

	for _, job := range jobs {
		if err := e.processJob(ctx, job); err != nil {
			e.logger.Error("settla-analytics: export job failed",
				"job_id", job.ID,
				"error", err,
			)
			now := time.Now().UTC()
			_ = e.jobStore.UpdateExportJobStatus(ctx, job.ID,
				"failed", "", "", nil, 0, err.Error(), &now)
		}
	}
	return nil
}

func (e *Exporter) processJob(ctx context.Context, job domain.ExportJob) error {
	e.logger.Info("settla-analytics: processing export job",
		"job_id", job.ID,
		"export_type", job.ExportType,
		"tenant_id", job.TenantID,
	)

	// Mark as processing
	_ = e.jobStore.UpdateExportJobStatus(ctx, job.ID, "processing", "", "", nil, 0, "", nil)

	// Parse time range from parameters
	from, to := e.parseTimeRange(job.Parameters)

	fileName := fmt.Sprintf("%s_%s_%s.csv", job.ExportType, job.TenantID, job.ID)
	filePath := filepath.Join(e.storagePath, fileName)

	rowCount, err := e.generateCSV(ctx, job.TenantID, job.ExportType, from, to, filePath)
	if err != nil {
		return fmt.Errorf("generating CSV: %w", err)
	}

	now := time.Now().UTC()
	expiresAt := now.Add(1 * time.Hour)
	downloadURL := fmt.Sprintf("/v1/analytics/export/%s/download", job.ID)

	return e.jobStore.UpdateExportJobStatus(ctx, job.ID,
		"completed", filePath, downloadURL, &expiresAt, rowCount, "", &now)
}

func (e *Exporter) parseTimeRange(params map[string]any) (time.Time, time.Time) {
	to := time.Now().UTC()
	from := to.AddDate(0, 0, -7)

	if period, ok := params["period"].(string); ok {
		switch period {
		case "24h":
			from = to.Add(-24 * time.Hour)
		case "30d":
			from = to.AddDate(0, 0, -30)
		}
	}

	if fromStr, ok := params["from"].(string); ok {
		if t, err := time.Parse(time.RFC3339, fromStr); err == nil {
			from = t
		}
	}
	if toStr, ok := params["to"].(string); ok {
		if t, err := time.Parse(time.RFC3339, toStr); err == nil {
			to = t
		}
	}

	return from, to
}

func (e *Exporter) generateCSV(ctx context.Context, tenantID uuid.UUID, exportType string, from, to time.Time, filePath string) (int64, error) {
	f, err := os.Create(filePath)
	if err != nil {
		return 0, fmt.Errorf("creating file: %w", err)
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	switch exportType {
	case "transfers":
		return e.exportTransfers(ctx, tenantID, from, to, w)
	case "fees":
		return e.exportFees(ctx, tenantID, from, to, w)
	case "providers":
		return e.exportProviders(ctx, tenantID, from, to, w)
	default:
		return 0, fmt.Errorf("unsupported export type: %s", exportType)
	}
}

func (e *Exporter) exportTransfers(ctx context.Context, tenantID uuid.UUID, from, to time.Time, w *csv.Writer) (int64, error) {
	corridors, err := e.source.GetCorridorMetrics(ctx, tenantID, from, to)
	if err != nil {
		return 0, err
	}

	_ = w.Write([]string{"source_currency", "dest_currency", "transfer_count", "volume_usd", "fees_usd", "completed", "failed", "success_rate", "avg_latency_ms"})

	for _, c := range corridors {
		_ = w.Write([]string{
			c.SourceCurrency, c.DestCurrency,
			fmt.Sprintf("%d", c.TransferCount),
			c.VolumeUSD.String(), c.FeesUSD.String(),
			fmt.Sprintf("%d", c.Completed), fmt.Sprintf("%d", c.Failed),
			c.SuccessRate.String(), fmt.Sprintf("%d", c.AvgLatencyMs),
		})
	}

	return int64(len(corridors)), nil
}

func (e *Exporter) exportFees(ctx context.Context, tenantID uuid.UUID, from, to time.Time, w *csv.Writer) (int64, error) {
	fees, err := e.source.GetFeeBreakdown(ctx, tenantID, from, to)
	if err != nil {
		return 0, err
	}

	_ = w.Write([]string{"source_currency", "dest_currency", "transfer_count", "volume_usd", "on_ramp_fees_usd", "off_ramp_fees_usd", "network_fees_usd", "total_fees_usd"})

	for _, f := range fees {
		_ = w.Write([]string{
			f.SourceCurrency, f.DestCurrency,
			fmt.Sprintf("%d", f.TransferCount),
			f.VolumeUSD.String(), f.OnRampFeesUSD.String(),
			f.OffRampFeesUSD.String(), f.NetworkFeesUSD.String(),
			f.TotalFeesUSD.String(),
		})
	}

	return int64(len(fees)), nil
}

func (e *Exporter) exportProviders(ctx context.Context, tenantID uuid.UUID, from, to time.Time, w *csv.Writer) (int64, error) {
	providers, err := e.source.GetProviderPerformance(ctx, tenantID, from, to)
	if err != nil {
		return 0, err
	}

	_ = w.Write([]string{"provider", "source_currency", "dest_currency", "transaction_count", "completed", "failed", "success_rate", "avg_settlement_ms", "total_volume"})

	for _, p := range providers {
		_ = w.Write([]string{
			p.Provider, p.SourceCurrency, p.DestCurrency,
			fmt.Sprintf("%d", p.TransactionCount),
			fmt.Sprintf("%d", p.Completed), fmt.Sprintf("%d", p.Failed),
			p.SuccessRate.String(), fmt.Sprintf("%d", p.AvgSettlementMs),
			p.TotalVolume.String(),
		})
	}

	return int64(len(providers)), nil
}
