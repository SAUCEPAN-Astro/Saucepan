-- Researcher text alerts/updates inbox
CREATE TABLE IF NOT EXISTS researcher_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('alert', 'update')),
    event_type TEXT NOT NULL,
    message TEXT NOT NULL,
    campaign_id UUID,
    task_id INTEGER,
    payload JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    acked_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS ix_researcher_events_poll ON researcher_events (user_id, kind, created_at);
