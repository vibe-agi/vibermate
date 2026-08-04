-- +goose Up
-- The public Activity collection is an Exchange projection. Filtering before
-- keyset pagination is required for full pages without skips, and the partial
-- index keeps that read proportional to Exchange evidence rather than every
-- local control-plane event.
CREATE INDEX runtime_activities_exchange_latest
    ON runtime_activities (sequence DESC)
    WHERE kind = 'exchange.completed';
