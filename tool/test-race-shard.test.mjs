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
const runtimeData = 'github.com/vibe-agi/vibermate/internal/runtimedata';
const runtimeProduct = 'github.com/vibe-agi/vibermate/internal/productruntime';
const isolated = [native, persistence, runtimeData, runtimeProduct];
const ordinary = [
  'github.com/vibe-agi/vibermate/cmd/future-command',
  `${native}extra`,
  `${native}/nested`,
  `${persistence}extra`,
  `${runtimeData}extra`,
  `${runtimeData}/nested`,
  `${runtimeProduct}extra`,
  `${runtimeProduct}/nested`,
  'example.org/another/runtimepersistence',
];
const raceOnly = 'github.com/vibe-agi/vibermate/internal/future-race-only';
const universe = [native, ...ordinary, persistence, runtimeData, runtimeProduct, raceOnly];
const shards = ['native', 'persistence', 'runtime-data', 'runtime-product', 'rest'];
const expected = { native: [native], persistence: [persistence], 'runtime-data': [runtimeData], 'runtime-product': [runtimeProduct], rest: [...ordinary, raceOnly] };

const catalog = ['TestSchemaSwitchFuture', 'TestRevision2Future', 'TestSchemaMigrationFuture',
  'TestContentSourceFuture', 'TestContentSourceBoundsManifestBytesBeforeScan', 'TestNotContentSource', 'TestOrdinary', 'TestNotSchemaSwitch', 'TestRaceOnly', 'FuzzFuture', 'ExampleFuture', 'Example', 'BenchmarkFuture'];
const splitModes = ['persistence-migration', 'persistence-rest'];

function fakeGoMain() {
  const fs = require('node:fs');
  const config = JSON.parse(fs.readFileSync(process.env.FAKE_GO_CONFIG, 'utf8'));
  const args = process.argv.slice(2);
  fs.appendFileSync(process.env.FAKE_GO_CALLS, JSON.stringify({ args, goflags: process.env.GOFLAGS }) + '\n');
  if (args[0] === 'list') {
    const packages = args.includes('-race') ? config.packages : config.packages.filter(p => p !== config.raceOnly);
    process.stdout.write(packages.length ? packages.join('\n') + '\n' : '');
    process.exit(config.listExit);
  }
  if (args[0] === 'env') {
    const defaults = { GOOS: 'linux', GOARCH: 'amd64', GOVERSION: 'go1.26.9', CGO_ENABLED: '1', GOFLAGS: process.env.GOFLAGS };
    process.stdout.write(JSON.stringify(Object.fromEntries(args.slice(2).map(k => [k, (config.build || {})[k] ?? defaults[k] ?? '']))));
    process.exit(config.envExit || 0);
  }
  if (args[0] === 'test' && args.includes('-c')) {
    if (config.buildExit) process.exit(config.buildExit);
    const binary = args[args.indexOf('-o') + 1];
    fs.writeFileSync(binary, '#!' + process.execPath + '\n' + `
const fs = require('node:fs');
const c = JSON.parse(fs.readFileSync(process.env.FAKE_GO_CONFIG, 'utf8'));
fs.appendFileSync(process.env.FAKE_GO_CALLS, JSON.stringify({args:['catalog',...process.argv.slice(2)],goflags:process.env.GOFLAGS})+'\\n');
process.stdout.write(c.catalogText ?? (c.catalog.length ? c.catalog.join('\\n')+'\\n' : ''));
process.exit(c.catalogExit || 0);
`, { mode: 0o755 });
    process.exit(0);
  }
  if (args[0] === 'test') {
    if (!args.includes('-json')) {
      process.stdout.write('test stdout inherited\n');
      process.stderr.write('test stderr inherited\n');
      process.exit(config.testExit);
    }
    const pattern = new RegExp(args[args.indexOf('-run') + 1]);
    const names = config.catalog.filter(n => !n.startsWith('Benchmark') && pattern.test(n));
    const emit = event => process.stdout.write(JSON.stringify({Package:'github.com/vibe-agi/vibermate/internal/runtimepersistence',...event})+'\n');
    for (const name of names) {
      if (config.omitAll || config.omit === name) continue;
      emit({Action:'run',Test:name});
      emit({Action:'run',Test:name+'/dynamic/child'});
      emit({Action:'pass',Test:name+'/dynamic/child'});
      emit({Action:config.testExit ? 'fail' : 'pass',Test:name});
    }
    if (config.unselected) emit({Action:'pass',Test:'TestUnselected'});
    if (config.duplicateOutcome && names.length) emit({Action:'pass',Test:names[0]});
    if (config.invalidJSON) process.stdout.write('not json\n');
    process.stderr.write('test stderr inherited\n');
    process.exit(config.testExit);
  }
  process.exit(99);
}

