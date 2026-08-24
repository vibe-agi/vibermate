-- +goose Up
-- Original Destination Exchanges have an Environment, Client Endpoint, and
-- Protocol Plan, but deliberately have no synthetic Upstream Route or Account.
-- Rebuild the table because SQLite cannot relax an existing CHECK constraint.
CREATE TABLE runtime_activities_next(
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

INSERT INTO runtime_activities_next
SELECT * FROM runtime_activities;

DROP TABLE runtime_activities;
ALTER TABLE runtime_activities_next RENAME TO runtime_activities;

CREATE INDEX runtime_activities_latest
ON runtime_activities(sequence DESC);
CREATE INDEX runtime_activities_exchange_latest
ON runtime_activities(sequence DESC)
WHERE kind IN('exchange.started', 'exchange.completed');
CREATE INDEX runtime_activities_exchange_capture_run_latest
ON runtime_activities(capture_run_id, sequence DESC)
WHERE kind IN('exchange.started', 'exchange.completed')
  AND capture_run_id <> '';
CREATE INDEX runtime_activities_exchange_manual_capture_latest
ON runtime_activities(manual_capture_id, sequence DESC)
WHERE kind IN('exchange.started', 'exchange.completed')
  AND manual_capture_id <> '';
CREATE INDEX runtime_activities_exchange_subject
ON runtime_activities(subject_id, sequence DESC)
WHERE kind IN('exchange.started', 'exchange.completed');
CREATE INDEX runtime_activities_exchange_conversation_latest
ON runtime_activities(conversation_projection_id, sequence DESC)
WHERE kind IN('exchange.started', 'exchange.completed')
  AND conversation_projection_id <> '';
