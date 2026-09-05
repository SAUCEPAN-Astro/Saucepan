-- Campaign board bridge (#470): mirror pier / on-pier-code notes from the
-- retained MQTT campaign board (/board/campaign/{id}/{node}) into the same
-- append log the researcher reads over HTTP (campaign_board_notes, #331-C2).
-- The collector subscribes to /board/campaign/+/+ and upserts each note here.
--
-- The MQTT board is a retained signal stream; this table is an append log.
-- source_sent_at is retained as sender timing metadata. Migration 020 adds
-- source_message_id as the primary deduplication key because two independent
-- fast signals can share a timestamp. NULL for researcher-authored notes,
-- which never enter this bridge.

ALTER TABLE campaign_board_notes
  ADD COLUMN IF NOT EXISTS source_sent_at TIMESTAMPTZ;

CREATE UNIQUE INDEX IF NOT EXISTS ux_campaign_board_notes_bridge
  ON campaign_board_notes (campaign_id, author, source_sent_at)
  WHERE source_sent_at IS NOT NULL;
