-- name: DispensePoolAddress :one
-- Dispenses a pre-generated address from the pool using SKIP LOCKED to avoid contention.
-- Returns the dispensed address row.
UPDATE crypto_address_pool AS cap
SET dispensed = true, dispensed_at = now(), session_id = @session_id
WHERE cap.id = (
    SELECT cap2.id FROM crypto_address_pool cap2
    WHERE cap2.tenant_id = @tenant_id AND cap2.chain = @chain AND cap2.dispensed = false
    ORDER BY cap2.derivation_index ASC
    LIMIT 1
    FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: InsertPoolAddress :one
INSERT INTO crypto_address_pool (
    tenant_id, chain, address, derivation_index
) VALUES (
    $1, $2, $3, $4
) RETURNING *;

-- name: CountAvailablePoolAddresses :one
SELECT COUNT(*) FROM crypto_address_pool
WHERE tenant_id = $1 AND chain = $2 AND dispensed = false;

-- name: GetAddressIndex :one
SELECT * FROM crypto_deposit_address_index
WHERE chain = $1 AND address = $2;

-- name: InsertAddressIndex :one
INSERT INTO crypto_deposit_address_index (
    chain, address, tenant_id, session_id
) VALUES (
    $1, $2, $3, $4
) RETURNING *;

-- name: IncrementDerivationCounter :one
-- Atomically increments the derivation counter and returns the new index.
INSERT INTO crypto_derivation_counters (tenant_id, chain, next_index)
VALUES ($1, $2, 1)
ON CONFLICT (tenant_id, chain)
DO UPDATE SET next_index = crypto_derivation_counters.next_index + 1
RETURNING next_index - 1 AS current_index;

-- name: GetDerivationCounter :one
SELECT next_index FROM crypto_derivation_counters
WHERE tenant_id = $1 AND chain = $2;

-- name: ListPoolAddressesByTenant :many
SELECT * FROM crypto_address_pool
WHERE tenant_id = $1 AND chain = $2
ORDER BY derivation_index ASC
LIMIT $3 OFFSET $4;
