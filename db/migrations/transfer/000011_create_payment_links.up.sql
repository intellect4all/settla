CREATE TABLE payment_links (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id UUID NOT NULL REFERENCES tenants(id),
    short_code TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    session_config JSONB NOT NULL,
    use_limit INT,
    use_count INT NOT NULL DEFAULT 0,
    expires_at TIMESTAMPTZ,
    redirect_url TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'ACTIVE',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_payment_links_tenant ON payment_links(tenant_id, created_at DESC);
CREATE INDEX idx_payment_links_active ON payment_links(status) WHERE status = 'ACTIVE';
