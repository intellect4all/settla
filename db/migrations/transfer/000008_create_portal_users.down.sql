DROP TABLE IF EXISTS portal_users;

-- Revert kyb_status constraint to original values
ALTER TABLE tenants DROP CONSTRAINT IF EXISTS tenants_kyb_status_check;
ALTER TABLE tenants ADD CONSTRAINT tenants_kyb_status_check
    CHECK (kyb_status IN ('PENDING','VERIFIED','REJECTED'));
