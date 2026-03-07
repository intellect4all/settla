package cache

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestLocalCache_SetGet(t *testing.T) {
	c := NewLocalCache(100)
	c.Set("key1", "value1", 5*time.Second)

	val, ok := c.Get("key1")
	if !ok {
		t.Fatal("expected cache hit")
	}
	if val != "value1" {
		t.Fatalf("expected value1, got %v", val)
	}
}

func TestLocalCache_Miss(t *testing.T) {
	c := NewLocalCache(100)
	_, ok := c.Get("nonexistent")
	if ok {
		t.Fatal("expected cache miss")
	}
}

func TestLocalCache_Expiry(t *testing.T) {
	c := NewLocalCache(100)

	now := time.Now()
	c.nowFunc = func() time.Time { return now }
	c.Set("key1", "value1", 1*time.Second)

	// Advance time past TTL.
	c.nowFunc = func() time.Time { return now.Add(2 * time.Second) }

	_, ok := c.Get("key1")
	if ok {
		t.Fatal("expected cache miss after expiry")
	}
}

func TestLocalCache_Overwrite(t *testing.T) {
	c := NewLocalCache(100)
	c.Set("key1", "v1", 5*time.Second)
	c.Set("key1", "v2", 5*time.Second)

	val, ok := c.Get("key1")
	if !ok || val != "v2" {
		t.Fatalf("expected v2, got %v (ok=%v)", val, ok)
	}
}

func TestLocalCache_Delete(t *testing.T) {
	c := NewLocalCache(100)
	c.Set("key1", "value1", 5*time.Second)
	c.Delete("key1")

	_, ok := c.Get("key1")
	if ok {
		t.Fatal("expected miss after delete")
	}
}

func TestLocalCache_MaxSize(t *testing.T) {
	c := NewLocalCache(3)
	c.Set("a", 1, 5*time.Second)
	c.Set("b", 2, 5*time.Second)
	c.Set("c", 3, 5*time.Second)
	// This should evict one entry.
	c.Set("d", 4, 5*time.Second)

	if c.Len() > 3 {
		t.Fatalf("expected max 3 entries, got %d", c.Len())
	}
	// The new entry must be present.
	val, ok := c.Get("d")
	if !ok || val != 4 {
		t.Fatal("expected new entry to be present")
	}
}

func TestLocalCache_EvictsExpiredFirst(t *testing.T) {
	c := NewLocalCache(2)

	now := time.Now()
	c.nowFunc = func() time.Time { return now }
	c.Set("old", "stale", 1*time.Second)
	c.Set("fresh", "good", 10*time.Second)

	// Advance past old's TTL.
	c.nowFunc = func() time.Time { return now.Add(2 * time.Second) }

	// Adding a third should evict the expired "old", not the fresh one.
	c.Set("new", "entry", 10*time.Second)

	if _, ok := c.Get("fresh"); !ok {
		t.Fatal("fresh entry should still be present")
	}
	if _, ok := c.Get("new"); !ok {
		t.Fatal("new entry should be present")
	}
}

func TestLocalCache_ConcurrentAccess(t *testing.T) {
	c := NewLocalCache(1000)
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("key-%d", i)
			c.Set(key, i, 5*time.Second)
			c.Get(key)
		}(i)
	}
	wg.Wait()
}

func BenchmarkLocalCache_Get(b *testing.B) {
	c := NewLocalCache(10000)
	c.Set("bench-key", "bench-value", 1*time.Minute)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Get("bench-key")
		}
	})
}

func BenchmarkLocalCache_Set(b *testing.B) {
	c := NewLocalCache(10000)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			c.Set(fmt.Sprintf("key-%d", i), i, 1*time.Minute)
			i++
		}
	})
}
