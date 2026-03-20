package cache

import (
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/intellect4all/settla/domain"
)

const (
	// DefaultQuoteTTL is the default Redis TTL for quotes.
	// Quotes have their own ExpiresAt; this is a safety cap.
	DefaultQuoteTTL = 15 * time.Minute
)

// QuoteCache caches quotes in Redis, keyed by tenant and quote ID.
// Key format: settla:quote:{tenant_id}:{quote_id}
//
// Uses gob encoding instead of JSON to preserve exact decimal.Decimal precision.
// JSON marshals decimals as bare numbers which can lose precision beyond ~15 digits
// if any intermediary (proxy, logger) parses them as float64.
type QuoteCache struct {
	redis *RedisCache
}

// NewQuoteCache creates a new quote cache backed by Redis.
func NewQuoteCache(redis *RedisCache) *QuoteCache {
	return &QuoteCache{redis: redis}
}

// quoteKey returns the Redis key for a quote.
func quoteKey(tenantID, quoteID uuid.UUID) string {
	return fmt.Sprintf("settla:quote:%s:%s", tenantID.String(), quoteID.String())
}

// Get retrieves a quote from the cache. Returns (nil, nil) on cache miss.
func (qc *QuoteCache) Get(ctx context.Context, tenantID, quoteID uuid.UUID) (*domain.Quote, error) {
	key := quoteKey(tenantID, quoteID)
	data, err := qc.redis.Get(ctx, key)
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("settla-cache: get quote %s: %w", quoteID, err)
	}
	var quote domain.Quote
	if err := gob.NewDecoder(bytes.NewReader([]byte(data))).Decode(&quote); err != nil {
		return nil, fmt.Errorf("settla-cache: decode quote %s: %w", quoteID, err)
	}
	return &quote, nil
}

// Set stores a quote in the cache. TTL is computed from the quote's ExpiresAt,
// capped at DefaultQuoteTTL.
func (qc *QuoteCache) Set(ctx context.Context, quote *domain.Quote) error {
	key := quoteKey(quote.TenantID, quote.ID)

	ttl := time.Until(quote.ExpiresAt)
	if ttl <= 0 {
		// Quote already expired — don't cache.
		return nil
	}
	if ttl > DefaultQuoteTTL {
		ttl = DefaultQuoteTTL
	}

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(quote); err != nil {
		return fmt.Errorf("settla-cache: encode quote %s: %w", quote.ID, err)
	}
	return qc.redis.Set(ctx, key, buf.String(), ttl)
}

// Delete removes a quote from the cache.
func (qc *QuoteCache) Delete(ctx context.Context, tenantID, quoteID uuid.UUID) error {
	key := quoteKey(tenantID, quoteID)
	return qc.redis.Delete(ctx, key)
}
