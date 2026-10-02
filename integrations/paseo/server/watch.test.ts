import assert from "node:assert/strict";
import { test } from "node:test";
import { setImmediate } from "node:timers/promises";
import type { PluginClientContext, PluginOpenScreenInput } from "@getpaseo/plugin/client";
import { watchSelections } from "../client/watch.ts";
import type { Pending } from "../shared/contracts.ts";

test("entry watcher opens a mobile-capable screen without a mounted sidebar, one selection at a time", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const opened: PluginOpenScreenInput[] = [];
  const pending = (id: string): Pending => ({ id, agentId: id, provider: "codex", cwd: "/p", expiresAt: Date.now() + 20_000, profiles: [] });
  let items = [pending("a"), pending("b")];
  let calls = 0;
  const client = { rpc: async () => { calls++; return { items }; }, openScreen: (input: PluginOpenScreenInput) => opened.push(input) };
  const close = watchSelections(client as unknown as Pick<PluginClientContext, "rpc" | "openScreen">);
  await setImmediate();
  assert.deepEqual(opened, [{ screenId: "profiles", params: { selection: "a" } }]);
  t.mock.timers.tick(1000);
  await setImmediate();
  assert.equal(opened.length, 1);
  items = [pending("b")];
  t.mock.timers.tick(1000);
  await setImmediate();
  assert.deepEqual(opened[1], { screenId: "profiles", params: { selection: "b" } });
  close();
  const before = calls;
  t.mock.timers.tick(10_000);
  await setImmediate();
  assert.equal(calls, before);
});

test("a disposed plugin cannot navigate after an in-flight poll resolves", async () => {
  let finish!: (value: { items: Pending[] }) => void;
  let navigated = false;
  const client = { rpc: () => new Promise((resolve) => { finish = resolve; }), openScreen: () => { navigated = true; } };
  const close = watchSelections(client as unknown as Pick<PluginClientContext, "rpc" | "openScreen">);
  close();
  finish({ items: [{ id: "a", agentId: "a", provider: "codex", cwd: "/p", expiresAt: Date.now(), profiles: [] }] });
  await setImmediate();
  assert.equal(navigated, false);
});
