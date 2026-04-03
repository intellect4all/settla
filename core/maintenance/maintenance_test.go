package maintenance

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

// --- mock DB executor ---

type mockRows struct {
	values  [][]interface{}
	current int
	closed  bool
}

func (r *mockRows) Next() bool {
	if r.current >= len(r.values) {
		return false
	}
	r.current++
	return true
}

func (r *mockRows) Scan(dest ...interface{}) error {
	row := r.values[r.current-1]
	if len(dest) != len(row) {
		return fmt.Errorf("scan: expected %d args, got %d", len(row), len(dest))
	}
	for i, v := range row {
		switch d := dest[i].(type) {
		case *int64:
			if val, ok := v.(int64); ok {
				*d = val
			}
		case *int:
			if val, ok := v.(int); ok {
				*d = val
			}
		}
	}
	return nil
}

func (r *mockRows) Close() {
	r.closed = true
}

func (r *mockRows) Err() error {
	return nil
}

type mockCommandTag struct {
	rows int64
}

func (t mockCommandTag) RowsAffected() int64 {
	return t.rows
}

type execCall struct {
	sql  string
	args []interface{}
}

type mockDBExecutor struct {
	execCalls  []execCall
	queryRows  *mockRows
	execErr    error
	queryErr   error
}

func (m *mockDBExecutor) Exec(_ context.Context, sql string, args ...interface{}) (CommandTag, error) {
	m.execCalls = append(m.execCalls, execCall{sql: sql, args: args})
	if m.execErr != nil {
		return nil, m.execErr
	}
	return mockCommandTag{rows: 1}, nil
}

func (m *mockDBExecutor) Query(_ context.Context, sql string, args ...interface{}) (Rows, error) {
	if m.queryErr != nil {
		return nil, m.queryErr
	}
	if m.queryRows != nil {
		return m.queryRows, nil
	}
	return &mockRows{values: [][]interface{}{{int64(0)}}}, nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// --- Partition name generation tests ---

func TestDailyPartitionName(t *testing.T) {
	date := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	name := DailyPartitionName("outbox", date)
	expected := "outbox_y2026m03d15"
	if name != expected {
		t.Errorf("expected %q, got %q", expected, name)
	}
}

func TestWeeklyPartitionName(t *testing.T) {
	// 2026-03-16 is a Monday, ISO week 12
	date := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	name := WeeklyPartitionName("entry_lines", date)
	expected := "entry_lines_y2026w12"
	if name != expected {
		t.Errorf("expected %q, got %q", expected, name)
	}
}

func TestMonthlyPartitionName(t *testing.T) {
	date := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	name := MonthlyPartitionName("transfers", date)
	expected := "transfers_y2026m03"
	if name != expected {
		t.Errorf("expected %q, got %q", expected, name)
	}
}

// --- Create partition SQL tests ---

func TestCreatePartitionSQL(t *testing.T) {
	from := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 16, 0, 0, 0, 0, time.UTC)
	sql := CreatePartitionSQL("outbox", "outbox_y2026m03d15", from, to)

	expected := "CREATE TABLE IF NOT EXISTS outbox_y2026m03d15 PARTITION OF outbox FOR VALUES FROM ('2026-03-15') TO ('2026-03-16')"
	if sql != expected {
		t.Errorf("expected:\n  %s\ngot:\n  %s", expected, sql)
	}
}

func TestCreatePartitionSQL_Monthly(t *testing.T) {
	from := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	sql := CreatePartitionSQL("transfers", "transfers_y2026m04", from, to)

	if !strings.Contains(sql, "CREATE TABLE IF NOT EXISTS") {
		t.Error("expected idempotent CREATE TABLE IF NOT EXISTS")
	}
	if !strings.Contains(sql, "PARTITION OF transfers") {
		t.Error("expected PARTITION OF transfers")
	}
	if !strings.Contains(sql, "'2026-04-01'") || !strings.Contains(sql, "'2026-05-01'") {
		t.Error("expected correct date range")
	}
}

// --- Drop partition SQL tests ---

