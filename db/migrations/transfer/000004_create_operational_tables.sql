-- +goose Up


CREATE TABLE manual_reviews (
    id                    UUID PRIMARY KEY,
    transfer_id           UUID NOT NULL UNIQUE,
    tenant_id             UUID NOT NULL REFERENCES tenants(id),
    status                review_status_enum NOT NULL DEFAULT 'pending',
    transfer_status       TEXT NOT NULL,
    stuck_since           TIMESTAMPTZ NOT NULL,
    attempted_recoveries  INT NOT NULL DEFAULT 0,
    resolution            TEXT,
    resolved_by           TEXT,
    resolved_at           TIMESTAMPTZ,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_manual_reviews_tenant_status ON manual_reviews(tenant_id, status, created_at DESC);
CREATE INDEX idx_manual_reviews_transfer ON manual_reviews(transfer_id);
CREATE INDEX idx_manual_reviews_status_created ON manual_reviews(tenant_id, status, created_at DESC);


CREATE TABLE compensation_records (
    id              UUID PRIMARY KEY,
    transfer_id     UUID NOT NULL UNIQUE,
    tenant_id       UUID NOT NULL REFERENCES tenants(id),
    strategy        compensation_strategy_enum NOT NULL,
    steps_completed JSONB NOT NULL DEFAULT '[]',
    steps_failed    JSONB NOT NULL DEFAULT '[]',
    refund_amount   NUMERIC(28,8),
    refund_currency TEXT,
    fx_loss         NUMERIC(28,8),
    status          TEXT NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending','in_progress','completed','failed')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at    TIMESTAMPTZ
);

CREATE INDEX idx_compensation_records_tenant_status
    ON compensation_records(tenant_id, status, created_at DESC);
CREATE INDEX idx_compensation_records_transfer ON compensation_records(transfer_id);


CREATE TABLE net_settlements (
    id                UUID NOT NULL,
    tenant_id         UUID NOT NULL REFERENCES tenants(id),
    period_start      TIMESTAMPTZ NOT NULL,
    period_end        TIMESTAMPTZ NOT NULL,
    corridors         JSONB NOT NULL DEFAULT '[]',
    net_by_currency   JSONB NOT NULL DEFAULT '{}',
    total_fees_usd    NUMERIC(28,8) NOT NULL DEFAULT 0,
    instructions      JSONB NOT NULL DEFAULT '[]',
    status            TEXT NOT NULL DEFAULT 'pending'
                          CHECK (status IN ('pending','approved','settled','overdue')),
    due_date          DATE,
    settled_at        TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, created_at),
    UNIQUE (tenant_id, period_start, period_end, created_at)
) PARTITION BY RANGE (created_at);

CREATE INDEX idx_net_settlements_tenant_status ON net_settlements(tenant_id, status);
CREATE INDEX idx_net_settlements_due_date ON net_settlements(due_date)
    WHERE status IN ('pending','overdue');
CREATE INDEX idx_net_settlements_period ON net_settlements(status, period_start, period_end);
CREATE INDEX idx_net_settlements_status_created ON net_settlements(tenant_id, status, created_at DESC);

CREATE TABLE net_settlements_y2026m03 PARTITION OF net_settlements
    FOR VALUES FROM ('2026-03-01') TO ('2026-04-01');
CREATE TABLE net_settlements_y2026m04 PARTITION OF net_settlements
    FOR VALUES FROM ('2026-04-01') TO ('2026-05-01');
CREATE TABLE net_settlements_y2026m05 PARTITION OF net_settlements
    FOR VALUES FROM ('2026-05-01') TO ('2026-06-01');
CREATE TABLE net_settlements_y2026m06 PARTITION OF net_settlements
    FOR VALUES FROM ('2026-06-01') TO ('2026-07-01');
CREATE TABLE net_settlements_y2026m07 PARTITION OF net_settlements
    FOR VALUES FROM ('2026-07-01') TO ('2026-08-01');
CREATE TABLE net_settlements_y2026m08 PARTITION OF net_settlements
    FOR VALUES FROM ('2026-08-01') TO ('2026-09-01');
CREATE TABLE net_settlements_y2026m09 PARTITION OF net_settlements
    FOR VALUES FROM ('2026-09-01') TO ('2026-10-01');
CREATE TABLE net_settlements_default PARTITION OF net_settlements DEFAULT;


CREATE TABLE reconciliation_reports (
    id              UUID PRIMARY KEY,
    job_name        TEXT NOT NULL,
    run_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    duration_ms     INT NOT NULL DEFAULT 0,
    checks_run      INT NOT NULL DEFAULT 0,
    checks_passed   INT NOT NULL DEFAULT 0,
    discrepancies   JSONB NOT NULL DEFAULT '[]',
    auto_corrected  INT NOT NULL DEFAULT 0,
    needs_review    BOOLEAN NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_reconciliation_reports_job ON reconciliation_reports(job_name, run_at DESC);
CREATE INDEX idx_reconciliation_reports_needs_review
    ON reconciliation_reports(needs_review, created_at DESC)
    WHERE needs_review = true;
CREATE INDEX idx_reconciliation_reports_run_at ON reconciliation_reports(run_at DESC);

-- +goose Down
DROP TABLE IF EXISTS reconciliation_reports;
DROP TABLE IF EXISTS net_settlements;
DROP TABLE IF EXISTS compensation_records;
DROP TABLE IF EXISTS manual_reviews;
