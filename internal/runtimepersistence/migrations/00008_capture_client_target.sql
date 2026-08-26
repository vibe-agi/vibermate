-- +goose Up
-- A verified Agent's explicit Base URL is non-secret Capture authority. It is
-- separate from the canonical Client Flow that supplies the protocol contract.
ALTER TABLE capture_environment_assignments
ADD COLUMN client_target_origin TEXT NOT NULL DEFAULT ''
CHECK(length(CAST(client_target_origin AS BLOB)) <= 2048);

ALTER TABLE capture_environment_assignments
ADD COLUMN client_target_canonical_origin TEXT NOT NULL DEFAULT ''
CHECK(length(CAST(client_target_canonical_origin AS BLOB)) <= 2048);
