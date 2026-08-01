-- +goose Up
-- Connection rules were a constant compiled into the runtime, so nobody could
-- add a host, remove one, or see what was in force. They live here now.
--
-- The set carries one revision because it is replaced whole: a rule set that
-- would not construct must be refused before anything is written, and a
-- half-applied change would leave the runtime holding rules it never accepted.
CREATE TABLE connection_rule_sets (
    id INTEGER PRIMARY KEY NOT NULL CHECK (id = 1),
    revision INTEGER NOT NULL
        CHECK (revision BETWEEN 1 AND 9223372036854775807),
    updated_at_unix_ms INTEGER NOT NULL
) STRICT;

CREATE TABLE connection_rules (
    rule_id TEXT PRIMARY KEY NOT NULL
        CHECK (length(CAST(rule_id AS BLOB)) BETWEEN 1 AND 256),
    -- The default is stored like any other rule, so reading the set back shows
    -- every answer it can give rather than all but one.
    is_default INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0, 1)),
    priority INTEGER NOT NULL DEFAULT 0
        CHECK (priority BETWEEN 0 AND 4294967295),
    decision TEXT NOT NULL CHECK (decision IN ('allow', 'deny', 'ask')),
    match_kind TEXT NOT NULL
        CHECK (match_kind IN ('any', 'exact_host', 'exact_host_port')),
    match_host TEXT NOT NULL DEFAULT ''
        CHECK (length(CAST(match_host AS BLOB)) <= 253),
    match_port INTEGER NOT NULL DEFAULT 0
        CHECK (match_port BETWEEN 0 AND 65535),
    -- Design 06 closes the match language deliberately: a wildcard or regular
    -- expression is how an allow list quietly becomes an allow everything.
    CHECK (
        (match_kind = 'any' AND match_host = '' AND match_port = 0) OR
        (match_kind = 'exact_host' AND length(match_host) > 0 AND match_port = 0) OR
        (match_kind = 'exact_host_port' AND length(match_host) > 0 AND match_port > 0)
    ),
    -- The default answers every connection, so it cannot carry a target.
    CHECK (is_default = 0 OR match_kind = 'any'),
    -- INV-FIREWALL-NO-WILDCARD: a shipped default that allows everything makes
    -- the outbound firewall the one control that never fires. Allowing
    -- everything stays possible, but only as a rule a person wrote on purpose.
    CHECK (is_default = 0 OR decision <> 'allow')
) STRICT;

CREATE UNIQUE INDEX connection_rules_single_default
    ON connection_rules (is_default) WHERE is_default = 1;

CREATE INDEX connection_rules_precedence
    ON connection_rules (priority DESC, rule_id);
