-- Observations revision 1 -> 2: what each message transform script did to a
-- completed request. Additive; no existing row changes.
ALTER TABLE runtime_activities ADD COLUMN script_outcomes_json BLOB
  CHECK(script_outcomes_json IS NULL OR
  (kind = 'exchange.completed' AND
  length(script_outcomes_json) BETWEEN 2 AND 131072 AND
  json_valid(CAST(script_outcomes_json AS TEXT))));
