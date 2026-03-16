-- name: CreateDepositSession :one
INSERT INTO crypto_deposit_sessions (
    tenant_id, idempotency_key, status, chain, token, deposit_address,
    expected_amount, currency, collection_fee_bps, settlement_pref,
    derivation_index, expires_at, metadata, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, now()
) RETURNING *;

-- name: GetDepositSession :one
SELECT * FROM crypto_deposit_sessions
WHERE id = $1 AND tenant_id = $2;

-- name: GetDepositSessionForUpdate :one
SELECT * FROM crypto_deposit_sessions
WHERE id = $1 AND tenant_id = $2
FOR UPDATE;

-- name: GetDepositSessionByIdempotencyKey :one
SELECT * FROM crypto_deposit_sessions
WHERE tenant_id = $1 AND idempotency_key = $2;

-- name: GetDepositSessionByAddress :one
SELECT * FROM crypto_deposit_sessions
WHERE tenant_id = $1 AND deposit_address = $2
ORDER BY created_at DESC
LIMIT 1;

-- name: GetDepositSessionByAddressOnly :one
-- Chain monitor discovery: looks up a session by deposit address when tenant_id
-- is unknown. Deposit addresses are globally unique (HD wallet derived), so this
-- is safe. The returned session reveals the tenant_id.
SELECT * FROM crypto_deposit_sessions
WHERE deposit_address = $1
ORDER BY created_at DESC
LIMIT 1;

-- name: ListDepositSessionsByTenant :many
SELECT * FROM crypto_deposit_sessions
WHERE tenant_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: ListDepositSessionsByTenantAndStatus :many
SELECT * FROM crypto_deposit_sessions
WHERE tenant_id = $1 AND status = $2
ORDER BY created_at DESC
LIMIT $3 OFFSET $4;

-- name: UpdateDepositSessionStatus :exec
UPDATE crypto_deposit_sessions
SET status = $3, version = version + 1, updated_at = now()
WHERE id = $1 AND tenant_id = $2;

-- name: UpdateDepositSessionDetected :exec
UPDATE crypto_deposit_sessions
SET status = 'DETECTED', version = version + 1,
    received_amount = $3, detected_at = now(), updated_at = now()
WHERE id = $1 AND tenant_id = $2;

-- name: UpdateDepositSessionConfirmed :exec
UPDATE crypto_deposit_sessions
SET status = 'CONFIRMED', version = version + 1,
    received_amount = $3, confirmed_at = now(), updated_at = now()
WHERE id = $1 AND tenant_id = $2;

-- name: UpdateDepositSessionCredited :exec
UPDATE crypto_deposit_sessions
SET status = 'CREDITED', version = version + 1,
    fee_amount = $3, net_amount = $4, credited_at = now(), updated_at = now()
WHERE id = $1 AND tenant_id = $2;

-- name: UpdateDepositSessionSettled :exec
UPDATE crypto_deposit_sessions
SET status = 'SETTLED', version = version + 1,
    settlement_transfer_id = $3, settled_at = now(), updated_at = now()
WHERE id = $1 AND tenant_id = $2;

-- name: UpdateDepositSessionHeld :exec
UPDATE crypto_deposit_sessions
SET status = 'HELD', version = version + 1, updated_at = now()
WHERE id = $1 AND tenant_id = $2;

-- name: UpdateDepositSessionExpired :exec
UPDATE crypto_deposit_sessions
SET status = 'EXPIRED', version = version + 1,
    expired_at = now(), updated_at = now()
WHERE id = $1 AND tenant_id = $2;

-- name: UpdateDepositSessionFailed :exec
UPDATE crypto_deposit_sessions
SET status = 'FAILED', version = version + 1,
    failure_reason = $3, failure_code = $4,
    failed_at = now(), updated_at = now()
WHERE id = $1 AND tenant_id = $2;

-- name: UpdateDepositSessionCancelled :exec
UPDATE crypto_deposit_sessions
SET status = 'CANCELLED', version = version + 1, updated_at = now()
WHERE id = $1 AND tenant_id = $2;

-- name: GetExpiredPendingSessions :many
SELECT * FROM crypto_deposit_sessions
WHERE status = 'PENDING_PAYMENT'
  AND expires_at < now()
ORDER BY expires_at ASC
LIMIT $1;

-- name: AccumulateReceivedAmount :exec
UPDATE crypto_deposit_sessions
SET received_amount = received_amount + $3, updated_at = now()
WHERE id = $1 AND tenant_id = $2;

-- name: CreateDepositTransaction :one
INSERT INTO crypto_deposit_transactions (
    session_id, tenant_id, chain, tx_hash, from_address, to_address,
    token_contract, amount, block_number, block_hash,
    confirmations, required_confirm, detected_at, created_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, now(), now()
) RETURNING *;

-- name: GetDepositTransactionByHash :one
SELECT * FROM crypto_deposit_transactions
WHERE chain = $1 AND tx_hash = $2;

-- name: ListDepositTransactionsBySession :many
SELECT * FROM crypto_deposit_transactions
WHERE session_id = $1
ORDER BY created_at DESC;

-- name: UpdateDepositTransactionConfirmations :exec
UPDATE crypto_deposit_transactions
SET confirmations = $3, confirmed = $4, confirmed_at = $5
WHERE id = $1 AND created_at = $2;

-- name: GetUnconfirmedDepositTransactions :many
SELECT * FROM crypto_deposit_transactions
WHERE confirmed = false
ORDER BY created_at ASC
LIMIT $1;

-- name: CountDepositSessionsByTenantAndStatus :one
SELECT COUNT(*) FROM crypto_deposit_sessions
WHERE tenant_id = $1 AND status = $2;

-- name: GetDepositSessionByIDOnly :one
SELECT id, tenant_id, status, chain, token, deposit_address, expected_amount,
       received_amount, currency, expires_at, created_at, updated_at
FROM crypto_deposit_sessions
WHERE id = $1;
