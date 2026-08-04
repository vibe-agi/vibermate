import assert from "node:assert/strict";
import test from "node:test";
import { immutableActionReference } from "./check-action-pins.mjs";

test("workflow dependencies require immutable commit or container digests", () => {
  assert.equal(immutableActionReference("./.github/actions/local"), true);
  assert.equal(
    immutableActionReference(`actions/checkout@${"a".repeat(40)}`),
    true,
  );
  assert.equal(
    immutableActionReference(`docker://example.test/tool@sha256:${"b".repeat(64)}`),
    true,
  );
  assert.equal(immutableActionReference("actions/checkout@v4"), false);
  assert.equal(immutableActionReference("owner/action@main"), false);
  assert.equal(immutableActionReference("docker://example.test/tool:latest"), false);
});
