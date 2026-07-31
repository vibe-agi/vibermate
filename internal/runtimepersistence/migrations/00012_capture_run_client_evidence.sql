-- +goose Up
DROP INDEX capture_runs_active_expiry;

ALTER TABLE capture_runs RENAME TO capture_runs_revision_11;

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
    client_catalog_revision INTEGER NOT NULL
        CHECK (client_catalog_revision BETWEEN 1 AND 9223372036854775807),
    adapter_id TEXT NOT NULL DEFAULT ''
        CHECK (length(CAST(adapter_id AS BLOB)) <= 128),
    adapter_revision INTEGER NOT NULL DEFAULT 0
        CHECK (adapter_revision BETWEEN 0 AND 9223372036854775807),
    adapter_version TEXT NOT NULL DEFAULT ''
        CHECK (length(CAST(adapter_version AS BLOB)) <= 128),
    adapter_install_shape TEXT NOT NULL DEFAULT ''
        CHECK (length(CAST(adapter_install_shape AS BLOB)) <= 64),
    adapter_release_sha256 BLOB NOT NULL DEFAULT X''
        CHECK (length(adapter_release_sha256) IN (0, 32)),
    adapter_launch_recipe TEXT NOT NULL DEFAULT ''
        CHECK (length(CAST(adapter_launch_recipe AS BLOB)) <= 64),
    adapter_features INTEGER NOT NULL DEFAULT 0
        CHECK (adapter_features BETWEEN 0 AND 9223372036854775807),
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
        (
            adapter_id = ''
            AND adapter_revision = 0
            AND adapter_version = ''
            AND adapter_install_shape = ''
            AND length(adapter_release_sha256) = 0
            AND adapter_launch_recipe = ''
            AND adapter_features = 0
        ) OR (
            length(CAST(adapter_id AS BLOB)) > 0
            AND adapter_revision > 0
            AND length(CAST(adapter_version AS BLOB)) > 0
            AND adapter_install_shape <> ''
            AND length(adapter_release_sha256) = 32
            AND adapter_launch_recipe <> ''
        )
    ),
    CHECK (
        (state = 'created' AND process_id = 0) OR
        (state = 'attached' AND process_id > 0) OR
        state IN ('finished', 'revoked', 'expired')
    )
) STRICT;

INSERT INTO capture_runs (
    run_id,
    proxy_capability_hash,
    control_capability_hash,
    cwd,
    executable_label,
    client_catalog_revision,
    process_id,
    state,
    created_at_unix_ms,
    expires_at_unix_ms,
    updated_at_unix_ms
)
SELECT
    run_id,
    proxy_capability_hash,
    control_capability_hash,
    cwd,
    executable_label,
    1,
    process_id,
    state,
    created_at_unix_ms,
    expires_at_unix_ms,
    updated_at_unix_ms
FROM capture_runs_revision_11;

DROP TABLE capture_runs_revision_11;

CREATE INDEX capture_runs_active_expiry
    ON capture_runs (state, expires_at_unix_ms);
