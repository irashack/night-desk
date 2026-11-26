-- The key behind feedback's opaque reader_id (#33). Agent keys see a stable
-- per-paper HMAC of the reader's issuer and subject, never the pair itself.
-- The key lives in the database so backups and restores keep reader ids stable.
CREATE TABLE reader_id_key (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	key BLOB NOT NULL CHECK (length(key) = 32)
);
INSERT INTO reader_id_key(id, key) VALUES (1, randomblob(32));
