-- Unreleased control revision 3; later task amendments precede final freeze.
CREATE TABLE runtime_metadata(
  singleton INTEGER PRIMARY KEY NOT NULL CHECK(singleton = 1),
  schema_identity TEXT NOT NULL CHECK(schema_identity = 'vibermate-control'),
  schema_revision INTEGER NOT NULL CHECK(schema_revision >= 1),
  initialized_at TEXT NOT NULL CHECK(length(initialized_at) > 0)
) STRICT;

CREATE TABLE capture_environment_assignments(
  capture_kind TEXT NOT NULL
  CHECK(capture_kind IN('managed_run', 'manual_capture')),
  capture_id TEXT NOT NULL
  CHECK(length(CAST(capture_id AS BLOB)) BETWEEN 1 AND 128),
  environment_id TEXT NOT NULL
  CHECK(length(CAST(environment_id AS BLOB)) BETWEEN 1 AND 128),
  environment_revision INTEGER NOT NULL
  CHECK(environment_revision BETWEEN 1 AND 9223372036854775807),
  environment_digest BLOB NOT NULL
  CHECK(length(environment_digest) = 32),
  assignment_revision INTEGER NOT NULL
  CHECK(assignment_revision BETWEEN 1 AND 9223372036854775807),
  source TEXT NOT NULL
  CHECK(source IN('launch', 'manual_create', 'system_transparent')),
  launch_environment_id TEXT NOT NULL
  CHECK(length(CAST(launch_environment_id AS BLOB)) BETWEEN 1 AND 128),
  launch_environment_revision INTEGER NOT NULL
  CHECK(launch_environment_revision BETWEEN 1 AND 9223372036854775807),
  launch_environment_digest BLOB NOT NULL
  CHECK(length(launch_environment_digest) = 32),
  protected_authorities_json TEXT NOT NULL
  CHECK(json_valid(protected_authorities_json)
    AND json_type(protected_authorities_json) = 'array'),
  managed_authorities_json TEXT NOT NULL
  CHECK(json_valid(managed_authorities_json)
    AND json_type(managed_authorities_json) = 'array'),
  launch_authority_digest BLOB NOT NULL
  CHECK(length(launch_authority_digest) = 32),
  updated_at_unix_ms INTEGER NOT NULL, client_target_origin TEXT NOT NULL DEFAULT ''
CHECK(length(CAST(client_target_origin AS BLOB)) <= 2048), client_target_canonical_origin TEXT NOT NULL DEFAULT ''
CHECK(length(CAST(client_target_canonical_origin AS BLOB)) <= 2048),
  PRIMARY KEY(capture_kind, capture_id)
) STRICT;

CREATE TABLE capture_runs(
  run_id TEXT PRIMARY KEY NOT NULL
  CHECK(length(CAST(run_id AS BLOB)) BETWEEN 1 AND 128),
  proxy_capability_hash BLOB NOT NULL UNIQUE
  CHECK(length(proxy_capability_hash) = 32),
  control_capability_hash BLOB NOT NULL UNIQUE
  CHECK(length(control_capability_hash) = 32),
  cwd TEXT NOT NULL
  CHECK(length(CAST(cwd AS BLOB)) BETWEEN 1 AND 4096),
  canonical_executable_path TEXT NOT NULL
  CHECK(length(CAST(canonical_executable_path AS BLOB)) BETWEEN 1 AND 4096),
  executable_label TEXT NOT NULL
  CHECK(length(CAST(executable_label AS BLOB)) BETWEEN 1 AND 256),
  client_catalog_revision INTEGER NOT NULL
  CHECK(client_catalog_revision BETWEEN 1 AND 9223372036854775807),
  adapter_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(adapter_id AS BLOB)) <= 128),
  adapter_revision INTEGER NOT NULL DEFAULT 0
  CHECK(adapter_revision BETWEEN 0 AND 9223372036854775807),
  adapter_version TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(adapter_version AS BLOB)) <= 128),
  adapter_install_shape TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(adapter_install_shape AS BLOB)) <= 64),
  adapter_release_sha256 BLOB NOT NULL DEFAULT X''
  CHECK(length(adapter_release_sha256) IN(0, 32)),
  adapter_launch_recipe TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(adapter_launch_recipe AS BLOB)) <= 64),
  adapter_features INTEGER NOT NULL DEFAULT 0
  CHECK(adapter_features BETWEEN 0 AND 9223372036854775807),
  process_id INTEGER NOT NULL DEFAULT 0
  CHECK(process_id >= 0),
  state TEXT NOT NULL
  CHECK(state IN('created', 'attached', 'finished', 'revoked', 'expired')),
  created_at_unix_ms INTEGER NOT NULL,
  expires_at_unix_ms INTEGER NOT NULL,
  updated_at_unix_ms INTEGER NOT NULL,
  observation TEXT NOT NULL DEFAULT 'waiting_for_traffic'
  CHECK(observation IN('waiting_for_traffic', 'observed')),
  first_observed_at_unix_ms INTEGER,
  recognition TEXT NOT NULL DEFAULT 'unknown'
  CHECK(recognition IN('unknown', 'unverified', 'recognized', 'verified')),
  local_user_label TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(local_user_label AS BLOB)) <= 128),
  home_directory TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(home_directory AS BLOB)) <= 4096),
  operating_system TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(operating_system AS BLOB)) <= 64),
  operating_system_version TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(operating_system_version AS BLOB)) <= 256),
  architecture TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(architecture AS BLOB)) <= 64),
  time_zone TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(time_zone AS BLOB)) <= 128),
  git_json TEXT NOT NULL DEFAULT 'null'
  CHECK(json_valid(git_json) AND length(CAST(git_json AS BLOB)) <= 2048),
  machine_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(machine_id AS BLOB)) <= 128),
  machine_registration_revision INTEGER NOT NULL DEFAULT 0
  CHECK(machine_registration_revision BETWEEN 0 AND 9223372036854775807),
  workspace_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(workspace_id AS BLOB)) <= 128),
  workspace_label TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(workspace_label AS BLOB)) <= 120),
  workspace_evidence TEXT NOT NULL DEFAULT ''
  CHECK(workspace_evidence IN('', 'local_launcher', 'registered_companion')),
  workspace_derivation_revision INTEGER NOT NULL DEFAULT 0
  CHECK(workspace_derivation_revision BETWEEN 0 AND 9223372036854775807), runtime_user_id TEXT
REFERENCES runtime_users(user_id)
CHECK(runtime_user_id IS NULL OR
  length(CAST(runtime_user_id AS BLOB)) BETWEEN 1 AND 128), runtime_username TEXT
CHECK(runtime_username IS NULL OR
  length(CAST(runtime_username AS BLOB)) BETWEEN 3 AND 64), login_session_id TEXT
REFERENCES runtime_user_login_sessions(session_id)
CHECK(login_session_id IS NULL OR
  length(CAST(login_session_id AS BLOB)) BETWEEN 1 AND 128), device_name TEXT
