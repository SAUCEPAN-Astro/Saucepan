-- Hot Path: Postgres LISTEN/NOTIFY setup
-- Self-contained — creates tables if they don't exist

-- Minimal tasks table for hot path testing
CREATE TABLE IF NOT EXISTS tasks (
    id              SERIAL PRIMARY KEY,
    public_id       UUID NOT NULL DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL DEFAULT '',
    priority        INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL DEFAULT 'pending',
    assigned_telescope_id TEXT,
    integration_time DOUBLE PRECISION,
    normalized_integration_budget_s DOUBLE PRECISION,
    normalized_integration_earned_s DOUBLE PRECISION NOT NULL DEFAULT 0,
    min_power       DOUBLE PRECISION,
    target_magnitude DOUBLE PRECISION,
    product_mode    TEXT NOT NULL DEFAULT 'per_frame'
                    CHECK (product_mode IN ('per_frame', 'time_bin', 'stack')),
    required_filters TEXT[],
    target_ra       DOUBLE PRECISION,
    target_dec      DOUBLE PRECISION,
    min_altitude_deg DOUBLE PRECISION,
    allow_emulator   BOOLEAN NOT NULL DEFAULT false,
    min_aperture_mm           DOUBLE PRECISION,
    min_sub_exposure_s        DOUBLE PRECISION,
    min_resolution_arcsec     DOUBLE PRECISION,
    max_resolution_arcsec     DOUBLE PRECISION,
    min_psf_fwhm_arcsec       DOUBLE PRECISION,
    max_psf_fwhm_arcsec       DOUBLE PRECISION,
    required_fov_width_arcmin  DOUBLE PRECISION,
    required_fov_height_arcmin DOUBLE PRECISION,
    science_band              TEXT,
    created_at      TIMESTAMP WITH TIME ZONE DEFAULT NOW(),
    updated_at      TIMESTAMP WITH TIME ZONE DEFAULT NOW()
);

-- Minimal telescopes table for warm cache
CREATE TABLE IF NOT EXISTS telescopes (
    telescope_id        TEXT PRIMARY KEY,
    name                TEXT DEFAULT '',
    is_active           BOOLEAN DEFAULT true,
    power               DOUBLE PRECISION DEFAULT 0.5,
    available_filters   TEXT[] DEFAULT '{}',
    reputation_stats    JSONB DEFAULT '{"reliability_score": 0.8}',
    aperture_mm         DOUBLE PRECISION,
    qe                  DOUBLE PRECISION,
    focal_length_mm     DOUBLE PRECISION,
    pixel_size_um       DOUBLE PRECISION,
    site_latitude       DOUBLE PRECISION,
    site_longitude      DOUBLE PRECISION,
    seeing_arcsec       DOUBLE PRECISION DEFAULT 1.5,
    median_seeing_arcsec DOUBLE PRECISION,
    limiting_magnitude  DOUBLE PRECISION,
    fov_width_arcmin    DOUBLE PRECISION DEFAULT 30,
    fov_height_arcmin   DOUBLE PRECISION DEFAULT 20,
    mount_type          INTEGER DEFAULT 0,
    max_stable_exposure_s     DOUBLE PRECISION,
    feature_vector      DOUBLE PRECISION[] DEFAULT '{}',
    obstruction_mask    JSONB,
    mount_limits        JSONB,
    horizon_profile     JSONB,
    is_emulator         BOOLEAN NOT NULL DEFAULT false,
    updated_at          TIMESTAMPTZ DEFAULT NOW()
);

