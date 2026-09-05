-- Reference release wiring: durable R2 upload sessions, worker pull queue,
-- and the campaign product mode needed to distinguish frames from stacks.
-- Idempotent so it can be replayed after init.sql.

ALTER TABLE tasks
    ADD COLUMN IF NOT EXISTS product_mode TEXT NOT NULL DEFAULT 'per_frame';
ALTER TABLE tasks
    ADD COLUMN IF NOT EXISTS target_magnitude DOUBLE PRECISION;
ALTER TABLE telescopes
    ADD COLUMN IF NOT EXISTS limiting_magnitude DOUBLE PRECISION;

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

UPDATE tasks
SET product_mode = 'per_frame'
WHERE product_mode IS NULL OR product_mode NOT IN ('per_frame', 'time_bin', 'stack');

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'tasks_product_mode_check'
    ) THEN
        ALTER TABLE tasks
            ADD CONSTRAINT tasks_product_mode_check
            CHECK (product_mode IN ('per_frame', 'time_bin', 'stack'));
    END IF;
END $$;

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

ALTER TABLE upload_sessions ADD COLUMN IF NOT EXISTS grade_meta JSONB NOT NULL DEFAULT '{}';
ALTER TABLE upload_sessions ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;
ALTER TABLE upload_sessions ADD COLUMN IF NOT EXISTS completed_at TIMESTAMPTZ;

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

ALTER TABLE worker_pending_jobs ADD COLUMN IF NOT EXISTS telescope_id TEXT;
ALTER TABLE worker_pending_jobs ADD COLUMN IF NOT EXISTS object_keys JSONB NOT NULL DEFAULT '[]';
ALTER TABLE worker_pending_jobs ADD COLUMN IF NOT EXISTS product_mode TEXT NOT NULL DEFAULT 'per_frame';
ALTER TABLE worker_pending_jobs ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'pending';
