-- +goose Up
-- The interim rule was an explicit placeholder for the ask path: it allowed
-- every unmatched host so the proxy stayed usable while nothing could block a
-- connection on a person. That path exists now, an answer can be remembered,
-- and the window can put the question in front of someone, so the placeholder
-- is removed and the released default becomes `ask` as design 06 §4.1
-- requires.
--
-- Rules a person wrote are untouched. Only the rule this product shipped, by
-- the name it shipped under, is withdrawn.
DELETE FROM connection_rules
 WHERE rule_id = 'interim.allow-unmatched-pending-ask';

UPDATE connection_rules
   SET rule_id = 'default.ask',
       decision = 'ask'
 WHERE is_default = 1
   AND rule_id = 'default.deny'
   AND decision = 'deny';

UPDATE connection_rule_sets
   SET revision = revision + 1
 WHERE id = 1;
