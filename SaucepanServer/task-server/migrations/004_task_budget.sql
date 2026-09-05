-- Task 2 m-equivalent budget columns
-- Apply: psql "$PG_DSN" -f migrations/004_task_budget.sql

ALTER TABLE tasks
  ADD COLUMN IF NOT EXISTS normalized_integration_budget_s DOUBLE PRECISION;

ALTER TABLE tasks
  ADD COLUMN IF NOT EXISTS normalized_integration_earned_s DOUBLE PRECISION NOT NULL DEFAULT 0;
