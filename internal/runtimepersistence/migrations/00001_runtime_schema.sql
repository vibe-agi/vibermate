-- +goose Up
-- ViberMate has not shipped a database format. This file is the single clean
-- development baseline; compatibility migrations start only after a released
-- format exists.
CREATE TABLE runtime_metadata(
  singleton INTEGER PRIMARY KEY NOT NULL CHECK(singleton = 1),
  schema_identity TEXT NOT NULL CHECK(schema_identity = 'vibermate-runtime-clean-baseline'),
  schema_source_sha256 TEXT NOT NULL
  CHECK(length(schema_source_sha256) = 64),
  initialized_at TEXT NOT NULL CHECK(length(initialized_at) > 0)
) STRICT;
CREATE TABLE runtime_connection_events(
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
  CHECK(decryption IN('blind', 'mitm', 'none')),
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
CREATE INDEX runtime_connection_events_latest
ON runtime_connection_events(
  sequence DESC
);
CREATE INDEX runtime_connection_events_timeline
ON runtime_connection_events(
  connection_id,
  sequence
);
CREATE INDEX runtime_connection_events_ingress_latest
ON runtime_connection_events(
  ingress_id,
  sequence DESC
);
CREATE TABLE runtime_activities(
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
  transport_evidence_json BLOB
  CHECK(transport_evidence_json IS NULL OR(kind = 'exchange.completed' AND
length(transport_evidence_json) BETWEEN 2 AND 65536 AND
json_valid(CAST(transport_evidence_json AS TEXT))))
  ,
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
protocol_plan_revision > 0 AND route_id <> '' AND route_revision > 0)),
  CHECK((account_id = '' AND account_revision = 0 AND credential_epoch = 0) OR
(account_id <> '' AND account_revision > 0 AND credential_epoch > 0)),
  CHECK(kind NOT IN('exchange.started', 'exchange.completed') OR
  client_endpoint_id <> ''),
  CHECK(kind <> 'credential.secret_replaced' OR account_id <> ''),
  CHECK(kind NOT IN('environment.applied', 'environment.disabled',
'environment.enabled', 'environment.deleted') OR
(environment_id <> '' AND client_endpoint_id = '' AND account_id = ''))
) STRICT;
CREATE INDEX runtime_activities_latest
ON runtime_activities(sequence DESC);
CREATE TABLE runtime_exchange_content_messages(
  digest TEXT PRIMARY KEY NOT NULL
  CHECK(length(CAST(digest AS BLOB)) = 64 AND lower(digest) = digest),
  payload_json BLOB NOT NULL
  CHECK(length(payload_json) BETWEEN 2 AND 33554432 AND
  json_valid(CAST(payload_json AS TEXT)))
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
CREATE INDEX runtime_exchange_content_transcripts_parent
ON runtime_exchange_content_transcripts(parent_digest);
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
  manifest_json BLOB NOT NULL
  CHECK(length(manifest_json) BETWEEN 2 AND 33554432 AND
  json_valid(CAST(manifest_json AS TEXT))),
  CHECK((scope_kind = '' AND scope_id = '') OR
  (scope_kind <> '' AND scope_id <> '')),
  CHECK((base_transcript_digest IS NULL AND inherited_message_count = 0) OR
  (base_transcript_digest IS NOT NULL AND inherited_message_count > 0))
) STRICT;
CREATE INDEX runtime_exchange_contents_expiry
ON runtime_exchange_contents(expires_at_unix_ms);
CREATE INDEX runtime_exchange_contents_scope_expected
ON runtime_exchange_contents(
  scope_kind,
  scope_id,
  expected_message_count DESC,
  recorded_at_unix_ms DESC
);
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
  CHECK(workspace_derivation_revision BETWEEN 0 AND 9223372036854775807),
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
CREATE INDEX capture_runs_active_expiry
ON capture_runs(
  state,
  expires_at_unix_ms
);
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
CREATE UNIQUE INDEX tool_approvals_decision_idempotency
ON tool_approvals(
  decision_idempotency_key
)
WHERE decision_idempotency_key <> '';
CREATE INDEX tool_approvals_state_created
ON tool_approvals(
  state,
  created_at_unix_ms DESC
);
CREATE UNIQUE INDEX tool_approvals_pending_aggregate
ON tool_approvals(
  aggregate_key
)
WHERE state = 'pending';
CREATE INDEX runtime_activities_exchange_latest
ON runtime_activities(
  sequence DESC
)
WHERE kind IN('exchange.started', 'exchange.completed');
CREATE INDEX runtime_activities_exchange_subject
ON runtime_activities(
  subject_id,
  sequence DESC
)
WHERE kind IN('exchange.started', 'exchange.completed');
CREATE TABLE runtime_egress_attempts(
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  attempt_id TEXT NOT NULL UNIQUE
  CHECK(length(CAST(attempt_id AS BLOB)) BETWEEN 1 AND 512),
  connection_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(connection_id AS BLOB)) <= 512),
  purpose TEXT NOT NULL
  CHECK(purpose IN('provider_attempt',
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
CREATE INDEX runtime_egress_attempts_latest
ON runtime_egress_attempts(
  sequence DESC
);
CREATE INDEX runtime_egress_attempts_by_connection
ON runtime_egress_attempts(
  connection_id,
  sequence
);
CREATE INDEX runtime_egress_attempts_by_parent
ON runtime_egress_attempts(
  parent_kind,
  parent_id,
  sequence
);
CREATE INDEX runtime_egress_attempts_by_exchange
ON runtime_egress_attempts(
  parent_exchange_id,
  sequence DESC
)
WHERE parent_exchange_id <> '';
CREATE INDEX capture_runs_workspace_active
ON capture_runs(
  machine_id,
  workspace_id,
  state,
  updated_at_unix_ms DESC
);
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
CREATE INDEX client_enrollments_binding_state
ON client_enrollments(binding_id, state, expires_at_unix_ms);
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
CREATE INDEX enrolled_control_principals_binding_state
ON enrolled_control_principals(binding_id, state);
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
CREATE INDEX manual_captures_owner_updated
ON manual_captures(
  owner_kind,
  owner_id,
  updated_at_unix_ms DESC,
  capture_id ASC
);
CREATE INDEX manual_captures_active_expiry
ON manual_captures(
  state,
  expires_at_unix_ms
)
WHERE state = 'active'
    AND lifetime = 'temporary';

-- UpstreamEndpoint is the reusable upstream service authority. Environment
-- routes freeze one endpoint revision; ProviderAccounts cannot exist outside
-- the exact Endpoint that issued or accepts their credential.
CREATE TABLE upstream_endpoints(
  endpoint_id TEXT PRIMARY KEY NOT NULL
  CHECK(length(CAST(endpoint_id AS BLOB)) BETWEEN 1 AND 128),
  display_name TEXT NOT NULL
  CHECK(length(CAST(display_name AS BLOB)) BETWEEN 1 AND 256),
  origin TEXT NOT NULL UNIQUE
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
CREATE INDEX upstream_endpoints_state
ON upstream_endpoints(state, endpoint_id);

-- ProviderAccount persists only non-secret account configuration. The
-- credential bytes belong exclusively to the host-selected SecretStore;
-- secret_reference is an opaque typed locator, never a credential value.
CREATE TABLE provider_accounts(
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
CREATE INDEX provider_accounts_endpoint_state
ON provider_accounts(upstream_endpoint_id, state, account_id);

-- Environment is the only user-selectable traffic configuration authority.
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

CREATE TABLE capture_environment_assignments(
  capture_kind TEXT NOT NULL
  CHECK(capture_kind IN('managed_run', 'manual_capture')),
  capture_id TEXT NOT NULL
  CHECK(length(CAST(capture_id AS BLOB)) BETWEEN 1 AND 128),
  environment_id TEXT NOT NULL
  CHECK(length(CAST(environment_id AS BLOB)) BETWEEN 1 AND 128),
  assignment_revision INTEGER NOT NULL
  CHECK(assignment_revision BETWEEN 1 AND 9223372036854775807),
  source TEXT NOT NULL
  CHECK(source IN('launch', 'manual_create', 'workspace_default', 'operator_switch', 'system_transparent')),
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
  updated_at_unix_ms INTEGER NOT NULL,
  PRIMARY KEY(capture_kind, capture_id)
) STRICT;
CREATE INDEX capture_environment_assignments_environment
ON capture_environment_assignments(
  environment_id,
  capture_kind,
  capture_id
);

-- This optional preference selects the initial Environment for future managed
-- runs in one installation-scoped workspace. It grants no network, Root, or
-- credential authority; Capture assignment still freezes those at launch.
CREATE TABLE workspace_environment_defaults(
  machine_id TEXT NOT NULL
  CHECK(length(CAST(machine_id AS BLOB)) BETWEEN 1 AND 128),
  workspace_id TEXT NOT NULL
  CHECK(length(CAST(workspace_id AS BLOB)) BETWEEN 1 AND 128),
  environment_id TEXT NOT NULL
  CHECK(length(CAST(environment_id AS BLOB)) BETWEEN 1 AND 128
    AND environment_id <> 'system_transparent'),
  revision INTEGER NOT NULL
  CHECK(revision BETWEEN 1 AND 9223372036854775807),
  updated_at_unix_ms INTEGER NOT NULL,
  PRIMARY KEY(machine_id, workspace_id)
) STRICT;
CREATE INDEX workspace_environment_defaults_environment
ON workspace_environment_defaults(environment_id, machine_id, workspace_id);

INSERT INTO runtime_metadata (
  singleton,
  schema_identity,
  schema_source_sha256,
  initialized_at
)
VALUES (
  1,
  'vibermate-runtime-clean-baseline',
  '0000000000000000000000000000000000000000000000000000000000000000',
  strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
);
