-- name: CreateBankDepositSession :one
INSERT INTO bank_deposit_sessions (
    tenant_id, idempotency_key, status, banking_partner_id, account_number,
    account_name, sort_code, iban, account_type, currency, expected_amount,
    min_amount, max_amount, mismatch_policy, collection_fee_bps,
    settlement_pref, expires_at, metadata
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18
) RETURNING id, created_at, updated_at;

-- name: GetBankDepositSession :one
SELECT * FROM bank_deposit_sessions
WHERE id = $1 AND tenant_id = $2;

-- name: GetBankDepositSessionByIdempotencyKey :one
SELECT * FROM bank_deposit_sessions
WHERE tenant_id = $1 AND idempotency_key = $2;

-- name: GetBankDepositSessionByAccountNumber :one
SELECT * FROM bank_deposit_sessions
WHERE account_number = $1 AND status = 'PENDING_PAYMENT'
ORDER BY created_at DESC
LIMIT 1;

-- name: ListBankDepositSessionsByTenant :many
SELECT * FROM bank_deposit_sessions
WHERE tenant_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: GetExpiredPendingBankSessions :many
SELECT * FROM bank_deposit_sessions
WHERE status = 'PENDING_PAYMENT'
  AND expires_at < now()
ORDER BY expires_at ASC
LIMIT $1;

-- name: AccumulateBankReceivedAmount :exec
UPDATE bank_deposit_sessions
SET received_amount = received_amount + $3, updated_at = now()
WHERE id = $1 AND tenant_id = $2;
