-- +goose Up
-- A failed request said only its reason code, and the facts that would make it
-- diagnosable were concatenated onto that code as suffixes. A reader could not
-- tell which part was the reason and which was the evidence, and a field the
-- translator does not model could not be named at all.
--
-- The evidence gets its own columns. Design 06 §4.1 bounds what may be stored:
-- these are structure — an HTTP status, a closed-vocabulary field name, and a
-- path of field names and indices — and never a value from the request.
ALTER TABLE runtime_activities
    ADD COLUMN provider_status INTEGER NOT NULL DEFAULT 0
        CHECK (provider_status BETWEEN 0 AND 599);

ALTER TABLE runtime_activities
    ADD COLUMN provider_field TEXT NOT NULL DEFAULT ''
        CHECK (length(CAST(provider_field AS BLOB)) <= 128);

ALTER TABLE runtime_activities
    ADD COLUMN client_field TEXT NOT NULL DEFAULT ''
        CHECK (length(CAST(client_field AS BLOB)) <= 128);

ALTER TABLE runtime_activities
    ADD COLUMN client_path TEXT NOT NULL DEFAULT ''
        CHECK (length(CAST(client_path AS BLOB)) <= 256);
