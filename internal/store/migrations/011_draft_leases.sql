ALTER TABLE runs ADD COLUMN draft_max_calls INTEGER NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN draft_max_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN draft_calls INTEGER NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN draft_tokens INTEGER NOT NULL DEFAULT 0;
CREATE TABLE draft_run_leases (
 id TEXT PRIMARY KEY,
 paper_id TEXT NOT NULL,
 date TEXT NOT NULL,
 expires_at INTEGER NOT NULL,
 reserved_calls INTEGER NOT NULL,
 reserved_tokens INTEGER NOT NULL,
 outcome TEXT,
 calls INTEGER,
 tokens INTEGER,
 revision INTEGER NOT NULL DEFAULT 0,
 FOREIGN KEY(paper_id,date) REFERENCES runs(paper_id,date)
);
CREATE INDEX draft_run_leases_date ON draft_run_leases(paper_id,date,expires_at);
