package cache

import (
	"sync"
	"time"
)

const (
	// DefaultLocalCacheSize is the maximum number of entries the local cache holds.
	DefaultLocalCacheSize = 10_000
)

// localEntry stores a cached value with its expiration timestamp.
type localEntry struct {
	value     any
	expiresAt time.Time
}

// LocalCache is a thread-safe in-process LRU cache with TTL-based expiry.
// It is designed for ~100ns lookups on the hot auth path.
type LocalCache struct {
	mu       sync.RWMutex
	entries  map[string]localEntry
	maxSize  int
	nowFunc  func() time.Time // for testing
}

// NewLocalCache creates a new local cache with the given maximum size.
func NewLocalCache(maxSize int) *LocalCache {
	if maxSize <= 0 {
		maxSize = DefaultLocalCacheSize
	}
	return &LocalCache{
		entries: make(map[string]localEntry, maxSize),
		maxSize: maxSize,
		nowFunc: time.Now,
	}
}

// Get retrieves a value from the local cache. Returns (value, true) on hit,
// (nil, false) on miss or expiry.
func (c *LocalCache) Get(key string) (any, bool) {
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()

	if !ok {
		return nil, false
	}
	if c.nowFunc().After(entry.expiresAt) {
		// Expired — lazy delete.
		c.mu.Lock()
		// Double-check under write lock in case it was refreshed.
		if e, still := c.entries[key]; still && c.nowFunc().After(e.expiresAt) {
			delete(c.entries, key)
		}
		c.mu.Unlock()
		return nil, false
	}
	return entry.value, true
}

// Set stores a value in the local cache with a TTL.
func (c *LocalCache) Set(key string, value any, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Evict if at capacity and this is a new key.
	if _, exists := c.entries[key]; !exists && len(c.entries) >= c.maxSize {
		c.evictExpired()
		// If still at capacity after expiry sweep, evict oldest.
		if len(c.entries) >= c.maxSize {
			c.evictOne()
		}
	}

	c.entries[key] = localEntry{
		value:     value,
		expiresAt: c.nowFunc().Add(ttl),
	}
}

// Delete removes a key from the local cache.
func (c *LocalCache) Delete(key string) {
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
}

// Len returns the current number of entries (including expired but not yet evicted).
func (c *LocalCache) Len() int {
	c.mu.RLock()
	n := len(c.entries)
	c.mu.RUnlock()
	return n
}

// evictExpired removes all expired entries. Caller must hold write lock.
func (c *LocalCache) evictExpired() {
	now := c.nowFunc()
	for k, e := range c.entries {
		if now.After(e.expiresAt) {
			delete(c.entries, k)
		}
	}
}

// evictOne removes one arbitrary entry. Caller must hold write lock.
func (c *LocalCache) evictOne() {
	for k := range c.entries {
		delete(c.entries, k)
		return
	}
}
