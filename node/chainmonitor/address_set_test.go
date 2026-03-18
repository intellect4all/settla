package chainmonitor

import (
	"sync"
	"testing"
)

func TestAddressSet_AddAndLookup(t *testing.T) {
	s := NewAddressSet()

	info := AddressInfo{Chain: "tron", Address: "TAddr1", TenantID: "tenant-1"}
	s.Add(info)
	s.Publish()

	got, ok := s.Lookup("tron", "TAddr1")
	if !ok {
		t.Fatal("expected to find address")
	}
	if got.TenantID != "tenant-1" {
		t.Errorf("got tenant %q, want tenant-1", got.TenantID)
	}
}

func TestAddressSet_Contains(t *testing.T) {
	s := NewAddressSet()
	s.Add(AddressInfo{Chain: "tron", Address: "TAddr1"})
	s.Publish()

	if !s.Contains("tron", "TAddr1") {
		t.Error("expected Contains to return true")
	}
	if s.Contains("tron", "TAddr2") {
		t.Error("expected Contains to return false for missing address")
	}
	if s.Contains("ethereum", "TAddr1") {
		t.Error("expected Contains to return false for wrong chain")
	}
}

func TestAddressSet_Replace(t *testing.T) {
	s := NewAddressSet()
	s.Add(AddressInfo{Chain: "tron", Address: "old-addr"})
	s.Publish()

	// Replace with new addresses
	s.Replace([]AddressInfo{
		{Chain: "tron", Address: "new-addr-1"},
		{Chain: "tron", Address: "new-addr-2"},
	})

	if s.Contains("tron", "old-addr") {
		t.Error("old address should be removed after Replace")
	}
	if !s.Contains("tron", "new-addr-1") {
		t.Error("new-addr-1 should be present after Replace")
	}
	if !s.Contains("tron", "new-addr-2") {
		t.Error("new-addr-2 should be present after Replace")
	}
	if s.Len() != 2 {
		t.Errorf("expected 2 addresses, got %d", s.Len())
	}
}

func TestAddressSet_SnapshotIsolation(t *testing.T) {
	s := NewAddressSet()
	s.Add(AddressInfo{Chain: "tron", Address: "addr1"})
	s.Publish()

	// Capture snapshot
	snap := s.Snapshot()

	// Add more addresses without publishing
	s.Add(AddressInfo{Chain: "tron", Address: "addr2"})

	// Snapshot should not contain the new address
	if _, ok := snap[addressKey("tron", "addr2")]; ok {
		t.Error("snapshot should be isolated from unpublished additions")
	}

	// After publish, new snapshot should contain it
	s.Publish()
	snap2 := s.Snapshot()
	if _, ok := snap2[addressKey("tron", "addr2")]; !ok {
		t.Error("new snapshot should contain published address")
	}
}

func TestAddressSet_ConcurrentAccess(t *testing.T) {
	s := NewAddressSet()
	var wg sync.WaitGroup

	// Concurrent writers
	for i := range 100 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s.Add(AddressInfo{Chain: "tron", Address: string(rune('A' + i%26))})
			s.Publish()
		}(i)
	}

	// Concurrent readers
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = s.Snapshot()
			_ = s.Contains("tron", "A")
		}()
	}

	wg.Wait()
}
