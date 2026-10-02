import assert from "node:assert/strict";
import { test } from "node:test";
import { mkdtemp, rm, writeFile, readdir, realpath } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { Bindings } from "./bindings.ts";
import { Selections } from "./selections.ts";
import { profiles } from "./vibermate.ts";
import contribute from "../index.server.ts";
import { connectionSettings, serverSchema, type Connection } from "../shared/contracts.ts";
import type { PluginServerContext, PluginSessionOpenRequest } from "@getpaseo/plugin/server";

const run = promisify(execFile);
const choices = [{ id: "work", name: "Work" }, { id: "personal", name: "Personal" }];

test("server origins retain HTTPS and normalize ports for the native CLI without credentials or paths", () => {
  assert.equal(serverSchema.parse("https://runtime.example.com/"), "https://runtime.example.com:443");
  assert.equal(serverSchema.parse("http://127.0.0.1:9666"), "http://127.0.0.1:9666");
  assert.equal(serverSchema.parse("https://[::1]:9666"), "https://[::1]:9666");
  assert.equal(serverSchema.parse(""), "");
  for (const value of ["https://user:private@runtime.example.com", "https://runtime.example.com/path", "https://runtime.example.com?token=private", "file:///tmp/private", "https://runtime.example.com#private"])
    assert.equal(serverSchema.safeParse(value).success, false);
  assert.equal(connectionSettings.schema.safeParse({ stateDirectory: "../bindings" }).success, false);
});

test("concurrent choices are isolated, persisted before admission, and restored after restart", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "vibermate-paseo-bindings-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const bindings = new Bindings(directory);
  const selections = new Selections();
  const input = { provider: "codex", cwd: "/projects/shop", profiles: choices, server: "https://runtime.example.com:443", bindings };
  const first = selections.request({ ...input, agentId: "conversation/a" }, new AbortController().signal);
  const second = selections.request({ ...input, agentId: "conversation/b" }, new AbortController().signal);
  const [a, b] = selections.pending();
  await assert.rejects(selections.request({ ...input, agentId: "conversation/a" }, new AbortController().signal), /already waiting/);
  await assert.rejects(selections.choose(a.id, "not-offered"), /offered/);
  await selections.choose(b.id, "personal");
  await selections.choose(a.id, "work");
  assert.equal((await first).environmentId, "work");
  assert.equal((await second).environmentId, "personal");
  assert.deepEqual(await new Bindings(directory).get("conversation/a"), {
    agentId: "conversation/a", environmentId: "work", server: input.server,
  });
  assert.equal((await new Bindings(directory).get("conversation/b"))?.environmentId, "personal");
  assert.equal(await bindings.get("unknown"), null);
  await assert.rejects(selections.choose(a.id, "personal"), /ended/);
  assert.equal((await readdir(directory)).length, 2);
});

test("cancel, timeout, aborted hooks, shutdown, and failed persistence never admit startup", async () => {
  let saved = 0;
  const bindings = { async put() { saved++; } };
  const input = { agentId: "a", provider: "claude", cwd: "/project", profiles: choices, server: "", bindings };
  const selections = new Selections();
  const canceled = assert.rejects(selections.request(input, new AbortController().signal), /canceled/);
  selections.cancel(selections.pending()[0].id);
  await canceled;
  await assert.rejects(selections.request(input, new AbortController().signal, 5), /timed out/);
  const lifetime = new AbortController();
  const aborted = assert.rejects(selections.request(input, lifetime.signal), /canceled/);
  lifetime.abort();
  await aborted;
  await assert.rejects(selections.request(input, lifetime.signal), /canceled/);
  const stopped = assert.rejects(selections.request(input, new AbortController().signal), /stopped/);
  selections.close();
  await stopped;
  assert.equal(saved, 0);
  assert.deepEqual(selections.pending(), []);
  const broken = { async put() { throw new Error("disk full"); } };
  const failed = assert.rejects(selections.request({ ...input, bindings: broken }, new AbortController().signal), /could not be saved/);
  await assert.rejects(selections.choose(selections.pending()[0].id, "work"), /disk full/);
  await failed;
});

test("a competing client cannot replace a confirmed choice while it is being committed", async () => {
  let commit!: () => void;
  const bindings = { put: () => new Promise<void>((resolve) => { commit = resolve; }) };
  const selections = new Selections();
  const pending = selections.request({ agentId: "a", provider: "codex", cwd: "/p", profiles: choices, server: "", bindings }, new AbortController().signal);
  const id = selections.pending()[0].id;
  const chosen = selections.choose(id, "work");
  await assert.rejects(selections.choose(id, "personal"), /ended/);
  commit();
  await chosen;
  assert.equal((await pending).environmentId, "work");
});

