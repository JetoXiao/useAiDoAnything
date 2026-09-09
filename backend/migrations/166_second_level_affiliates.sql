-- Second-level agency capability and ownership. The feature is opt-in per
-- first-level partner; existing affiliate relationships remain untouched.
CREATE TABLE IF NOT EXISTS affiliate_agency_capabilities (
    root_partner_user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    default_subagent_rate DECIMAL(8,4) NOT NULL DEFAULT 30,
    max_subagent_rate DECIMAL(8,4) NOT NULL DEFAULT 50,
    granted_by BIGINT NULL REFERENCES users(id) ON DELETE SET NULL,
    granted_at TIMESTAMPTZ NULL,
    revoked_at TIMESTAMPTZ NULL,
    revoke_reason TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (default_subagent_rate >= 0 AND default_subagent_rate <= 100),
    CHECK (max_subagent_rate >= 0 AND max_subagent_rate <= 100),
    CHECK (default_subagent_rate <= max_subagent_rate)
);

CREATE TABLE IF NOT EXISTS affiliate_subagents (
    id BIGSERIAL PRIMARY KEY,
    root_partner_user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    subagent_user_id BIGINT NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
    aff_code VARCHAR(32) NOT NULL UNIQUE,
    status VARCHAR(20) NOT NULL DEFAULT 'active',
    commission_rate DECIMAL(8,4) NOT NULL DEFAULT 30,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    disabled_at TIMESTAMPTZ NULL,
    CHECK (commission_rate >= 0 AND commission_rate <= 100)
);

CREATE INDEX IF NOT EXISTS idx_affiliate_subagents_root ON affiliate_subagents(root_partner_user_id, status);

COMMENT ON TABLE affiliate_agency_capabilities IS '管理员逐个授予一级合伙人的二级代理能力';
COMMENT ON TABLE affiliate_subagents IS '一级代理管理的二级代理';
