package main

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	settlagrpc "github.com/intellect4all/settla/api/grpc"
	"github.com/intellect4all/settla/core/maintenance"
	"github.com/intellect4all/settla/domain"
	"github.com/intellect4all/settla/store/transferdb"
)

// pgxPoolDBExecutor adapts pgxpool.Pool to the maintenance.DBExecutor interface.
type pgxPoolDBExecutor struct {
	pool *pgxpool.Pool
}

func newPgxPoolDBExecutor(pool *pgxpool.Pool) maintenance.DBExecutor {
	return &pgxPoolDBExecutor{pool: pool}
}

type pgxCommandTag struct{ rowsAffected int64 }

func (t pgxCommandTag) RowsAffected() int64 { return t.rowsAffected }

type pgxRows struct {
	rows interface {
		Next() bool
		Scan(dest ...any) error
		Close()
		Err() error
	}
}

func (r *pgxRows) Next() bool                     { return r.rows.Next() }
func (r *pgxRows) Scan(dest ...interface{}) error  { return r.rows.Scan(dest...) }
func (r *pgxRows) Close()                          { r.rows.Close() }
func (r *pgxRows) Err() error                      { return r.rows.Err() }

func (e *pgxPoolDBExecutor) Exec(ctx context.Context, sql string, args ...interface{}) (maintenance.CommandTag, error) {
	tag, err := e.pool.Exec(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgxCommandTag{rowsAffected: tag.RowsAffected()}, nil
}

func (e *pgxPoolDBExecutor) Query(ctx context.Context, sql string, args ...interface{}) (maintenance.Rows, error) {
	rows, err := e.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return &pgxRows{rows: rows}, nil
}

// API Key Validator Adapter

type apiKeyValidatorAdapter struct {
	q *transferdb.Queries
}

func (a *apiKeyValidatorAdapter) ValidateAPIKey(ctx context.Context, keyHash string) (settlagrpc.APIKeyResult, error) {
	row, err := a.q.ValidateAPIKey(ctx, keyHash)
	if err != nil {
		return settlagrpc.APIKeyResult{}, err
	}

	return settlagrpc.APIKeyResult{
		TenantID:         row.TenantUuid.String(),
		Slug:             row.Slug,
		Status:           row.TenantStatus,
		FeeScheduleJSON:  string(row.FeeSchedule),
		DailyLimitUSD:    decimalFromNumeric(row.DailyLimitUsd).String(),
		PerTransferLimit: decimalFromNumeric(row.PerTransferLimit).String(),
	}, nil
}

// Composite Analytics Adapters
// Bridge the AnalyticsAdapter and ExtendedAnalyticsAdapter into the unified
// interfaces required by the snapshot scheduler and exporter.

type compositeAnalyticsQuerier struct {
	analytics *transferdb.AnalyticsAdapter
	ext       *transferdb.ExtendedAnalyticsAdapter
}

func (c *compositeAnalyticsQuerier) GetCorridorMetrics(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.CorridorMetrics, error) {
	return c.analytics.GetCorridorMetrics(ctx, tenantID, from, to)
}

func (c *compositeAnalyticsQuerier) GetFeeBreakdown(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.FeeBreakdownEntry, error) {
	return c.ext.GetFeeBreakdown(ctx, tenantID, from, to)
}

func (c *compositeAnalyticsQuerier) GetTransferLatencyPercentiles(ctx context.Context, tenantID uuid.UUID, from, to time.Time) (*domain.LatencyPercentiles, error) {
	return c.analytics.GetTransferLatencyPercentiles(ctx, tenantID, from, to)
}

func (c *compositeAnalyticsQuerier) GetCryptoDepositAnalytics(ctx context.Context, tenantID uuid.UUID, from, to time.Time) (*domain.DepositAnalytics, error) {
	return c.ext.GetCryptoDepositAnalytics(ctx, tenantID, from, to)
}

func (c *compositeAnalyticsQuerier) GetBankDepositAnalytics(ctx context.Context, tenantID uuid.UUID, from, to time.Time) (*domain.DepositAnalytics, error) {
	return c.ext.GetBankDepositAnalytics(ctx, tenantID, from, to)
}

type compositeExportSource struct {
	analytics *transferdb.AnalyticsAdapter
	ext       *transferdb.ExtendedAnalyticsAdapter
}

func (c *compositeExportSource) GetFeeBreakdown(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.FeeBreakdownEntry, error) {
	return c.ext.GetFeeBreakdown(ctx, tenantID, from, to)
}

func (c *compositeExportSource) GetProviderPerformance(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.ProviderPerformance, error) {
	return c.ext.GetProviderPerformance(ctx, tenantID, from, to)
}

func (c *compositeExportSource) GetCorridorMetrics(ctx context.Context, tenantID uuid.UUID, from, to time.Time) ([]domain.CorridorMetrics, error) {
	return c.analytics.GetCorridorMetrics(ctx, tenantID, from, to)
}
