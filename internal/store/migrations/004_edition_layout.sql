ALTER TABLE editions ADD COLUMN layout TEXT CHECK(layout IS NULL OR json_valid(layout));