CHECK(device_name IS NULL OR
  length(CAST(device_name AS BLOB)) BETWEEN 1 AND 128),
  CHECK((runtime_user_id IS NULL AND runtime_username IS NULL AND
    login_session_id IS NULL AND device_name IS NULL) OR
    (runtime_user_id IS NOT NULL AND runtime_username IS NOT NULL AND
    login_session_id IS NOT NULL AND device_name IS NOT NULL)),
  CHECK(expires_at_unix_ms >= created_at_unix_ms),
  CHECK(updated_at_unix_ms >= created_at_unix_ms),
  CHECK((adapter_id = ''
AND adapter_revision = 0
AND adapter_version = ''
AND adapter_install_shape = ''
AND length(adapter_release_sha256) = 0
AND adapter_launch_recipe = ''
AND adapter_features = 0) OR(length(CAST(adapter_id AS BLOB)) > 0
AND adapter_revision > 0
AND length(CAST(adapter_version AS BLOB)) > 0
AND adapter_install_shape <> ''
AND length(adapter_release_sha256) = 32
AND adapter_launch_recipe <> '')),
  CHECK((state = 'created' AND process_id = 0) OR(state = 'attached' AND process_id > 0) OR
state IN('finished', 'revoked', 'expired'))
) STRICT;

CREATE TABLE client_enrollments(
  enrollment_id TEXT PRIMARY KEY NOT NULL
  CHECK(length(CAST(enrollment_id AS BLOB)) BETWEEN 1 AND 128),
  binding_id TEXT NOT NULL
  REFERENCES proxy_client_bindings(binding_id),
  binding_revision INTEGER NOT NULL
  CHECK(binding_revision BETWEEN 1 AND 9223372036854775807),
  state TEXT NOT NULL
  CHECK(state IN('active', 'consumed', 'revoked', 'expired')),
  credential_digest BLOB NOT NULL UNIQUE
  CHECK(length(credential_digest) = 32),
  created_at_unix_ms INTEGER NOT NULL,
  expires_at_unix_ms INTEGER NOT NULL,
  updated_at_unix_ms INTEGER NOT NULL,
  consumed_at_unix_ms INTEGER,
  machine_registration_id TEXT
  REFERENCES machine_registrations(machine_registration_id),
  CHECK(expires_at_unix_ms > created_at_unix_ms),
  CHECK(updated_at_unix_ms >= created_at_unix_ms),
  CHECK((state = 'consumed'
    AND consumed_at_unix_ms IS NOT NULL
    AND machine_registration_id IS NOT NULL
    AND length(CAST(machine_registration_id AS BLOB)) BETWEEN 1 AND 128)
    OR(state <> 'consumed'
      AND consumed_at_unix_ms IS NULL
      AND machine_registration_id IS NULL))
) STRICT;

CREATE TABLE code_library_collections(
  collection_id TEXT PRIMARY KEY NOT NULL
  CHECK(length(CAST(collection_id AS BLOB)) BETWEEN 1 AND 128),
  display_name TEXT NOT NULL
  CHECK(length(CAST(display_name AS BLOB)) BETWEEN 1 AND 256)
) STRICT;

CREATE TABLE code_library_transform_heads(
  transform_id TEXT PRIMARY KEY NOT NULL
  CHECK(length(CAST(transform_id AS BLOB)) BETWEEN 1 AND 128),
  current_revision INTEGER NOT NULL
  CHECK(current_revision BETWEEN 0 AND 9223372036854775807)
) STRICT;

CREATE TABLE code_library_transform_revisions(
  transform_id TEXT NOT NULL
  REFERENCES code_library_transform_heads(transform_id),
  revision INTEGER NOT NULL
  CHECK(revision BETWEEN 1 AND 9223372036854775807),
  collection_id TEXT NOT NULL
  REFERENCES code_library_collections(collection_id),
  display_name TEXT NOT NULL
  CHECK(length(CAST(display_name AS BLOB)) BETWEEN 1 AND 256),
  request_javascript TEXT NOT NULL
  CHECK(length(CAST(request_javascript AS BLOB)) BETWEEN 0 AND 65536),
  response_javascript TEXT NOT NULL
  CHECK(length(CAST(response_javascript AS BLOB)) BETWEEN 0 AND 65536),
  published_at_unix_ms INTEGER NOT NULL,
  PRIMARY KEY(transform_id, revision)
) STRICT;

CREATE TABLE code_library_account_selector_heads(
  selector_id TEXT PRIMARY KEY NOT NULL
  CHECK(length(CAST(selector_id AS BLOB)) BETWEEN 1 AND 128),
  current_revision INTEGER NOT NULL
  CHECK(current_revision BETWEEN 0 AND 9223372036854775807)
) STRICT;

CREATE TABLE code_library_account_selector_revisions(
  selector_id TEXT NOT NULL
  REFERENCES code_library_account_selector_heads(selector_id),
  revision INTEGER NOT NULL
  CHECK(revision BETWEEN 1 AND 9223372036854775807),
  collection_id TEXT NOT NULL
  REFERENCES code_library_collections(collection_id),
  display_name TEXT NOT NULL
  CHECK(length(CAST(display_name AS BLOB)) BETWEEN 1 AND 256),
  javascript TEXT NOT NULL
  CHECK(length(CAST(javascript AS BLOB)) BETWEEN 1 AND 65536),
  published_at_unix_ms INTEGER NOT NULL,
  PRIMARY KEY(selector_id, revision)
) STRICT;

CREATE TABLE egress_profile_heads(
  egress_id TEXT PRIMARY KEY NOT NULL
  CHECK(length(CAST(egress_id AS BLOB)) BETWEEN 1 AND 128),
  current_revision INTEGER NOT NULL
  CHECK(current_revision BETWEEN 0 AND 9223372036854775807)
) STRICT;

CREATE TABLE egress_profile_revisions(
  egress_id TEXT NOT NULL
  REFERENCES egress_profile_heads(egress_id),
  revision INTEGER NOT NULL
  CHECK(revision BETWEEN 1 AND 9223372036854775807),
  display_name TEXT NOT NULL
  CHECK(length(CAST(display_name AS BLOB)) BETWEEN 1 AND 256),
  policy_json BLOB NOT NULL
  CHECK(length(policy_json) BETWEEN 2 AND 65536 AND json_valid(CAST(policy_json AS TEXT))),
  published_at_unix_ms INTEGER NOT NULL,
  PRIMARY KEY(egress_id, revision)
) STRICT;

CREATE TABLE connection_rule_sets(
  id INTEGER PRIMARY KEY NOT NULL CHECK(id = 1),
  revision INTEGER NOT NULL
  CHECK(revision BETWEEN 1 AND 9223372036854775807),
  mode TEXT NOT NULL
  CHECK(mode IN('monitor', 'ask_unknown', 'deny_unknown')),
  updated_at_unix_ms INTEGER NOT NULL
) STRICT;

CREATE TABLE connection_rules(
  rule_id TEXT PRIMARY KEY NOT NULL
  CHECK(length(CAST(rule_id AS BLOB)) BETWEEN 1 AND 256),
  priority INTEGER NOT NULL DEFAULT 0
  CHECK(priority BETWEEN 0 AND 4294967295),
  decision TEXT NOT NULL CHECK(decision IN('allow', 'deny', 'ask')),
  match_kind TEXT NOT NULL
  CHECK(match_kind IN('exact_host', 'exact_host_port')),
  match_host TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(match_host AS BLOB)) <= 253),
  match_port INTEGER NOT NULL DEFAULT 0
  CHECK(match_port BETWEEN 0 AND 65535),
  -- Design 06 closes the match language deliberately: a wildcard or regular
  -- expression is how an allow list quietly becomes an allow everything.
  CHECK((match_kind = 'exact_host' AND length(match_host) > 0 AND match_port = 0) OR(match_kind = 'exact_host_port' AND length(match_host) > 0 AND match_port > 0))
) STRICT;

