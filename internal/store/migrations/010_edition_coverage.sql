ALTER TABLE editions ADD COLUMN coverage TEXT CHECK (coverage IS NULL OR json_valid(coverage));
