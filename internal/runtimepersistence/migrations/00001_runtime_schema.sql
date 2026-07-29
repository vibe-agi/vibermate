-- +goose Up
CREATE TABLE runtime_metadata (
    singleton INTEGER PRIMARY KEY NOT NULL CHECK (singleton = 1),
    initialized_at TEXT NOT NULL CHECK (length(initialized_at) > 0)
) STRICT;

INSERT INTO runtime_metadata (singleton, initialized_at)
VALUES (1, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
