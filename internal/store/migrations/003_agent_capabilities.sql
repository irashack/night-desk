ALTER TABLE agent_keys ADD COLUMN capabilities TEXT NOT NULL DEFAULT 'read,publish,edit,review';
