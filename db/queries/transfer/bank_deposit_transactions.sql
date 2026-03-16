-- name: CreateBankDepositTransaction :one
INSERT INTO bank_deposit_transactions (
    session_id, tenant_id, bank_reference, payer_name,
    payer_account_number, amount, currency, received_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
) RETURNING id, created_at;

-- name: GetBankDepositTransactionByRef :one
SELECT * FROM bank_deposit_transactions
WHERE bank_reference = $1
ORDER BY created_at DESC
LIMIT 1;

-- name: ListBankDepositTransactionsBySession :many
SELECT * FROM bank_deposit_transactions
WHERE session_id = $1
ORDER BY created_at ASC;