function fixture(t, overrides = {}) {
  const directory = mkdtempSync(join(tmpdir(), 'vibermate-race-shard-'));
  t.after(() => rmSync(directory, { recursive: true, force: true }));
  const calls = join(directory, 'calls.jsonl');
  const config = { packages: universe, raceOnly, catalog, listExit: 0, testExit: 0, ...overrides };
  writeFileSync(join(directory, 'config.json'), JSON.stringify(config));
  // Only external Go is replaced; execute the real Bash/Node orchestration.
  writeFileSync(join(directory, 'go'), `#!${process.execPath}\n(${fakeGoMain.toString()})();\n`, { mode: 0o755 });
  return {
    directory,
    manifest(mode) { return join(directory, `${mode}.json`); },
    run(args) {
      return spawnSync('bash', ['tool/test-race-shard.sh', ...args], {
        cwd: root, encoding: 'utf8', timeout: 15_000,
        env: { ...process.env, PATH: `${directory}:${process.env.PATH}`, GOFLAGS: '-mod=readonly -tags=fixture',
          RACE_PARTITION_MANIFEST: join(directory, `${args[0]}.json`),
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
    ...isolated.map(p => ({ packages: [...universe, p] })),
    ...isolated.map(p => ({ packages: universe.filter(candidate => candidate !== p) })),
    ...['', 'bad package', '-unsafe', './relative', 'example.org/pkg\r', 'example.org//empty'].map(p => ({ packages: [p, ...universe] })),
  ];
  for (const config of cases) {
    for (const shard of [...shards, ...splitModes]) {
      const go = fixture(t, config);
      const result = go.run([shard]);
      assert.notEqual(result.status, 0, `${shard}: ${JSON.stringify(config)}`);
      assert.match(result.stderr, /discovery|package/iu);
      assert.deepEqual(go.calls().map(call => call.args), [['list', '-race', './...']]);
    }
  }
});

test('an empty selected rest shard never starts a test process', t => {
  const go = fixture(t, { packages: isolated });
  const result = go.run(['rest']);
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /package/iu);
  assert.deepEqual(go.calls().map(call => call.args), [['list', '-race', './...']]);
});


test('split persistence catalog covers Test, Example, Fuzz and future race roots exactly once', t => {
  const go = fixture(t);
  const selected = [];
  for (const mode of splitModes) {
    const result = go.run([mode, '--list']);
    assert.equal(result.status, 0, result.stderr);
    const manifest = JSON.parse(readFileSync(go.manifest(mode)));
    assert.equal(manifest.status, 'discovered');
    assert.deepEqual(result.stdout.trim().split('\n'), manifest.selection);
    const moved = ['TestContentSourceFuture', 'TestContentSourceBoundsManifestBytesBeforeScan'];
    for (const name of moved) assert.equal(manifest.selection.includes(name), mode === 'persistence-migration');
    assert.equal(manifest.selection.includes('TestNotContentSource'), mode === 'persistence-rest');
    assert.deepEqual(manifest.catalog, catalog.filter(n => !n.startsWith('Benchmark')).sort());
    assert.deepEqual(manifest.benchmarks, ['BenchmarkFuture']);
    selected.push(...manifest.selection);
  }
  assert.equal(new Set(selected).size, selected.length);
  assert.deepEqual(selected.sort(), catalog.filter(n => !n.startsWith('Benchmark')).sort());
  const calls = go.calls();
  assert(calls.filter(c => c.args.includes('-c')).every(c => c.args.includes('-race') && c.args.includes('-short') && c.args.includes('-p=1')));
  assert(!calls.some(c => c.args.includes('-run')), 'list must not run cases');
});

for (const mode of splitModes) for (const status of [0, 23]) {
  test(`${mode} runs exact whole parents with original race flags and propagates ${status}`, t => {
    const go = fixture(t, { testExit: status });
    const result = go.run([mode]);
    assert.equal(result.status, status, result.stderr);
    const manifest = JSON.parse(readFileSync(go.manifest(mode)));
    const call = go.calls().find(c => c.args.includes('-run'));
    assert.deepEqual(call.args.slice(0, 8), ['test','-race','-short','-p=1',persistence,'-count=1','-timeout=120m','-json']);
    assert.equal(call.goflags, '-mod=readonly -tags=fixture');
    const pattern = call.args[call.args.indexOf('-run')+1];
    assert(pattern.startsWith('^(') && pattern.endsWith(')$') && !pattern.includes('/'));
    assert.deepEqual(manifest.outcomes.map(r => r.name).sort(), manifest.selection);
    assert.equal(manifest.status, status ? 'failed' : 'passed');
    assert.match(result.stdout, /dynamic\/child/);
    assert.match(result.stderr, /test stderr inherited/);
  });
}

test('split discovery fails closed before execution on all malformed or empty catalogs', t => {
  const invalid = [{envExit:11},{buildExit:12},{catalogExit:13},{catalogText:'TestNoNewline'},{catalog:[]},{catalog:['BenchmarkOnly']},
    {catalog:['TestOrdinary']},{catalog:['TestRevision2Only']},{catalog:[...catalog,'TestOrdinary']},
    ...['','-unsafe','TestA/child','Test bad','TestX\r','UnknownThing'].map(n => ({catalog:[...catalog,n]}))];
  for (const config of invalid) for (const mode of splitModes) {
    const go = fixture(t, config);
    const result = go.run([mode]);
    assert.notEqual(result.status, 0, `${mode}: ${JSON.stringify(config)}`);
    assert(!go.calls().some(c => c.args.includes('-run')));
  }
});

test('split success cannot hide absent, extra, duplicate, or malformed execution outcomes', t => {
  for (const config of [{omitAll:true}, {omit:'TestRevision2Future'}, {unselected:true}, {duplicateOutcome:true}, {invalidJSON:true}]) {
    const go = fixture(t, config);
    const result = go.run(['persistence-migration']);
    assert.notEqual(result.status, 0, JSON.stringify(config));
    assert.equal(JSON.parse(readFileSync(go.manifest('persistence-migration'))).status, 'failed');
  }
});

function compare(files) {
  return spawnSync(process.execPath, ['tool/persistence-race-partition.mjs','compare',...files], {cwd:root,encoding:'utf8'});
}

test('comparison requires identical clean source/build/catalog and two complete disjoint passing lanes', t => {
  const go = fixture(t);
  for (const mode of splitModes) assert.equal(go.run([mode]).status, 0);
  const files = splitModes.map(mode => go.manifest(mode));
  // Contract tests run in an intentionally edited worktree; model hosted clean checkouts here.
  const originals = files.map(file => ({...JSON.parse(readFileSync(file)), source:{...JSON.parse(readFileSync(file)).source,status:''}}));
  const save = values => values.forEach((v,i) => writeFileSync(files[i], JSON.stringify(v)));
  save(originals); assert.equal(compare(files).status,0);
  const mutations = [m => m.source.revision='1'.repeat(40), m => m.source.status=' M dirty',
    m => m.build.GOARCH='arm64', m => m.catalog.push('TestExtra'), m => m.packages.push('example.org/extra'),
    m => m.status='discovered', m => m.status='failed', m => m.lane='migration',
    m => m.selection.pop(), m => m.outcomes.pop(), m => m.outcomes[0].status='fail'];
  for (const mutate of mutations) {
    const values=structuredClone(originals); mutate(values[1]); save(values);
    assert.notEqual(compare(files).status,0);
  }
  save(originals); assert.notEqual(compare([files[0],files[0]]).status,0);
});

test('workflow runs both persistence lanes and requires manifest comparison without changing other campaigns', () => {
  const source=readFileSync(join(root,'.github/workflows/ci.yml'),'utf8');
  assert.match(source,/shard: \[native, persistence-migration, persistence-rest, runtime-data, runtime-product, rest\]/);
  assert.match(source,/race-coverage:[\s\S]*?needs: race[\s\S]*?persistence-race-partition\.mjs compare/);
  assert.match(source,/RACE_PARTITION_MANIFEST:/);
  const raceJob=source.split('  race:')[1].split('  race-coverage:')[0];
  assert.match(raceJob,/fail-fast: false/);
  assert.match(raceJob,/timeout-minutes: 150/);
  assert.match(raceJob,/if: always\(\) && startsWith\(matrix.shard, 'persistence-'\)/);
  assert.match(raceJob,/if-no-files-found: error/);
  assert.match(source,/name: race-\$\{\{ matrix\.shard \}\}-\$\{\{ github\.run_attempt \}\}/);
  assert.match(source,/pattern: race-persistence-\*-\$\{\{ github\.run_attempt \}\}/);
  assert.match(source,/store-crash-race:[\s\S]*?make test-store-crash-race/);
  assert.match(source,/store-response-race:[\s\S]*?make test-store-response-race/);
  assert(!source.includes('continue-on-error: true'));
});
