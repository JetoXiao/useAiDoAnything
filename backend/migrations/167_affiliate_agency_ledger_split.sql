ALTER TABLE user_affiliate_ledger ADD COLUMN IF NOT EXISTS agency_level SMALLINT NOT NULL DEFAULT 1;
ALTER TABLE user_affiliate_ledger ADD COLUMN IF NOT EXISTS root_partner_user_id BIGINT NULL REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE user_affiliate_ledger ADD COLUMN IF NOT EXISTS commission_rate_snapshot DECIMAL(8,4) NULL;
ALTER TABLE user_affiliate_ledger ADD COLUMN IF NOT EXISTS root_commission_amount DECIMAL(20,8) NULL;

CREATE INDEX IF NOT EXISTS idx_user_affiliate_ledger_root_partner ON user_affiliate_ledger(root_partner_user_id, created_at DESC);
