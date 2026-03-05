-- name: CreateTransfer :one
INSERT INTO transfers (
    tenant_id, external_ref, idempotency_key, status,
    source_currency, source_amount, dest_currency, dest_amount,
    stable_coin, stable_amount, chain, fx_rate, fees,
    sender, recipient, quote_id,
    on_ramp_provider_id, off_ramp_provider_id
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16,
    $17, $18
) RETURNING *;

-- name: GetTransfer :one
SELECT * FROM transfers
WHERE id = $1 AND tenant_id = $2;

-- name: GetTransferByID :one
SELECT * FROM transfers
WHERE id = $1;

-- name: GetTransferByIdempotencyKey :one
SELECT * FROM transfers
WHERE tenant_id = $1 AND idempotency_key = $2
LIMIT 1;

-- name: GetTransferByExternalRef :one
SELECT * FROM transfers
WHERE tenant_id = $1 AND external_ref = $2
LIMIT 1;

-- name: ListTransfersByTenant :many
SELECT * FROM transfers
WHERE tenant_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: ListTransfersByStatus :many
SELECT * FROM transfers
WHERE tenant_id = $1 AND status = $2
ORDER BY created_at DESC
LIMIT $3 OFFSET $4;

-- name: ListTransfersInDateRange :many
SELECT * FROM transfers
WHERE tenant_id = $1
  AND created_at >= $2
  AND created_at < $3
ORDER BY created_at DESC
LIMIT $4 OFFSET $5;

-- name: UpdateTransferStatus :exec
UPDATE transfers
SET status = $3,
    version = version + 1,
    updated_at = now(),
    funded_at = CASE WHEN $3 = 'FUNDED' THEN now() ELSE funded_at END,
    completed_at = CASE WHEN $3 = 'COMPLETED' THEN now() ELSE completed_at END,
    failed_at = CASE WHEN $3 = 'FAILED' THEN now() ELSE failed_at END
WHERE id = $1 AND tenant_id = $2;

-- name: UpdateTransferStatusWithVersion :exec
UPDATE transfers
SET status = $3,
    version = version + 1,
    updated_at = now()
WHERE id = $1 AND tenant_id = $2 AND version = $4;

-- name: UpdateTransferFailure :exec
UPDATE transfers
SET status = 'FAILED',
    version = version + 1,
    updated_at = now(),
    failed_at = now(),
    failure_reason = $3,
    failure_code = $4
WHERE id = $1 AND tenant_id = $2;

-- name: UpdateTransferDestAmount :exec
UPDATE transfers
SET dest_amount = $3, fx_rate = $4, stable_coin = $5, stable_amount = $6, updated_at = now()
WHERE id = $1 AND tenant_id = $2;

-- name: SumDailyVolumeByTenant :one
SELECT COALESCE(SUM(source_amount), 0)::NUMERIC(28,8) AS total_volume
FROM transfers
WHERE tenant_id = $1
  AND created_at >= $2
  AND created_at < $3
  AND status NOT IN ('FAILED', 'REFUNDED');

-- name: CountTransfersByTenant :one
SELECT count(*) FROM transfers
WHERE tenant_id = $1;

-- name: CreateTransferEvent :one
INSERT INTO transfer_events (
    transfer_id, tenant_id, from_status, to_status,
    metadata, provider_ref
) VALUES (
    $1, $2, $3, $4, $5, $6
) RETURNING *;

-- name: ListTransferEvents :many
SELECT * FROM transfer_events
WHERE tenant_id = $1 AND transfer_id = $2
ORDER BY occurred_at DESC;

-- name: CreateQuote :one
INSERT INTO quotes (
    tenant_id, source_currency, source_amount,
    dest_currency, dest_amount, stable_amount, fx_rate,
    fees, route, expires_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
) RETURNING *;

-- name: GetQuote :one
SELECT * FROM quotes
WHERE id = $1 AND tenant_id = $2;

-- name: GetActiveQuote :one
SELECT * FROM quotes
WHERE id = $1 AND tenant_id = $2
  AND expires_at > now();

-- name: CreateProviderTransaction :one
INSERT INTO provider_transactions (
    tenant_id, provider, tx_type, external_id,
    transfer_id, status, amount, currency,
    chain, tx_hash, metadata
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
) RETURNING *;

-- name: UpdateProviderTransactionStatus :exec
UPDATE provider_transactions
SET status = $2, updated_at = now()
WHERE id = $1;

-- name: UpdateProviderTransactionHash :exec
UPDATE provider_transactions
SET tx_hash = $2, updated_at = now()
WHERE id = $1;

-- name: ListProviderTransactions :many
SELECT * FROM provider_transactions
WHERE tenant_id = $1 AND transfer_id = $2
ORDER BY created_at;
