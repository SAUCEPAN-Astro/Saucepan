-- Task public UUID + revision audit
-- Apply: psql "$PG_DSN" -f migrations/003_task_public_id.sql

ALTER TABLE tasks
  ADD COLUMN IF NOT EXISTS public_id UUID NOT NULL DEFAULT gen_random_uuid();

CREATE UNIQUE INDEX IF NOT EXISTS ix_tasks_public_id ON tasks (public_id);

CREATE TABLE IF NOT EXISTS task_revisions (
  id SERIAL PRIMARY KEY,
  old_public_id UUID NOT NULL,
  new_public_id UUID NOT NULL,
  old_task_id INTEGER NOT NULL,
  new_task_id INTEGER NOT NULL,
  actor_user_id UUID,
  patch JSONB,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
