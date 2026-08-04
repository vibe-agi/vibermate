-- +goose Up
-- Request detail is keyed by the public Exchange identity. Keep both terminal
-- Activity lookup and its per-egress evidence proportional to that Exchange,
-- while leaving the existing global keyset indexes intact.
CREATE INDEX runtime_activities_exchange_subject
    ON runtime_activities (subject_id, sequence DESC)
    WHERE kind = 'exchange.completed';

CREATE INDEX runtime_egress_attempts_by_exchange
    ON runtime_egress_attempts (parent_exchange_id, sequence DESC)
    WHERE parent_exchange_id <> '';
