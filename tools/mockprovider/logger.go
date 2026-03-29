package main

import (
	"sync"
	"time"
)

const maxLogEntries = 1000

// RequestLog stores a ring buffer of recent requests for debugging.
type RequestLog struct {
	mu      sync.Mutex
	entries []LogEntry
	pos     int
	full    bool
}

// LogEntry records a single request.
type LogEntry struct {
	Timestamp  time.Time `json:"timestamp"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	ProviderID string    `json:"provider_id,omitempty"`
	StatusCode int       `json:"status_code"`
	LatencyMs  int64     `json:"latency_ms"`
	Error      string    `json:"error,omitempty"`
}

// NewRequestLog creates a new ring buffer request log.
func NewRequestLog() *RequestLog {
	return &RequestLog{
		entries: make([]LogEntry, maxLogEntries),
	}
}

// Add appends an entry to the ring buffer.
func (l *RequestLog) Add(entry LogEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.entries[l.pos] = entry
	l.pos = (l.pos + 1) % maxLogEntries
	if l.pos == 0 {
		l.full = true
	}
}

// Entries returns all logged entries in chronological order.
func (l *RequestLog) Entries() []LogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()

	if !l.full {
		result := make([]LogEntry, l.pos)
		copy(result, l.entries[:l.pos])
		return result
	}

	// Ring buffer is full: entries from pos..end are oldest, 0..pos-1 are newest.
	result := make([]LogEntry, maxLogEntries)
	copy(result, l.entries[l.pos:])
	copy(result[maxLogEntries-l.pos:], l.entries[:l.pos])
	return result
}

// Stats returns aggregate statistics.
type Stats struct {
	TotalRequests int     `json:"total_requests"`
	ErrorCount    int     `json:"error_count"`
	AvgLatencyMs  float64 `json:"avg_latency_ms"`
}

// GetStats computes aggregate statistics from logged entries.
func (l *RequestLog) GetStats() Stats {
	entries := l.Entries()
	if len(entries) == 0 {
		return Stats{}
	}

	var totalLatency int64
	var errors int
	for _, e := range entries {
		totalLatency += e.LatencyMs
		if e.StatusCode >= 400 {
			errors++
		}
	}

	return Stats{
		TotalRequests: len(entries),
		ErrorCount:    errors,
		AvgLatencyMs:  float64(totalLatency) / float64(len(entries)),
	}
}