CREATE TABLE enrolled_control_principals(
  principal_id TEXT PRIMARY KEY NOT NULL
  CHECK(length(CAST(principal_id AS BLOB)) BETWEEN 1 AND 128),
  binding_id TEXT NOT NULL
  REFERENCES proxy_client_bindings(binding_id),
  binding_revision INTEGER NOT NULL
  CHECK(binding_revision BETWEEN 1 AND 9223372036854775807),
  machine_registration_id TEXT NOT NULL UNIQUE
  REFERENCES machine_registrations(machine_registration_id),
  credential_revision INTEGER NOT NULL
  CHECK(credential_revision BETWEEN 1 AND 9223372036854775807),
  credential_digest BLOB NOT NULL UNIQUE
  CHECK(length(credential_digest) = 32),
  allowed_grant_kinds INTEGER NOT NULL
  CHECK(allowed_grant_kinds BETWEEN 1 AND 3),
  state TEXT NOT NULL
  CHECK(state IN('active', 'revoked')),
  created_at_unix_ms INTEGER NOT NULL,
  updated_at_unix_ms INTEGER NOT NULL,
  CHECK(updated_at_unix_ms >= created_at_unix_ms)
) STRICT;

CREATE TABLE environment_drafts(
  environment_id TEXT PRIMARY KEY NOT NULL
  REFERENCES environment_revision_counters(environment_id),
  base_revision INTEGER NOT NULL
  CHECK(base_revision BETWEEN 0 AND 9223372036854775807),
  draft_revision INTEGER NOT NULL
  CHECK(draft_revision BETWEEN 1 AND 9223372036854775807),
  candidate_revision INTEGER NOT NULL
  CHECK(candidate_revision BETWEEN 1 AND 9223372036854775807),
  format_version INTEGER NOT NULL
  CHECK(format_version = 1),
  payload_json BLOB NOT NULL
  CHECK(length(payload_json) BETWEEN 2 AND 1048576
    AND json_valid(CAST(payload_json AS TEXT))),
  candidate_digest BLOB NOT NULL
  CHECK(length(candidate_digest) = 32),
  updated_at_unix_ms INTEGER NOT NULL,
  CHECK(candidate_revision = base_revision + 1)
) STRICT;

CREATE TABLE environment_revision_counters(
  environment_id TEXT PRIMARY KEY NOT NULL
  CHECK(length(CAST(environment_id AS BLOB)) BETWEEN 1 AND 128),
  active_revision INTEGER NOT NULL DEFAULT 0
  CHECK(active_revision BETWEEN 0 AND 9223372036854775807),
  draft_revision INTEGER NOT NULL DEFAULT 0
  CHECK(draft_revision BETWEEN 0 AND 9223372036854775807)
) STRICT;

CREATE TABLE environment_revisions(
  environment_id TEXT NOT NULL
  REFERENCES environment_revision_counters(environment_id),
  revision INTEGER NOT NULL
  CHECK(revision BETWEEN 1 AND 9223372036854775807),
  name TEXT NOT NULL
  CHECK(length(CAST(name AS BLOB)) BETWEEN 1 AND 256),
  state TEXT NOT NULL
  CHECK(state IN('active', 'disabled')),
  format_version INTEGER NOT NULL
  CHECK(format_version = 1),
  payload_json BLOB NOT NULL
  CHECK(length(payload_json) BETWEEN 2 AND 1048576
    AND json_valid(CAST(payload_json AS TEXT))),
  candidate_digest BLOB NOT NULL
  CHECK(length(candidate_digest) = 32),
  published_at_unix_ms INTEGER NOT NULL,
  PRIMARY KEY(environment_id, revision)
) STRICT;

CREATE TABLE machine_registrations(
  machine_registration_id TEXT PRIMARY KEY NOT NULL
  CHECK(length(CAST(machine_registration_id AS BLOB)) BETWEEN 1 AND 128),
  machine_id TEXT NOT NULL
  CHECK(length(CAST(machine_id AS BLOB)) BETWEEN 1 AND 128),
  binding_id TEXT NOT NULL
  REFERENCES proxy_client_bindings(binding_id),
  binding_revision INTEGER NOT NULL
  CHECK(binding_revision BETWEEN 1 AND 9223372036854775807),
  revision INTEGER NOT NULL
  CHECK(revision BETWEEN 1 AND 9223372036854775807),
  state TEXT NOT NULL
  CHECK(state IN('active', 'revoked', 're_enrollment_required')),
  display_name TEXT NOT NULL
  CHECK(length(CAST(display_name AS BLOB)) BETWEEN 1 AND 128),
  created_at_unix_ms INTEGER NOT NULL,
  updated_at_unix_ms INTEGER NOT NULL,
  CHECK(updated_at_unix_ms >= created_at_unix_ms),
  UNIQUE(binding_id, machine_id)
) STRICT;

CREATE TABLE manual_captures(
  capture_id TEXT PRIMARY KEY NOT NULL
  CHECK(length(CAST(capture_id AS BLOB)) BETWEEN 1 AND 128),
  owner_kind TEXT NOT NULL
  CHECK(owner_kind IN('local_installation', 'proxy_client_binding')),
  owner_id TEXT NOT NULL
  CHECK(length(CAST(owner_id AS BLOB)) <= 128),
  display_name TEXT NOT NULL
  CHECK(length(CAST(display_name AS BLOB)) BETWEEN 1 AND 128),
  client_class TEXT NOT NULL
  CHECK(client_class IN('cli', 'desktop_app', 'other')),
  lifetime TEXT NOT NULL
  CHECK(lifetime IN('temporary', 'until_revoked')),
  state TEXT NOT NULL
  CHECK(state IN('active', 'revoked', 'expired')),
  credential_revision INTEGER NOT NULL
  CHECK(credential_revision BETWEEN 1 AND 9223372036854775807),
  proxy_credential_hash BLOB NOT NULL UNIQUE
  CHECK(length(proxy_credential_hash) = 32),
  observation TEXT NOT NULL
  CHECK(observation IN('waiting_for_traffic', 'observed')),
  created_at_unix_ms INTEGER NOT NULL,
  updated_at_unix_ms INTEGER NOT NULL,
  expires_at_unix_ms INTEGER,
  last_observed_at_unix_ms INTEGER,
  CHECK((owner_kind = 'local_installation' AND owner_id = '') OR(owner_kind = 'proxy_client_binding' AND length(CAST(owner_id AS BLOB)) BETWEEN 1 AND 128)),
  CHECK((lifetime = 'temporary' AND expires_at_unix_ms IS NOT NULL) OR(lifetime = 'until_revoked' AND expires_at_unix_ms IS NULL AND state != 'expired')),
  CHECK((observation = 'waiting_for_traffic' AND last_observed_at_unix_ms IS NULL) OR(observation = 'observed' AND last_observed_at_unix_ms IS NOT NULL)),
  CHECK(updated_at_unix_ms >= created_at_unix_ms),
  CHECK(expires_at_unix_ms IS NULL OR expires_at_unix_ms >= created_at_unix_ms),
  CHECK(last_observed_at_unix_ms IS NULL OR(last_observed_at_unix_ms >= created_at_unix_ms AND last_observed_at_unix_ms <= updated_at_unix_ms))
) STRICT;

