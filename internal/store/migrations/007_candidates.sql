CREATE TABLE candidates (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 paper_id TEXT NOT NULL,
 section TEXT NOT NULL,
 canonical_url TEXT NOT NULL,
 aliases TEXT NOT NULL CHECK(json_valid(aliases) AND json_type(aliases)='array'),
 outlet TEXT NOT NULL,
 title TEXT NOT NULL,
 published_at TEXT,
 fetched_at TEXT NOT NULL,
 byline TEXT NOT NULL CHECK(json_valid(byline) AND json_type(byline)='array'),
 text TEXT NOT NULL CHECK(length(CAST(text AS BLOB))<=204800),
 state TEXT NOT NULL CHECK(state IN ('ok','unavailable')),
 wire_group TEXT,
 repeat INTEGER NOT NULL DEFAULT 0 CHECK(repeat IN (0,1)),
 UNIQUE(paper_id,canonical_url)
);
CREATE INDEX candidates_paper_section_fetched ON candidates(paper_id,section,fetched_at);
CREATE INDEX candidates_paper_fetched ON candidates(paper_id,fetched_at);
