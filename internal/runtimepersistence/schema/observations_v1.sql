-- Unreleased observations revision 1. No cross-role foreign keys.
CREATE TABLE runtime_metadata(
  singleton INTEGER PRIMARY KEY NOT NULL CHECK(singleton = 1),
  schema_identity TEXT NOT NULL CHECK(schema_identity = 'vibermate-observations'),
  schema_revision INTEGER NOT NULL CHECK(schema_revision >= 1),
  initialized_at TEXT NOT NULL CHECK(length(initialized_at) > 0)
) STRICT;

CREATE TABLE "runtime_activities"(
 workspace_id TEXT NOT NULL DEFAULT '',
 workspace_label TEXT NOT NULL DEFAULT '',
 capture_label TEXT NOT NULL DEFAULT '',
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
  provider_error_code TEXT NOT NULL DEFAULT '' CHECK(length(provider_error_code) <= 128),
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
  recording_scope_kind TEXT NOT NULL DEFAULT '' CHECK(recording_scope_kind IN('', 'managed_run', 'manual_capture')),
  recording_scope_id TEXT NOT NULL DEFAULT '' CHECK(length(CAST(recording_scope_id AS BLOB)) <= 128),
  recording_archive_epoch INTEGER NOT NULL DEFAULT 0 CHECK(recording_archive_epoch >= 0),
  recording_capture_generation INTEGER NOT NULL DEFAULT 0 CHECK(recording_capture_generation >= 0),
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
  , inspection_purpose TEXT NOT NULL DEFAULT '' CHECK(inspection_purpose IN('', 'client_tools'))) STRICT;

