-- Inactive delta; the separately reviewed migration runner owns the transaction
-- and revision publication. Preserve the old autoincrement high-water mark.
ALTER TABLE runtime_connection_events ADD COLUMN inspection_purpose TEXT NOT NULL DEFAULT '' CHECK(inspection_purpose IN('', 'client_tools'));
CREATE TEMP TABLE native_migration_audit_sequence(seq INTEGER NOT NULL);
INSERT INTO native_migration_audit_sequence(seq)
SELECT seq FROM sqlite_sequence WHERE name = 'runtime_egress_attempts';
CREATE TABLE runtime_egress_attempts_v2(
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
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
) STRICT;
INSERT INTO runtime_egress_attempts_v2(sequence,attempt_id,connection_id,purpose,payload_class,parent_kind,parent_id,parent_exchange_id,caller_kind,caller_id,target_origin,policy_id,policy_revision,policy_authority,rule_id,proxy_id,proxy_revision,account_id,account_settings_revision,reused_transport,started_at_unix_ms,completed_at_unix_ms,outcome,error_class,bytes_out,bytes_in)
SELECT sequence,attempt_id,connection_id,purpose,payload_class,parent_kind,parent_id,parent_exchange_id,caller_kind,caller_id,target_origin,policy_id,policy_revision,policy_authority,rule_id,proxy_id,proxy_revision,account_id,account_settings_revision,reused_transport,started_at_unix_ms,completed_at_unix_ms,outcome,error_class,bytes_out,bytes_in FROM runtime_egress_attempts;
DROP TABLE runtime_egress_attempts;
ALTER TABLE runtime_egress_attempts_v2 RENAME TO runtime_egress_attempts;
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

UPDATE sqlite_sequence SET seq = max(seq, (SELECT seq FROM native_migration_audit_sequence))
WHERE name = 'runtime_egress_attempts' AND EXISTS(SELECT 1 FROM native_migration_audit_sequence);
INSERT INTO sqlite_sequence(name,seq)
SELECT 'runtime_egress_attempts',seq FROM native_migration_audit_sequence
WHERE NOT EXISTS(SELECT 1 FROM sqlite_sequence WHERE name = 'runtime_egress_attempts');
-- Even an empty INSERT SELECT creates a zero sequence row. If v1 had no
-- audit sequence entry, preserve that absence rather than inventing metadata.
DELETE FROM sqlite_sequence WHERE name = 'runtime_egress_attempts'
  AND NOT EXISTS(SELECT 1 FROM native_migration_audit_sequence);
DROP TABLE native_migration_audit_sequence;

-- Prepared native revision-2 DDL; not used by the active revision-1 Store.
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

CREATE TABLE native_tool_events(
  sequence INTEGER PRIMARY KEY AUTOINCREMENT,
  operation_id TEXT NOT NULL REFERENCES native_tool_operations(operation_id) ON DELETE CASCADE CHECK(length(operation_id) = 32 AND operation_id NOT GLOB '*[^0-9a-f]*'),
  operation_revision INTEGER NOT NULL CHECK(operation_revision BETWEEN 1 AND 9223372036854775807),
  kind TEXT NOT NULL  CHECK(kind IN('received','validated','awaiting_decision','allowed','denied','started','progress','completed','failed','canceled','uncertain','revoked')),
  occurred_at_unix_ms INTEGER NOT NULL,
  UNIQUE(operation_id,operation_revision)
) STRICT;

CREATE TABLE native_tool_contents(
  operation_id TEXT NOT NULL REFERENCES native_tool_operations(operation_id) ON DELETE CASCADE CHECK(length(operation_id) = 32 AND operation_id NOT GLOB '*[^0-9a-f]*'),
  part TEXT NOT NULL  CHECK(part IN('tool_arguments','tool_result')),
  body_digest BLOB NOT NULL CHECK(length(body_digest) = 32) REFERENCES runtime_evidence_bodies(digest),
  expires_at_unix_ms INTEGER NOT NULL,
  PRIMARY KEY(operation_id,part)
) STRICT;

CREATE UNIQUE INDEX native_tool_approvals_idempotency
ON native_tool_approvals(idempotency_key) WHERE idempotency_key <> '';
CREATE INDEX native_tool_sessions_scope_state ON native_tool_sessions(capture_kind,capture_id,state);
CREATE INDEX native_tool_operations_scope_created ON native_tool_operations(capture_kind,capture_id,created_at_unix_ms,operation_id);
CREATE INDEX native_tool_operations_expiry ON native_tool_operations(metadata_expires_at_unix_ms);
CREATE INDEX native_tool_approvals_state_created ON native_tool_approvals(state,created_at_unix_ms);
CREATE INDEX native_tool_events_operation_sequence ON native_tool_events(operation_id,sequence);
CREATE INDEX native_tool_contents_expiry ON native_tool_contents(expires_at_unix_ms);
CREATE INDEX native_tool_contents_body ON native_tool_contents(body_digest);

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
CREATE TRIGGER native_tool_content_insert_guard
BEFORE INSERT ON native_tool_contents
WHEN NOT EXISTS(SELECT 1 FROM native_tool_operations
  WHERE operation_id = NEW.operation_id AND recording = 'full' AND rpc_kind = 'tools_call')
BEGIN
  SELECT RAISE(ABORT,'native content requires full tools call recording');
END;
CREATE TRIGGER native_tool_content_update_guard
BEFORE UPDATE ON native_tool_contents
WHEN NOT EXISTS(SELECT 1 FROM native_tool_operations
  WHERE operation_id = NEW.operation_id AND recording = 'full' AND rpc_kind = 'tools_call')
BEGIN
  SELECT RAISE(ABORT,'native content requires full tools call recording');
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
INSERT INTO runtime_egress_default(singleton,revision) VALUES(1,1);
