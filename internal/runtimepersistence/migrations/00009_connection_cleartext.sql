-- +goose Up
-- Cleartext HTTP selected by a verified Agent target is semantically inspected
-- without TLS termination. Rebuild the table because SQLite cannot widen an
-- existing CHECK constraint in place.
CREATE TABLE runtime_connection_events_next(
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

INSERT INTO runtime_connection_events_next(
  sequence, connection_id, ingress_id, source_label, source_confidence,
  environment_id, environment_name, environment_revision,
  client_endpoint_id, client_endpoint_revision, requested_host, observed_sni,
  route_host, ip, port, decision, rule_id, credential_binding_id,
  egress_scope, egress_source, egress_rule_id, egress_selector_run_id,
  egress_proxy_id, egress_policy_revision, decryption, phase, bytes_up,
  bytes_down, started_at_unix_ms, ended_at_unix_ms, outcome, error_class
)
SELECT
  sequence, connection_id, ingress_id, source_label, source_confidence,
  environment_id, environment_name, environment_revision,
  client_endpoint_id, client_endpoint_revision, requested_host, observed_sni,
  route_host, ip, port, decision, rule_id, credential_binding_id,
  egress_scope, egress_source, egress_rule_id, egress_selector_run_id,
  egress_proxy_id, egress_policy_revision, decryption, phase, bytes_up,
  bytes_down, started_at_unix_ms, ended_at_unix_ms, outcome, error_class
FROM runtime_connection_events;

DROP TABLE runtime_connection_events;
ALTER TABLE runtime_connection_events_next RENAME TO runtime_connection_events;

CREATE INDEX runtime_connection_events_latest
ON runtime_connection_events(sequence DESC);
CREATE INDEX runtime_connection_events_timeline
ON runtime_connection_events(connection_id, sequence);
CREATE INDEX runtime_connection_events_ingress_latest
ON runtime_connection_events(ingress_id, sequence DESC);
