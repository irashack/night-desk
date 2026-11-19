-- Operational records contain validation paths/rules and check counts, never bodies.
CREATE TABLE publish_diagnostics (
 id INTEGER PRIMARY KEY,
 paper_id TEXT NOT NULL,
 date TEXT NOT NULL,
 path TEXT NOT NULL CHECK(path IN ('put','draft','deadline')),
 published INTEGER NOT NULL CHECK(published IN (0,1)),
 warnings TEXT NOT NULL CHECK(json_valid(warnings)),
 errors TEXT NOT NULL CHECK(json_valid(errors)),
 source_checks TEXT NOT NULL CHECK(json_valid(source_checks)),
 recorded_at TEXT NOT NULL
);
CREATE INDEX publish_diagnostics_window ON publish_diagnostics(paper_id,date);
CREATE INDEX publish_diagnostics_retention ON publish_diagnostics(paper_id,recorded_at);
