CREATE TABLE edition_items (
 paper_id TEXT NOT NULL,
 date TEXT NOT NULL,
 item_id TEXT NOT NULL,
 url TEXT NOT NULL,
 section TEXT NOT NULL,
 PRIMARY KEY(paper_id,date,item_id),
 FOREIGN KEY(paper_id,date) REFERENCES editions(paper_id,date) ON DELETE CASCADE
);

INSERT INTO edition_items(paper_id,date,item_id,url,section)
SELECT e.paper_id,e.date,json_extract(i.value,'$.id'),
       json_extract(i.value,'$.url'),json_extract(s.value,'$.key')
FROM editions AS e, json_each(e.body,'$.sections') AS s,
     json_each(s.value,'$.items') AS i;
