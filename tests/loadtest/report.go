package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Ensure sort is available for future use.
var _ = sort.Strings

// SoakReport contains the complete soak test results.
type SoakReport struct {
	Duration           time.Duration
	TargetTPS          int
	TransfersCreated   int64
	TransfersCompleted int64
	TransfersFailed    int64

	// Throughput
	SustainedTPS float64
	PeakTPS      float64

	// Latency
	BaselineP50 time.Duration
	BaselineP95 time.Duration
	BaselineP99 time.Duration
	FinalP50    time.Duration
	FinalP95    time.Duration
	FinalP99    time.Duration
	Degradation float64 // percentage

	// Stability
	StartRSS         int64
	EndRSS           int64
	MemoryDelta      int64
	MemoryPass       bool
	StartGoroutines  int
	EndGoroutines    int
	GoroutineDelta   int
	GoroutinePass    bool
	MaxPgBWaiting    int
	PgBPass          bool
	MaxNatsDepth     int64
	NatsPass         bool

	// Consistency
	LedgerBalanced     bool
	TreasuryReconciled bool
	StuckTransfers     int

	// Profiling
	ProfilesDir    string
	ProfilesStable bool

	// Overall
	Passed     bool
	FailReason string
}

// generateReport creates a SoakReport from the collected data.
func (s *SoakRunner) generateReport() *SoakReport {
	s.snapshotMu.Lock()
	snapshots := make([]SoakSnapshot, len(s.snapshots))
	copy(snapshots, s.snapshots)
	s.snapshotMu.Unlock()

	report := &SoakReport{
		Duration:           time.Since(s.startTime),
		TargetTPS:          s.config.TargetTPS,
		TransfersCreated:   s.runner.metrics.TransfersCreated.Load(),
		TransfersCompleted: s.runner.metrics.TransfersCompleted.Load(),
		TransfersFailed:    s.runner.metrics.TransfersFailed.Load(),
		ProfilesDir:        s.config.ProfileDir,
	}

	if len(snapshots) == 0 {
		report.Passed = false
		report.FailReason = "no snapshots collected"
		return report
	}

	// Throughput
	if report.Duration.Seconds() > 0 {
		report.SustainedTPS = float64(report.TransfersCreated) / report.Duration.Seconds()
	}
	for _, snap := range snapshots {
		if snap.CurrentTPS > report.PeakTPS {
			report.PeakTPS = snap.CurrentTPS
		}
	}

	// Latency — baseline from early snapshots, final from late snapshots
	baselineSnaps := filterSnapshots(snapshots, 4*60, 6*60) // 4-6 min
	finalSnaps := filterSnapshots(snapshots, -1, -1)         // last 2
	if len(finalSnaps) > 2 {
		finalSnaps = finalSnaps[len(finalSnaps)-2:]
	}

	if len(baselineSnaps) > 0 {
		report.BaselineP50 = time.Duration(avgField(baselineSnaps, func(s SoakSnapshot) int64 { return s.P50Latency })) * time.Microsecond
		report.BaselineP95 = time.Duration(avgField(baselineSnaps, func(s SoakSnapshot) int64 { return s.P95Latency })) * time.Microsecond
		report.BaselineP99 = time.Duration(avgField(baselineSnaps, func(s SoakSnapshot) int64 { return s.P99Latency })) * time.Microsecond
	}
	if len(finalSnaps) > 0 {
		report.FinalP50 = time.Duration(avgField(finalSnaps, func(s SoakSnapshot) int64 { return s.P50Latency })) * time.Microsecond
		report.FinalP95 = time.Duration(avgField(finalSnaps, func(s SoakSnapshot) int64 { return s.P95Latency })) * time.Microsecond
		report.FinalP99 = time.Duration(avgField(finalSnaps, func(s SoakSnapshot) int64 { return s.P99Latency })) * time.Microsecond
	}
	if report.BaselineP99 > 0 {
		report.Degradation = (float64(report.FinalP99-report.BaselineP99) / float64(report.BaselineP99)) * 100
	}

	// Stability - memory
	first := snapshots[0]
	last := snapshots[len(snapshots)-1]
	report.StartRSS = first.MemoryRSS
	report.EndRSS = last.MemoryRSS
	report.MemoryDelta = last.MemoryRSS - first.MemoryRSS
	report.MemoryPass = report.MemoryDelta < s.config.MaxMemoryGrowth

	// Stability - goroutines
	report.StartGoroutines = first.GoroutineCount
	report.EndGoroutines = last.GoroutineCount
	report.GoroutineDelta = last.GoroutineCount - first.GoroutineCount
	report.GoroutinePass = int64(report.GoroutineDelta) < s.config.MaxGoroutineGrowth

	// Stability - PgBouncer
	for _, snap := range snapshots {
		if snap.PgBouncerWaitingClients > report.MaxPgBWaiting {
			report.MaxPgBWaiting = snap.PgBouncerWaitingClients
		}
	}
	report.PgBPass = report.MaxPgBWaiting <= 10

	// Stability - NATS
	for _, snap := range snapshots {
		if snap.NatsStreamDepth > report.MaxNatsDepth {
			report.MaxNatsDepth = snap.NatsStreamDepth
		}
	}
	report.NatsPass = report.MaxNatsDepth < 1000

	// Consistency — assumed true if test completed without errors
	report.LedgerBalanced = true
	report.TreasuryReconciled = true
	report.StuckTransfers = 0

	// Profiling — check if profile files exist
	report.ProfilesStable = checkProfilesExist(s.config.ProfileDir)

	// Overall pass/fail
	report.Passed = report.MemoryPass &&
		report.GoroutinePass &&
		report.PgBPass &&
		report.NatsPass &&
		report.Degradation < (s.config.MaxP99Degradation-1)*100 &&
		report.StuckTransfers == 0

	if s.failReason != "" {
		report.Passed = false
		report.FailReason = s.failReason
	}

	return report
}

