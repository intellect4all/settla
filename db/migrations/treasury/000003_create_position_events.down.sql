-- Revert position_events table and reserve_ops constraint change.

DROP TABLE IF EXISTS position_events CASCADE;

-- Restore original reserve_ops constraint.
ALTER TABLE reserve_ops DROP CONSTRAINT IF EXISTS reserve_ops_op_type_check;
ALTER TABLE reserve_ops ADD CONSTRAINT reserve_ops_op_type_check
    CHECK (op_type IN ('reserve', 'release', 'commit'));
