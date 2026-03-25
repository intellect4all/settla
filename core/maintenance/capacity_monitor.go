package maintenance

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// CapacityThresholds defines disk usage alert levels.
var CapacityThresholds = struct {
	WarnPercent     float64
	CriticalPercent float64
}{
	WarnPercent:     70.0,
	CriticalPercent: 85.0,
}

// DatabaseSize records a point-in-time database size measurement.
type DatabaseSize struct {
	Database  string
	SizeBytes int64
	Timestamp time.Time
}

// CapacityAlert represents a disk usage alert.
type CapacityAlert struct {
	Database    string
	SizeBytes   int64
	MaxBytes    int64
	UsedPercent float64
	Level       string // "warn" or "critical"
	Message     string
}

// CapacityMetrics holds Prometheus metrics for the capacity monitor.
// Separated from the monitor struct so callers can provide pre-registered metrics.
type CapacityMetrics struct {
	DatabaseSizeGauge *prometheus.GaugeVec
	GrowthRateGauge   *prometheus.GaugeVec
}

// NewCapacityMetrics creates and registers capacity metrics with the default Prometheus registry.
// Call this once per process, not per test.
func NewCapacityMetrics() *CapacityMetrics {
	return &CapacityMetrics{
		DatabaseSizeGauge: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "settla_database_size_bytes",
			Help: "Current database size in bytes.",
		}, []string{"database"}),
		GrowthRateGauge: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "settla_database_growth_bytes_per_hour",
			Help: "Estimated database growth rate in bytes per hour.",
		}, []string{"database"}),
	}
}

// CapacityMonitor checks disk usage and growth projections.
type CapacityMonitor struct {
	db      DBExecutor
	logger  *slog.Logger
	metrics *CapacityMetrics

	// History for growth rate calculation
	mu      sync.Mutex
	history map[string][]DatabaseSize // database -> recent measurements

	// Configuration
	databases    []string // database names to monitor
	maxHistory   int      // max data points to keep per database
	maxSizeBytes int64    // assumed max disk size for percentage calculation
}

// NewCapacityMonitor creates a capacity monitor for the given databases.
// If metrics is nil, Prometheus metrics are not exported.
func NewCapacityMonitor(
	db DBExecutor,
	logger *slog.Logger,
	databases []string,
	maxSizeBytes int64,
	metrics *CapacityMetrics,
) *CapacityMonitor {
	return &CapacityMonitor{
		db:           db,
		logger:       logger.With("module", "core.maintenance.capacity"),
		metrics:      metrics,
		databases:    databases,
		history:      make(map[string][]DatabaseSize),
		maxHistory:   96, // 24 hours of 15-minute checks
		maxSizeBytes: maxSizeBytes,
	}
}

// CheckCapacity queries database sizes, records measurements, calculates growth
// rates, and returns any alerts.
func (cm *CapacityMonitor) CheckCapacity(ctx context.Context) ([]CapacityAlert, error) {
	cm.logger.Info("settla-maintenance: capacity check starting")

	var alerts []CapacityAlert

	for _, dbName := range cm.databases {
		sizeBytes, err := cm.queryDatabaseSize(ctx, dbName)
		if err != nil {
			cm.logger.Error("settla-maintenance: failed to query database size",
				"database", dbName,
				"error", err,
			)
			continue
		}

		// Record measurement
		measurement := DatabaseSize{
			Database:  dbName,
			SizeBytes: sizeBytes,
			Timestamp: time.Now().UTC(),
		}
		cm.recordMeasurement(measurement)

		// Update Prometheus gauge
		if cm.metrics != nil {
			cm.metrics.DatabaseSizeGauge.WithLabelValues(dbName).Set(float64(sizeBytes))
		}

		// Calculate growth rate
		growthRate := cm.calculateGrowthRate(dbName)
		if growthRate > 0 && cm.metrics != nil {
			cm.metrics.GrowthRateGauge.WithLabelValues(dbName).Set(growthRate)
		}

		// Check thresholds
		if cm.maxSizeBytes > 0 {
			usedPercent := float64(sizeBytes) / float64(cm.maxSizeBytes) * 100.0

			if usedPercent >= CapacityThresholds.CriticalPercent {
				alert := CapacityAlert{
					Database:    dbName,
					SizeBytes:   sizeBytes,
					MaxBytes:    cm.maxSizeBytes,
					UsedPercent: usedPercent,
					Level:       "critical",
					Message:     FormatCapacityAlert(dbName, sizeBytes, cm.maxSizeBytes, usedPercent, "critical"),
				}
				alerts = append(alerts, alert)
				cm.logger.Error("settla-maintenance: CRITICAL disk usage",
					"database", dbName,
					"size_bytes", sizeBytes,
					"used_percent", fmt.Sprintf("%.1f%%", usedPercent),
					"growth_rate_bytes_per_hour", growthRate,
				)
			} else if usedPercent >= CapacityThresholds.WarnPercent {
				alert := CapacityAlert{
					Database:    dbName,
					SizeBytes:   sizeBytes,
					MaxBytes:    cm.maxSizeBytes,
					UsedPercent: usedPercent,
					Level:       "warn",
					Message:     FormatCapacityAlert(dbName, sizeBytes, cm.maxSizeBytes, usedPercent, "warn"),
				}
				alerts = append(alerts, alert)
				cm.logger.Warn("settla-maintenance: disk usage warning",
					"database", dbName,
					"size_bytes", sizeBytes,
					"used_percent", fmt.Sprintf("%.1f%%", usedPercent),
					"growth_rate_bytes_per_hour", growthRate,
				)
			} else {
				cm.logger.Info("settla-maintenance: database size OK",
					"database", dbName,
					"size_bytes", sizeBytes,
					"used_percent", fmt.Sprintf("%.1f%%", usedPercent),
					"growth_rate_bytes_per_hour", growthRate,
				)
			}
		}
	}

	return alerts, nil
}

