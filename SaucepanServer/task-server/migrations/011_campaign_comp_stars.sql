-- Optional comparison stars for campaign photometry (#58)
-- Apply: psql "$PG_DSN" -f migrations/011_campaign_comp_stars.sql

ALTER TABLE campaigns
  ADD COLUMN IF NOT EXISTS comp_stars JSONB NOT NULL DEFAULT '[]'::jsonb;
