-- Revert net_settlements from partitioned back to non-partitioned table.

BEGIN;

ALTER TABLE net_settlements RENAME TO net_settlements_partitioned;

CREATE TABLE net_settlements (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        UUID NOT NULL,
    period_start     TIMESTAMPTZ NOT NULL,
    period_end       TIMESTAMPTZ NOT NULL,
    corridors        JSONB NOT NULL DEFAULT '[]',
    net_by_currency  JSONB NOT NULL DEFAULT '{}',
    total_fees_usd   NUMERIC(28, 8) NOT NULL DEFAULT 0,
    instructions     JSONB NOT NULL DEFAULT '[]',
    status           TEXT NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending', 'approved', 'settled', 'overdue')),
    due_date         DATE,
    settled_at       TIMESTAMPTZ,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(tenant_id, period_start, period_end)
);

INSERT INTO net_settlements SELECT * FROM net_settlements_partitioned;
DROP TABLE net_settlements_partitioned;

CREATE INDEX idx_net_settlements_tenant_status ON net_settlements (tenant_id, status);
CREATE INDEX idx_net_settlements_due_date ON net_settlements (due_date)
    WHERE status IN ('pending', 'overdue');

COMMIT;
