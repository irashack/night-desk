-- Archive search over schema v2: bylines, a column's stance and the outlets
-- (and their writers) that also covered a story. The index is derived from
-- published editions, so it is rebuilt from them.
DROP TABLE archive_items;

CREATE VIRTUAL TABLE archive_items USING fts5(
 paper_id UNINDEXED,
 date UNINDEXED,
 item_id UNINDEXED,
 kind UNINDEXED,
 title,
 summary,
 why,
 source,
 byline,
 stance,
 outlets,
 outlet_bylines
);

INSERT INTO archive_items(paper_id,date,item_id,kind,title,summary,why,source,byline,stance,outlets,outlet_bylines)
SELECT e.paper_id,e.date,json_extract(i.value,'$.id'),coalesce(json_extract(i.value,'$.kind'),''),
       json_extract(i.value,'$.title'),json_extract(i.value,'$.summary'),
       coalesce(json_extract(i.value,'$.why'),''),json_extract(i.value,'$.source'),
       coalesce(json_extract(i.value,'$.byline'),''),coalesce(json_extract(i.value,'$.stance'),''),
       coalesce((SELECT group_concat(json_extract(o.value,'$.outlet'),char(10))
                 FROM json_each(i.value,'$.sources') AS o),''),
       coalesce((SELECT group_concat(json_extract(o.value,'$.byline'),char(10))
                 FROM json_each(i.value,'$.sources') AS o
                 WHERE json_extract(o.value,'$.byline') IS NOT NULL),'')
FROM editions AS e, json_each(e.body,'$.sections') AS s,
     json_each(s.value,'$.items') AS i;
