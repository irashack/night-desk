-- Reader API tokens are sessions too: a native app's sign-in names the app
-- and device it was granted to (NULL for a browser's session), so a reader
-- can tell them apart and sign one out.
ALTER TABLE sessions ADD COLUMN client TEXT;
