-- +goose Up
CREATE TABLE access_bindings (
    access_id TEXT PRIMARY KEY NOT NULL
        CHECK (length(CAST(access_id AS BLOB)) BETWEEN 1 AND 128),
    revision INTEGER NOT NULL
        CHECK (revision BETWEEN 1 AND 9223372036854775807),
    name TEXT NOT NULL
        CHECK (length(CAST(name AS BLOB)) BETWEEN 1 AND 256),
    description TEXT NOT NULL
        CHECK (length(CAST(description AS BLOB)) <= 4096)
) STRICT;
