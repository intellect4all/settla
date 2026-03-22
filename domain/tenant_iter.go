package domain

import (
	"context"

	"github.com/google/uuid"
)

// DefaultTenantBatchSize is the default page size for paginated tenant iteration.
const DefaultTenantBatchSize int32 = 500

// TenantPageFetcher fetches a page of active tenant IDs from Postgres.
// Implementations should ORDER BY id and use LIMIT/OFFSET.
type TenantPageFetcher func(ctx context.Context, limit, offset int32) ([]uuid.UUID, error)

// ForEachTenantBatch paginates through tenants using fetch and calls fn for each batch.
// Stops early if ctx is cancelled or fn returns an error.
func ForEachTenantBatch(ctx context.Context, fetch TenantPageFetcher, batchSize int32, fn func(ids []uuid.UUID) error) error {
	var offset int32
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		ids, err := fetch(ctx, batchSize, offset)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}

		if err := fn(ids); err != nil {
			return err
		}

		if int32(len(ids)) < batchSize {
			return nil
		}
		offset += batchSize
	}
}
