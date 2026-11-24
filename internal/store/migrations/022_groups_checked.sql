-- Whether a session's sign-in asked for the groups claim. Sessions from before
-- group readers (or from a configuration without them) never did, so their
-- empty groups say nothing about membership: a group paper asks them to sign in
-- again instead of answering 404.
ALTER TABLE sessions ADD COLUMN groups_checked INTEGER NOT NULL DEFAULT 0;
