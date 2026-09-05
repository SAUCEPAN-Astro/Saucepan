-- Campaign science hook operator approval (compute placement; #50)
-- Apply: psql "$PG_DSN" -f migrations/010_campaign_hooks.sql

ALTER TABLE campaigns
  ADD COLUMN IF NOT EXISTS hook_approved BOOLEAN NOT NULL DEFAULT false;
