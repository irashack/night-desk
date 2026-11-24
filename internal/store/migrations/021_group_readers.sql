-- Group readers: the groups claim a session signed in with (a JSON array of
-- strings), kept on push subscriptions too so delivery can recheck readership
-- by group. Sign-in refreshes the subscriptions' copy.
ALTER TABLE sessions ADD COLUMN groups TEXT NOT NULL DEFAULT '[]';
ALTER TABLE push_subscriptions ADD COLUMN reader_groups TEXT NOT NULL DEFAULT '[]';
