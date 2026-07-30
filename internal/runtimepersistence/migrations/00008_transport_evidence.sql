-- +goose Up
ALTER TABLE runtime_activities
ADD COLUMN transport_evidence_json BLOB
    CHECK (
        transport_evidence_json IS NULL OR (
            kind = 'exchange.completed' AND
            length(transport_evidence_json) BETWEEN 2 AND 65536 AND
            json_valid(CAST(transport_evidence_json AS TEXT))
        )
    );
