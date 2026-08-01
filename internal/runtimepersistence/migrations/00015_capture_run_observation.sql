-- +goose Up
-- Launching a child proves only that a child was launched. A run that never
-- carried authenticated traffic is waiting for it, not captured, and design 02
-- requires the product to say so. Existing rows keep the honest default: this
-- runtime never observed them, because it had no way to record that it had.
ALTER TABLE capture_runs
    ADD COLUMN observation TEXT NOT NULL DEFAULT 'waiting_for_traffic'
        CHECK (observation IN ('waiting_for_traffic', 'observed'));

ALTER TABLE capture_runs
    ADD COLUMN first_observed_at_unix_ms INTEGER;