CREATE TABLE "provider_accounts"(
  account_id TEXT PRIMARY KEY NOT NULL
  CHECK(length(CAST(account_id AS BLOB)) BETWEEN 1 AND 128),
  display_name TEXT NOT NULL
  CHECK(length(CAST(display_name AS BLOB)) BETWEEN 1 AND 256),
  credential_origin TEXT NOT NULL,
  endpoint_associations TEXT NOT NULL CHECK(json_valid(endpoint_associations)),
  association_revision INTEGER NOT NULL CHECK(association_revision BETWEEN 1 AND 9223372036854775807),
  note TEXT NOT NULL DEFAULT '' CHECK(length(note) <= 256),
  note_revision INTEGER NOT NULL DEFAULT 0 CHECK(note_revision BETWEEN 0 AND 9223372036854775807),
  settings_revision INTEGER NOT NULL CHECK(settings_revision BETWEEN 1 AND 9223372036854775807),
  egress_profile_json TEXT NOT NULL CHECK(json_valid(egress_profile_json) AND length(CAST(egress_profile_json AS BLOB))<=69632),
  automatic_refresh INTEGER NOT NULL CHECK(automatic_refresh IN (0,1)),
  realm_id TEXT NOT NULL
  CHECK(length(CAST(realm_id AS BLOB)) BETWEEN 1 AND 128),
  driver_ref TEXT NOT NULL
  CHECK(length(CAST(driver_ref AS BLOB)) BETWEEN 1 AND 128),
  secret_reference TEXT NOT NULL UNIQUE
  CHECK(length(CAST(secret_reference AS BLOB)) BETWEEN 1 AND 1024),
  state TEXT NOT NULL
  CHECK(state IN('active', 'disabled')),
  revision INTEGER NOT NULL
  CHECK(revision BETWEEN 1 AND 9223372036854775807),
  created_at_unix_ms INTEGER NOT NULL,
  updated_at_unix_ms INTEGER NOT NULL,
  CHECK(updated_at_unix_ms >= created_at_unix_ms)
) STRICT;

CREATE TABLE proxy_client_bindings(
  binding_id TEXT PRIMARY KEY NOT NULL
  CHECK(length(CAST(binding_id AS BLOB)) BETWEEN 1 AND 128),
  revision INTEGER NOT NULL
  CHECK(revision BETWEEN 1 AND 9223372036854775807),
  state TEXT NOT NULL
  CHECK(state IN('active', 'revoked')),
  display_name TEXT NOT NULL
  CHECK(length(CAST(display_name AS BLOB)) BETWEEN 1 AND 128),
  allowed_ingress_scopes_json BLOB NOT NULL
  CHECK(length(allowed_ingress_scopes_json) BETWEEN 3 AND 65536
    AND json_valid(CAST(allowed_ingress_scopes_json AS TEXT))),
  allowed_environment_ids_json BLOB NOT NULL
  CHECK(length(allowed_environment_ids_json) BETWEEN 3 AND 65536
    AND json_valid(CAST(allowed_environment_ids_json AS TEXT))),
  quota_policy_id TEXT NOT NULL
  CHECK(length(CAST(quota_policy_id AS BLOB)) BETWEEN 1 AND 128),
  allowed_grant_kinds INTEGER NOT NULL
  CHECK(allowed_grant_kinds BETWEEN 1 AND 3),
  created_at_unix_ms INTEGER NOT NULL,
  updated_at_unix_ms INTEGER NOT NULL,
  CHECK(updated_at_unix_ms >= created_at_unix_ms)
) STRICT;

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

CREATE TABLE tool_approvals(
  approval_id TEXT PRIMARY KEY NOT NULL
  CHECK(length(CAST(approval_id AS BLOB)) BETWEEN 1 AND 512),
  revision INTEGER NOT NULL
  CHECK(revision BETWEEN 1 AND 9223372036854775807),
  kind TEXT NOT NULL
  CHECK(kind IN('tool_intent', 'network_ask', 'client_root_ask')),
  exchange_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(exchange_id AS BLOB)) <= 512),
  environment_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(environment_id AS BLOB)) <= 128),
  environment_revision INTEGER NOT NULL DEFAULT 0
  CHECK(environment_revision BETWEEN 0 AND 9223372036854775807),
  environment_digest BLOB NOT NULL DEFAULT x''
  CHECK(length(environment_digest) IN(0, 32)),
  route_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(route_id AS BLOB)) <= 128),
  route_revision INTEGER NOT NULL DEFAULT 0
  CHECK(route_revision BETWEEN 0 AND 9223372036854775807),
  subject_refs_json BLOB NOT NULL
  CHECK(length(subject_refs_json) BETWEEN 3 AND 65536),
  subject_labels_json BLOB NOT NULL
  CHECK(length(subject_labels_json) BETWEEN 3 AND 65536),
  target_host TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(target_host AS BLOB)) <= 253),
  target_port INTEGER NOT NULL DEFAULT 0
  CHECK(target_port BETWEEN 0 AND 65535),
  aggregate_key TEXT NOT NULL
  CHECK(length(CAST(aggregate_key AS BLOB)) BETWEEN 1 AND 512),
  request_count INTEGER NOT NULL DEFAULT 1
  CHECK(request_count > 0),
  waiter_count INTEGER NOT NULL DEFAULT 1
  CHECK(waiter_count > 0 AND waiter_count <= request_count),
  state TEXT NOT NULL
  CHECK(state IN('pending', 'allowed', 'denied', 'canceled', 'expired')),
  decision TEXT NOT NULL DEFAULT ''
  CHECK(decision IN('', 'allow-once', 'deny')),
  decision_scope TEXT NOT NULL DEFAULT ''
  CHECK(decision_scope IN('', 'request', 'host_port')),
  decision_reason TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(decision_reason AS BLOB)) <= 512),
  decision_idempotency_key TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(decision_idempotency_key AS BLOB)) <= 512),
  created_at_unix_ms INTEGER NOT NULL,
  expires_at_unix_ms INTEGER NOT NULL,
  resolved_at_unix_ms INTEGER NOT NULL DEFAULT 0,
  CHECK(expires_at_unix_ms > created_at_unix_ms),
  CHECK(resolved_at_unix_ms = 0 OR resolved_at_unix_ms >= created_at_unix_ms),
  CHECK((exchange_id = '' AND environment_id = ''
AND environment_revision = 0 AND length(environment_digest) = 0
AND route_id = '' AND route_revision = 0) OR(length(CAST(exchange_id AS BLOB)) BETWEEN 1 AND 512
AND length(CAST(environment_id AS BLOB)) BETWEEN 1 AND 128
AND environment_revision >= 1 AND length(environment_digest) = 32
AND length(CAST(route_id AS BLOB)) BETWEEN 1 AND 128 AND route_revision >= 1)),
  CHECK(kind <> 'tool_intent' OR(environment_revision >= 1 AND route_revision >= 1)),
  -- A network ask is about one connection and says which; every other kind
  -- carries no connection it would never decide.
  CHECK((kind = 'network_ask' AND length(target_host) > 0 AND target_port > 0) OR(kind <> 'network_ask' AND target_host = '' AND target_port = 0)),
  -- Only a question about a connection can be remembered.
  CHECK(decision_scope <> 'host_port' OR kind = 'network_ask'),
  CHECK((state = 'pending' AND decision = '' AND decision_scope = ''
AND decision_reason = '' AND decision_idempotency_key = ''
AND resolved_at_unix_ms = 0) OR(state = 'allowed' AND decision = 'allow-once'
AND decision_scope <> '' AND decision_reason = ''
AND length(decision_idempotency_key) >= 16
AND resolved_at_unix_ms > 0) OR(state = 'denied' AND decision = 'deny'
AND decision_scope <> '' AND length(decision_reason) > 0
AND length(decision_idempotency_key) >= 16
AND resolved_at_unix_ms > 0) OR(state IN('canceled', 'expired') AND decision = ''
AND decision_scope = '' AND length(decision_reason) > 0
AND decision_idempotency_key = '' AND resolved_at_unix_ms > 0))
) STRICT;

