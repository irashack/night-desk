-- Items the gathering worker dropped with an ingestion filter, counted per rule
-- and paper-local date. Counts only: never titles or URLs.
CREATE TABLE gather_drops (
 paper_id TEXT NOT NULL,
 date TEXT NOT NULL,
 rule TEXT NOT NULL,
 count INTEGER NOT NULL CHECK (count >= 0),
 PRIMARY KEY (paper_id, date, rule)
);
