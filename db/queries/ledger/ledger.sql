-- name: CreateAccount :one
INSERT INTO accounts (
    tenant_id, code, name, type, currency, normal_balance, parent_id, metadata
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
) RETURNING *;

-- name: GetAccount :one
SELECT * FROM accounts WHERE id = $1;

-- name: GetAccountByCode :one
SELECT * FROM accounts WHERE code = $1;

-- name: ListAccountsByTenant :many
SELECT * FROM accounts
WHERE tenant_id = $1
ORDER BY code;

-- name: ListSystemAccounts :many
SELECT * FROM accounts
WHERE tenant_id IS NULL
ORDER BY code;

-- name: ListAllAccounts :many
SELECT * FROM accounts
ORDER BY code;

-- name: CreateJournalEntry :one
INSERT INTO journal_entries (
    tenant_id, idempotency_key, effective_date, description,
    reference_type, reference_id, reversal_of, metadata
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
) RETURNING *;

-- name: GetJournalEntry :one
SELECT * FROM journal_entries
WHERE id = $1 AND posted_at = $2;

-- name: GetJournalEntryByIdempotencyKey :one
SELECT * FROM journal_entries
WHERE tenant_id = $1 AND idempotency_key = $2
LIMIT 1;

-- name: ListJournalEntriesByReference :many
SELECT * FROM journal_entries
WHERE reference_type = $1 AND reference_id = $2
ORDER BY posted_at DESC;

-- name: ListJournalEntriesByTenant :many
SELECT * FROM journal_entries
WHERE tenant_id = $1
ORDER BY posted_at DESC
LIMIT $2 OFFSET $3;

-- name: ListJournalEntriesInDateRange :many
SELECT * FROM journal_entries
WHERE tenant_id = $1
  AND posted_at >= $2
  AND posted_at < $3
ORDER BY posted_at DESC
LIMIT $4 OFFSET $5;

-- name: CreateEntryLine :one
INSERT INTO entry_lines (
    journal_entry_id, account_id, entry_type, amount, currency, description
) VALUES (
    $1, $2, $3, $4, $5, $6
) RETURNING *;

-- name: ListEntryLinesByJournal :many
SELECT * FROM entry_lines
WHERE journal_entry_id = $1
ORDER BY created_at;

-- name: ListEntryLinesWithAccountByJournal :many
SELECT el.id, el.journal_entry_id, el.account_id, el.entry_type,
       el.amount, el.currency, el.created_at,
       a.code AS account_code, a.name AS account_name,
       a.type AS account_type, a.currency AS account_currency,
       a.normal_balance AS account_normal_balance
FROM entry_lines el
JOIN accounts a ON a.id = el.account_id
WHERE el.journal_entry_id = $1
ORDER BY el.created_at;

-- name: ListEntryLinesByAccount :many
SELECT * FROM entry_lines
WHERE account_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: ListEntryLinesByAccountInDateRange :many
SELECT * FROM entry_lines
WHERE account_id = $1
  AND created_at >= $2
  AND created_at < $3
ORDER BY created_at DESC
LIMIT $4 OFFSET $5;

-- name: UpsertBalanceSnapshot :one
INSERT INTO balance_snapshots (
    account_id, balance, last_entry_id, version
) VALUES (
    $1, $2, $3, $4
)
ON CONFLICT (account_id) DO UPDATE SET
    balance = EXCLUDED.balance,
    last_entry_id = EXCLUDED.last_entry_id,
    version = EXCLUDED.version,
    updated_at = now()
RETURNING *;

-- name: GetBalanceSnapshot :one
SELECT * FROM balance_snapshots
WHERE account_id = $1;

-- name: ListBalanceSnapshots :many
SELECT bs.*, a.code, a.currency, a.type, a.tenant_id
FROM balance_snapshots bs
JOIN accounts a ON a.id = bs.account_id
ORDER BY a.code;

-- name: ListBalanceSnapshotsByTenant :many
SELECT bs.*, a.code, a.currency, a.type
FROM balance_snapshots bs
JOIN accounts a ON a.id = bs.account_id
WHERE a.tenant_id = $1
ORDER BY a.code;

-- name: GetAccountBalanceByCode :one
-- Get balance for an account by its code from balance_snapshots.
-- Returns NULL if no snapshot exists yet (handled as zero by caller).
SELECT bs.balance
FROM balance_snapshots bs
JOIN accounts a ON a.id = bs.account_id
WHERE a.code = $1
LIMIT 1;
