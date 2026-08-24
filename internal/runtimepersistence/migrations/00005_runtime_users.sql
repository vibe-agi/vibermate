-- +goose Up
CREATE TABLE runtime_users(
  user_id TEXT PRIMARY KEY NOT NULL
  CHECK(length(CAST(user_id AS BLOB)) BETWEEN 1 AND 128),
  username TEXT NOT NULL UNIQUE
  CHECK(length(CAST(username AS BLOB)) BETWEEN 3 AND 64),
  password_hash TEXT NOT NULL
  CHECK(length(CAST(password_hash AS BLOB)) BETWEEN 64 AND 512),
  state TEXT NOT NULL
  CHECK(state IN('active', 'disabled')),
  created_at_unix_ms INTEGER NOT NULL,
  updated_at_unix_ms INTEGER NOT NULL,
  CHECK(updated_at_unix_ms >= created_at_unix_ms)
) STRICT;
CREATE INDEX runtime_users_state_username
ON runtime_users(state, username);

CREATE TABLE runtime_user_login_sessions(
  session_id TEXT PRIMARY KEY NOT NULL
  CHECK(length(CAST(session_id AS BLOB)) BETWEEN 1 AND 128),
  user_id TEXT NOT NULL
  REFERENCES runtime_users(user_id),
  token_digest BLOB NOT NULL UNIQUE
  CHECK(length(token_digest) = 32),
  machine_id TEXT NOT NULL
  CHECK(length(CAST(machine_id AS BLOB)) BETWEEN 1 AND 128),
  device_name TEXT NOT NULL
  CHECK(length(CAST(device_name AS BLOB)) BETWEEN 1 AND 128),
  created_at_unix_ms INTEGER NOT NULL,
  expires_at_unix_ms INTEGER NOT NULL,
  revoked_at_unix_ms INTEGER,
  CHECK(expires_at_unix_ms > created_at_unix_ms),
  CHECK(revoked_at_unix_ms IS NULL OR revoked_at_unix_ms >= created_at_unix_ms)
) STRICT;
CREATE INDEX runtime_user_login_sessions_user_expiry
ON runtime_user_login_sessions(user_id, expires_at_unix_ms DESC);

ALTER TABLE capture_runs ADD COLUMN runtime_user_id TEXT
REFERENCES runtime_users(user_id)
CHECK(runtime_user_id IS NULL OR
  length(CAST(runtime_user_id AS BLOB)) BETWEEN 1 AND 128);
ALTER TABLE capture_runs ADD COLUMN login_session_id TEXT
REFERENCES runtime_user_login_sessions(session_id)
CHECK(login_session_id IS NULL OR
  length(CAST(login_session_id AS BLOB)) BETWEEN 1 AND 128);
CREATE INDEX capture_runs_runtime_user_updated
ON capture_runs(runtime_user_id, updated_at_unix_ms DESC)
WHERE runtime_user_id IS NOT NULL;

-- +goose Down
DROP INDEX capture_runs_runtime_user_updated;
ALTER TABLE capture_runs DROP COLUMN login_session_id;
ALTER TABLE capture_runs DROP COLUMN runtime_user_id;
DROP TABLE runtime_user_login_sessions;
DROP TABLE runtime_users;
