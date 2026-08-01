-- +goose Up
-- One ApprovalCenter serves more than one kind. A network ask is decided
-- before any Access is resolved, so its plan binding columns must be optional;
-- the tool-intent kind still supplies them and its validation still requires
-- them. Existing rows are tool intents and keep their binding.
ALTER TABLE tool_approvals
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'tool_intent'
        CHECK (kind IN ('tool_intent', 'network_ask'));

ALTER TABLE tool_approvals
    ADD COLUMN aggregate_key TEXT NOT NULL DEFAULT ''
        CHECK (length(CAST(aggregate_key AS BLOB)) <= 512);

ALTER TABLE tool_approvals
    ADD COLUMN request_count INTEGER NOT NULL DEFAULT 1
        CHECK (request_count > 0);

ALTER TABLE tool_approvals
    ADD COLUMN waiter_count INTEGER NOT NULL DEFAULT 1
        CHECK (waiter_count > 0);

-- A pre-migration row has no aggregate key. Its identity is unique and stable,
-- so it becomes its own key rather than being merged with anything.
UPDATE tool_approvals
   SET aggregate_key = 'legacy:' || approval_id
 WHERE aggregate_key = '';
