-- name: ListBankingPartners :many
SELECT * FROM banking_partners
WHERE is_active = true
ORDER BY name ASC;

-- name: GetBankingPartner :one
SELECT * FROM banking_partners
WHERE id = $1;

-- name: CreateBankingPartner :one
INSERT INTO banking_partners (name, webhook_secret, supported_currencies, metadata)
VALUES ($1, $2, $3, $4)
RETURNING *;
