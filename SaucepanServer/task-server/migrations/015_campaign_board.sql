-- Campaign board (#179 / #331-C2): the researcher-facing side of the campaign
-- messageboard. Piers coordinate over the retained MQTT board
-- (/board/campaign/{id}/{node}, #463/#470); the SDK holds no MQTT credential,
-- so a researcher reads and posts over HTTP against this append log instead.
-- Pier notes are mirrored here by the collector bridge in migration 016;
-- researcher HTTP posts are fanned out to MQTT by the apiserver when its
-- campaign-board broker publisher is configured.

CREATE TABLE IF NOT EXISTS campaign_board_notes (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  campaign_id UUID NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
  author      TEXT NOT NULL,              -- "researcher" or a node_id
  event_type  TEXT NOT NULL DEFAULT 'note',
  message     TEXT NOT NULL DEFAULT '',
  payload     JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS ix_campaign_board_notes_poll
  ON campaign_board_notes (campaign_id, created_at, id);
