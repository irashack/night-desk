-- Retain merged ids so delayed ingestion chunks still join the current group.
-- Aliases point directly to the canonical group and survive candidate pruning.
CREATE TABLE candidate_group_aliases (
 paper_id TEXT NOT NULL,
 alias_id TEXT NOT NULL CHECK(length(alias_id)>0),
 group_id TEXT NOT NULL CHECK(length(group_id)>0 AND group_id<>alias_id),
 PRIMARY KEY(paper_id, alias_id)
);
CREATE INDEX candidate_group_aliases_paper_group ON candidate_group_aliases(paper_id, group_id);