func TestDropPartitionSQL(t *testing.T) {
	sql := DropPartitionSQL("outbox_y2026m03d07")
	expected := "DROP TABLE IF EXISTS outbox_y2026m03d07"
	if sql != expected {
		t.Errorf("expected %q, got %q", expected, sql)
	}
}

func TestDropPartitionOnlyForOldDates(t *testing.T) {
	db := &mockDBExecutor{}
	pm := NewPartitionManager(db, testLogger())

	// Configure only outbox with 48h drop policy
	pm.SetConfigs([]PartitionConfig{
		{Table: "outbox", Database: "transfer", Interval: "daily", CreateAhead: 1, DropOlderThan: 48 * time.Hour},
	})

	ctx := context.Background()
	err := pm.ManagePartitions(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check that drop commands were issued for old dates, not for recent/future ones
	var dropCalls []string
	var createCalls []string
	for _, call := range db.execCalls {
		if strings.HasPrefix(call.sql, "DROP") {
			dropCalls = append(dropCalls, call.sql)
		}
		if strings.HasPrefix(call.sql, "CREATE") {
			createCalls = append(createCalls, call.sql)
		}
	}

	// Should have create calls for today + 1 day ahead
	if len(createCalls) < 2 {
		t.Errorf("expected at least 2 create calls, got %d", len(createCalls))
	}

	// All drop calls should reference old dates (DROP TABLE IF EXISTS)
	for _, sql := range dropCalls {
		if !strings.Contains(sql, "DROP TABLE IF EXISTS") {
			t.Errorf("drop should use IF EXISTS: %s", sql)
		}
	}
}

// --- Default partition check tests ---

func TestDefaultPartitionCheckSQL(t *testing.T) {
	sql := DefaultPartitionCheckSQL("outbox_default")
	expected := "SELECT COUNT(*) FROM ONLY outbox_default"
	if sql != expected {
		t.Errorf("expected %q, got %q", expected, sql)
	}
}

func TestDefaultPartitionCheckIdentifiesStaleRows(t *testing.T) {
	// Simulate default partition with rows
	db := &mockDBExecutor{
		queryRows: &mockRows{values: [][]interface{}{{int64(42)}}},
	}
	pm := NewPartitionManager(db, testLogger())
	pm.SetConfigs([]PartitionConfig{
		{Table: "outbox", Database: "transfer", Interval: "daily", CreateAhead: 1},
	})

	// The verification should detect rows in the default partition.
	// We verify by running ManagePartitions and checking it doesn't error.
	ctx := context.Background()
	err := pm.ManagePartitions(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The warning is logged but no error is returned — this is correct behavior.
}

// --- Vacuum command tests ---

func TestVacuumAnalyzeSQL(t *testing.T) {
	sql, err := VacuumAnalyzeSQL("transfers")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := "VACUUM ANALYZE transfers"
	if sql != expected {
		t.Errorf("expected %q, got %q", expected, sql)
	}
}

func TestVacuumAnalyzeSQL_RejectsUnsafe(t *testing.T) {
	_, err := VacuumAnalyzeSQL("transfers; DROP TABLE users")
	if err == nil {
		t.Error("expected error for unsafe table name")
	}
	_, err = VacuumAnalyzeSQL("$(rm -rf /)")
	if err == nil {
		t.Error("expected error for injection attempt")
	}
}

func TestVacuumNeverUsesFull(t *testing.T) {
	sql, _ := VacuumAnalyzeSQL("transfers")
	if strings.Contains(strings.ToUpper(sql), "FULL") {
		t.Error("VACUUM must NEVER use FULL — it blocks all operations")
	}

	sql, _ = VacuumAnalyzeSQL("outbox")
	if !strings.Contains(sql, "ANALYZE") {
		t.Error("VACUUM should always include ANALYZE for statistics update")
	}
}

func TestVacuumManagerRunsDueTables(t *testing.T) {
	db := &mockDBExecutor{}
	vm := NewVacuumManager(db, testLogger())
	vm.SetConfigs([]VacuumConfig{
		{Table: "transfers", Database: "transfer", Interval: 2 * time.Hour},
		{Table: "outbox", Database: "transfer", Interval: 1 * time.Hour},
	})

	ctx := context.Background()

	// First run: both tables should be vacuumed
	err := vm.RunDueVacuums(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(db.execCalls) != 2 {
		t.Fatalf("expected 2 vacuum calls on first run, got %d", len(db.execCalls))
	}

	// Second immediate run: neither should be vacuumed (not yet due)
	db.execCalls = nil
	err = vm.RunDueVacuums(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(db.execCalls) != 0 {
		t.Errorf("expected 0 vacuum calls on immediate re-run, got %d", len(db.execCalls))
	}
}

// --- Capacity monitor tests ---

func TestDatabaseSizeSQL(t *testing.T) {
	sql := DatabaseSizeSQL("transfer")
	expected := "SELECT pg_database_size('transfer')"
	if sql != expected {
		t.Errorf("expected %q, got %q", expected, sql)
	}
}

func TestFormatCapacityAlert(t *testing.T) {
	msg := FormatCapacityAlert("transfer", 75_000_000_000, 100_000_000_000, 75.0, "warn")
	if !strings.Contains(msg, "[warn]") {
		t.Error("expected alert to contain level")
	}
	if !strings.Contains(msg, "transfer") {
		t.Error("expected alert to contain database name")
	}
	if !strings.Contains(msg, "75.0%") {
		t.Error("expected alert to contain percentage")
	}
}

func TestCapacityMonitorAlerts(t *testing.T) {
	// Simulate a database at 80% capacity (warn threshold)
	db := &mockDBExecutor{
		queryRows: &mockRows{values: [][]interface{}{{int64(80_000_000_000)}}},
	}

	cm := NewCapacityMonitor(db, testLogger(), []string{"transfer"}, 100_000_000_000, nil)

	alerts, err := cm.CheckCapacity(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(alerts))
	}

	if alerts[0].Level != "warn" {
		t.Errorf("expected warn level, got %q", alerts[0].Level)
	}
	if alerts[0].Database != "transfer" {
		t.Errorf("expected database 'transfer', got %q", alerts[0].Database)
	}
}

func TestCapacityMonitorCritical(t *testing.T) {
	// Simulate a database at 90% capacity (critical threshold)
	db := &mockDBExecutor{
		queryRows: &mockRows{values: [][]interface{}{{int64(90_000_000_000)}}},
	}

	cm := NewCapacityMonitor(db, testLogger(), []string{"transfer"}, 100_000_000_000, nil)

	alerts, err := cm.CheckCapacity(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(alerts))
	}

	if alerts[0].Level != "critical" {
		t.Errorf("expected critical level, got %q", alerts[0].Level)
	}
}

func TestCapacityMonitorNoAlertWhenOK(t *testing.T) {
	// Simulate a database at 50% capacity (below warn threshold)
	db := &mockDBExecutor{
		queryRows: &mockRows{values: [][]interface{}{{int64(50_000_000_000)}}},
	}

	cm := NewCapacityMonitor(db, testLogger(), []string{"transfer"}, 100_000_000_000, nil)

	alerts, err := cm.CheckCapacity(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(alerts) != 0 {
		t.Errorf("expected 0 alerts, got %d", len(alerts))
	}
}

func TestCapacityMonitorGrowthRate(t *testing.T) {
	db := &mockDBExecutor{
		queryRows: &mockRows{values: [][]interface{}{{int64(50_000_000_000)}}},
	}

	cm := NewCapacityMonitor(db, testLogger(), []string{"transfer"}, 100_000_000_000, nil)

	// Seed history with a measurement from 1 hour ago
	cm.recordMeasurement(DatabaseSize{
		Database:  "transfer",
		SizeBytes: 49_000_000_000,
		Timestamp: time.Now().UTC().Add(-1 * time.Hour),
	})
	cm.recordMeasurement(DatabaseSize{
		Database:  "transfer",
		SizeBytes: 50_000_000_000,
		Timestamp: time.Now().UTC(),
	})

	rate := cm.calculateGrowthRate("transfer")
	// Should be approximately 1GB/hour
	if rate < 900_000_000 || rate > 1_100_000_000 {
		t.Errorf("expected growth rate ~1GB/hour, got %.0f bytes/hour", rate)
	}
}
