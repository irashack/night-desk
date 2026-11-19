-- Feeds found for sections that find their own sources: verified by the worker
-- before they are stored. Discovery attempts are rate limited per section.
CREATE TABLE section_sources (
 paper_id TEXT NOT NULL,
 section_key TEXT NOT NULL,
 feed TEXT NOT NULL,
 outlet TEXT NOT NULL,
 added_at TEXT NOT NULL,
 PRIMARY KEY (paper_id, section_key, feed)
);
CREATE TABLE section_discovery (
 paper_id TEXT NOT NULL,
 section_key TEXT NOT NULL,
 attempted_at TEXT NOT NULL,
 PRIMARY KEY (paper_id, section_key)
);
