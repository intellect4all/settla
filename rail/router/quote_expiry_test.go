package router

import (
	"testing"
	"time"
)

func TestQuoteExpiry(t *testing.T) {
	tests := []struct {
		name             string
		estimatedSeconds int
		want             time.Duration
	}{
		{"zero falls back to 5m", 0, 5 * time.Minute},
		{"negative falls back to 5m", -10, 5 * time.Minute},
		{"30s clamped to 2m min", 30, 2 * time.Minute},
		{"60s gives exactly 2m", 60, 2 * time.Minute},
		{"61s gives 2m2s", 61, 2*time.Minute + 2*time.Second},
		{"300s gives 10m", 300, 10 * time.Minute},
		{"900s clamped to 30m max", 900, 30 * time.Minute},
		{"1800s clamped to 30m max", 1800, 30 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := quoteExpiry(tt.estimatedSeconds)
			if got != tt.want {
				t.Errorf("quoteExpiry(%d) = %v, want %v", tt.estimatedSeconds, got, tt.want)
			}
		})
	}
}
