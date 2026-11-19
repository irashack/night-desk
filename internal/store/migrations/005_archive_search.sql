CREATE VIRTUAL TABLE archive_items USING fts5(
 paper_id UNINDEXED,
 date UNINDEXED,
 item_id UNINDEXED,
 title,
 summary,
 why,
 source
);

INSERT INTO archive_items(paper_id,date,item_id,title,summary,why,source)
SELECT e.paper_id,e.date,json_extract(i.value,'$.id'),
       json_extract(i.value,'$.title'),json_extract(i.value,'$.summary'),
       coalesce(json_extract(i.value,'$.why'),''),json_extract(i.value,'$.source')
FROM editions AS e, json_each(e.body,'$.sections') AS s,
     json_each(s.value,'$.items') AS i;

CREATE INDEX feedback_reader_saves ON feedback(
 paper_id,reader_issuer,reader_subject,date,item_id,id DESC
) WHERE kind IN ('save','unsave');
