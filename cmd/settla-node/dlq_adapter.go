package main

import (
	"context"
	"time"

	settlagrpc "github.com/intellect4all/settla/api/grpc"
	"github.com/intellect4all/settla/node/worker"
)

// dlqInspectorAdapter bridges worker.DLQMonitor to grpc.DLQInspector.
type dlqInspectorAdapter struct {
	monitor *worker.DLQMonitor
}

var _ settlagrpc.DLQInspector = (*dlqInspectorAdapter)(nil)

func newDLQInspectorAdapter(m *worker.DLQMonitor) *dlqInspectorAdapter {
	return &dlqInspectorAdapter{monitor: m}
}

func (a *dlqInspectorAdapter) ListEntries(limit int) []settlagrpc.DLQEntryView {
	entries := a.monitor.ListEntries(limit)
	views := make([]settlagrpc.DLQEntryView, len(entries))
	for i, e := range entries {
		views[i] = settlagrpc.DLQEntryView{
			ID:           e.ID,
			SourceStream: e.SourceStream,
			EventType:    e.EventType,
			Subject:      e.Subject,
			TenantID:     e.TenantID,
			EventID:      e.EventID,
			DataPreview:  e.DataPreview,
			ReceivedAt:   e.ReceivedAt.Format(time.RFC3339),
		}
	}
	return views
}

func (a *dlqInspectorAdapter) GetEntry(id string) *settlagrpc.DLQEntryView {
	e := a.monitor.GetEntry(id)
	if e == nil {
		return nil
	}
	return &settlagrpc.DLQEntryView{
		ID:           e.ID,
		SourceStream: e.SourceStream,
		EventType:    e.EventType,
		Subject:      e.Subject,
		TenantID:     e.TenantID,
		EventID:      e.EventID,
		DataPreview:  e.DataPreview,
		ReceivedAt:   e.ReceivedAt.Format(time.RFC3339),
	}
}

func (a *dlqInspectorAdapter) Stats() settlagrpc.DLQStatsView {
	s := a.monitor.Stats()
	view := settlagrpc.DLQStatsView{
		TotalReceived:  s.TotalReceived,
		BufferedCount:  s.BufferedCount,
		BySourceStream: s.BySourceStream,
		ByEventType:    s.ByEventType,
	}
	if s.OldestEntry != nil {
		view.OldestEntry = s.OldestEntry.Format(time.RFC3339)
	}
	if s.NewestEntry != nil {
		view.NewestEntry = s.NewestEntry.Format(time.RFC3339)
	}
	return view
}

func (a *dlqInspectorAdapter) Replay(ctx context.Context, entryID string) error {
	return a.monitor.Replay(ctx, entryID)
}
