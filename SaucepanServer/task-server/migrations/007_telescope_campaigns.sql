-- Node campaign opt-in list
ALTER TABLE telescopes
  ADD COLUMN IF NOT EXISTS enabled_campaign_ids TEXT[] NOT NULL DEFAULT '{}';
