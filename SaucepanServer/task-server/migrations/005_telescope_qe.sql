-- Telescope quantum efficiency for c_hw
-- Apply: psql "$PG_DSN" -f migrations/005_telescope_qe.sql

ALTER TABLE telescopes
  ADD COLUMN IF NOT EXISTS qe DOUBLE PRECISION;
