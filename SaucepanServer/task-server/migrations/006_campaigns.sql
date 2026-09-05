-- Campaigns + tasks.campaign_id
-- Apply: psql "$PG_DSN" -f migrations/006_campaigns.sql

CREATE TABLE IF NOT EXISTS campaigns (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'draft',
    created_by UUID,
    points_multiplier DOUBLE PRECISION NOT NULL DEFAULT 1.0,
    test_only BOOLEAN NOT NULL DEFAULT false,
    pack_json JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expanded_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS ix_campaigns_status ON campaigns (status);

ALTER TABLE tasks ADD COLUMN IF NOT EXISTS campaign_id UUID REFERENCES campaigns(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS ix_tasks_campaign_id ON tasks (campaign_id);