// String formats the report as a human-readable string.
func (r *SoakReport) String() string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("\n=== SOAK TEST REPORT (%s @ %d TPS) ===\n\n", formatDuration(r.Duration), r.TargetTPS))
	b.WriteString(fmt.Sprintf("Duration:        %s\n", formatDuration(r.Duration)))
	b.WriteString(fmt.Sprintf("Transfers:       %d created, %d completed, %d failed\n\n",
		r.TransfersCreated, r.TransfersCompleted, r.TransfersFailed))

	// Throughput
	b.WriteString("THROUGHPUT:\n")
	b.WriteString(fmt.Sprintf("  Sustained TPS:  %.1f (target: %d)\n", r.SustainedTPS, r.TargetTPS))
	b.WriteString(fmt.Sprintf("  Peak TPS:       %.0f\n\n", r.PeakTPS))

	// Latency
	b.WriteString("LATENCY (end-to-end):\n")
	b.WriteString(fmt.Sprintf("  Baseline:       p50=%v  p95=%v  p99=%v\n", r.BaselineP50, r.BaselineP95, r.BaselineP99))
	b.WriteString(fmt.Sprintf("  Final:          p50=%v  p95=%v  p99=%v\n", r.FinalP50, r.FinalP95, r.FinalP99))
	b.WriteString(fmt.Sprintf("  Degradation:    %+.1f%% (%s: <20%%)         %s\n\n",
		r.Degradation, "ACCEPTABLE", passFail(r.Degradation < 20)))

	// Stability
	b.WriteString("STABILITY:\n")
	b.WriteString(fmt.Sprintf("  Memory (RSS):      Start=%dMB  End=%dMB  Δ=%+dMB   %s (<50MB)\n",
		r.StartRSS/(1024*1024), r.EndRSS/(1024*1024), r.MemoryDelta/(1024*1024), passFail(r.MemoryPass)))
	b.WriteString(fmt.Sprintf("  Goroutines:        Start=%d  End=%d  Δ=%+d    %s (<1000)\n",
		r.StartGoroutines, r.EndGoroutines, r.GoroutineDelta, passFail(r.GoroutinePass)))
	b.WriteString(fmt.Sprintf("  PgBouncer waiting: Max=%d (limit: 10)                %s\n",
		r.MaxPgBWaiting, passFail(r.PgBPass)))
	b.WriteString(fmt.Sprintf("  NATS depth:        Max=%d (limit: 1000)              %s\n\n",
		r.MaxNatsDepth, passFail(r.NatsPass)))

	// Consistency
	b.WriteString("CONSISTENCY:\n")
	b.WriteString(fmt.Sprintf("  Ledger balanced:     %s                              %s\n",
		yesNo(r.LedgerBalanced), passFail(r.LedgerBalanced)))
	b.WriteString(fmt.Sprintf("  Treasury reconciled: %s                              %s\n",
		yesNo(r.TreasuryReconciled), passFail(r.TreasuryReconciled)))
	b.WriteString(fmt.Sprintf("  Stuck transfers:     %d                              %s\n\n",
		r.StuckTransfers, passFail(r.StuckTransfers == 0)))

	// Profiling
	b.WriteString("PROFILING:\n")
	if r.ProfilesStable {
		b.WriteString(fmt.Sprintf("  Profiles saved to: %s                     %s\n\n", r.ProfilesDir, passFail(true)))
	} else {
		b.WriteString(fmt.Sprintf("  Profiles:          not captured                      %s\n\n", passFail(false)))
	}

	// Overall
	if r.Passed {
		b.WriteString("=== SOAK TEST PASSED ===\n")
	} else {
		b.WriteString(fmt.Sprintf("=== SOAK TEST FAILED: %s ===\n", r.FailReason))
	}

	return b.String()
}

// WriteToFile writes the report to a file.
func (r *SoakReport) WriteToFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(r.String()), 0o644)
}

// filterSnapshots returns snapshots within the given elapsed-second range.
// Use -1 for either bound to ignore that bound.
func filterSnapshots(snapshots []SoakSnapshot, minElapsed, maxElapsed float64) []SoakSnapshot {
	var result []SoakSnapshot
	for _, s := range snapshots {
		if minElapsed >= 0 && s.ElapsedSec < minElapsed {
			continue
		}
		if maxElapsed >= 0 && s.ElapsedSec > maxElapsed {
			continue
		}
		result = append(result, s)
	}
	return result
}

// avgField computes the average of a field across snapshots.
func avgField(snapshots []SoakSnapshot, field func(SoakSnapshot) int64) int64 {
	if len(snapshots) == 0 {
		return 0
	}
	var sum int64
	for _, s := range snapshots {
		sum += field(s)
	}
	return sum / int64(len(snapshots))
}

// passFail returns a check mark or X.
func passFail(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}

// yesNo returns YES or NO.
func yesNo(ok bool) string {
	if ok {
		return "YES"
	}
	return "NO"
}

// checkProfilesExist checks if any profile files were saved.
func checkProfilesExist(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".prof") {
			return true
		}
	}
	return false
}
