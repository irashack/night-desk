-- Frozen v0.3.0 schema and synthetic data. Do not regenerate from current migrations.
-- Only migrations 001 and 002 have been applied. Keys below are test-only hashes.
CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at TEXT NOT NULL);
CREATE TABLE agent_keys (
 id TEXT PRIMARY KEY,
 paper_id TEXT NOT NULL,
 hash BLOB NOT NULL UNIQUE CHECK(length(hash)=32),
 label TEXT NOT NULL,
 created_at TEXT NOT NULL,
 last_used_at TEXT,
 revoked_at TEXT
);
CREATE TABLE editions (
 paper_id TEXT NOT NULL,
 date TEXT NOT NULL,
 schema INTEGER NOT NULL,
 body TEXT NOT NULL CHECK(json_valid(body)),
 published_at TEXT NOT NULL,
 PRIMARY KEY(paper_id,date)
);
CREATE TABLE images (
 sha256 TEXT PRIMARY KEY,
 width INTEGER NOT NULL,
 height INTEGER NOT NULL,
 bytes INTEGER NOT NULL,
 created_at TEXT NOT NULL
);
CREATE TABLE edition_images (
 paper_id TEXT NOT NULL,
 date TEXT NOT NULL,
 item_id TEXT NOT NULL,
 image_sha256 TEXT NOT NULL REFERENCES images(sha256),
 PRIMARY KEY(paper_id,date,item_id),
 FOREIGN KEY(paper_id,date) REFERENCES editions(paper_id,date) ON DELETE CASCADE
);
CREATE TABLE feedback (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 paper_id TEXT NOT NULL,
 date TEXT NOT NULL,
 item_id TEXT NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('up','down','save','unsave')),
 reader_issuer TEXT NOT NULL,
 reader_subject TEXT NOT NULL,
 at TEXT NOT NULL
);
CREATE INDEX feedback_paper_cursor ON feedback(paper_id,id);
CREATE TABLE sessions (
 id_hash BLOB PRIMARY KEY CHECK(length(id_hash)=32),
 issuer TEXT NOT NULL,
 subject TEXT NOT NULL,
 created_at TEXT NOT NULL,
 expires_at TEXT NOT NULL,
 last_seen_at TEXT NOT NULL,
 csrf TEXT NOT NULL
);
INSERT INTO schema_migrations VALUES ('migrations/001_initial.sql','2026-10-29T00:00:00Z');
CREATE TABLE paper_editorial (
 paper_id TEXT NOT NULL,
 revision INTEGER NOT NULL CHECK(revision>0),
 body TEXT CHECK(body IS NULL OR json_valid(body)),
 saved_at TEXT NOT NULL,
 PRIMARY KEY(paper_id,revision)
);
INSERT INTO schema_migrations VALUES ('migrations/002_paper_editorial.sql','2026-10-29T00:00:00Z');
INSERT INTO agent_keys VALUES ('key-a','a',X'0a22103cf38cb334ad8575ed2f88ab593ddc3b311e796a240baf3f62543421a0','v0.3 fixture','2026-10-29T00:00:00Z',NULL,NULL);
INSERT INTO editions VALUES ('a','2026-10-27',1,'{"schema":1,"lead":"economy-a","sections":[{"key":"faith","items":[{"id":"faith-a","kind":"news","title":"Archived a faith 2026-10-27","summary":"Reporting preserved across upgrades.","source":"Fixture wire","url":"https://example.test/a/faith"}]},{"key":"economy","items":[{"id":"economy-a","kind":"news","title":"Archived a economy 2026-10-27","summary":"Reporting preserved across upgrades.","source":"Fixture wire","url":"https://example.test/a/economy"}]}],"motto":{"kind":"quotation","text":"Keep the record.","source":"Fixture editor"}}','2026-10-29T00:00:00Z');
INSERT INTO editions VALUES ('a','2026-10-28',1,'{"schema":1,"lead":"economy-a","sections":[{"key":"faith","items":[{"id":"faith-a","kind":"news","title":"Archived a faith 2026-10-28","summary":"Reporting preserved across upgrades.","source":"Fixture wire","url":"https://example.test/a/faith"}]},{"key":"economy","items":[{"id":"economy-a","kind":"news","title":"Archived a economy 2026-10-28","summary":"Reporting preserved across upgrades.","source":"Fixture wire","url":"https://example.test/a/economy"}]}],"motto":{"kind":"quotation","text":"Keep the record.","source":"Fixture editor"}}','2026-10-29T00:00:00Z');
INSERT INTO images VALUES ('1111111111111111111111111111111111111111111111111111111111111111',640,480,1234,'2026-10-29T00:00:00Z');
INSERT INTO edition_images VALUES ('a','2026-10-28','economy-a','1111111111111111111111111111111111111111111111111111111111111111');
INSERT INTO paper_editorial VALUES ('a',1,'{"masthead":"Archive A","sections":[{"key":"faith","title":"Faith archive","required":false},{"key":"economy","title":"Economy archive","required":false}],"boxes":{"weather":false,"sun":false,"solunar":false,"markets":false}}','2026-10-29T00:00:00Z');
INSERT INTO paper_editorial VALUES ('a',2,'{"masthead":"Current A","sections":[{"key":"economy","title":"Current economy","required":false}],"boxes":{"weather":false,"sun":false,"solunar":false,"markets":false}}','2026-10-29T00:00:00Z');
INSERT INTO agent_keys VALUES ('key-b','b',X'0fe022538b000e1b47eb2ab880a224f08810cdee09736cf2a6c377263e141190','v0.3 fixture','2026-10-29T00:00:00Z',NULL,NULL);
INSERT INTO editions VALUES ('b','2026-10-28',1,'{"schema":1,"lead":"economy-b","sections":[{"key":"faith","items":[{"id":"faith-b","kind":"news","title":"Archived b faith 2026-10-28","summary":"Reporting preserved across upgrades.","source":"Fixture wire","url":"https://example.test/b/faith"}]},{"key":"economy","items":[{"id":"economy-b","kind":"news","title":"Archived b economy 2026-10-28","summary":"Reporting preserved across upgrades.","source":"Fixture wire","url":"https://example.test/b/economy"}]}],"motto":{"kind":"quotation","text":"Keep the record.","source":"Fixture editor"}}','2026-10-29T00:00:00Z');
INSERT INTO images VALUES ('2222222222222222222222222222222222222222222222222222222222222222',640,480,1234,'2026-10-29T00:00:00Z');
INSERT INTO edition_images VALUES ('b','2026-10-28','economy-b','2222222222222222222222222222222222222222222222222222222222222222');
INSERT INTO paper_editorial VALUES ('b',1,'{"masthead":"Archive B","sections":[{"key":"faith","title":"Faith archive","required":false},{"key":"economy","title":"Economy archive","required":false}],"boxes":{"weather":false,"sun":false,"solunar":false,"markets":false}}','2026-10-29T00:00:00Z');
INSERT INTO feedback VALUES (7,'a','2026-10-28','economy-a','up','https://fixture.example','reader-a','2026-10-29T00:00:00Z');
INSERT INTO feedback VALUES (12,'b','2026-10-28','economy-b','save','https://fixture.example','reader-b','2026-10-29T00:00:00Z');
INSERT INTO feedback VALUES (19,'a','2026-10-28','economy-a','save','https://fixture.example','reader-a','2026-10-29T00:00:00Z');
