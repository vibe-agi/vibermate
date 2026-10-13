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
