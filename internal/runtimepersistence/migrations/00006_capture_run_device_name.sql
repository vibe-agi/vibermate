-- +goose Up
ALTER TABLE capture_runs ADD COLUMN device_name TEXT
CHECK(device_name IS NULL OR
  length(CAST(device_name AS BLOB)) BETWEEN 1 AND 128);
UPDATE capture_runs
SET device_name = (
  SELECT sessions.device_name
  FROM runtime_user_login_sessions AS sessions
  WHERE sessions.session_id = capture_runs.login_session_id
)
WHERE login_session_id IS NOT NULL;

-- +goose Down
ALTER TABLE capture_runs DROP COLUMN device_name;
