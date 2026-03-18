package rpc

import (
	"context"
	"fmt"
	"testing"
)

func TestFailoverManager_FirstProviderSucceeds(t *testing.T) {
	providers := []*Provider{
		{Name: "primary", RPCURL: "http://primary"},
		{Name: "backup", RPCURL: "http://backup"},
	}
	fm := NewFailoverManager(providers, nil)

	calls := 0
	err := fm.Execute(context.Background(), func(_ context.Context, rpcURL, _ string) error {
		calls++
		if rpcURL != "http://primary" {
			t.Errorf("expected primary URL, got %s", rpcURL)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestFailoverManager_FailoverToSecond(t *testing.T) {
	providers := []*Provider{
		{Name: "primary", RPCURL: "http://primary"},
		{Name: "backup", RPCURL: "http://backup"},
	}
	fm := NewFailoverManager(providers, nil)

	var urls []string
	err := fm.Execute(context.Background(), func(_ context.Context, rpcURL, _ string) error {
		urls = append(urls, rpcURL)
		if rpcURL == "http://primary" {
			return fmt.Errorf("primary down")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(urls) != 2 {
		t.Errorf("expected 2 calls, got %d", len(urls))
	}
}

func TestFailoverManager_AllFail(t *testing.T) {
	providers := []*Provider{
		{Name: "primary", RPCURL: "http://primary"},
		{Name: "backup", RPCURL: "http://backup"},
	}
	fm := NewFailoverManager(providers, nil)

	err := fm.Execute(context.Background(), func(_ context.Context, _, _ string) error {
		return fmt.Errorf("provider down")
	})
	if err == nil {
		t.Fatal("expected error when all providers fail")
	}
}

func TestProvider_CircuitBreaker(t *testing.T) {
	p := &Provider{Name: "test"}

	// Initially available
	if !p.IsAvailable() {
		t.Error("provider should be available initially")
	}

	// Record failures up to threshold
	for range failureThreshold {
		p.RecordFailure()
	}

	// Circuit should be open now
	if p.IsAvailable() {
		t.Error("provider should be unavailable after reaching failure threshold")
	}

	// Success resets
	p.RecordSuccess()
	if !p.IsAvailable() {
		t.Error("provider should be available after success")
	}
}

func TestFailoverManager_AvailableCount(t *testing.T) {
	providers := []*Provider{
		{Name: "a"},
		{Name: "b"},
		{Name: "c"},
	}
	fm := NewFailoverManager(providers, nil)

	if fm.AvailableCount() != 3 {
		t.Errorf("expected 3 available, got %d", fm.AvailableCount())
	}

	// Open circuit on one provider
	for range failureThreshold {
		providers[0].RecordFailure()
	}

	if fm.AvailableCount() != 2 {
		t.Errorf("expected 2 available, got %d", fm.AvailableCount())
	}
}
