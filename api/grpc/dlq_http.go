package grpc

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

// DLQInspector is the interface the DLQ monitor exposes for ops inspection.
// Implemented by an adapter in cmd/settla-node that wraps the DLQMonitor.
type DLQInspector interface {
	ListEntries(limit int) []DLQEntryView
	GetEntry(id string) *DLQEntryView
	Stats() DLQStatsView
	Replay(ctx context.Context, entryID string) error
}

// DLQEntryView is the JSON view of a DLQ entry returned by the ops API.
type DLQEntryView struct {
	ID           string `json:"id"`
	SourceStream string `json:"source_stream"`
	EventType    string `json:"event_type"`
	Subject      string `json:"subject"`
	TenantID     string `json:"tenant_id,omitempty"`
	EventID      string `json:"event_id,omitempty"`
	DataPreview  string `json:"data_preview"`
	ReceivedAt   string `json:"received_at"`
}

// DLQStatsView is the JSON view of DLQ statistics.
type DLQStatsView struct {
	TotalReceived  int64            `json:"total_received"`
	BufferedCount  int              `json:"buffered_count"`
	BySourceStream map[string]int64 `json:"by_source_stream"`
	ByEventType    map[string]int64 `json:"by_event_type"`
	OldestEntry    string           `json:"oldest_entry,omitempty"`
	NewestEntry    string           `json:"newest_entry,omitempty"`
}

// RegisterDLQHandlers registers DLQ inspection and replay HTTP handlers.
//
//	GET  /internal/ops/dlq/stats              — aggregate DLQ statistics
//	GET  /internal/ops/dlq/messages?limit=50  — list recent DLQ entries
//	POST /internal/ops/dlq/messages/{id}/replay — replay a DLQ entry
func RegisterDLQHandlers(mux *http.ServeMux, inspector DLQInspector, logger *slog.Logger) {
	if inspector == nil {
		return
	}

	mux.HandleFunc("/internal/ops/dlq/", func(w http.ResponseWriter, r *http.Request) {
		sub := strings.TrimPrefix(r.URL.Path, "/internal/ops/dlq/")
		sub = strings.TrimSuffix(sub, "/")

		switch {
		case r.Method == http.MethodGet && sub == "stats":
			handleDLQStats(w, inspector)

		case r.Method == http.MethodGet && sub == "messages":
			handleDLQMessages(w, r, inspector)

		case r.Method == http.MethodPost && strings.HasPrefix(sub, "messages/") && strings.HasSuffix(sub, "/replay"):
			handleDLQReplay(w, r, inspector, logger)

		default:
			writeDLQJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		}
	})
}

func handleDLQStats(w http.ResponseWriter, inspector DLQInspector) {
	stats := inspector.Stats()
	writeDLQJSON(w, http.StatusOK, stats)
}

func handleDLQMessages(w http.ResponseWriter, r *http.Request, inspector DLQInspector) {
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}

	entries := inspector.ListEntries(limit)
	if entries == nil {
		entries = []DLQEntryView{}
	}
	writeDLQJSON(w, http.StatusOK, map[string]any{
		"messages": entries,
		"count":    len(entries),
	})
}

func handleDLQReplay(w http.ResponseWriter, r *http.Request, inspector DLQInspector, logger *slog.Logger) {
	sub := strings.TrimPrefix(r.URL.Path, "/internal/ops/dlq/messages/")
	parts := strings.Split(sub, "/")
	if len(parts) < 2 {
		writeDLQJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid path"})
		return
	}
	entryID := parts[0]

	if err := inspector.Replay(r.Context(), entryID); err != nil {
		logger.Error("ops: DLQ replay failed", "entry_id", entryID, "error", err)
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		writeDLQJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	writeDLQJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// writeDLQJSON writes v as JSON with the given HTTP status code.
// Separate from writeJSON in ops_http.go to allow this file to be used
// independently (e.g., registered on settla-node's HTTP mux).
func writeDLQJSON(w http.ResponseWriter, status int, v any) {
	writeJSON(w, status, v)
}
