package cache

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/intellect4all/settla/domain"
)

const tenantIndexKey = "settla:active_tenants"

// TenantIndex maintains a Redis SET of active tenant IDs for efficient iteration.
// Workers use ForEach (SSCAN-based) instead of querying Postgres for all tenant IDs.
// The index is synced on tenant lifecycle events (SADD/SREM) and reconciled
// periodically from Postgres as a safety net.
type TenantIndex struct {
	client   *redis.Client
	fallback domain.TenantPageFetcher
	logger   *slog.Logger
}

// NewTenantIndex creates a TenantIndex backed by the given Redis client.
// fallback is used when Redis is unavailable — it paginates through Postgres instead.
func NewTenantIndex(client *redis.Client, fallback domain.TenantPageFetcher, logger *slog.Logger) *TenantIndex {
	return &TenantIndex{
		client:   client,
		fallback: fallback,
		logger:   logger,
	}
}

// Add registers a tenant as active (SADD). Called when a tenant status transitions to ACTIVE.
func (t *TenantIndex) Add(ctx context.Context, tenantID uuid.UUID) error {
	if err := t.client.SAdd(ctx, tenantIndexKey, tenantID.String()).Err(); err != nil {
		return fmt.Errorf("settla-tenant-index: adding tenant %s: %w", tenantID, err)
	}
	return nil
}

// Remove deregisters a tenant (SREM). Called when a tenant is suspended or deactivated.
func (t *TenantIndex) Remove(ctx context.Context, tenantID uuid.UUID) error {
	if err := t.client.SRem(ctx, tenantIndexKey, tenantID.String()).Err(); err != nil {
		return fmt.Errorf("settla-tenant-index: removing tenant %s: %w", tenantID, err)
	}
	return nil
}

// Count returns the number of active tenants in the index (SCARD).
func (t *TenantIndex) Count(ctx context.Context) (int64, error) {
	n, err := t.client.SCard(ctx, tenantIndexKey).Result()
	if err != nil {
		return 0, fmt.Errorf("settla-tenant-index: counting tenants: %w", err)
	}
	return n, nil
}

// ForEach iterates over all active tenant IDs using Redis SSCAN.
// Each batch of up to batchSize IDs is passed to fn.
// If Redis is unavailable, falls back to paginated Postgres queries.
func (t *TenantIndex) ForEach(ctx context.Context, batchSize int32, fn func(ids []uuid.UUID) error) error {
	err := t.forEachRedis(ctx, batchSize, fn)
	if err == nil {
		return nil
	}

	// Redis unavailable — fall back to paginated Postgres
	t.logger.Warn("settla-tenant-index: Redis unavailable, falling back to Postgres", "error", err)
	if t.fallback == nil {
		return fmt.Errorf("settla-tenant-index: Redis unavailable and no fallback configured: %w", err)
	}
	return domain.ForEachTenantBatch(ctx, t.fallback, batchSize, fn)
}

func (t *TenantIndex) forEachRedis(ctx context.Context, batchSize int32, fn func(ids []uuid.UUID) error) error {
	var cursor uint64
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		keys, nextCursor, err := t.client.SScan(ctx, tenantIndexKey, cursor, "", int64(batchSize)).Result()
		if err != nil {
			return fmt.Errorf("settla-tenant-index: scanning tenants: %w", err)
		}

		if len(keys) > 0 {
			ids := make([]uuid.UUID, 0, len(keys))
			for _, k := range keys {
				id, err := uuid.Parse(k)
				if err != nil {
					t.logger.Warn("settla-tenant-index: skipping invalid UUID in tenant set", "value", k)
					continue
				}
				ids = append(ids, id)
			}

			if len(ids) > 0 {
				if err := fn(ids); err != nil {
					return err
				}
			}
		}

		if nextCursor == 0 {
			return nil
		}
		cursor = nextCursor
	}
}

// Rebuild replaces the Redis SET with a fresh set of active tenant IDs from Postgres.
// Uses paginated queries so it never loads all IDs into memory at once.
// Called at startup and periodically as a reconciliation safety net.
func (t *TenantIndex) Rebuild(ctx context.Context) error {
	if t.fallback == nil {
		return fmt.Errorf("settla-tenant-index: no fallback fetcher configured for rebuild")
	}

	// Use a temporary key + RENAME for atomic swap (no window where the set is empty).
	tmpKey := tenantIndexKey + ":rebuild"
	pipe := t.client.Pipeline()
	pipe.Del(ctx, tmpKey)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("settla-tenant-index: clearing temp key: %w", err)
	}

	var total int
	err = domain.ForEachTenantBatch(ctx, t.fallback, domain.DefaultTenantBatchSize, func(ids []uuid.UUID) error {
		members := make([]any, len(ids))
		for i, id := range ids {
			members[i] = id.String()
		}
		if err := t.client.SAdd(ctx, tmpKey, members...).Err(); err != nil {
			return fmt.Errorf("settla-tenant-index: adding batch to temp key: %w", err)
		}
		total += len(ids)
		return nil
	})
	if err != nil {
		// Clean up temp key on failure
		t.client.Del(ctx, tmpKey)
		return fmt.Errorf("settla-tenant-index: rebuild failed during Postgres iteration: %w", err)
	}

	// Atomic swap
	if err := t.client.Rename(ctx, tmpKey, tenantIndexKey).Err(); err != nil {
		return fmt.Errorf("settla-tenant-index: renaming temp key: %w", err)
	}

	t.logger.Info("settla-tenant-index: rebuild complete", "tenants", total)
	return nil
}
