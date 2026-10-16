import assert from 'node:assert/strict';
import test from 'node:test';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../', import.meta.url));
const native = 'github.com/vibe-agi/vibermate/internal/nativemcp';
const persistence = 'github.com/vibe-agi/vibermate/internal/runtimepersistence';
const ordinary = [
  'github.com/vibe-agi/vibermate/cmd/future-command',
  `${native}extra`,
  `${native}/nested`,
  `${persistence}extra`,
  'example.org/another/runtimepersistence',
];
const raceOnly = 'github.com/vibe-agi/vibermate/internal/future-race-only';
const universe = [native, ...ordinary, persistence, raceOnly];
const shards = ['native', 'persistence', 'rest'];
const expected = { native: [native], persistence: [persistence], rest: [...ordinary, raceOnly] };

function fixture(t, overrides = {}) {
  const directory = mkdtempSync(join(tmpdir(), 'vibermate-race-shard-'));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  const calls = join(directory, 'calls.jsonl');
  const config = { packages: universe, raceOnly, listExit: 0, testExit: 0, ...overrides };
  writeFileSync(join(directory, 'config.json'), JSON.stringify(config));
  // Only the external Go process is replaced: execute the real Bash CLI.
  writeFileSync(join(directory, 'go'), `#!${process.execPath}
const fs = require('node:fs');
const config = JSON.parse(fs.readFileSync(process.env.FAKE_GO_CONFIG, 'utf8'));
const args = process.argv.slice(2);
fs.appendFileSync(process.env.FAKE_GO_CALLS, JSON.stringify({ args, goflags: process.env.GOFLAGS }) + '\\n');
if (args[0] === 'list') {
  const packages = args.includes('-race') ? config.packages : config.packages.filter(p => p !== config.raceOnly);
  process.stdout.write(packages.length ? packages.join('\\n') + '\\n' : '');
  process.exit(config.listExit);
}
if (args[0] === 'test') {
  process.stdout.write('test stdout inherited\\n');
  process.stderr.write('test stderr inherited\\n');
  process.exit(config.testExit);
}
process.exit(99);
`, { mode: 0o755 });
  return {
    run(args) {
      return spawnSync('bash', ['tool/test-race-shard.sh', ...args], {
        cwd: root, encoding: 'utf8', timeout: 10_000,
        env: { ...process.env, PATH: `${directory}:${process.env.PATH}`, GOFLAGS: '-mod=readonly -tags=fixture',
          FAKE_GO_CONFIG: join(directory, 'config.json'), FAKE_GO_CALLS: calls },
      });
    },
    calls() {
      try { return readFileSync(calls, 'utf8').trim().split('\n').map(line => JSON.parse(line)); }
      catch (error) { if (error.code === 'ENOENT') return []; throw error; }
    },
  };
}

test('race shards partition future, similarly named, and race-only packages exactly once', t => {
  const go = fixture(t);
  const membership = [];
  for (const shard of shards) {
    const result = go.run([shard, '--list']);
    assert.equal(result.status, 0, result.stderr);
    const packages = result.stdout.trim().split('\n');
    assert.deepEqual(packages, expected[shard]);
    membership.push(...packages);
  }
  assert.equal(new Set(membership).size, membership.length, 'shards must be disjoint');
  assert.deepEqual([...membership].sort(), [...universe].sort(), 'shards must cover the entire race universe');
  assert.deepEqual(go.calls(), shards.map(() => ({ args: ['list', '-race', './...'], goflags: '-mod=readonly -tags=fixture' })));
});

for (const shard of shards) {
  for (const status of [0, 23]) {
    test(`${shard} executes all selected packages with unchanged containment and propagates exit ${status}`, t => {
      const go = fixture(t, { testExit: status });
      const result = go.run([shard]);
      assert.equal(result.status, status, result.stderr);
      assert.match(result.stdout, /test stdout inherited/u);
      assert.match(result.stderr, /test stderr inherited/u);
      assert.deepEqual(go.calls(), [
        { args: ['list', '-race', './...'], goflags: '-mod=readonly -tags=fixture' },
        { args: ['test', '-race', '-short', '-p=1', ...expected[shard], '-count=1', '-timeout=60m'], goflags: '-mod=readonly -tags=fixture' },
      ]);
    });
  }
}

test('invalid arguments fail before discovery or execution', t => {
  for (const args of [[], ['unknown'], ['--list'], ['native', '--unknown'], ['rest', '--list', 'extra'], ['native', 'rest']]) {
    const go = fixture(t);
    const result = go.run(args);
    assert.notEqual(result.status, 0, JSON.stringify(args));
    assert.match(result.stderr, /usage/iu);
    assert.deepEqual(go.calls(), []);
  }
});

test('failed or malformed discovery never starts a test process for any shard', t => {
  const cases = [
    { listExit: 19 },
    { packages: [] },
    { packages: [...universe, native] },
    { packages: universe.filter(p => p !== native) },
    { packages: universe.filter(p => p !== persistence) },
    ...['', 'bad package', '-unsafe', './relative', 'example.org/pkg\r', 'example.org//empty'].map(p => ({ packages: [p, ...universe] })),
  ];
  for (const config of cases) {
    for (const shard of shards) {
      const go = fixture(t, config);
      const result = go.run([shard]);
      assert.notEqual(result.status, 0, `${shard}: ${JSON.stringify(config)}`);
      assert.match(result.stderr, /discovery|package/iu);
      assert.deepEqual(go.calls().map(call => call.args), [['list', '-race', './...']]);
    }
  }
});

test('an empty selected rest shard never starts a test process', t => {
  const go = fixture(t, { packages: [native, persistence] });
  const result = go.run(['rest']);
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /package/iu);
  assert.deepEqual(go.calls().map(call => call.args), [['list', '-race', './...']]);
});
