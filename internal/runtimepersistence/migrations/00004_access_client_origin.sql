-- +goose Up
CREATE TABLE access_client_origins (
    access_id TEXT PRIMARY KEY NOT NULL
        REFERENCES access_bindings(access_id) ON DELETE CASCADE,
    client_origin TEXT NOT NULL
        CHECK (length(CAST(client_origin AS BLOB)) BETWEEN 9 AND 2048),
    endpoint_authority TEXT NOT NULL UNIQUE
        CHECK (length(CAST(endpoint_authority AS BLOB)) BETWEEN 3 AND 2048),
    agent_endpoint_id TEXT NOT NULL
        CHECK (length(CAST(agent_endpoint_id AS BLOB)) BETWEEN 1 AND 128)
) STRICT;

INSERT INTO access_client_origins (
    access_id,
    client_origin,
    endpoint_authority,
    agent_endpoint_id
)
SELECT
    access_id,
    json_extract(CAST(payload_json AS TEXT), '$.agentEndpoint.clientOrigin'),
    CASE
        WHEN substr(
            json_extract(CAST(payload_json AS TEXT), '$.agentEndpoint.clientOrigin'),
            -1
        ) = ']'
        THEN substr(
            json_extract(CAST(payload_json AS TEXT), '$.agentEndpoint.clientOrigin'),
            9
        ) || ':443'
        WHEN instr(
            substr(
                json_extract(CAST(payload_json AS TEXT), '$.agentEndpoint.clientOrigin'),
                9
            ),
            ':'
        ) = 0
        THEN substr(
            json_extract(CAST(payload_json AS TEXT), '$.agentEndpoint.clientOrigin'),
            9
        ) || ':443'
        ELSE substr(
            json_extract(CAST(payload_json AS TEXT), '$.agentEndpoint.clientOrigin'),
            9
        )
    END,
    json_extract(CAST(payload_json AS TEXT), '$.agentEndpoint.id')
FROM access_plan_aggregates;