// queryDatabaseSize returns the size of a database in bytes using pg_database_size().
func (cm *CapacityMonitor) queryDatabaseSize(ctx context.Context, dbName string) (int64, error) {
	sql := DatabaseSizeSQL(dbName)
	rows, err := cm.db.Query(ctx, sql)
	if err != nil {
		return 0, fmt.Errorf("settla-maintenance: querying size of %s: %w", dbName, err)
	}
	defer rows.Close()

	var sizeBytes int64
	if rows.Next() {
		if err := rows.Scan(&sizeBytes); err != nil {
			return 0, fmt.Errorf("settla-maintenance: scanning size of %s: %w", dbName, err)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	return sizeBytes, nil
}

// recordMeasurement stores a database size data point and trims history.
func (cm *CapacityMonitor) recordMeasurement(m DatabaseSize) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	history := cm.history[m.Database]
	history = append(history, m)
	if len(history) > cm.maxHistory {
		history = history[len(history)-cm.maxHistory:]
	}
	cm.history[m.Database] = history
}

// calculateGrowthRate estimates bytes/hour based on recent measurements.
func (cm *CapacityMonitor) calculateGrowthRate(dbName string) float64 {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	history := cm.history[dbName]
	if len(history) < 2 {
		return 0
	}

	oldest := history[0]
	newest := history[len(history)-1]

	duration := newest.Timestamp.Sub(oldest.Timestamp)
	if duration <= 0 {
		return 0
	}

	bytesGrown := newest.SizeBytes - oldest.SizeBytes
	hours := duration.Hours()
	if hours == 0 {
		return 0
	}

	return float64(bytesGrown) / hours
}

// --- SQL generation functions (exported for testing) ---

// validSQLIdentifier checks that a string is a safe SQL identifier
// (lowercase alphanumeric + underscores only).
func validSQLIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
			return false
		}
	}
	return true
}

// DatabaseSizeSQL returns a query to get the size of a database.
// Panics if dbName contains invalid characters.
func DatabaseSizeSQL(dbName string) string {
	if !validSQLIdentifier(dbName) {
		panic("settla-maintenance: invalid database name: " + dbName)
	}
	return fmt.Sprintf("SELECT pg_database_size('%s')", dbName)
}

// FormatCapacityAlert formats a human-readable capacity alert message.
func FormatCapacityAlert(dbName string, sizeBytes, maxBytes int64, usedPercent float64, level string) string {
	sizeGB := float64(sizeBytes) / (1024 * 1024 * 1024)
	maxGB := float64(maxBytes) / (1024 * 1024 * 1024)
	return fmt.Sprintf("[%s] Database %s: %.1f GB / %.1f GB (%.1f%% used)",
		level, dbName, sizeGB, maxGB, usedPercent)
}
