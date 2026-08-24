-- +goose Up
-- An origin is a routable address, not an Endpoint identity. One upstream
-- service can explicitly expose more than one protocol at the same canonical
-- origin, while every ProviderAccount remains owned by one exact Endpoint.
-- Rebuild the parent and child together so foreign-key enforcement stays on
-- throughout the migration and existing Account bindings are preserved.
CREATE TABLE upstream_endpoints_next(
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
CREATE TABLE provider_accounts_next(
  account_id TEXT PRIMARY KEY NOT NULL
  CHECK(length(CAST(account_id AS BLOB)) BETWEEN 1 AND 128),
  display_name TEXT NOT NULL
  CHECK(length(CAST(display_name AS BLOB)) BETWEEN 1 AND 256),
  upstream_endpoint_id TEXT NOT NULL
  REFERENCES upstream_endpoints_next(endpoint_id),
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
INSERT INTO upstream_endpoints_next(
  endpoint_id, display_name, origin, realm_id,
  backend_protocols_json, capabilities_json, drivers_json,
  state, revision, created_at_unix_ms, updated_at_unix_ms
)
SELECT endpoint_id, display_name, origin, realm_id,
       backend_protocols_json, capabilities_json, drivers_json,
       state, revision, created_at_unix_ms, updated_at_unix_ms
FROM upstream_endpoints;
INSERT INTO provider_accounts_next(
  account_id, display_name, upstream_endpoint_id, realm_id, driver_ref,
  secret_reference, state, revision, created_at_unix_ms, updated_at_unix_ms
)
SELECT account_id, display_name, upstream_endpoint_id, realm_id, driver_ref,
       secret_reference, state, revision, created_at_unix_ms, updated_at_unix_ms
FROM provider_accounts;
DROP TABLE provider_accounts;
DROP TABLE upstream_endpoints;
ALTER TABLE upstream_endpoints_next RENAME TO upstream_endpoints;
ALTER TABLE provider_accounts_next RENAME TO provider_accounts;
CREATE INDEX upstream_endpoints_state
ON upstream_endpoints(state, endpoint_id);
CREATE INDEX provider_accounts_endpoint_state
ON provider_accounts(upstream_endpoint_id, state, account_id);

-- +goose Down
CREATE TABLE upstream_endpoints_previous(
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
CREATE TABLE provider_accounts_previous(
  account_id TEXT PRIMARY KEY NOT NULL
  CHECK(length(CAST(account_id AS BLOB)) BETWEEN 1 AND 128),
  display_name TEXT NOT NULL
  CHECK(length(CAST(display_name AS BLOB)) BETWEEN 1 AND 256),
  upstream_endpoint_id TEXT NOT NULL
  REFERENCES upstream_endpoints_previous(endpoint_id),
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
INSERT INTO upstream_endpoints_previous(
  endpoint_id, display_name, origin, realm_id,
  backend_protocols_json, capabilities_json, drivers_json,
  state, revision, created_at_unix_ms, updated_at_unix_ms
)
SELECT endpoint_id, display_name, origin, realm_id,
       backend_protocols_json, capabilities_json, drivers_json,
       state, revision, created_at_unix_ms, updated_at_unix_ms
FROM upstream_endpoints;
INSERT INTO provider_accounts_previous(
  account_id, display_name, upstream_endpoint_id, realm_id, driver_ref,
  secret_reference, state, revision, created_at_unix_ms, updated_at_unix_ms
)
SELECT account_id, display_name, upstream_endpoint_id, realm_id, driver_ref,
       secret_reference, state, revision, created_at_unix_ms, updated_at_unix_ms
FROM provider_accounts;
DROP TABLE provider_accounts;
DROP TABLE upstream_endpoints;
ALTER TABLE upstream_endpoints_previous RENAME TO upstream_endpoints;
ALTER TABLE provider_accounts_previous RENAME TO provider_accounts;
CREATE INDEX upstream_endpoints_state
ON upstream_endpoints(state, endpoint_id);
CREATE INDEX provider_accounts_endpoint_state
ON provider_accounts(upstream_endpoint_id, state, account_id);
