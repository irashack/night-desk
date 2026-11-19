-- Estimated drafting cost in millionths of a US dollar, from the worker's
-- configured tier prices. Zero when no prices are configured.
ALTER TABLE runs ADD COLUMN draft_cost_micros INTEGER NOT NULL DEFAULT 0;
ALTER TABLE draft_run_leases ADD COLUMN cost_micros INTEGER NOT NULL DEFAULT 0;
