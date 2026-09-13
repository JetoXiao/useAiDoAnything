-- Keep the second-level status update compatible with databases where the
-- affiliate_subagents table was created before the status metadata columns
-- were introduced. CREATE TABLE IF NOT EXISTS does not alter an existing
-- table, so these guards are required for in-place upgrades.
ALTER TABLE affiliate_subagents
    ADD COLUMN IF NOT EXISTS disabled_at TIMESTAMPTZ NULL;

ALTER TABLE affiliate_subagents
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
