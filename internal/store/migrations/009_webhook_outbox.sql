CREATE TABLE events (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 id TEXT NOT NULL UNIQUE,
 paper TEXT NOT NULL,
 type TEXT NOT NULL,
 data TEXT NOT NULL CHECK(json_valid(data)),
 created_at TEXT NOT NULL,
 coalesce_minute INTEGER
);
CREATE INDEX events_paper_sequence ON events(paper, sequence);
CREATE UNIQUE INDEX events_feedback_minute ON events(paper, type, coalesce_minute) WHERE coalesce_minute IS NOT NULL;
CREATE TABLE deliveries (
 event_id TEXT NOT NULL REFERENCES events(id),
 webhook_url TEXT NOT NULL,
 attempts INTEGER NOT NULL DEFAULT 0,
 next_attempt_at INTEGER NOT NULL,
 delivered_at TEXT,
 last_status INTEGER,
 failed_at TEXT,
 PRIMARY KEY(event_id, webhook_url)
);
CREATE INDEX deliveries_due ON deliveries(next_attempt_at) WHERE delivered_at IS NULL AND failed_at IS NULL;
