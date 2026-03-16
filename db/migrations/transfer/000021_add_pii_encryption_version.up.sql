-- Add PII encryption key version tracking to transfers.
-- When encryption keys are rotated, new transfers use the latest version.
-- On read, the adapter uses this column to select the correct decryption key.
-- Existing rows default to version 1 (the original key).
ALTER TABLE transfers
    ADD COLUMN pii_encryption_version SMALLINT NOT NULL DEFAULT 1;

COMMENT ON COLUMN transfers.pii_encryption_version IS
    'PII encryption key version used to encrypt sender/recipient fields. '
    'Used during decryption to select the correct DEK version. '
    'Version 0 indicates plaintext (no encryption). Default 1 is the initial key.';
