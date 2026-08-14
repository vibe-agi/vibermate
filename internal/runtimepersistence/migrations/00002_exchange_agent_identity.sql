-- +goose Up
-- Client-owned Agent session identities are durable structural evidence. They
-- outlive retention-bound semantic content so later Exchanges can still be
-- associated without guessing from time, title, or prompt text.
CREATE TABLE runtime_exchange_agent_identities(
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
  provider_response_id TEXT NOT NULL
  CHECK(length(CAST(provider_response_id AS BLOB)) BETWEEN 1 AND 512),
  provider_message_id TEXT NOT NULL DEFAULT ''
  CHECK(length(CAST(provider_message_id AS BLOB)) <= 512),
  protocol_ids_json TEXT NOT NULL CHECK(json_valid(protocol_ids_json)),
  attributes_json TEXT NOT NULL CHECK(json_valid(attributes_json)),
  evidence_source TEXT NOT NULL CHECK(evidence_source = 'client_local_state'),
  confidence TEXT NOT NULL CHECK(confidence = 'exact'),
  observed_at_unix_ms INTEGER NOT NULL,
  CHECK((actor_id = '' AND actor_label = '' AND actor_type = ''
    AND actor_is_subagent = 0) OR actor_id <> '')
) STRICT;
CREATE INDEX runtime_exchange_agent_identities_session
ON runtime_exchange_agent_identities(client_kind, session_id, exchange_id);
CREATE INDEX runtime_exchange_agent_identities_actor
ON runtime_exchange_agent_identities(
  client_kind, session_id, actor_id, exchange_id
) WHERE actor_id <> '';

-- +goose Down
DROP INDEX runtime_exchange_agent_identities_actor;
DROP INDEX runtime_exchange_agent_identities_session;
DROP TABLE runtime_exchange_agent_identities;
