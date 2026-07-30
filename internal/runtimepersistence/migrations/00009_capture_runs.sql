-- +goose Up
CREATE TABLE capture_runs (
    run_id TEXT PRIMARY KEY NOT NULL
        CHECK (length(CAST(run_id AS BLOB)) BETWEEN 1 AND 128),
    proxy_capability_hash BLOB NOT NULL UNIQUE
        CHECK (length(proxy_capability_hash) = 32),
    control_capability_hash BLOB NOT NULL UNIQUE
        CHECK (length(control_capability_hash) = 32),
    cwd TEXT NOT NULL
        CHECK (length(CAST(cwd AS BLOB)) BETWEEN 1 AND 4096),
    executable_label TEXT NOT NULL
        CHECK (length(CAST(executable_label AS BLOB)) BETWEEN 1 AND 256),
    process_id INTEGER NOT NULL DEFAULT 0
        CHECK (process_id >= 0),
    state TEXT NOT NULL
        CHECK (state IN ('created', 'attached', 'finished', 'revoked', 'expired')),
    created_at_unix_ms INTEGER NOT NULL,
    expires_at_unix_ms INTEGER NOT NULL,
    updated_at_unix_ms INTEGER NOT NULL,
    CHECK (expires_at_unix_ms >= created_at_unix_ms),
    CHECK (updated_at_unix_ms >= created_at_unix_ms),
    CHECK (
        (state = 'created' AND process_id = 0) OR
        (state = 'attached' AND process_id > 0) OR
        state IN ('finished', 'revoked', 'expired')
    )
) STRICT;

CREATE INDEX capture_runs_active_expiry
    ON capture_runs (state, expires_at_unix_ms);
