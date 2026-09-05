-- Campaign board signal identity (#545): independent opaque messages need a
-- stable deduplication key in the collector bridge. source_sent_at remains
-- useful metadata, but timestamps alone can collapse two fast messages.

ALTER TABLE campaign_board_notes
  ADD COLUMN IF NOT EXISTS source_message_id TEXT;

UPDATE campaign_board_notes
SET source_message_id = source_sent_at::text
WHERE source_message_id IS NULL AND source_sent_at IS NOT NULL;

DROP INDEX IF EXISTS ux_campaign_board_notes_bridge;

CREATE UNIQUE INDEX IF NOT EXISTS ux_campaign_board_notes_message_id
  ON campaign_board_notes (campaign_id, author, source_message_id)
  WHERE source_message_id IS NOT NULL;
