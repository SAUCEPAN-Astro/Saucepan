-- Task assignments join table (#402).
--
-- A cohort task is dispatched to N piers, but `tasks.assigned_telescope_id` is a
-- single TEXT column, so only the primary assignee ever had a DB assignment row.
-- Cohort members 2..N were then rejected at upload
-- (`cmd/apiserver/upload_assign.go` matched the one column). This table records
-- every assignee of a task so all cohort members pass upload auth.
--
-- `tasks.assigned_telescope_id` is KEPT as a legacy mirror of the primary
-- assignee (least churn); this join table is authoritative for multi-assignee.
--
-- `lease_expires_at` is defined here but left NULL — #403 owns lease semantics
-- and will populate it. Declaring the column now avoids a second migration.
--
-- Idempotent: CREATE TABLE / CREATE INDEX IF NOT EXISTS and an ON CONFLICT
-- DO NOTHING backfill, so the whole directory can be replayed.

CREATE TABLE IF NOT EXISTS task_assignments (
    task_id          INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    telescope_id     TEXT NOT NULL,
    role             TEXT NOT NULL DEFAULT 'cohort'
                       CHECK (role IN ('primary', 'cohort')),
    status           TEXT NOT NULL DEFAULT 'assigned'
                       CHECK (status IN ('assigned', 'in_progress', 'completed', 'expired', 'released')),
    assigned_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    lease_expires_at TIMESTAMPTZ,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (task_id, telescope_id)
);

-- Fast "what is this pier actively assigned to" lookup for upload auth.
CREATE INDEX IF NOT EXISTS idx_task_assignments_active
    ON task_assignments (telescope_id)
    WHERE status IN ('assigned', 'in_progress');

-- Backfill pre-migration single-column assignments as the primary row, mapping
-- the task status onto the assignment status.
INSERT INTO task_assignments (task_id, telescope_id, role, status)
SELECT id,
       assigned_telescope_id,
       'primary',
       CASE
         WHEN status = 'completed'   THEN 'completed'
         WHEN status = 'in_progress' THEN 'in_progress'
         WHEN status IN ('expired', 'superseded', 'cancelled', 'failed') THEN 'expired'
         ELSE 'assigned'
       END
FROM tasks
WHERE assigned_telescope_id IS NOT NULL
  AND TRIM(assigned_telescope_id) <> ''
ON CONFLICT DO NOTHING;
