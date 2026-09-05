-- Developer API keys + standalone task inbox (#43)
-- Apply: psql "$PG_DSN" -f migrations/012_developer_api.sql

ALTER TABLE tasks
  ADD COLUMN IF NOT EXISTS developer_user_id UUID;
ALTER TABLE tasks
  ADD COLUMN IF NOT EXISTS original_spec JSONB NOT NULL DEFAULT '{}'::jsonb;

CREATE INDEX IF NOT EXISTS ix_tasks_developer_user_id ON tasks (developer_user_id);

CREATE TABLE IF NOT EXISTS developer_api_keys (
    id SERIAL PRIMARY KEY,
    user_id UUID NOT NULL,
    name TEXT NOT NULL,
    key_prefix TEXT NOT NULL,
    key_hash TEXT NOT NULL UNIQUE,
    scopes TEXT[] NOT NULL DEFAULT '{}',
    is_active BOOLEAN NOT NULL DEFAULT true,
    last_used_at TIMESTAMPTZ,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS ix_developer_api_keys_user ON developer_api_keys (user_id);

CREATE TABLE IF NOT EXISTS developer_deliveries (
    id SERIAL PRIMARY KEY,
    user_id UUID NOT NULL,
    task_id INTEGER NOT NULL,
    status TEXT NOT NULL DEFAULT 'completed',
    failure_reason TEXT,
    graded_object_key TEXT,
    bucket TEXT NOT NULL DEFAULT 'saucepan',
    original_spec JSONB NOT NULL DEFAULT '{}',
    frame_grade_id INTEGER,
    upload_id VARCHAR(128),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    acked_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS ix_developer_deliveries_poll ON developer_deliveries (user_id, created_at);
