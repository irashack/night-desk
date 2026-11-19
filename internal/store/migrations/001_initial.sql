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
