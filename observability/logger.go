package observability

import (
	"log/slog"
	"os"
	"strings"
)

// NewLogger creates a structured logger configured by environment.
// In production (SETTLA_ENV=production), uses JSON output.
// In development, uses text output for readability.
// Standard fields (service, version) are added to every log entry.
func NewLogger(service, version string) *slog.Logger {
	env := os.Getenv("SETTLA_ENV")
	levelStr := os.Getenv("SETTLA_LOG_LEVEL")

	level := parseLevel(levelStr)

	opts := &slog.HandlerOptions{Level: level}

	var handler slog.Handler
	if strings.EqualFold(env, "production") {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	return slog.New(handler).With(
		"service", service,
		"version", version,
	)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
