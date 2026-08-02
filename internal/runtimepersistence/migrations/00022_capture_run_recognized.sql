-- +goose Up
-- A run can now be recognized: no catalogued build matched it, but the
-- platform confirmed a catalogued publisher signed it and it was not modified.
-- The previous column admitted only the three states a digest catalog can
-- produce, and a catalog frozen at release cannot name every build a user base
-- runs.
--
-- SQLite cannot alter a CHECK constraint in place, so the column is rebuilt.
-- No row changes value: every existing recognition is still one of the three
-- the old constraint allowed.
ALTER TABLE capture_runs RENAME COLUMN recognition TO recognition_old;

ALTER TABLE capture_runs
    ADD COLUMN recognition TEXT NOT NULL DEFAULT 'unknown'
        CHECK (recognition IN
            ('unknown', 'unverified', 'recognized', 'verified'));

UPDATE capture_runs SET recognition = recognition_old;

ALTER TABLE capture_runs DROP COLUMN recognition_old;