test("the production hook reuses the conversation's server and profile across plugin reloads", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "vibermate-paseo-hook-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const config = connectionSettings.schema.parse({ enabled: true, stateDirectory: directory, server: "https://new.example:443" });
  await new Bindings(directory).put({ agentId: "a", environmentId: "personal", server: "https://original.example:443" });
  let hook!: (input: { request: PluginSessionOpenRequest }, context: { signal: AbortSignal }) => Promise<PluginSessionOpenRequest>;
  const server = {
    registerSettings: () => ({ read: async () => ({ status: "ready", revision: "1", values: config }) }),
    handle() {},
    before(_name: string, callback: typeof hook) { hook = callback; return () => {}; },
  };
  const request: PluginSessionOpenRequest = { agentId: "a", workspaceId: "w", provider: "codex", cwd: "/p", reason: "resume", purpose: "interactive", env: { EXISTING: "kept" } };
  for (const reason of ["resume", "refresh"] as const) {
    const close = contribute(server as unknown as PluginServerContext);
    const result = await hook({ request: { ...request, reason } }, { signal: new AbortController().signal });
    assert.equal(result.env.VIBERMATE_PASEO_ENVIRONMENT_ID, "personal");
    assert.equal(result.env.VIBERMATE_PASEO_SERVER, "https://original.example:443");
    assert.equal(result.env.EXISTING, "kept");
    const unrelated = { ...request, provider: "pi" };
    assert.deepEqual(await hook({ request: unrelated }, { signal: new AbortController().signal }), unrelated);
    close();
  }
});

test("launchers preserve argv and cwd, pass probes through, and refuse a conversation without a profile", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "vibermate paseo launch-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const fake = join(directory, "fake cli");
  await writeFile(fake, `#!${process.execPath}\nprocess.stdout.write(JSON.stringify({args:process.argv.slice(2),cwd:process.cwd()}));\n`, { mode: 0o700 });
  const base = { ...process.env, PASEO_AGENT_ID: "", PASEO_AGENT_CWD: "", VIBERMATE_PASEO_ENVIRONMENT_ID: "", VIBERMATE_PASEO_SERVER: "", VIBERMATE_PASEO_CLI: fake, VIBERMATE_PASEO_CLAUDE_BINARY: fake, VIBERMATE_PASEO_CODEX_BINARY: fake };
  for (const provider of ["claude", "codex"]) {
    const wrapper = fileURLToPath(new URL(`../bin/${provider}`, import.meta.url));
    const probe = await run("sh", [wrapper, "--version"], { env: base });
    assert.deepEqual(JSON.parse(probe.stdout).args, ["--version"]);
    await assert.rejects(run("sh", [wrapper, "app-server"], { env: { ...base, PASEO_AGENT_ID: "a" } }), /no selected profile/);
    const args = ["app-server", "--config", 'value="spaces and $literal"'];
    for (const server of ["", "https://runtime.example.com:443"]) {
      const launched = await run("sh", [wrapper, ...args], { env: { ...base, PASEO_AGENT_ID: "a", PASEO_AGENT_CWD: directory, VIBERMATE_PASEO_ENVIRONMENT_ID: "work", VIBERMATE_PASEO_SERVER: server } });
      assert.deepEqual(JSON.parse(launched.stdout), { args: ["run", ...(server ? ["--server", server] : []), "--env", "work", "--", fake, ...args], cwd: await realpath(directory) });
    }
  }
});

test("profile queries use the CLI login and reject malformed, duplicate or secret-bearing catalogs", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "vibermate-paseo-catalog-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const fake = join(directory, "vibermate");
  const connection: Connection = { enabled: true, cli: fake, server: "", stateDirectory: directory };
  for (const page of [
    { schema: "vibermate-capture-environments/v1", items: choices },
    { schema: "old-version", items: choices },
    { schema: "vibermate-capture-environments/v1", items: [choices[0], choices[0]] },
    { schema: "vibermate-capture-environments/v1", items: [{ ...choices[0], credential: "private" }] },
  ]) {
    await writeFile(fake, `#!${process.execPath}\nif(JSON.stringify(process.argv.slice(2))!==JSON.stringify(["profiles","--json"]))process.exit(1);\nprocess.stdout.write(${JSON.stringify(JSON.stringify(page))});\n`, { mode: 0o700 });
    if (page.items === choices && page.schema === "vibermate-capture-environments/v1") assert.deepEqual(await profiles(connection), choices);
    else await assert.rejects(profiles(connection));
  }
});
