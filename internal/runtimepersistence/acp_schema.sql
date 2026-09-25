-- Additive extension: never change the released schema.sql digest for ACP.
CREATE TABLE acp_schema_metadata (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    revision INTEGER NOT NULL CHECK (revision = 1),
    source_sha256 TEXT NOT NULL
);
CREATE TABLE acp_observations (
    run_id TEXT PRIMARY KEY REFERENCES capture_runs(run_id) ON DELETE CASCADE,
    policy TEXT NOT NULL,
    expires_at_unix_ms INTEGER NOT NULL,
    snapshot BLOB,
    revision INTEGER NOT NULL CHECK (revision > 0),
    final INTEGER NOT NULL CHECK (final IN (0, 1))
);
CREATE INDEX acp_observation_expiry ON acp_observations(expires_at_unix_ms);
