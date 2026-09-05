-- Researcher approval for campaign create (manual ops gate)
-- Apply: psql "$PG_DSN" -f migrations/002_researcher_approval.sql

ALTER TABLE users
  ADD COLUMN IF NOT EXISTS researcher_approved BOOLEAN NOT NULL DEFAULT false;

ALTER TABLE users
  ADD COLUMN IF NOT EXISTS researcher_approved_at TIMESTAMPTZ;

ALTER TABLE users
  ADD COLUMN IF NOT EXISTS researcher_approved_by TEXT;
