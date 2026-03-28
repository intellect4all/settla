package observability

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RegisterPoolMetrics starts a background goroutine that polls pool.Stat()
// every 5 seconds and updates the PgxPool* gauges. The goroutine stops when
// ctx is cancelled.
func RegisterPoolMetrics(ctx context.Context, pool *pgxpool.Pool, dbName string, m *Metrics) {
	if pool == nil || m == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				stat := pool.Stat()
				m.PgxPoolMaxConns.WithLabelValues(dbName).Set(float64(stat.MaxConns()))
				m.PgxPoolCurrentConns.WithLabelValues(dbName).Set(float64(stat.AcquiredConns() + stat.IdleConns()))
				m.PgxPoolIdleConns.WithLabelValues(dbName).Set(float64(stat.IdleConns()))
			}
		}
	}()
}
