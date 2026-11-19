CREATE TABLE paper_editorial (
 paper_id TEXT NOT NULL,
 revision INTEGER NOT NULL CHECK(revision>0),
 body TEXT CHECK(body IS NULL OR json_valid(body)),
 saved_at TEXT NOT NULL,
 PRIMARY KEY(paper_id,revision)
);
