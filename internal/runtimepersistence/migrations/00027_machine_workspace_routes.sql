-- +goose Up
-- CaptureRun rows minted before machine-scoped workspace identity keep empty
-- scope fields. Their short-lived capabilities retain the historical default
-- route behaviour, but they can never resolve a workspace-specific binding.
ALTER TABLE capture_runs
    ADD COLUMN local_user_label TEXT NOT NULL DEFAULT ''
        CHECK (length(CAST(local_user_label AS BLOB)) <= 128);

ALTER TABLE capture_runs
    ADD COLUMN machine_id TEXT NOT NULL DEFAULT ''
        CHECK (length(CAST(machine_id AS BLOB)) <= 128);

ALTER TABLE capture_runs
    ADD COLUMN machine_registration_revision INTEGER NOT NULL DEFAULT 0
        CHECK (machine_registration_revision BETWEEN 0 AND 9223372036854775807);

ALTER TABLE capture_runs
    ADD COLUMN workspace_id TEXT NOT NULL DEFAULT ''
        CHECK (length(CAST(workspace_id AS BLOB)) <= 128);

ALTER TABLE capture_runs
    ADD COLUMN workspace_label TEXT NOT NULL DEFAULT ''
        CHECK (length(CAST(workspace_label AS BLOB)) <= 120);

ALTER TABLE capture_runs
    ADD COLUMN workspace_evidence TEXT NOT NULL DEFAULT ''
        CHECK (workspace_evidence IN
            ('', 'local_launcher', 'registered_companion'));

ALTER TABLE capture_runs
    ADD COLUMN workspace_derivation_revision INTEGER NOT NULL DEFAULT 0
        CHECK (workspace_derivation_revision BETWEEN 0 AND 9223372036854775807);

CREATE INDEX capture_runs_workspace_active
    ON capture_runs (machine_id, workspace_id, state, updated_at_unix_ms DESC);

CREATE TABLE workspace_route_bindings (
    binding_id TEXT PRIMARY KEY NOT NULL
        CHECK (length(CAST(binding_id AS BLOB)) BETWEEN 1 AND 128),
    access_id TEXT NOT NULL,
    machine_id TEXT NOT NULL
        CHECK (length(CAST(machine_id AS BLOB)) BETWEEN 1 AND 128),
    workspace_id TEXT NOT NULL
        CHECK (length(CAST(workspace_id AS BLOB)) BETWEEN 1 AND 128),
	 machine_registration_revision INTEGER NOT NULL
	     CHECK (machine_registration_revision BETWEEN 1 AND 9223372036854775807),
	 workspace_label TEXT NOT NULL
	     CHECK (length(CAST(workspace_label AS BLOB)) BETWEEN 1 AND 120),
	 workspace_evidence TEXT NOT NULL
	     CHECK (workspace_evidence IN ('local_launcher', 'registered_companion')),
    profile_id TEXT NOT NULL,
    revision INTEGER NOT NULL
        CHECK (revision BETWEEN 1 AND 9223372036854775807),
    updated_at_unix_ms INTEGER NOT NULL,
    UNIQUE (access_id, machine_id, workspace_id)
) STRICT;

CREATE INDEX workspace_route_bindings_updated
    ON workspace_route_bindings (updated_at_unix_ms DESC, binding_id ASC);
