-- +goose Up
-- A run recorded whether its client verified, but not whether the catalog
-- knows that client at all. The two are different facts and only one of them
-- can be recovered from the adapter evidence: a run with no evidence might be
-- a program nobody catalogued, or a catalogued client at a version this build
-- has none for. The second is a client that worked yesterday and stopped after
-- an update, and it is the one a person needs told to them.
ALTER TABLE capture_runs
    ADD COLUMN recognition TEXT NOT NULL DEFAULT 'unknown'
        CHECK (recognition IN ('unknown', 'unverified', 'verified'));

-- Existing rows carrying adapter evidence were verified by definition.
UPDATE capture_runs
   SET recognition = 'verified'
 WHERE adapter_id <> '';
