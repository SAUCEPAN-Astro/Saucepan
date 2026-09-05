-- Task lease reclaim support (#403).
--
-- #402 declared task_assignments.lease_expires_at (left NULL). This migration
-- adds the two `tasks` columns the orchestrator's assign + reclaim paths depend
-- on. persistTaskAssignment (#400/#402) already writes
-- last_assignment_attempt_at, and the reclaim loop bumps failure_count on every
-- lapsed lease.
--
--   failure_count               Number of times this task's lease lapsed with no
--                               active assignee left and it was requeued. Once
--                               it reaches TASK_MAX_FAILURES the task goes to a
--                               terminal 'expired' and is never requeued again.
--   last_assignment_attempt_at  Set on every assign claim and every reclaim
--                               requeue. The exponential re-selection backoff
--                               min(TASK_RECLAIM_BACKOFF_BASE * 2^failure_count,
--                               30m) is measured from it.
--
-- Idempotent: ADD COLUMN / CREATE INDEX IF NOT EXISTS, so the whole directory
-- replays cleanly with psql.

ALTER TABLE tasks ADD COLUMN IF NOT EXISTS failure_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS last_assignment_attempt_at TIMESTAMPTZ;

-- Sweep support for reclaimLoop: expired-lease lookup over still-active rows.
CREATE INDEX IF NOT EXISTS idx_task_assignments_lease_expiry
    ON task_assignments (lease_expires_at)
    WHERE status IN ('assigned', 'in_progress');
