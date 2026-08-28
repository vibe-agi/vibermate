-- Current unreleased ViberMate runtime schema. Compatibility starts with the first release.
CREATE TABLE runtime_metadata(
  singleton INTEGER PRIMARY KEY NOT NULL CHECK(singleton = 1),
  schema_identity TEXT NOT NULL CHECK(schema_identity = 'vibermate-runtime-clean-baseline'),
  schema_revision INTEGER NOT NULL CHECK(schema_revision = 1),
  schema_source_sha256 TEXT NOT NULL CHECK(length(schema_source_sha256) = 64),
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
  length(CAST(runtime_user_id AS BLOB)) BETWEEN 1 AND 128), login_session_id TEXT
REFERENCES runtime_user_login_sessions(session_id)
CHECK(login_session_id IS NULL OR
  length(CAST(login_session_id AS BLOB)) BETWEEN 1 AND 128), device_name TEXT
CHECK(device_name IS NULL OR
  length(CAST(device_name AS BLOB)) BETWEEN 1 AND 128),
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
  CHECK(length(policy_json) BETWEEN 2 AND 8192 AND json_valid(CAST(policy_json AS TEXT))),
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
  upstream_endpoint_id TEXT NOT NULL
  REFERENCES upstream_endpoints(endpoint_id),
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
CREATE TABLE "runtime_activities"(
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  activity_id TEXT NOT NULL UNIQUE
  CHECK(length(CAST(activity_id AS BLOB)) BETWEEN 1 AND 512),
  occurred_at_unix_ms INTEGER NOT NULL,
  kind TEXT NOT NULL
  CHECK(kind IN('environment.applied',
  'environment.disabled',
  'environment.enabled',
  'environment.deleted',
  'credential.secret_replaced',
  'offline_hold.entered',
  'offline_hold.resumed',
  'approval.pending',
  'approval.resolved',
  'exchange.started',
  'exchange.completed')),
  environment_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(environment_id AS BLOB)) <= 128),
  environment_revision INTEGER NOT NULL DEFAULT 0
  CHECK(environment_revision BETWEEN 0 AND 9223372036854775807),
  environment_digest TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(environment_digest AS BLOB)) <= 64),
  client_endpoint_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(client_endpoint_id AS BLOB)) <= 128),
  client_endpoint_revision INTEGER NOT NULL DEFAULT 0
  CHECK(client_endpoint_revision BETWEEN 0 AND 9223372036854775807),
  protocol_plan_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(protocol_plan_id AS BLOB)) <= 128),
  protocol_plan_revision INTEGER NOT NULL DEFAULT 0
  CHECK(protocol_plan_revision BETWEEN 0 AND 9223372036854775807),
  route_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(route_id AS BLOB)) <= 128),
  route_revision INTEGER NOT NULL DEFAULT 0
  CHECK(route_revision BETWEEN 0 AND 9223372036854775807),
  account_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(account_id AS BLOB)) <= 128),
  account_revision INTEGER NOT NULL DEFAULT 0
  CHECK(account_revision BETWEEN 0 AND 9223372036854775807),
  credential_epoch INTEGER NOT NULL DEFAULT 0
  CHECK(credential_epoch BETWEEN 0 AND 9223372036854775807),
  subject_id TEXT NOT NULL
  CHECK(length(CAST(subject_id AS BLOB)) BETWEEN 1 AND 512),
  status TEXT NOT NULL
  CHECK(status IN('succeeded', 'pending', 'failed', 'canceled')),
  reason_code TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(reason_code AS BLOB)) <= 512),
  source_kind TEXT NOT NULL DEFAULT ''
  CHECK(source_kind IN('', 'capture_run', 'manual_proxy', 'system_proxy')),
  source_display_name TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(source_display_name AS BLOB)) <= 512),
  source_recognition TEXT NOT NULL DEFAULT ''
  CHECK(source_recognition IN('', 'verified', 'configured', 'unknown')),
  capture_run_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(capture_run_id AS BLOB)) <= 128),
  manual_capture_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(manual_capture_id AS BLOB)) <= 128),
  connection_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(connection_id AS BLOB)) <= 128),
  conversation_projection_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(conversation_projection_id AS BLOB)) <= 512),
  conversation_display_name TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(conversation_display_name AS BLOB)) <= 512),
  conversation_kind TEXT NOT NULL DEFAULT ''
  CHECK(conversation_kind IN('', 'pending_exchange', 'main', 'agent',
  'isolated_subagent', 'isolated_exchange')),
  conversation_evidence TEXT NOT NULL DEFAULT ''
  CHECK(conversation_evidence IN('', 'pending', 'capture_run', 'explicit_session',
  'explicit_actor', 'client_asserted_subagent', 'ambiguous_actor',
  'undecoded_exchange', 'exchange_boundary')),
  conversation_actor TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(conversation_actor AS BLOB)) <= 512),
  transport_evidence_json BLOB
  CHECK(transport_evidence_json IS NULL OR
  (kind = 'exchange.completed' AND
  length(transport_evidence_json) BETWEEN 2 AND 65536 AND
  json_valid(CAST(transport_evidence_json AS TEXT)))),
  provider_status INTEGER NOT NULL DEFAULT 0
  CHECK(provider_status BETWEEN 0 AND 599),
  provider_field TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(provider_field AS BLOB)) <= 128),
  client_field TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(client_field AS BLOB)) <= 128),
  client_path TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(client_path AS BLOB)) <= 256),
  CHECK((environment_id = '' AND environment_revision = 0 AND
  environment_digest = '') OR
  (environment_id <> '' AND environment_revision > 0 AND
  length(CAST(environment_digest AS BLOB)) = 64)),
  CHECK((client_endpoint_id = '' AND client_endpoint_revision = 0 AND
  protocol_plan_id = '' AND protocol_plan_revision = 0 AND route_id = '' AND
  route_revision = 0) OR
  (environment_id <> '' AND client_endpoint_id <> '' AND
  client_endpoint_revision > 0 AND protocol_plan_id <> '' AND
  protocol_plan_revision > 0 AND
  ((route_id = '' AND route_revision = 0) OR
  (route_id <> '' AND route_revision > 0)))),
  CHECK((account_id = '' AND account_revision = 0 AND credential_epoch = 0) OR
  (account_id <> '' AND account_revision > 0 AND credential_epoch > 0 AND
  (kind = 'credential.secret_replaced' OR
  (kind = 'exchange.completed' AND route_id <> '' AND route_revision > 0)))),
  CHECK(kind NOT IN('exchange.started', 'exchange.completed') OR
  client_endpoint_id <> ''),
  CHECK((kind IN('exchange.started', 'exchange.completed') AND
  conversation_projection_id <> '' AND conversation_kind <> '' AND
  conversation_evidence <> '') OR
  (kind NOT IN('exchange.started', 'exchange.completed') AND
  conversation_projection_id = '' AND conversation_display_name = '' AND
  conversation_kind = '' AND conversation_evidence = '' AND
  conversation_actor = '')),
  CHECK(kind <> 'credential.secret_replaced' OR account_id <> ''),
  CHECK(kind NOT IN('environment.applied', 'environment.disabled',
  'environment.enabled', 'environment.deleted') OR
  (environment_id <> '' AND client_endpoint_id = '' AND account_id = ''))
) STRICT;
CREATE TABLE "runtime_connection_events"(
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  connection_id TEXT NOT NULL
  CHECK(length(CAST(connection_id AS BLOB)) BETWEEN 1 AND 512),
  ingress_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(ingress_id AS BLOB)) <= 512),
  source_label TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(source_label AS BLOB)) <= 512),
  source_confidence TEXT NOT NULL
  CHECK(source_confidence IN('verified', 'configured', 'unknown')),
  environment_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(environment_id AS BLOB)) <= 128),
  environment_name TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(environment_name AS BLOB)) <= 256),
  environment_revision INTEGER NOT NULL DEFAULT 0
  CHECK(environment_revision BETWEEN 0 AND 9223372036854775807),
  client_endpoint_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(client_endpoint_id AS BLOB)) <= 128),
  client_endpoint_revision INTEGER NOT NULL DEFAULT 0
  CHECK(client_endpoint_revision BETWEEN 0 AND 9223372036854775807),
  requested_host TEXT NOT NULL
  CHECK(length(CAST(requested_host AS BLOB)) BETWEEN 1 AND 1024),
  observed_sni TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(observed_sni AS BLOB)) <= 1024),
  route_host TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(route_host AS BLOB)) <= 1024),
  ip TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(ip AS BLOB)) <= 1024),
  port INTEGER NOT NULL
  CHECK(port BETWEEN 0 AND 65535),
  decision TEXT NOT NULL DEFAULT ''
  CHECK(decision IN('', 'allow', 'deny', 'ask')),
  rule_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(rule_id AS BLOB)) <= 512),
  credential_binding_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(credential_binding_id AS BLOB)) <= 512),
  egress_scope TEXT NOT NULL DEFAULT ''
  CHECK(egress_scope IN('', 'environment', 'network')),
  egress_source TEXT NOT NULL DEFAULT ''
  CHECK(egress_source IN('',
  'environment_rule',
  'environment_plugin',
  'environment_default',
  'network_rule',
  'network_default')),
  egress_rule_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(egress_rule_id AS BLOB)) <= 512),
  egress_selector_run_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(egress_selector_run_id AS BLOB)) <= 512),
  egress_proxy_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(egress_proxy_id AS BLOB)) <= 512),
  egress_policy_revision INTEGER NOT NULL DEFAULT 0
  CHECK(egress_policy_revision >= 0),
  decryption TEXT NOT NULL
  CHECK(decryption IN('blind', 'mitm', 'cleartext', 'none')),
  phase TEXT NOT NULL
  CHECK(phase IN('attempted', 'asked', 'decided', 'connected', 'closed', 'failed')),
  bytes_up INTEGER NOT NULL DEFAULT 0
  CHECK(bytes_up >= 0),
  bytes_down INTEGER NOT NULL DEFAULT 0
  CHECK(bytes_down >= 0),
  started_at_unix_ms INTEGER NOT NULL,
  ended_at_unix_ms INTEGER,
  outcome TEXT NOT NULL DEFAULT ''
  CHECK(outcome IN('', 'completed', 'denied', 'canceled', 'failed')),
  error_class TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(error_class AS BLOB)) <= 512)
) STRICT;
CREATE TABLE runtime_egress_attempts(
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  attempt_id TEXT NOT NULL UNIQUE
  CHECK(length(CAST(attempt_id AS BLOB)) BETWEEN 1 AND 512),
  connection_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(connection_id AS BLOB)) <= 512),
  purpose TEXT NOT NULL
  CHECK(purpose IN('provider_attempt',
'upstream_model_discovery',
'model_metadata_directory',
'route_operation',
'original_origin',
'agent_probe',
'blind_tunnel',
'auxiliary_llm',
'language_transform',
'plugin_catalog_sync',
'plugin_artifact_fetch',
'update')),
  -- No unknown member: an unclassified operation never produces an outbound
  -- attempt, so a row claiming one would be a contradiction.
  payload_class TEXT NOT NULL
  CHECK(payload_class IN('none',
'control',
'client_data',
'client_semantic',
'opaque_tunnel',
'runtime')),
  parent_kind TEXT NOT NULL
  CHECK(parent_kind IN('upstream_attempt',
'client_operation',
'original_request',
'blind_connection',
'runtime_action')),
  parent_id TEXT NOT NULL
  CHECK(length(CAST(parent_id AS BLOB)) BETWEEN 1 AND 512),
  parent_exchange_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(parent_exchange_id AS BLOB)) <= 512),
  caller_kind TEXT NOT NULL
  CHECK(caller_kind IN('core', 'plugin')),
  caller_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(caller_id AS BLOB)) <= 512),
  target_origin TEXT NOT NULL
  CHECK(length(CAST(target_origin AS BLOB)) BETWEEN 1 AND 1024),
  policy_id TEXT NOT NULL
  CHECK(length(CAST(policy_id AS BLOB)) BETWEEN 1 AND 512),
  policy_revision INTEGER NOT NULL
  CHECK(policy_revision > 0),
  policy_authority TEXT NOT NULL
  CHECK(policy_authority IN('environment', 'network', 'runtime')),
  rule_id TEXT NOT NULL
  CHECK(length(CAST(rule_id AS BLOB)) BETWEEN 1 AND 512),
  proxy_id TEXT NOT NULL
  CHECK(length(CAST(proxy_id AS BLOB)) BETWEEN 1 AND 512),
  reused_transport INTEGER NOT NULL DEFAULT 0
  CHECK(reused_transport IN(0, 1)),
  started_at_unix_ms INTEGER NOT NULL,
  completed_at_unix_ms INTEGER,
  outcome TEXT NOT NULL DEFAULT ''
  CHECK(outcome IN('', 'completed', 'failed', 'canceled')),
  error_class TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(error_class AS BLOB)) <= 512),
  bytes_out INTEGER NOT NULL DEFAULT 0
  CHECK(bytes_out >= 0),
  bytes_in INTEGER NOT NULL DEFAULT 0
  CHECK(bytes_in >= 0)
) STRICT;
CREATE TABLE runtime_evidence_bodies(
  digest BLOB PRIMARY KEY NOT NULL CHECK(length(digest) = 32),
  plain_bytes INTEGER NOT NULL CHECK(plain_bytes BETWEEN 1 AND 16777216),
  chunk_manifest BLOB NOT NULL
  CHECK(length(chunk_manifest) % 32 = 0 AND
        length(chunk_manifest) BETWEEN 32 AND 262144)
) STRICT;
CREATE TABLE runtime_evidence_chunks(
  digest BLOB PRIMARY KEY NOT NULL CHECK(length(digest) = 32),
  -- Bounded because plain_bytes reaches an allocation as a capacity, and this
  -- database is deliberately unprotected at rest.
  plain_bytes INTEGER NOT NULL CHECK(plain_bytes BETWEEN 1 AND 65536),
  codec TEXT NOT NULL CHECK(codec IN('identity', 'zstd')),
  payload BLOB NOT NULL CHECK(length(payload) > 0)
) STRICT;
CREATE TABLE "runtime_exchange_agent_identities"(
  exchange_id TEXT PRIMARY KEY NOT NULL
  CHECK(length(CAST(exchange_id AS BLOB)) BETWEEN 1 AND 512),
  client_kind TEXT NOT NULL CHECK(client_kind IN('claude', 'codex')),
  session_id TEXT NOT NULL
  CHECK(length(CAST(session_id AS BLOB)) BETWEEN 1 AND 512),
  session_resumable INTEGER NOT NULL CHECK(session_resumable IN(0, 1)),
  actor_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(actor_id AS BLOB)) <= 512),
  actor_label TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(actor_label AS BLOB)) <= 512),
  actor_type TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(actor_type AS BLOB)) <= 512),
  actor_is_subagent INTEGER NOT NULL CHECK(actor_is_subagent IN(0, 1)),
  provider_response_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(provider_response_id AS BLOB)) <= 512),
  provider_message_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(provider_message_id AS BLOB)) <= 512),
  protocol_ids_json TEXT NOT NULL CHECK(json_valid(protocol_ids_json)),
  attributes_json TEXT NOT NULL CHECK(json_valid(attributes_json)),
  evidence_source TEXT NOT NULL
  CHECK(evidence_source IN('client_protocol_evidence', 'client_local_state')),
  confidence TEXT NOT NULL CHECK(confidence = 'exact'),
  observed_at_unix_ms INTEGER NOT NULL,
  CHECK((actor_id = '' AND actor_label = '' AND actor_type = ''
    AND actor_is_subagent = 0) OR actor_id <> '')
) STRICT;
CREATE TABLE runtime_exchange_content_blocks(
  digest TEXT PRIMARY KEY NOT NULL
  CHECK(length(CAST(digest AS BLOB)) = 64 AND lower(digest) = digest),
  plain_bytes INTEGER NOT NULL CHECK(plain_bytes BETWEEN 1 AND 33554432),
  codec TEXT NOT NULL CHECK(codec IN('identity', 'zstd')),
  payload BLOB NOT NULL CHECK(length(payload) > 0)
) STRICT;
CREATE TABLE runtime_exchange_content_messages(
  digest TEXT PRIMARY KEY NOT NULL
  CHECK(length(CAST(digest AS BLOB)) = 64 AND lower(digest) = digest),
  role TEXT NOT NULL
  CHECK(role IN('system', 'developer', 'user', 'assistant', 'tool')),
  agent_json BLOB
  CHECK(agent_json IS NULL OR(length(agent_json) BETWEEN 2 AND 4096 AND
  json_valid(CAST(agent_json AS TEXT)))),
  block_manifest TEXT NOT NULL
  CHECK(length(CAST(block_manifest AS BLOB)) % 64 = 0 AND
  length(CAST(block_manifest AS BLOB)) BETWEEN 64 AND 1048576)
) STRICT;
CREATE TABLE runtime_exchange_content_transcripts(
  digest TEXT PRIMARY KEY NOT NULL
  CHECK(length(CAST(digest AS BLOB)) = 64 AND lower(digest) = digest),
  parent_digest TEXT
  REFERENCES runtime_exchange_content_transcripts(digest),
  message_digest TEXT NOT NULL
  REFERENCES runtime_exchange_content_messages(digest),
  depth INTEGER NOT NULL CHECK(depth BETWEEN 1 AND 100001)
) STRICT;
CREATE TABLE runtime_exchange_contents(
  exchange_id TEXT PRIMARY KEY NOT NULL
  CHECK(length(CAST(exchange_id AS BLOB)) BETWEEN 1 AND 512),
  scope_kind TEXT NOT NULL DEFAULT ''
  CHECK(scope_kind IN('', 'managed_run', 'manual_capture')),
  scope_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(scope_id AS BLOB)) <= 128),
  mode TEXT NOT NULL
  CHECK(mode IN('full', 'metadata_only')),
  recorded_at_unix_ms INTEGER NOT NULL,
  expires_at_unix_ms INTEGER NOT NULL
  CHECK(expires_at_unix_ms > recorded_at_unix_ms),
  request_transcript_digest TEXT NOT NULL
  REFERENCES runtime_exchange_content_transcripts(digest),
  expected_transcript_digest TEXT NOT NULL
  REFERENCES runtime_exchange_content_transcripts(digest),
  base_transcript_digest TEXT
  REFERENCES runtime_exchange_content_transcripts(digest),
  request_message_count INTEGER NOT NULL
  CHECK(request_message_count BETWEEN 1 AND 100001),
  expected_message_count INTEGER NOT NULL
  CHECK(expected_message_count BETWEEN request_message_count AND 100001),
  inherited_message_count INTEGER NOT NULL
  CHECK(inherited_message_count BETWEEN 0 AND request_message_count),
  response_message_digest TEXT
  REFERENCES runtime_exchange_content_messages(digest),
  -- The dialect's top-level instruction parameter, content-addressed like any
  -- message but deliberately outside the transcript chain: it is per-request
  -- configuration, and real clients change it every turn.
  system_message_digest TEXT
  REFERENCES runtime_exchange_content_messages(digest),
  manifest_json BLOB NOT NULL
  CHECK(length(manifest_json) BETWEEN 2 AND 33554432 AND
  json_valid(CAST(manifest_json AS TEXT))),
  CHECK((scope_kind = '' AND scope_id = '') OR
  (scope_kind <> '' AND scope_id <> '')),
  CHECK((base_transcript_digest IS NULL AND inherited_message_count = 0) OR
  (base_transcript_digest IS NOT NULL AND inherited_message_count > 0))
) STRICT;
CREATE TABLE runtime_raw_evidence_envelopes(
  envelope_id TEXT PRIMARY KEY NOT NULL
  CHECK(length(CAST(envelope_id AS BLOB)) BETWEEN 1 AND 512),
  writer_id TEXT NOT NULL
  CHECK(length(CAST(writer_id AS BLOB)) BETWEEN 1 AND 512),
  watermark INTEGER NOT NULL CHECK(watermark BETWEEN 1 AND 9223372036854775807),
  layer TEXT NOT NULL CHECK(layer IN(
    'client_ingress', 'transform_request_input', 'provider_egress',
    'provider_response', 'transform_response_input', 'client_downstream'
  )),
  scope_kind TEXT NOT NULL
  CHECK(scope_kind IN('runtime', 'managed_run', 'manual_capture')),
  scope_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(scope_id AS BLOB)) <= 512),
  exchange_id TEXT NOT NULL
  CHECK(length(CAST(exchange_id AS BLOB)) BETWEEN 1 AND 512),
  connection_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(connection_id AS BLOB)) <= 512),
  attempt_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(attempt_id AS BLOB)) <= 512),
  environment_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(environment_id AS BLOB)) <= 512),
  environment_revision INTEGER NOT NULL DEFAULT 0
  CHECK(environment_revision BETWEEN 0 AND 9223372036854775807),
  environment_digest TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(environment_digest AS BLOB)) <= 128),
  client_endpoint_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(client_endpoint_id AS BLOB)) <= 512),
  client_endpoint_revision INTEGER NOT NULL DEFAULT 0
  CHECK(client_endpoint_revision BETWEEN 0 AND 9223372036854775807),
  upstream_endpoint_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(upstream_endpoint_id AS BLOB)) <= 512),
  upstream_endpoint_revision INTEGER NOT NULL DEFAULT 0
  CHECK(upstream_endpoint_revision BETWEEN 0 AND 9223372036854775807),
  protocol_plan_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(protocol_plan_id AS BLOB)) <= 512),
  protocol_plan_revision INTEGER NOT NULL DEFAULT 0
  CHECK(protocol_plan_revision BETWEEN 0 AND 9223372036854775807),
  route_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(route_id AS BLOB)) <= 512),
  route_revision INTEGER NOT NULL DEFAULT 0
  CHECK(route_revision BETWEEN 0 AND 9223372036854775807),
  account_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(account_id AS BLOB)) <= 512),
  account_revision INTEGER NOT NULL DEFAULT 0
  CHECK(account_revision BETWEEN 0 AND 9223372036854775807),
  credential_epoch INTEGER NOT NULL DEFAULT 0
  CHECK(credential_epoch BETWEEN 0 AND 9223372036854775807),
  observed_at_unix_ms INTEGER NOT NULL,
  expires_at_unix_ms INTEGER NOT NULL
  CHECK(expires_at_unix_ms > observed_at_unix_ms),
  method TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(method AS BLOB)) <= 32),
  status_code INTEGER NOT NULL DEFAULT 0
  CHECK(status_code = 0 OR status_code BETWEEN 100 AND 599),
  scheme TEXT NOT NULL DEFAULT '' CHECK(length(CAST(scheme AS BLOB)) <= 16),
  authority TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(authority AS BLOB)) <= 4096),
  path TEXT NOT NULL DEFAULT '' CHECK(length(CAST(path AS BLOB)) <= 4096),
  raw_query TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(raw_query AS BLOB)) <= 4096),
  content_type TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(content_type AS BLOB)) <= 4096),
  content_encoding TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(content_encoding AS BLOB)) <= 4096),
  representation TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(representation AS BLOB)) <= 4096),
  canonicalization TEXT NOT NULL
  CHECK(canonicalization = 'go_net_http_v1'),
  header_count INTEGER NOT NULL CHECK(header_count >= 0),
  trailer_count INTEGER NOT NULL CHECK(trailer_count >= 0),
  body_bytes INTEGER NOT NULL CHECK(body_bytes >= 0),
  body_sha256 BLOB NOT NULL CHECK(length(body_sha256) = 32),
  digest_scope TEXT NOT NULL
  CHECK(digest_scope IN('full_body', 'observed_prefix', 'unavailable')),
  payload_state TEXT NOT NULL
  CHECK(payload_state IN('captured', 'metadata_only', 'truncated', 'unavailable')),
  payload_reason TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(payload_reason AS BLOB)) <= 128),
  redacted_credential_fields TEXT NOT NULL DEFAULT '[]'
  CHECK(json_valid(redacted_credential_fields) AND
        json_type(redacted_credential_fields) = 'array' AND
        length(CAST(redacted_credential_fields AS BLOB)) <= 4096),
  payload_metadata BLOB NOT NULL,
  stored_body_digest BLOB
  REFERENCES runtime_evidence_bodies(digest)
  CHECK(stored_body_digest IS NULL OR length(stored_body_digest) = 32),
  UNIQUE(writer_id, watermark),
  CHECK((scope_kind = 'runtime' AND scope_id = '') OR
        (scope_kind <> 'runtime' AND scope_id <> '')),
  CHECK((payload_state IN('captured', 'truncated') AND
         length(payload_metadata) > 0) OR
        (payload_state IN('metadata_only', 'unavailable') AND
         length(payload_metadata) = 0 AND stored_body_digest IS NULL))
) STRICT;
CREATE TABLE runtime_raw_evidence_redaction(
  singleton INTEGER PRIMARY KEY NOT NULL CHECK(singleton = 1),
  salt BLOB NOT NULL CHECK(length(salt) = 32)
) STRICT;
CREATE TABLE runtime_raw_evidence_reveal_audits(
  audit_sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  envelope_id TEXT NOT NULL
  REFERENCES runtime_raw_evidence_envelopes(envelope_id) ON DELETE CASCADE,
  exchange_id TEXT NOT NULL
  CHECK(length(CAST(exchange_id AS BLOB)) BETWEEN 1 AND 512),
  actor_id TEXT NOT NULL
  CHECK(length(CAST(actor_id AS BLOB)) BETWEEN 1 AND 512),
  outcome TEXT NOT NULL CHECK(outcome IN('succeeded', 'unavailable')),
  occurred_at_unix_ms INTEGER NOT NULL
) STRICT;
CREATE TABLE runtime_raw_evidence_writer_sessions(
  writer_id TEXT PRIMARY KEY NOT NULL
  CHECK(length(CAST(writer_id AS BLOB)) BETWEEN 1 AND 512),
  started_at_unix_ms INTEGER NOT NULL,
  maximum_unflushed_ms INTEGER NOT NULL CHECK(maximum_unflushed_ms > 0),
  state TEXT NOT NULL CHECK(state IN('open', 'closed', 'recovered_unclean')),
  ended_at_unix_ms INTEGER,
  CHECK((state = 'open' AND ended_at_unix_ms IS NULL) OR
        (state <> 'open' AND ended_at_unix_ms IS NOT NULL))
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
CREATE INDEX provider_accounts_endpoint_state
ON provider_accounts(upstream_endpoint_id, state, account_id);
CREATE INDEX runtime_activities_exchange_capture_run_latest
ON runtime_activities(capture_run_id, sequence DESC)
WHERE kind IN('exchange.started', 'exchange.completed')
  AND capture_run_id <> '';
CREATE INDEX runtime_activities_exchange_conversation_latest
ON runtime_activities(conversation_projection_id, sequence DESC)
WHERE kind IN('exchange.started', 'exchange.completed')
  AND conversation_projection_id <> '';
CREATE INDEX runtime_activities_exchange_latest
ON runtime_activities(sequence DESC)
WHERE kind IN('exchange.started', 'exchange.completed');
CREATE INDEX runtime_activities_exchange_manual_capture_latest
ON runtime_activities(manual_capture_id, sequence DESC)
WHERE kind IN('exchange.started', 'exchange.completed')
  AND manual_capture_id <> '';
CREATE INDEX runtime_activities_exchange_subject
ON runtime_activities(subject_id, sequence DESC)
WHERE kind IN('exchange.started', 'exchange.completed');
CREATE INDEX runtime_activities_latest
ON runtime_activities(sequence DESC);
CREATE INDEX runtime_connection_events_ingress_latest
ON runtime_connection_events(ingress_id, sequence DESC);
CREATE INDEX runtime_connection_events_latest
ON runtime_connection_events(sequence DESC);
CREATE INDEX runtime_connection_events_timeline
ON runtime_connection_events(connection_id, sequence);
CREATE INDEX runtime_egress_attempts_by_connection
ON runtime_egress_attempts(
  connection_id,
  sequence
);
CREATE INDEX runtime_egress_attempts_by_exchange
ON runtime_egress_attempts(
  parent_exchange_id,
  sequence DESC
)
WHERE parent_exchange_id <> '';
CREATE INDEX runtime_egress_attempts_by_parent
ON runtime_egress_attempts(
  parent_kind,
  parent_id,
  sequence
);
CREATE INDEX runtime_egress_attempts_latest
ON runtime_egress_attempts(
  sequence DESC
);
CREATE INDEX runtime_exchange_agent_identities_actor
ON runtime_exchange_agent_identities(
  client_kind, session_id, actor_id, exchange_id
) WHERE actor_id <> '';
CREATE INDEX runtime_exchange_agent_identities_session
ON runtime_exchange_agent_identities(client_kind, session_id, exchange_id);
CREATE INDEX runtime_exchange_content_transcripts_parent
ON runtime_exchange_content_transcripts(parent_digest);
CREATE INDEX runtime_exchange_contents_expiry
ON runtime_exchange_contents(expires_at_unix_ms);
CREATE INDEX runtime_exchange_contents_scope_expected
ON runtime_exchange_contents(
  scope_kind,
  scope_id,
  expected_message_count DESC,
  recorded_at_unix_ms DESC
);
CREATE INDEX runtime_raw_evidence_exchange
ON runtime_raw_evidence_envelopes(
  exchange_id,
  observed_at_unix_ms,
  watermark
);
CREATE INDEX runtime_raw_evidence_expiry
ON runtime_raw_evidence_envelopes(expires_at_unix_ms);
CREATE INDEX runtime_raw_evidence_reveal_exchange
ON runtime_raw_evidence_reveal_audits(exchange_id, occurred_at_unix_ms DESC);
CREATE INDEX runtime_raw_evidence_scope
ON runtime_raw_evidence_envelopes(
  scope_kind,
  scope_id,
  observed_at_unix_ms DESC
);
CREATE INDEX runtime_raw_evidence_stored_body
ON runtime_raw_evidence_envelopes(stored_body_digest)
WHERE stored_body_digest IS NOT NULL;
CREATE INDEX runtime_raw_evidence_writer_state
ON runtime_raw_evidence_writer_sessions(state, started_at_unix_ms);
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