-- Phase 1 email+password auth
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS users (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  username TEXT NOT NULL,
  email TEXT UNIQUE,
  password_hash TEXT NOT NULL,
  email_verified BOOLEAN NOT NULL DEFAULT false,
  verification_code TEXT,
  verification_code_expires_at TIMESTAMPTZ,
  reset_code TEXT,
  reset_code_expires_at TIMESTAMPTZ,
  researcher_approved BOOLEAN NOT NULL DEFAULT false,
  researcher_approved_at TIMESTAMPTZ,
  researcher_approved_by TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username_lower ON users (lower(username));

CREATE TABLE IF NOT EXISTS user_devices (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  node_id TEXT UNIQUE NOT NULL,
  device_token_hash TEXT NOT NULL,
  telescope_id TEXT,
  label TEXT,
  last_seen_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_user_devices_user_id ON user_devices(user_id);
CREATE INDEX IF NOT EXISTS idx_users_email ON users(email) WHERE email IS NOT NULL;

CREATE TABLE IF NOT EXISTS user_sessions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash TEXT NOT NULL,
  user_agent TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  expires_at TIMESTAMPTZ NOT NULL,
  revoked_at TIMESTAMPTZ,
  replaced_by UUID REFERENCES user_sessions(id) ON DELETE SET NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_user_sessions_token_hash ON user_sessions (token_hash);
CREATE INDEX IF NOT EXISTS ix_user_sessions_user_active ON user_sessions (user_id) WHERE revoked_at IS NULL;

-- Legacy auth entries (device-secret login — deprecated, kept for migration)
CREATE TABLE IF NOT EXISTS auth_entries (
    email TEXT PRIMARY KEY,
    node_id TEXT UNIQUE NOT NULL,
    device_secret_hash TEXT NOT NULL,
    is_verified BOOLEAN NOT NULL DEFAULT false,
    verification_code TEXT,
    verification_code_expires_at TIMESTAMPTZ,
    reset_code TEXT,
    reset_code_expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_auth_entries_node_id ON auth_entries(node_id);

-- Seed a test telescope
INSERT INTO telescopes (telescope_id, is_active, power, reputation_stats)
VALUES ('test-node-1', true, 0.8, '{"reliability_score": 0.9}')
ON CONFLICT (telescope_id) DO NOTHING;

INSERT INTO telescopes (telescope_id, is_active, power, reputation_stats)
VALUES ('test-node-2', true, 0.6, '{"reliability_score": 0.7}')
ON CONFLICT (telescope_id) DO NOTHING;

-- Seed 50 sim telescopes with site coordinates for slew calculation
DO $$
DECLARE
    sites float8[][] := ARRAY[
        [28.3, -16.6], [-30.2, -70.8], [19.8, -155.5], [37.2, -118.3], [31.7, -110.9],
        [32.0, 35.5], [41.0, 25.0], [-33.0, 151.0], [25.0, 102.0], [-25.0, 28.0]
    ];
BEGIN
    FOR i IN 1..50 LOOP
        INSERT INTO telescopes (telescope_id, is_active, power, available_filters,
            reputation_stats, aperture_mm, focal_length_mm, pixel_size_um,
            site_latitude, site_longitude)
        VALUES (
            format('sim-node-%s', i), true,
            (ARRAY[0.9, 0.7, 0.4, 0.85, 0.6])[((i-1) % 5) + 1],
            ARRAY['R','G','B'],
            jsonb_build_object('reliability_score', (ARRAY[0.95, 0.82, 0.65, 0.98, 0.75])[((i-1) % 5) + 1]),
            (ARRAY[400, 200, 100, 500, 150])[((i-1) % 5) + 1],
            (ARRAY[1600, 900, 400, 2000, 750])[((i-1) % 5) + 1],
            (ARRAY[3.76, 4.63, 5.60, 3.76, 4.63])[((i-1) % 5) + 1],
            sites[((i-1) % 10) + 1][1],
            sites[((i-1) % 10) + 1][2]
        )
        ON CONFLICT (telescope_id) DO NOTHING;
    END LOOP;
END $$;

-- 1. NOTIFY on new task insertion
CREATE OR REPLACE FUNCTION notify_new_task()
RETURNS TRIGGER AS $$
BEGIN
  PERFORM pg_notify(
    'new_task_channel',
    json_build_object(
      'task_id', NEW.id,
      'priority', NEW.priority,
      'name', NEW.name,
      'min_power', NEW.min_power,
      'target_magnitude', NEW.target_magnitude,
      'required_filters', NEW.required_filters,
      'integration_time', NEW.integration_time,
      'target_ra', NEW.target_ra,
      'target_dec', NEW.target_dec,
      'min_altitude_deg', NEW.min_altitude_deg,
      'allow_emulator', NEW.allow_emulator,
      'min_aperture_mm', NEW.min_aperture_mm,
      'min_sub_exposure_s', NEW.min_sub_exposure_s,
      'min_resolution_arcsec', NEW.min_resolution_arcsec,
      'max_resolution_arcsec', NEW.max_resolution_arcsec,
      'min_psf_fwhm_arcsec', NEW.min_psf_fwhm_arcsec,
      'max_psf_fwhm_arcsec', NEW.max_psf_fwhm_arcsec,
      'required_fov_width_arcmin', NEW.required_fov_width_arcmin,
      'required_fov_height_arcmin', NEW.required_fov_height_arcmin,
      'science_band', NEW.science_band,
      'created_at', to_char(NEW.created_at, 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
    )::text
  );
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS task_inserted_notify ON tasks;
CREATE TRIGGER task_inserted_notify
AFTER INSERT ON tasks
FOR EACH ROW
EXECUTE FUNCTION notify_new_task();

-- 2. NOTIFY on priority update
CREATE OR REPLACE FUNCTION notify_task_priority_change()
RETURNS TRIGGER AS $$
BEGIN
  IF OLD.priority IS DISTINCT FROM NEW.priority THEN
    PERFORM pg_notify(
      'task_priority_channel',
      json_build_object(
        'task_id', NEW.id,
        'old_priority', OLD.priority,
        'new_priority', NEW.priority
      )::text
    );
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS task_priority_changed ON tasks;
CREATE TRIGGER task_priority_changed
AFTER UPDATE OF priority ON tasks
FOR EACH ROW
WHEN (OLD.priority IS DISTINCT FROM NEW.priority)
EXECUTE FUNCTION notify_task_priority_change();

-- 3. NOTIFY on task status change
CREATE OR REPLACE FUNCTION notify_task_status_change()
RETURNS TRIGGER AS $$
BEGIN
  IF OLD.status IS DISTINCT FROM NEW.status THEN
    PERFORM pg_notify(
      'task_status_channel',
      json_build_object(
        'task_id', NEW.id,
        'old_status', OLD.status,
        'new_status', NEW.status,
        'assigned_telescope_id', NEW.assigned_telescope_id
      )::text
    );
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS task_status_changed ON tasks;
CREATE TRIGGER task_status_changed
AFTER UPDATE OF status ON tasks
FOR EACH ROW
WHEN (OLD.status IS DISTINCT FROM NEW.status)
EXECUTE FUNCTION notify_task_status_change();

-- Track F: per-frame grades (datalake sync → Go apiserver ingest)
CREATE TABLE IF NOT EXISTS frame_grades (
    id              SERIAL PRIMARY KEY,
    upload_id       VARCHAR(64),
    idempotency_key VARCHAR(128) NOT NULL UNIQUE,
    task_id         INTEGER REFERENCES tasks(id) ON DELETE SET NULL,
    telescope_id    TEXT NOT NULL REFERENCES telescopes(telescope_id) ON DELETE CASCADE,
    telescope_external_id VARCHAR(100),
    headline_grade  INTEGER NOT NULL DEFAULT 0,
    dimensions      JSONB NOT NULL,
    points_earned   DOUBLE PRECISION NOT NULL DEFAULT 0.0,
    points_breakdown JSONB,
    stack_eligible  BOOLEAN NOT NULL DEFAULT true,
    sp_exptime      DOUBLE PRECISION,
    grader_version  VARCHAR(32),
    quality_metrics JSONB,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS ix_frame_grades_upload_id ON frame_grades (upload_id);

-- L1 denormalized catalog index (METADATA Phase 2 / #33)
CREATE TABLE IF NOT EXISTS frame_catalog (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    frame_id        TEXT,
    upload_id       TEXT UNIQUE,
    telescope_id    TEXT NOT NULL,
    task_id         TEXT,
    campaign_id     TEXT,
    object_key      TEXT NOT NULL,
    checksum_sha256 TEXT,
    date_obs        TIMESTAMPTZ,
    mjd_obs         DOUBLE PRECISION,
    ra_deg          DOUBLE PRECISION,
    dec_deg         DOUBLE PRECISION,
    filter          TEXT,
    exptime_sec     DOUBLE PRECISION,
    airmass         DOUBLE PRECISION,
    fwhm_arcsec     DOUBLE PRECISION,
    snr             DOUBLE PRECISION,
    tier            SMALLINT,
    calstat         TEXT,
    phot_flag       TEXT,
    headline_grade  SMALLINT,
    stack_eligible  BOOLEAN,
    grade_json      JSONB,
    zp              DOUBLE PRECISION,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS ix_frame_catalog_sky ON frame_catalog (ra_deg, dec_deg);
CREATE INDEX IF NOT EXISTS ix_frame_catalog_time ON frame_catalog (date_obs);
CREATE INDEX IF NOT EXISTS ix_frame_catalog_tele_filter ON frame_catalog (telescope_id, filter);
CREATE INDEX IF NOT EXISTS ix_frame_catalog_campaign ON frame_catalog (campaign_id);

ALTER TABLE tasks ADD COLUMN IF NOT EXISTS accumulated_exposure_seconds DOUBLE PRECISION NOT NULL DEFAULT 0.0;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS allow_emulator BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS obstruction_mask JSONB;
ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS mount_limits JSONB;
ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS horizon_profile JSONB;
ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS is_emulator BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS min_aperture_mm DOUBLE PRECISION;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS min_sub_exposure_s DOUBLE PRECISION;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS min_resolution_arcsec DOUBLE PRECISION;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS max_resolution_arcsec DOUBLE PRECISION;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS min_psf_fwhm_arcsec DOUBLE PRECISION;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS max_psf_fwhm_arcsec DOUBLE PRECISION;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS required_fov_width_arcmin DOUBLE PRECISION;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS required_fov_height_arcmin DOUBLE PRECISION;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS science_band TEXT;
ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS max_stable_exposure_s DOUBLE PRECISION;
ALTER TABLE users ADD COLUMN IF NOT EXISTS researcher_approved BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE users ADD COLUMN IF NOT EXISTS researcher_approved_at TIMESTAMPTZ;
ALTER TABLE users ADD COLUMN IF NOT EXISTS researcher_approved_by TEXT;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS public_id UUID NOT NULL DEFAULT gen_random_uuid();
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
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS normalized_integration_budget_s DOUBLE PRECISION;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS normalized_integration_earned_s DOUBLE PRECISION NOT NULL DEFAULT 0;
ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS qe DOUBLE PRECISION;
ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS owner_user_id UUID REFERENCES users(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS ix_telescopes_owner_user_id ON telescopes (owner_user_id) WHERE owner_user_id IS NOT NULL;

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
ALTER TABLE campaigns ADD COLUMN IF NOT EXISTS hook_approved BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE campaigns ADD COLUMN IF NOT EXISTS comp_stars JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS campaign_id UUID REFERENCES campaigns(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS ix_tasks_campaign_id ON tasks (campaign_id);
ALTER TABLE telescopes ADD COLUMN IF NOT EXISTS enabled_campaign_ids TEXT[] NOT NULL DEFAULT '{}';

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

ALTER TABLE tasks ADD COLUMN IF NOT EXISTS developer_user_id UUID;
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS original_spec JSONB NOT NULL DEFAULT '{}'::jsonb;
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

-- Device-authenticated R2 multipart upload sessions. The Go upload handlers
-- use this table to bind every presigned part and completion to one pier.
CREATE TABLE IF NOT EXISTS upload_sessions (
    session_id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    s3_upload_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    telescope_id TEXT NOT NULL,
    object_path TEXT NOT NULL,
    bucket TEXT NOT NULL DEFAULT 'saucepan',
    grade_meta JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    completed_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX IF NOT EXISTS ux_upload_sessions_s3_upload_id
    ON upload_sessions (s3_upload_id);
CREATE INDEX IF NOT EXISTS ix_upload_sessions_active_expires
    ON upload_sessions (expires_at) WHERE completed_at IS NULL;
CREATE INDEX IF NOT EXISTS ix_upload_sessions_node_id
    ON upload_sessions (node_id);

-- Durable pull queue for the local storage/compute worker.
CREATE TABLE IF NOT EXISTS worker_pending_jobs (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id BIGINT NOT NULL,
    campaign_id BIGINT NOT NULL DEFAULT 0,
    telescope_id TEXT,
    object_key TEXT NOT NULL,
    object_keys JSONB NOT NULL DEFAULT '[]',
    product_mode TEXT NOT NULL DEFAULT 'per_frame'
        CHECK (product_mode IN ('per_frame', 'time_bin', 'stack')),
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'leased', 'done', 'failed')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    leased_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS ix_worker_pending_jobs_poll
    ON worker_pending_jobs (status, created_at);
CREATE UNIQUE INDEX IF NOT EXISTS ux_worker_pending_jobs_object_key
    ON worker_pending_jobs (object_key) WHERE status IN ('pending', 'leased');

-- Keep init.sql usable by itself as well as through the numbered migrations.
ALTER TABLE tasks ADD COLUMN IF NOT EXISTS product_mode TEXT NOT NULL DEFAULT 'per_frame';
ALTER TABLE worker_pending_jobs ADD COLUMN IF NOT EXISTS product_mode TEXT NOT NULL DEFAULT 'per_frame';
