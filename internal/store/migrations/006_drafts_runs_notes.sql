CREATE TABLE drafts (
 paper_id TEXT NOT NULL,
 date TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('gathering','drafted','in_review','published','held')),
 revision INTEGER NOT NULL CHECK(revision>0),
 body TEXT CHECK(body IS NULL OR json_valid(body)),
 approved_revision INTEGER,
 unreviewed INTEGER NOT NULL DEFAULT 0 CHECK(unreviewed IN (0,1)),
 updated_at TEXT NOT NULL,
 PRIMARY KEY(paper_id,date)
);
CREATE TABLE draft_actions (
 paper_id TEXT NOT NULL,
 date TEXT NOT NULL,
 revision INTEGER NOT NULL,
 who TEXT NOT NULL,
 action TEXT NOT NULL,
 at TEXT NOT NULL,
 note TEXT NOT NULL,
 PRIMARY KEY(paper_id,date,revision),
 FOREIGN KEY(paper_id,date) REFERENCES drafts(paper_id,date)
);
CREATE TABLE runs (
 paper_id TEXT NOT NULL,
 date TEXT NOT NULL,
 state TEXT NOT NULL DEFAULT 'pending',
 attempts INTEGER NOT NULL DEFAULT 0,
 draft_attempts INTEGER NOT NULL DEFAULT 0,
 updated_at TEXT NOT NULL,
 PRIMARY KEY(paper_id,date)
);
CREATE TABLE edition_review (
 paper_id TEXT NOT NULL,
 date TEXT NOT NULL,
 unreviewed INTEGER NOT NULL CHECK(unreviewed IN (0,1)),
 PRIMARY KEY(paper_id,date),
 FOREIGN KEY(paper_id,date) REFERENCES editions(paper_id,date) ON DELETE CASCADE
);
CREATE TABLE paper_notes (
 paper_id TEXT NOT NULL,
 revision INTEGER NOT NULL CHECK(revision>0),
 text TEXT NOT NULL,
 saved_at TEXT NOT NULL,
 PRIMARY KEY(paper_id,revision)
);