CREATE TABLE "upstream_endpoints"(
  endpoint_id TEXT PRIMARY KEY NOT NULL
  CHECK(length(CAST(endpoint_id AS BLOB)) BETWEEN 1 AND 128),
  display_name TEXT NOT NULL
  CHECK(length(CAST(display_name AS BLOB)) BETWEEN 1 AND 256),
  origin TEXT NOT NULL
  CHECK(length(CAST(origin AS BLOB)) BETWEEN 1 AND 4096),
  realm_id TEXT NOT NULL
  CHECK(length(CAST(realm_id AS BLOB)) BETWEEN 1 AND 128),
  backend_protocols_json BLOB NOT NULL
  CHECK(length(backend_protocols_json) BETWEEN 3 AND 8192 AND
    json_valid(CAST(backend_protocols_json AS TEXT)) AND
    json_type(CAST(backend_protocols_json AS TEXT)) = 'array'),
  capabilities_json BLOB NOT NULL
  CHECK(length(capabilities_json) BETWEEN 3 AND 8192 AND
    json_valid(CAST(capabilities_json AS TEXT)) AND
    json_type(CAST(capabilities_json AS TEXT)) = 'array'),
  drivers_json BLOB NOT NULL
  CHECK(length(drivers_json) BETWEEN 3 AND 8192 AND
    json_valid(CAST(drivers_json AS TEXT)) AND
    json_type(CAST(drivers_json AS TEXT)) = 'array'),
  state TEXT NOT NULL
  CHECK(state IN('active', 'disabled')),
  revision INTEGER NOT NULL
  CHECK(revision BETWEEN 1 AND 9223372036854775807),
  created_at_unix_ms INTEGER NOT NULL,
  updated_at_unix_ms INTEGER NOT NULL,
  CHECK(updated_at_unix_ms >= created_at_unix_ms)
) STRICT;

CREATE INDEX capture_environment_assignments_environment
ON capture_environment_assignments(
  environment_id,
  capture_kind,
  capture_id
);

CREATE INDEX capture_runs_active_expiry
ON capture_runs(
  state,
  expires_at_unix_ms
);

CREATE INDEX capture_runs_runtime_user_updated
ON capture_runs(runtime_user_id, updated_at_unix_ms DESC)
WHERE runtime_user_id IS NOT NULL;

CREATE INDEX capture_runs_workspace_active
ON capture_runs(
  machine_id,
  workspace_id,
  state,
  updated_at_unix_ms DESC
);

CREATE INDEX client_enrollments_binding_state
ON client_enrollments(binding_id, state, expires_at_unix_ms);

CREATE INDEX connection_rules_precedence
ON connection_rules(
  priority DESC,
  rule_id
);

CREATE UNIQUE INDEX connection_rules_target
ON connection_rules(
  match_kind,
  match_host,
  match_port
);

CREATE INDEX enrolled_control_principals_binding_state
ON enrolled_control_principals(binding_id, state);

CREATE INDEX manual_captures_active_expiry
ON manual_captures(
  state,
  expires_at_unix_ms
)
WHERE state = 'active'
    AND lifetime = 'temporary';

CREATE INDEX manual_captures_owner_updated
ON manual_captures(
  owner_kind,
  owner_id,
  updated_at_unix_ms DESC,
  capture_id ASC
);

CREATE INDEX provider_accounts_origin_state
ON provider_accounts(credential_origin, state, account_id);

CREATE INDEX runtime_user_login_sessions_user_expiry
ON runtime_user_login_sessions(user_id, expires_at_unix_ms DESC);

CREATE INDEX runtime_users_state_username
ON runtime_users(state, username);

CREATE UNIQUE INDEX tool_approvals_decision_idempotency
ON tool_approvals(
  decision_idempotency_key
)
WHERE decision_idempotency_key <> '';

CREATE UNIQUE INDEX tool_approvals_pending_aggregate
ON tool_approvals(
  aggregate_key
)
WHERE state = 'pending';

CREATE INDEX tool_approvals_state_created
ON tool_approvals(
  state,
  created_at_unix_ms DESC
);

CREATE INDEX upstream_endpoints_state
ON upstream_endpoints(state, endpoint_id);

CREATE TABLE runtime_usage_policy(
  singleton INTEGER PRIMARY KEY CHECK(singleton=1),
  enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
  retention_days INTEGER NOT NULL CHECK(retention_days BETWEEN 1 AND 365),
  revision INTEGER NOT NULL CHECK(revision>0), collecting_since_unix_ms INTEGER
) STRICT;

CREATE TABLE runtime_usage_retention_caps(
  retention_days INTEGER PRIMARY KEY CHECK(retention_days BETWEEN 1 AND 365),
  through_sequence INTEGER NOT NULL CHECK(through_sequence>0),
  after_sequence INTEGER NOT NULL DEFAULT 0 CHECK(after_sequence>=0 AND after_sequence<through_sequence)
) STRICT;

CREATE TABLE runtime_user_policies(
  user_id TEXT PRIMARY KEY NOT NULL REFERENCES runtime_users(user_id) ON DELETE CASCADE,
  all_environments INTEGER NOT NULL DEFAULT 0 CHECK(all_environments IN (0, 1)),
  allowed_environment_ids_json BLOB NOT NULL
  CHECK(length(allowed_environment_ids_json) BETWEEN 2 AND 65536 AND
        json_valid(allowed_environment_ids_json) AND
        json_type(allowed_environment_ids_json) = 'array'),
  daily_agent_api_call_warning INTEGER NOT NULL DEFAULT 0
  CHECK(daily_agent_api_call_warning BETWEEN 0 AND 1000000000000000),
  daily_token_warning INTEGER NOT NULL DEFAULT 0
  CHECK(daily_token_warning BETWEEN 0 AND 1000000000000000),
  CHECK(all_environments = 0 OR json_array_length(allowed_environment_ids_json) = 0)
) STRICT;

