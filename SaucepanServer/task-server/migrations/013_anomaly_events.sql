-- Anomaly mesh coincidence events (#82)
-- Apply: psql "$PG_DSN" -f migrations/013_anomaly_events.sql

CREATE TABLE IF NOT EXISTS anomaly_events (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ra_deg DOUBLE PRECISION NOT NULL,
    dec_deg DOUBLE PRECISION NOT NULL,
    t_start TIMESTAMPTZ NOT NULL,
    t_end TIMESTAMPTZ NOT NULL,
    site_ids JSONB NOT NULL,
    node_ids JSONB NOT NULL,
    alert_count INTEGER NOT NULL,
    status TEXT NOT NULL,
    payload JSONB NOT NULL
);
