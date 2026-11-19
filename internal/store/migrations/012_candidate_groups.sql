-- Grouping is mutable provenance, separate from immutable fetched content.
CREATE UNIQUE INDEX candidates_paper_id ON candidates(paper_id, id);
CREATE TABLE candidate_groups (
 paper_id TEXT NOT NULL,
 candidate_id INTEGER NOT NULL,
 group_id TEXT NOT NULL CHECK(length(group_id)>0),
 PRIMARY KEY(paper_id, candidate_id),
 FOREIGN KEY(paper_id, candidate_id) REFERENCES candidates(paper_id, id) ON DELETE CASCADE
);
CREATE INDEX candidate_groups_paper_group ON candidate_groups(paper_id, group_id);
INSERT INTO candidate_groups(paper_id, candidate_id, group_id)
 SELECT paper_id, id, wire_group FROM candidates WHERE wire_group IS NOT NULL AND wire_group<>'';