CREATE TABLE native_tool_scopes(
  capture_kind TEXT NOT NULL  CHECK(capture_kind IN('managed_run','manual_capture')),
  capture_id TEXT NOT NULL  CHECK(length(CAST(capture_id AS BLOB)) BETWEEN 1 AND 128),
  revision INTEGER NOT NULL CHECK(revision BETWEEN 1 AND 9223372036854775807),
  owner_epoch INTEGER NOT NULL CHECK(owner_epoch BETWEEN 0 AND 9223372036854775807),
  state TEXT NOT NULL  CHECK(state IN('unbound','active','revoked','closed')),
  replay_barrier INTEGER NOT NULL CHECK(replay_barrier IN(0,1)),
  expires_at_unix_ms INTEGER NOT NULL,
  PRIMARY KEY(capture_kind,capture_id),
  CHECK(state <> 'active' OR owner_epoch > 0)
) STRICT;

CREATE TABLE native_tool_sessions(
  session_id TEXT NOT NULL PRIMARY KEY CHECK(length(session_id) = 32 AND session_id NOT GLOB '*[^0-9a-f]*'),
  capture_kind TEXT NOT NULL  CHECK(capture_kind IN('managed_run','manual_capture')),
  capture_id TEXT NOT NULL  CHECK(length(CAST(capture_id AS BLOB)) BETWEEN 1 AND 128),
  environment_id TEXT NOT NULL  CHECK(length(CAST(environment_id AS BLOB)) BETWEEN 1 AND 128),
  environment_revision INTEGER NOT NULL CHECK(environment_revision BETWEEN 1 AND 9223372036854775807),
  environment_digest BLOB NOT NULL CHECK(length(environment_digest) = 32),
  assignment_revision INTEGER NOT NULL CHECK(assignment_revision BETWEEN 1 AND 9223372036854775807),
  route_id TEXT NOT NULL  CHECK(length(CAST(route_id AS BLOB)) BETWEEN 1 AND 128),
  route_revision INTEGER NOT NULL CHECK(route_revision BETWEEN 1 AND 9223372036854775807),
  target_origin TEXT NOT NULL  CHECK(length(CAST(target_origin AS BLOB)) BETWEEN 1 AND 1024),
  protocol_version TEXT NOT NULL CHECK(protocol_version = '2025-06-18'),
  revision INTEGER NOT NULL CHECK(revision BETWEEN 1 AND 9223372036854775807),
  owner_epoch INTEGER NOT NULL CHECK(owner_epoch BETWEEN 1 AND 9223372036854775807),
  state TEXT NOT NULL  CHECK(state IN('initializing','ready','revoked','expired')),
  catalog_generation INTEGER NOT NULL CHECK(catalog_generation BETWEEN 0 AND 9223372036854775807),
  catalog_digest BLOB CHECK(catalog_digest IS NULL OR length(catalog_digest) = 32),
  expires_at_unix_ms INTEGER NOT NULL,
  FOREIGN KEY(capture_kind,capture_id) REFERENCES native_tool_scopes(capture_kind,capture_id) ON DELETE CASCADE,
  FOREIGN KEY(environment_id,environment_revision) REFERENCES environment_revisions(environment_id,revision),
  CHECK((catalog_generation = 0) = (catalog_digest IS NULL))
) STRICT;

CREATE TABLE native_tool_operations(
  operation_id TEXT NOT NULL PRIMARY KEY CHECK(length(operation_id) = 32 AND operation_id NOT GLOB '*[^0-9a-f]*'),
  capture_kind TEXT NOT NULL  CHECK(capture_kind IN('managed_run','manual_capture')),
  capture_id TEXT NOT NULL  CHECK(length(CAST(capture_id AS BLOB)) BETWEEN 1 AND 128),
  environment_id TEXT NOT NULL  CHECK(length(CAST(environment_id AS BLOB)) BETWEEN 1 AND 128),
  environment_revision INTEGER NOT NULL CHECK(environment_revision BETWEEN 1 AND 9223372036854775807),
  environment_digest BLOB NOT NULL CHECK(length(environment_digest) = 32),
  connection_id TEXT NOT NULL  CHECK(length(CAST(connection_id AS BLOB)) BETWEEN 1 AND 512),
  target_origin TEXT NOT NULL  CHECK(length(CAST(target_origin AS BLOB)) BETWEEN 1 AND 1024),
  method TEXT NOT NULL  CHECK(method IN('POST','GET','DELETE')),
  rpc_kind TEXT NOT NULL  CHECK(rpc_kind IN('unclassified','initialize','initialized','tools_list','tools_call','ping','cancel','server_reply','get_stream','delete_session')),
  revision INTEGER NOT NULL CHECK(revision BETWEEN 1 AND 9223372036854775807),
  state TEXT NOT NULL  CHECK(state IN('received','validated','awaiting_decision','denied','started','completed','failed','canceled','uncertain')),
  decision_source TEXT NOT NULL  CHECK(decision_source IN('none','policy','user')),
  recording TEXT NOT NULL  CHECK(recording IN('off','metadata_only','full')),
  retention_days INTEGER NOT NULL CHECK(retention_days BETWEEN 0 AND 3650),
  created_at_unix_ms INTEGER NOT NULL,
  recording_archive_epoch INTEGER NOT NULL CHECK(recording_archive_epoch>0),
  recording_capture_generation INTEGER NOT NULL CHECK(recording_capture_generation>0),
  last_event_kind TEXT NOT NULL,
  last_event_at_unix_ms INTEGER NOT NULL CHECK(last_event_at_unix_ms >= created_at_unix_ms),
  maximum_event_at_unix_ms INTEGER NOT NULL CHECK(maximum_event_at_unix_ms >= last_event_at_unix_ms),
  metadata_expires_at_unix_ms INTEGER NOT NULL CHECK(metadata_expires_at_unix_ms > created_at_unix_ms),
  completed_at_unix_ms INTEGER,
  bytes_in INTEGER CHECK(bytes_in IS NULL OR bytes_in >= 0),
  bytes_out INTEGER CHECK(bytes_out IS NULL OR bytes_out >= 0),
  terminal_reason TEXT NOT NULL  CHECK(terminal_reason IN('','permission_off','policy_strict','approval_denied','approval_expired','revoked','canceled','transport_failed','rpc_error','execution_uncertain','completed')),
  FOREIGN KEY(capture_kind,capture_id) REFERENCES native_tool_scopes(capture_kind,capture_id) ON DELETE CASCADE,
  FOREIGN KEY(environment_id,environment_revision) REFERENCES environment_revisions(environment_id,revision),
  CHECK((state IN('denied','completed','failed','canceled','uncertain')) = (completed_at_unix_ms IS NOT NULL)),
  CHECK(state IN('denied','completed','failed','canceled','uncertain') OR terminal_reason = ''),
  CHECK((recording = 'off' AND retention_days = 0) OR (recording IN('metadata_only','full') AND retention_days BETWEEN 1 AND 3650))
) STRICT;

