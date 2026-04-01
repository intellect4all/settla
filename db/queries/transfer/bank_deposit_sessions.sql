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
-- SECURITY NOTE: This query intentionally omits tenant_id because it is used by
-- the inbound bank webhook flow, where the bank callback identifies the session
-- by account number only. The caller (BankDepositWorker) validates the tenant
-- context after loading the session. Never expose this query to tenant-facing APIs.
SELECT * FROM bank_deposit_sessions
WHERE account_number = $1 AND status = 'PENDING_PAYMENT'
ORDER BY created_at DESC
LIMIT 1;

-- name: ListBankDepositSessionsByTenantFirst :many
-- First page (no cursor): returns the most recent sessions.
SELECT * FROM bank_deposit_sessions
WHERE tenant_id = $1
ORDER BY created_at DESC
LIMIT $2;

-- name: ListBankDepositSessionsByTenantCursor :many
-- Subsequent pages: cursor-based pagination using created_at.
SELECT * FROM bank_deposit_sessions
WHERE tenant_id = $1
  AND created_at < @cursor_created_at
ORDER BY created_at DESC
LIMIT @page_size;

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

-- name: CreateBankDepositSessionFull :one
-- Creates a bank deposit session with all fields including pre-generated ID,
-- version, and amount fields. Used by CreateSessionWithOutbox.
INSERT INTO bank_deposit_sessions (
    id, tenant_id, idempotency_key, status, version,
    banking_partner_id, account_number, account_name, sort_code, iban, account_type,
    currency, expected_amount, min_amount, max_amount, received_amount,
    fee_amount, net_amount, mismatch_policy, collection_fee_bps,
    settlement_pref, expires_at, metadata
) VALUES (
    @id, @tenant_id, @idempotency_key, @status, @version,
    @banking_partner_id, @account_number, @account_name, @sort_code, @iban, @account_type,
    @currency, @expected_amount, @min_amount, @max_amount, @received_amount,
    @fee_amount, @net_amount, @mismatch_policy, @collection_fee_bps,
    @settlement_pref, @expires_at, @metadata
) RETURNING created_at, updated_at;

-- name: TransitionBankDepositSession :execresult
-- Atomically transitions a bank deposit session with optimistic lock, updating
-- amounts, payer info, failure details, and status-specific timestamps.
UPDATE bank_deposit_sessions
SET status = @new_status::bank_deposit_session_status_enum,
    version = @new_version,
    updated_at = now(),
    received_amount = @received_amount,
    fee_amount = @fee_amount,
    net_amount = @net_amount,
    settlement_transfer_id = @settlement_transfer_id,
    payer_name = COALESCE(NULLIF(@payer_name, ''), payer_name),
    payer_reference = COALESCE(NULLIF(@payer_reference, ''), payer_reference),
    bank_reference = COALESCE(NULLIF(@bank_reference_val, ''), bank_reference),
    payment_received_at = CASE WHEN @status_text::text = 'PAYMENT_RECEIVED' AND payment_received_at IS NULL THEN now() ELSE payment_received_at END,
    credited_at  = CASE WHEN @status_text::text = 'CREDITED'  AND credited_at IS NULL THEN now() ELSE credited_at END,
    settled_at   = CASE WHEN @status_text::text = 'SETTLED'   AND settled_at IS NULL THEN now() ELSE settled_at END,
    expired_at   = CASE WHEN @status_text::text = 'EXPIRED'   AND expired_at IS NULL THEN now() ELSE expired_at END,
    failed_at    = CASE WHEN @status_text::text = 'FAILED'    AND failed_at IS NULL THEN now() ELSE failed_at END,
    failure_reason = COALESCE(NULLIF(@failure_reason, ''), failure_reason),
    failure_code   = COALESCE(NULLIF(@failure_code, ''), failure_code)
WHERE id = @id AND version = @expected_version;
