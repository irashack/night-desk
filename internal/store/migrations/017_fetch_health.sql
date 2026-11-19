-- Article fetch outcomes the gathering worker reports, per host and paper-local
-- date. Counts and host names only: never URLs or article text.
CREATE TABLE fetch_health (
 paper_id TEXT NOT NULL,
 date TEXT NOT NULL,
 host TEXT NOT NULL,
 ok INTEGER NOT NULL DEFAULT 0,
 failed_http_4xx INTEGER NOT NULL DEFAULT 0,
 failed_http_429 INTEGER NOT NULL DEFAULT 0,
 failed_http_5xx INTEGER NOT NULL DEFAULT 0,
 failed_network INTEGER NOT NULL DEFAULT 0,
 failed_too_large INTEGER NOT NULL DEFAULT 0,
 cooled_off INTEGER NOT NULL DEFAULT 0,
 readability INTEGER NOT NULL DEFAULT 0,
 json_ld INTEGER NOT NULL DEFAULT 0,
 amp INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY (paper_id, date, host)
);