CREATE TABLE native_tool_bindings(
  operation_id TEXT NOT NULL PRIMARY KEY REFERENCES native_tool_operations(operation_id) ON DELETE CASCADE CHECK(length(operation_id) = 32 AND operation_id NOT GLOB '*[^0-9a-f]*'),
  session_id TEXT NOT NULL REFERENCES native_tool_sessions(session_id) CHECK(length(session_id) = 32 AND session_id NOT GLOB '*[^0-9a-f]*'),
  session_revision INTEGER NOT NULL CHECK(session_revision BETWEEN 1 AND 9223372036854775807),
  catalog_generation INTEGER NOT NULL CHECK(catalog_generation BETWEEN 0 AND 9223372036854775807),
  catalog_digest BLOB CHECK(catalog_digest IS NULL OR length(catalog_digest) = 32),
  route_id TEXT NOT NULL  CHECK(length(CAST(route_id AS BLOB)) BETWEEN 1 AND 128),
  route_revision INTEGER NOT NULL CHECK(route_revision BETWEEN 1 AND 9223372036854775807),
  owner_epoch INTEGER NOT NULL CHECK(owner_epoch BETWEEN 1 AND 9223372036854775807),
  assignment_revision INTEGER NOT NULL CHECK(assignment_revision BETWEEN 1 AND 9223372036854775807),
  request_digest BLOB NOT NULL CHECK(length(request_digest) = 32),
  params_digest BLOB NOT NULL CHECK(length(params_digest) = 32),
  group_digest BLOB NOT NULL CHECK(length(group_digest) = 32),
  binding_digest BLOB NOT NULL CHECK(length(binding_digest) = 32),
  replay_key BLOB CHECK(replay_key IS NULL OR length(replay_key) = 32),
  parent_operation_id TEXT CHECK(parent_operation_id IS NULL OR (length(parent_operation_id) = 32 AND parent_operation_id NOT GLOB '*[^0-9a-f]*')),
  server_request_digest BLOB CHECK(server_request_digest IS NULL OR length(server_request_digest) = 32),
  tool_name TEXT NOT NULL DEFAULT '' CHECK(length(CAST(tool_name AS BLOB)) BETWEEN 0 AND 256),
  cancel_rpc_id_digest BLOB CHECK(cancel_rpc_id_digest IS NULL OR length(cancel_rpc_id_digest) = 32),
  CHECK((catalog_generation = 0) = (catalog_digest IS NULL)),
  CHECK((parent_operation_id IS NULL) = (server_request_digest IS NULL)),
  UNIQUE(operation_id,binding_digest)
) STRICT;

CREATE TABLE native_tool_inbound(
  session_id TEXT NOT NULL REFERENCES native_tool_sessions(session_id) ON DELETE CASCADE CHECK(length(session_id) = 32 AND session_id NOT GLOB '*[^0-9a-f]*'),
  rpc_id_digest BLOB NOT NULL CHECK(length(rpc_id_digest) = 32),
  request_kind TEXT NOT NULL  CHECK(request_kind IN('initialize','initialized','tools_list','tools_call','ping','cancel','server_reply')),
  request_digest BLOB NOT NULL CHECK(length(request_digest) = 32),
  operation_id TEXT NOT NULL UNIQUE CHECK(length(operation_id) = 32 AND operation_id NOT GLOB '*[^0-9a-f]*'),
  PRIMARY KEY(session_id,rpc_id_digest,request_kind)
) STRICT;

CREATE TABLE native_tool_binding_egress(
  operation_id TEXT NOT NULL PRIMARY KEY REFERENCES native_tool_bindings(operation_id) ON DELETE CASCADE CHECK(length(operation_id) = 32 AND operation_id NOT GLOB '*[^0-9a-f]*'),
  binding_digest BLOB NOT NULL CHECK(length(binding_digest) = 32),
  binding_version INTEGER NOT NULL CHECK(binding_version = 2),
  profile_json TEXT NOT NULL CHECK(length(CAST(profile_json AS BLOB)) BETWEEN 2 AND 69632 AND json_valid(profile_json)),
  selection_source TEXT NOT NULL CHECK(selection_source IN('flow','global','builtin')),
  default_revision INTEGER NOT NULL CHECK(default_revision BETWEEN 0 AND 9223372036854775807),
  CHECK((selection_source = 'flow' AND default_revision = 0) OR (selection_source IN('global','builtin') AND default_revision > 0)),
  CHECK(selection_source <> 'builtin' OR (json_extract(profile_json,'$.id') = 'profile.direct' AND json_extract(profile_json,'$.revision') = 1))
) STRICT;

CREATE TRIGGER native_tool_binding_egress_insert_guard
BEFORE INSERT ON native_tool_binding_egress
WHEN NOT EXISTS(SELECT 1 FROM native_tool_bindings b JOIN native_tool_operations o ON o.operation_id=b.operation_id
  WHERE b.operation_id=NEW.operation_id AND b.binding_digest=NEW.binding_digest AND o.state='received')
BEGIN
  SELECT RAISE(ABORT,'effective egress differs from newly owned binding');
END;

CREATE TRIGGER native_tool_binding_egress_immutable
BEFORE UPDATE ON native_tool_binding_egress BEGIN
  SELECT RAISE(ABORT,'immutable native binding egress');
END;

CREATE TRIGGER native_tool_binding_egress_delete_guard
BEFORE DELETE ON native_tool_binding_egress
WHEN EXISTS(SELECT 1 FROM native_tool_operations WHERE operation_id=OLD.operation_id)
BEGIN
  SELECT RAISE(ABORT,'effective egress is owned by operation');
END;

CREATE TABLE native_tool_approvals(
  approval_id TEXT NOT NULL PRIMARY KEY CHECK(length(approval_id) = 32 AND approval_id NOT GLOB '*[^0-9a-f]*'),
  operation_id TEXT NOT NULL UNIQUE REFERENCES native_tool_operations(operation_id) ON DELETE CASCADE CHECK(length(operation_id) = 32 AND operation_id NOT GLOB '*[^0-9a-f]*'),
  binding_digest BLOB NOT NULL CHECK(length(binding_digest) = 32),
  revision INTEGER NOT NULL CHECK(revision BETWEEN 1 AND 9223372036854775807),
  state TEXT NOT NULL  CHECK(state IN('pending','allowed','denied','canceled','expired')),
  created_at_unix_ms INTEGER NOT NULL,
  expires_at_unix_ms INTEGER NOT NULL CHECK(expires_at_unix_ms > created_at_unix_ms),
  resolved_at_unix_ms INTEGER CHECK(resolved_at_unix_ms IS NULL OR resolved_at_unix_ms >= created_at_unix_ms),
  idempotency_key TEXT NOT NULL  CHECK(length(CAST(idempotency_key AS BLOB)) BETWEEN 0 AND 512),
  consumed_at_unix_ms INTEGER,
  CHECK((state = 'pending') = (resolved_at_unix_ms IS NULL)),
  CHECK((state IN('allowed','denied') AND length(CAST(idempotency_key AS BLOB)) BETWEEN 16 AND 512) OR (state IN('pending','canceled','expired') AND idempotency_key = '')),
  CHECK(consumed_at_unix_ms IS NULL OR (state = 'allowed' AND consumed_at_unix_ms >= resolved_at_unix_ms))
) STRICT;