CREATE TABLE runtime_egress_attempts(
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  recording_scope_kind TEXT NOT NULL DEFAULT '' CHECK(recording_scope_kind IN('', 'managed_run', 'manual_capture')),
  recording_scope_id TEXT NOT NULL DEFAULT '' CHECK(length(CAST(recording_scope_id AS BLOB)) <= 128),
  recording_archive_epoch INTEGER NOT NULL DEFAULT 0 CHECK(recording_archive_epoch >= 0),
  recording_capture_generation INTEGER NOT NULL DEFAULT 0 CHECK(recording_capture_generation >= 0),
  attempt_id TEXT NOT NULL UNIQUE
  CHECK(length(CAST(attempt_id AS BLOB)) BETWEEN 1 AND 512),
  connection_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(connection_id AS BLOB)) <= 512),
  purpose TEXT NOT NULL
  CHECK(purpose IN('provider_attempt',
'upstream_model_discovery',
'upstream_account_read',
'upstream_account_action',
'credential_refresh',
'model_metadata_directory',
'route_operation',
'original_origin',
'agent_probe',
'blind_tunnel',
'datagram',
'auxiliary_llm',
'language_transform',
'plugin_catalog_sync',
'plugin_artifact_fetch',
'update',
'client_tool_service')),
  -- No unknown member: an unclassified operation never produces an outbound
  -- attempt, so a row claiming one would be a contradiction.
  payload_class TEXT NOT NULL
  CHECK(payload_class IN('none',
'control',
'client_data',
'client_semantic',
'opaque_tunnel',
'opaque_datagram',
'runtime')),
  parent_kind TEXT NOT NULL
  CHECK(parent_kind IN('upstream_attempt',
'client_operation',
'original_request',
'blind_connection',
'datagram_connection',
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
  proxy_revision INTEGER NOT NULL CHECK(proxy_revision >= 0),
  selection_source TEXT NOT NULL DEFAULT '' CHECK(selection_source IN('', 'account', 'flow', 'global', 'builtin', 'transport')),
  default_revision INTEGER NOT NULL DEFAULT 0 CHECK(default_revision >= 0)
  CHECK((selection_source IN('', 'account', 'flow', 'transport') AND default_revision = 0) OR
        (selection_source IN('global', 'builtin') AND default_revision > 0))
  CHECK(selection_source = '' OR proxy_revision > 0)
  CHECK(selection_source NOT IN('builtin', 'transport') OR (proxy_id = 'profile.direct' AND proxy_revision = 1)),
  account_id TEXT NOT NULL CHECK(length(CAST(account_id AS BLOB)) <= 512),
  account_settings_revision INTEGER NOT NULL CHECK(account_settings_revision >= 0)
  CHECK((account_id = '' AND account_settings_revision = 0) OR
        (account_id <> '' AND account_settings_revision > 0 AND proxy_revision > 0)),
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
  CHECK(bytes_in >= 0),
  byte_counts_known INTEGER NOT NULL DEFAULT 1 CHECK(byte_counts_known IN(0,1)),
  CHECK(purpose <> 'client_tool_service' OR (parent_kind = 'client_operation'
    AND parent_exchange_id = '' AND policy_authority = 'environment'
    AND caller_kind = 'core' AND caller_id = '' AND account_id = ''
    AND account_settings_revision = 0 AND payload_class IN('client_data','none')))
  , CHECK(purpose <> 'datagram' OR (parent_kind = 'datagram_connection'
    AND parent_exchange_id = '' AND policy_authority = 'network'
    AND caller_kind = 'core' AND caller_id = '' AND account_id = ''
    AND account_settings_revision = 0 AND payload_class = 'opaque_datagram'
    AND target_origin LIKE 'udp://%'))
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

CREATE TABLE runtime_evidence_chunk_refs(
  body_digest BLOB NOT NULL REFERENCES runtime_evidence_bodies(digest) ON DELETE CASCADE,
  chunk_digest BLOB NOT NULL REFERENCES runtime_evidence_chunks(digest),
  PRIMARY KEY(body_digest, chunk_digest)
) STRICT, WITHOUT ROWID;

CREATE INDEX runtime_evidence_chunk_refs_chunk
ON runtime_evidence_chunk_refs(chunk_digest);

CREATE TRIGGER runtime_evidence_chunk_refs_insert
AFTER INSERT ON runtime_evidence_bodies BEGIN
  INSERT INTO runtime_evidence_chunk_refs
  SELECT NEW.digest, substr(NEW.chunk_manifest, position, 32) FROM (
    WITH RECURSIVE spans(position) AS (
      VALUES(1) UNION ALL SELECT position+32 FROM spans
      WHERE position+32 <= length(NEW.chunk_manifest)
    ) SELECT position FROM spans
  ) WHERE true ON CONFLICT DO NOTHING;
END;

CREATE TRIGGER runtime_evidence_chunk_refs_update
AFTER UPDATE OF chunk_manifest ON runtime_evidence_bodies BEGIN
  DELETE FROM runtime_evidence_chunk_refs WHERE body_digest=OLD.digest;
  INSERT INTO runtime_evidence_chunk_refs
  SELECT NEW.digest, substr(NEW.chunk_manifest, position, 32) FROM (
    WITH RECURSIVE spans(position) AS (
      VALUES(1) UNION ALL SELECT position+32 FROM spans
      WHERE position+32 <= length(NEW.chunk_manifest)
    ) SELECT position FROM spans
  ) WHERE true ON CONFLICT DO NOTHING;
END;

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

CREATE TABLE runtime_exchange_content_block_refs(
  message_digest TEXT NOT NULL REFERENCES runtime_exchange_content_messages(digest) ON DELETE CASCADE,
  block_digest TEXT NOT NULL REFERENCES runtime_exchange_content_blocks(digest),
  PRIMARY KEY(message_digest, block_digest)
) STRICT, WITHOUT ROWID;

CREATE INDEX runtime_exchange_content_block_refs_block
ON runtime_exchange_content_block_refs(block_digest);

CREATE TRIGGER runtime_exchange_content_block_refs_insert
AFTER INSERT ON runtime_exchange_content_messages BEGIN
  INSERT INTO runtime_exchange_content_block_refs
  SELECT NEW.digest, substr(NEW.block_manifest, position, 64) FROM (
    WITH RECURSIVE spans(position) AS (
      VALUES(1) UNION ALL SELECT position+64 FROM spans
      WHERE position+64 <= length(NEW.block_manifest)
    ) SELECT position FROM spans
  ) WHERE true ON CONFLICT DO NOTHING;
END;

CREATE TRIGGER runtime_exchange_content_block_refs_update
AFTER UPDATE OF block_manifest ON runtime_exchange_content_messages BEGIN
  DELETE FROM runtime_exchange_content_block_refs WHERE message_digest=OLD.digest;
  INSERT INTO runtime_exchange_content_block_refs
  SELECT NEW.digest, substr(NEW.block_manifest, position, 64) FROM (
    WITH RECURSIVE spans(position) AS (
      VALUES(1) UNION ALL SELECT position+64 FROM spans
      WHERE position+64 <= length(NEW.block_manifest)
    ) SELECT position FROM spans
  ) WHERE true ON CONFLICT DO NOTHING;
END;

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

CREATE INDEX runtime_exchange_content_transcripts_message
ON runtime_exchange_content_transcripts(message_digest);

CREATE INDEX runtime_exchange_contents_request_transcript
ON runtime_exchange_contents(request_transcript_digest);

CREATE INDEX runtime_exchange_contents_expected_transcript
ON runtime_exchange_contents(expected_transcript_digest);

CREATE INDEX runtime_exchange_contents_base_transcript
ON runtime_exchange_contents(base_transcript_digest)
WHERE base_transcript_digest IS NOT NULL;

CREATE INDEX runtime_exchange_contents_system_message
ON runtime_exchange_contents(system_message_digest)
WHERE system_message_digest IS NOT NULL;

CREATE INDEX runtime_exchange_contents_response_message
ON runtime_exchange_contents(response_message_digest)
WHERE response_message_digest IS NOT NULL;

CREATE INDEX runtime_exchange_contents_expiry
ON runtime_exchange_contents(expires_at_unix_ms);

CREATE INDEX runtime_exchange_contents_scope_expected
ON runtime_exchange_contents(
  scope_kind,
  scope_id,
  expected_transcript_digest,
  expected_message_count
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

CREATE TABLE runtime_usage_observations(
 policy_revision INTEGER NOT NULL DEFAULT 1 CHECK(policy_revision>0),
 recording_scope_kind TEXT NOT NULL DEFAULT '',
 recording_scope_id TEXT NOT NULL DEFAULT '',
 recording_archive_epoch INTEGER NOT NULL DEFAULT 1 CHECK(recording_archive_epoch>0),
 recording_capture_generation INTEGER NOT NULL DEFAULT 0 CHECK(recording_capture_generation>=0),
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  exchange_id TEXT NOT NULL UNIQUE,
  capture_run_id TEXT NOT NULL, manual_capture_id TEXT NOT NULL,
  runtime_user_id TEXT NOT NULL,
  occurred_at_unix_ms INTEGER NOT NULL, expires_at_unix_ms INTEGER NOT NULL,
  observation_json TEXT NOT NULL CHECK(json_valid(observation_json) AND length(observation_json)<=16384),
  status TEXT GENERATED ALWAYS AS (json_extract(observation_json,'$.status')) STORED NOT NULL
    CHECK(status IN ('succeeded','failed','canceled')),
  source TEXT GENERATED ALWAYS AS (json_extract(observation_json,'$.source')) STORED NOT NULL,
  environment_id TEXT GENERATED ALWAYS AS (json_extract(observation_json,'$.environmentId')) STORED NOT NULL,
  account_id TEXT GENERATED ALWAYS AS (coalesce(json_extract(observation_json,'$.accountId'),'')) STORED NOT NULL,
  model_id TEXT GENERATED ALWAYS AS (coalesce(json_extract(observation_json,'$.upstreamModel'),'')) STORED NOT NULL,
  caller_id TEXT GENERATED ALWAYS AS (coalesce(json_extract(observation_json,'$.attribution.callerId'),'')) STORED NOT NULL,
  project_id TEXT GENERATED ALWAYS AS (coalesce(json_extract(observation_json,'$.attribution.projectId'),'')) STORED NOT NULL,
  branch_id TEXT GENERATED ALWAYS AS (
    CASE WHEN json_extract(observation_json,'$.attribution.gitAtLaunch.detached')=1 THEN 'detached'
      WHEN json_extract(observation_json,'$.attribution.gitAtLaunch.branch') IS NOT NULL
      THEN 'branch:'||json_extract(observation_json,'$.attribution.gitAtLaunch.branch') ELSE '' END
  ) STORED NOT NULL,
  client TEXT GENERATED ALWAYS AS (coalesce(json_extract(observation_json,'$.client'),'')) STORED NOT NULL,
  session_id TEXT GENERATED ALWAYS AS (coalesce(json_extract(observation_json,'$.sessionId'),'')) STORED NOT NULL,
  input_tokens INTEGER GENERATED ALWAYS AS (
    CASE WHEN json_extract(observation_json,'$.usage.InputUncached.Known')=1
      THEN json_extract(observation_json,'$.usage.InputUncached.Tokens') END
  ) STORED CHECK(input_tokens>=0),
  cache_read_tokens INTEGER GENERATED ALWAYS AS (
    CASE WHEN json_extract(observation_json,'$.usage.CacheRead.Known')=1
      THEN json_extract(observation_json,'$.usage.CacheRead.Tokens') END
  ) STORED CHECK(cache_read_tokens>=0),
  cache_write_tokens INTEGER GENERATED ALWAYS AS (
    CASE WHEN json_extract(observation_json,'$.usage.CacheWrite.Known')=1
      THEN json_extract(observation_json,'$.usage.CacheWrite.Tokens') END
  ) STORED CHECK(cache_write_tokens>=0),
  output_tokens INTEGER GENERATED ALWAYS AS (
    CASE WHEN json_extract(observation_json,'$.usage.Output.Known')=1
      THEN json_extract(observation_json,'$.usage.Output.Tokens') END
  ) STORED CHECK(output_tokens>=0),
  reasoning_tokens INTEGER GENERATED ALWAYS AS (
    CASE WHEN json_extract(observation_json,'$.usage.Reasoning.Known')=1
      THEN json_extract(observation_json,'$.usage.Reasoning.Tokens') END
  ) STORED CHECK(reasoning_tokens>=0)
) STRICT;

CREATE INDEX runtime_usage_period ON runtime_usage_observations(occurred_at_unix_ms,sequence,expires_at_unix_ms);

CREATE INDEX runtime_usage_user_period ON runtime_usage_observations(runtime_user_id,occurred_at_unix_ms,sequence,expires_at_unix_ms);

CREATE INDEX runtime_usage_expiry ON runtime_usage_observations(expires_at_unix_ms);

CREATE INDEX runtime_usage_capture ON runtime_usage_observations(capture_run_id,occurred_at_unix_ms);

CREATE INDEX runtime_usage_manual ON runtime_usage_observations(manual_capture_id);

CREATE INDEX runtime_usage_project ON runtime_usage_observations(project_id,occurred_at_unix_ms);

CREATE INDEX runtime_usage_caller ON runtime_usage_observations(caller_id,occurred_at_unix_ms);

CREATE INDEX runtime_usage_model ON runtime_usage_observations(model_id,occurred_at_unix_ms);

CREATE INDEX runtime_usage_session ON runtime_usage_observations(client,session_id,occurred_at_unix_ms);

CREATE INDEX runtime_connections_recording_scope ON runtime_connection_events(recording_scope_kind, recording_scope_id);

CREATE INDEX runtime_egress_recording_scope ON runtime_egress_attempts(recording_scope_kind, recording_scope_id);

CREATE TABLE observation_storage_state(
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 layout_id TEXT NOT NULL CHECK(length(CAST(layout_id AS BLOB)) BETWEEN 1 AND 128),
 archive_id TEXT NOT NULL CHECK(length(CAST(archive_id AS BLOB)) BETWEEN 1 AND 128)
) STRICT;

CREATE TABLE native_history_snapshots(
 operation_id TEXT NOT NULL CHECK(length(operation_id)=32 AND operation_id NOT GLOB '*[^0-9a-f]*'),
 operation_revision INTEGER NOT NULL CHECK(operation_revision>0),
 capture_kind TEXT NOT NULL CHECK(capture_kind IN('managed_run','manual_capture')),
 capture_id TEXT NOT NULL CHECK(length(CAST(capture_id AS BLOB)) BETWEEN 1 AND 128),
 recording_archive_epoch INTEGER NOT NULL CHECK(recording_archive_epoch>0),
 recording_capture_generation INTEGER NOT NULL CHECK(recording_capture_generation>0),
 environment_id TEXT NOT NULL CHECK(length(CAST(environment_id AS BLOB)) BETWEEN 1 AND 128),
 environment_revision INTEGER NOT NULL CHECK(environment_revision>0),
 environment_digest BLOB NOT NULL CHECK(length(environment_digest)=32),
 binding_digest BLOB CHECK(binding_digest IS NULL OR length(binding_digest)=32),
 approval_id TEXT CHECK(approval_id IS NULL OR (length(approval_id)=32 AND approval_id NOT GLOB '*[^0-9a-f]*')),
 recording TEXT NOT NULL CHECK(recording IN('off','metadata_only','full')),
 retention_days INTEGER NOT NULL CHECK(retention_days BETWEEN 0 AND 3650),
 rpc_kind TEXT NOT NULL CHECK(rpc_kind IN('unclassified','initialize','initialized','tools_list','tools_call','ping','cancel','server_reply','get_stream','delete_session')),
 tool_name TEXT NOT NULL CHECK(length(CAST(tool_name AS BLOB))<=256),
 target_origin TEXT NOT NULL CHECK(length(CAST(target_origin AS BLOB)) BETWEEN 1 AND 1024),
 credential_owner TEXT NOT NULL CHECK(length(CAST(credential_owner AS BLOB))<=128),
 state TEXT NOT NULL CHECK(state IN('received','validated','awaiting_decision','denied','started','completed','failed','canceled','uncertain')),
 decision_source TEXT NOT NULL CHECK(decision_source IN('none','policy','user')),
 terminal_reason TEXT NOT NULL CHECK(length(CAST(terminal_reason AS BLOB))<=64),
 created_at_unix_ms INTEGER NOT NULL CHECK(created_at_unix_ms>0),
 completed_at_unix_ms INTEGER CHECK(completed_at_unix_ms IS NULL OR completed_at_unix_ms>=created_at_unix_ms),
 bytes_in INTEGER CHECK(bytes_in IS NULL OR bytes_in>=0),
 bytes_out INTEGER CHECK(bytes_out IS NULL OR bytes_out>=0),
 event_kind TEXT NOT NULL CHECK(length(CAST(event_kind AS BLOB)) BETWEEN 1 AND 32),
 event_at_unix_ms INTEGER NOT NULL CHECK(event_at_unix_ms>=created_at_unix_ms),
 maximum_event_at_unix_ms INTEGER NOT NULL CHECK(maximum_event_at_unix_ms>=event_at_unix_ms),
 metadata_expires_at_unix_ms INTEGER NOT NULL CHECK(metadata_expires_at_unix_ms>created_at_unix_ms),
 PRIMARY KEY(operation_id,operation_revision)
) STRICT;
CREATE TABLE native_history_latest(
 operation_id TEXT PRIMARY KEY,
 operation_revision INTEGER NOT NULL,
 FOREIGN KEY(operation_id,operation_revision) REFERENCES native_history_snapshots(operation_id,operation_revision)
) STRICT;
CREATE TABLE native_inspection_index(
 position INTEGER PRIMARY KEY AUTOINCREMENT,
 operation_id TEXT NOT NULL UNIQUE CHECK(length(operation_id)=32 AND operation_id NOT GLOB '*[^0-9a-f]*'),
 capture_kind TEXT NOT NULL CHECK(capture_kind IN('managed_run','manual_capture')),
 capture_id TEXT NOT NULL CHECK(length(CAST(capture_id AS BLOB)) BETWEEN 1 AND 128),
 recording_archive_epoch INTEGER NOT NULL CHECK(recording_archive_epoch>0),
 recording_capture_generation INTEGER NOT NULL CHECK(recording_capture_generation>0),
 environment_id TEXT NOT NULL CHECK(length(CAST(environment_id AS BLOB)) BETWEEN 1 AND 128),
 environment_revision INTEGER NOT NULL CHECK(environment_revision>0),
 environment_digest BLOB NOT NULL CHECK(length(environment_digest)=32),
 rpc_kind TEXT NOT NULL CHECK(rpc_kind IN('unclassified','initialize','initialized','tools_list','tools_call','ping','cancel','server_reply','get_stream','delete_session')),
 target_origin TEXT NOT NULL CHECK(length(CAST(target_origin AS BLOB)) BETWEEN 1 AND 1024),
 recording TEXT NOT NULL CHECK(recording IN('off','metadata_only','full')),
 retention_days INTEGER NOT NULL CHECK(retention_days BETWEEN 0 AND 3650),
 created_at_unix_ms INTEGER NOT NULL CHECK(created_at_unix_ms>0),
 history_revision INTEGER NOT NULL DEFAULT 0 CHECK(history_revision>=0)
) STRICT;
CREATE INDEX native_history_expiry ON native_history_snapshots(metadata_expires_at_unix_ms,operation_id);
CREATE INDEX native_inspection_capture ON native_inspection_index(capture_kind,capture_id,position);

-- Immutable body/display coordinates; no foreign key to a live operation.
-- Task2 publishes this graph and the shared inspection index atomically.
CREATE TABLE native_tool_contents(
 operation_id TEXT NOT NULL CHECK(length(operation_id)=32 AND operation_id NOT GLOB '*[^0-9a-f]*'),
 part TEXT NOT NULL CHECK(part IN('tool_arguments','tool_result')),
 capture_kind TEXT NOT NULL CHECK(capture_kind IN('managed_run','manual_capture')),
 capture_id TEXT NOT NULL CHECK(length(CAST(capture_id AS BLOB)) BETWEEN 1 AND 128),
 recording_archive_epoch INTEGER NOT NULL CHECK(recording_archive_epoch>0),
 recording_capture_generation INTEGER NOT NULL CHECK(recording_capture_generation>0),
 environment_id TEXT NOT NULL CHECK(length(CAST(environment_id AS BLOB)) BETWEEN 1 AND 128),
 environment_revision INTEGER NOT NULL CHECK(environment_revision>0),
 environment_digest BLOB NOT NULL CHECK(length(environment_digest)=32),
 rpc_kind TEXT NOT NULL CHECK(rpc_kind='tools_call'),
 tool_name TEXT NOT NULL CHECK(length(CAST(tool_name AS BLOB)) BETWEEN 1 AND 256),
 target_origin TEXT NOT NULL CHECK(length(CAST(target_origin AS BLOB)) BETWEEN 1 AND 1024),
 credential_owner TEXT NOT NULL CHECK(length(CAST(credential_owner AS BLOB)) BETWEEN 1 AND 128),
 recording TEXT NOT NULL CHECK(recording='full'),
 retention_days INTEGER NOT NULL CHECK(retention_days BETWEEN 1 AND 3650),
 created_at_unix_ms INTEGER NOT NULL CHECK(created_at_unix_ms>0),
 expires_at_unix_ms INTEGER NOT NULL CHECK(expires_at_unix_ms>created_at_unix_ms),
 body_digest BLOB NOT NULL CHECK(length(body_digest)=32) REFERENCES runtime_evidence_bodies(digest),
 PRIMARY KEY(operation_id,part)
) STRICT;
CREATE INDEX native_tool_contents_body ON native_tool_contents(body_digest);
CREATE INDEX native_tool_contents_expiry ON native_tool_contents(expires_at_unix_ms);

CREATE TABLE capture_observations(
 capture_kind TEXT NOT NULL CHECK(capture_kind IN('managed_run','manual_capture')),
 capture_id TEXT NOT NULL CHECK(length(CAST(capture_id AS BLOB)) BETWEEN 1 AND 128),
 archive_epoch INTEGER NOT NULL CHECK(archive_epoch>0),
 capture_generation INTEGER NOT NULL CHECK(capture_generation>0),
 credential_revision INTEGER NOT NULL CHECK((capture_kind='managed_run' AND credential_revision=0) OR (capture_kind='manual_capture' AND credential_revision>0)),
 first_observed_at_unix_ms INTEGER NOT NULL CHECK(first_observed_at_unix_ms>0),
 last_observed_at_unix_ms INTEGER NOT NULL CHECK(last_observed_at_unix_ms>=first_observed_at_unix_ms),
 PRIMARY KEY(capture_kind,capture_id,archive_epoch,capture_generation,credential_revision)
) STRICT;

CREATE TABLE acp_snapshots(
 run_id TEXT NOT NULL CHECK(length(CAST(run_id AS BLOB)) BETWEEN 1 AND 128),
 archive_epoch INTEGER NOT NULL CHECK(archive_epoch>0),
 capture_generation INTEGER NOT NULL CHECK(capture_generation>0),
 recording TEXT NOT NULL CHECK(recording IN('off','metadata_only','full')),
 retention_days INTEGER NOT NULL CHECK(retention_days BETWEEN 0 AND 3650),
 expires_at_unix_ms INTEGER NOT NULL CHECK(expires_at_unix_ms>0),
 snapshot BLOB NOT NULL CHECK(length(snapshot)<=786432),
 revision INTEGER NOT NULL CHECK(revision>0),
 final INTEGER NOT NULL CHECK(final IN(0,1)),
 PRIMARY KEY(run_id,archive_epoch,capture_generation)
) STRICT;
