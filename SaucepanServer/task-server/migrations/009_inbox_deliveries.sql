-- Researcher FITS delivery inbox
CREATE TABLE IF NOT EXISTS inbox_deliveries (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL,
    campaign_id UUID NOT NULL,
    task_id INTEGER,
    task_public_id UUID,
    frame_grade_id INTEGER,
    upload_id VARCHAR(128),
    status TEXT NOT NULL DEFAULT 'completed',
    failure_reason TEXT,
    raw_object_key TEXT,
    graded_object_key TEXT,
    bucket TEXT NOT NULL DEFAULT 'saucepan',
    points_earned DOUBLE PRECISION,
    stack_eligible BOOLEAN,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    acked_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS ix_inbox_deliveries_poll ON inbox_deliveries (user_id, created_at);
CREATE INDEX IF NOT EXISTS ix_inbox_deliveries_campaign ON inbox_deliveries (campaign_id, created_at);
