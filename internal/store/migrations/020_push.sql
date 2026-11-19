-- Web push: one row per reader device that opted in. Only the push service's
-- endpoint is kept (pushes carry no payload, so no encryption keys are needed);
-- the session that subscribed it lets sign-out remove it.
CREATE TABLE push_subscriptions (
 id INTEGER PRIMARY KEY,
 paper_id TEXT NOT NULL,
 reader_issuer TEXT NOT NULL,
 reader_subject TEXT NOT NULL,
 session_hash BLOB NOT NULL CHECK(length(session_hash)=32),
 endpoint TEXT NOT NULL,
 expires_at TEXT,
 created_at TEXT NOT NULL,
 UNIQUE (paper_id, reader_issuer, reader_subject, endpoint)
);
CREATE INDEX push_subscriptions_session ON push_subscriptions(session_hash);
-- Dates already announced (or deliberately not announced), so a replaced
-- edition never pushes twice. Existing editions count as announced.
CREATE TABLE push_editions (
 paper_id TEXT NOT NULL,
 date TEXT NOT NULL,
 created_at TEXT NOT NULL,
 PRIMARY KEY (paper_id, date)
);
INSERT INTO push_editions(paper_id,date,created_at) SELECT paper_id,date,published_at FROM editions;
-- The push outbox: one pending notification per subscription and date.
CREATE TABLE push_deliveries (
 subscription_id INTEGER NOT NULL REFERENCES push_subscriptions(id) ON DELETE CASCADE,
 date TEXT NOT NULL,
 attempts INTEGER NOT NULL DEFAULT 0,
 next_attempt_at INTEGER NOT NULL,
 PRIMARY KEY (subscription_id, date)
);
CREATE INDEX push_deliveries_due ON push_deliveries(next_attempt_at);
