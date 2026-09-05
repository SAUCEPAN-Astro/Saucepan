-- Per-campaign on-pier-code kill switch (#470 step 7 / #520)
-- Apply: psql "$PG_DSN" -f migrations/014_campaign_pier_code_disabled.sql
--
-- Server-set. When true, the orchestrator still sends the campaign's grant
-- map + artifact ref on the assign, but flags PierCodeDisabled so piers skip
-- running or continuing that campaign's code at their next check-in.
-- Set/clear runbook: docs/ops/disable_campaign_pier_code.md

ALTER TABLE campaigns
  ADD COLUMN IF NOT EXISTS pier_code_disabled BOOLEAN NOT NULL DEFAULT FALSE;