CREATE TABLE native_tool_once(
  capture_kind TEXT NOT NULL  CHECK(capture_kind IN('managed_run','manual_capture')),
  capture_id TEXT NOT NULL  CHECK(length(CAST(capture_id AS BLOB)) BETWEEN 1 AND 128),
  replay_key BLOB NOT NULL CHECK(length(replay_key) = 32),
  operation_id TEXT NOT NULL UNIQUE CHECK(length(operation_id) = 32 AND operation_id NOT GLOB '*[^0-9a-f]*'),
  intent_digest BLOB NOT NULL CHECK(length(intent_digest) = 32),
  attempt_id TEXT NOT NULL UNIQUE CHECK(length(CAST(attempt_id AS BLOB)) BETWEEN 1 AND 512),
  claimed_at_unix_ms INTEGER NOT NULL,
  authority_revision INTEGER NOT NULL CHECK(authority_revision BETWEEN 1 AND 9223372036854775807),
  FOREIGN KEY(capture_kind,capture_id) REFERENCES native_tool_scopes(capture_kind,capture_id) ON DELETE CASCADE,
  PRIMARY KEY(capture_kind,capture_id,replay_key)
) STRICT;

CREATE UNIQUE INDEX native_tool_approvals_idempotency
ON native_tool_approvals(idempotency_key) WHERE idempotency_key <> '';

CREATE INDEX native_tool_sessions_scope_state ON native_tool_sessions(capture_kind,capture_id,state);

CREATE INDEX native_tool_operations_scope_created ON native_tool_operations(capture_kind,capture_id,created_at_unix_ms,operation_id);

CREATE INDEX native_tool_operations_expiry ON native_tool_operations(metadata_expires_at_unix_ms);

CREATE INDEX native_tool_approvals_state_created ON native_tool_approvals(state,created_at_unix_ms,approval_id);

CREATE TRIGGER native_tool_binding_immutable
BEFORE UPDATE ON native_tool_bindings BEGIN
  SELECT RAISE(ABORT,'immutable native binding');
END;

CREATE TRIGGER native_tool_binding_cancel_guard
BEFORE INSERT ON native_tool_bindings
WHEN NOT EXISTS(SELECT 1 FROM native_tool_operations WHERE operation_id=NEW.operation_id
  AND ((rpc_kind='cancel' AND NEW.cancel_rpc_id_digest IS NOT NULL)
    OR (rpc_kind<>'cancel' AND NEW.cancel_rpc_id_digest IS NULL)))
BEGIN
  SELECT RAISE(ABORT,'native cancellation target differs from kind');
END;

CREATE TRIGGER native_tool_operation_received
BEFORE INSERT ON native_tool_operations WHEN NEW.state <> 'received' BEGIN
  SELECT RAISE(ABORT,'native operation must be received');
END;

CREATE TRIGGER native_tool_operation_binding_guard
BEFORE UPDATE ON native_tool_operations
WHEN (NEW.state IN('validated','awaiting_decision','started','completed','failed','uncertain')
  AND NOT EXISTS(SELECT 1 FROM native_tool_bindings WHERE operation_id = NEW.operation_id))
  OR (OLD.completed_at_unix_ms IS NOT NULL
    AND (NEW.completed_at_unix_ms IS NULL OR NEW.completed_at_unix_ms <> OLD.completed_at_unix_ms))
BEGIN
  SELECT RAISE(ABORT,'invalid native operation transition');
END;

CREATE TABLE runtime_egress_default(
  singleton INTEGER PRIMARY KEY CHECK(singleton=1),
  revision INTEGER NOT NULL CHECK(revision BETWEEN 1 AND 9223372036854775807),
  egress_id TEXT,
  egress_revision INTEGER,
  CHECK((egress_id IS NULL AND egress_revision IS NULL) OR
        (egress_id IS NOT NULL AND length(CAST(egress_id AS BLOB)) BETWEEN 1 AND 128
         AND egress_revision IS NOT NULL AND egress_revision BETWEEN 1 AND 9223372036854775807))
) STRICT;

CREATE TABLE native_tool_deliveries(
 attempt_id TEXT NOT NULL PRIMARY KEY CHECK(length(CAST(attempt_id AS BLOB)) BETWEEN 1 AND 512),
 operation_id TEXT NOT NULL UNIQUE REFERENCES native_tool_operations(operation_id) ON DELETE CASCADE,
 connection_id TEXT NOT NULL CHECK(length(CAST(connection_id AS BLOB)) BETWEEN 1 AND 512),
 payload_class TEXT NOT NULL CHECK(payload_class IN('none','client_data')),
 target_origin TEXT NOT NULL CHECK(length(CAST(target_origin AS BLOB)) BETWEEN 1 AND 1024),
 policy_id TEXT NOT NULL CHECK(length(CAST(policy_id AS BLOB)) BETWEEN 1 AND 128),
 policy_revision INTEGER NOT NULL CHECK(policy_revision>0),
 rule_id TEXT NOT NULL CHECK(length(CAST(rule_id AS BLOB)) BETWEEN 1 AND 512),
 proxy_id TEXT NOT NULL CHECK(length(CAST(proxy_id AS BLOB)) BETWEEN 1 AND 512),
 proxy_revision INTEGER NOT NULL CHECK(proxy_revision>0),
 selection_source TEXT NOT NULL CHECK(selection_source IN('flow','global','builtin')),
 default_revision INTEGER NOT NULL CHECK((selection_source='flow' AND default_revision=0) OR (selection_source IN('global','builtin') AND default_revision>0)),
 started_at_unix_ms INTEGER NOT NULL CHECK(started_at_unix_ms>0),
 completed_at_unix_ms INTEGER CHECK(completed_at_unix_ms>=started_at_unix_ms),
 outcome TEXT NOT NULL DEFAULT '' CHECK(outcome IN('','completed','failed','canceled')),
 error_class TEXT NOT NULL DEFAULT '' CHECK(length(CAST(error_class AS BLOB))<=512),
 bytes_out INTEGER NOT NULL DEFAULT 0 CHECK(bytes_out>=0),
 bytes_in INTEGER NOT NULL DEFAULT 0 CHECK(bytes_in>=0),
 byte_counts_known INTEGER NOT NULL DEFAULT 1 CHECK(byte_counts_known IN(0,1)),
 CHECK((completed_at_unix_ms IS NULL)=(outcome='')),
 CHECK(completed_at_unix_ms IS NOT NULL OR (error_class='' AND bytes_out=0 AND bytes_in=0 AND byte_counts_known=1))
) STRICT;

CREATE TABLE runtime_storage_state(
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 layout_id TEXT NOT NULL CHECK(length(CAST(layout_id AS BLOB)) BETWEEN 1 AND 128),
 archive_epoch INTEGER NOT NULL CHECK(archive_epoch>0),
 visibility_revision INTEGER NOT NULL CHECK(visibility_revision>0)
) STRICT;
CREATE TABLE recording_capture_fences(
 capture_kind TEXT NOT NULL CHECK(capture_kind IN('managed_run','manual_capture')),
 capture_id TEXT NOT NULL CHECK(length(CAST(capture_id AS BLOB)) BETWEEN 1 AND 128),
 generation INTEGER NOT NULL CHECK(generation>0),
 deleted INTEGER NOT NULL CHECK(deleted IN(0,1)),
 PRIMARY KEY(capture_kind,capture_id)
) STRICT;

INSERT INTO runtime_usage_policy VALUES(1,0,90,1,NULL);

INSERT INTO runtime_egress_default(singleton,revision) VALUES(1,1);
