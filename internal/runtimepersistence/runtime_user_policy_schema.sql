CREATE TABLE runtime_user_policy_schema_metadata(
  singleton INTEGER PRIMARY KEY NOT NULL CHECK(singleton = 1),
  revision INTEGER NOT NULL CHECK(revision = 1),
  source_sha256 TEXT NOT NULL CHECK(length(source_sha256) = 64)
) STRICT;

CREATE TABLE runtime_user_policies(
  user_id TEXT PRIMARY KEY NOT NULL REFERENCES runtime_users(user_id) ON DELETE CASCADE,
  allowed_environment_ids_json BLOB NOT NULL
  CHECK(length(allowed_environment_ids_json) BETWEEN 2 AND 65536 AND
        json_valid(allowed_environment_ids_json) AND
        json_type(allowed_environment_ids_json) = 'array'),
  daily_agent_api_call_warning INTEGER NOT NULL DEFAULT 0
  CHECK(daily_agent_api_call_warning BETWEEN 0 AND 1000000000000000),
  daily_token_warning INTEGER NOT NULL DEFAULT 0
  CHECK(daily_token_warning BETWEEN 0 AND 1000000000000000)
) STRICT;
