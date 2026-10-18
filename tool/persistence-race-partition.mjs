import { spawn, spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdtempSync, mkdirSync, readFileSync, renameSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';

const pkg = 'github.com/vibe-agi/vibermate/internal/runtimepersistence';
const schema = 'vibermate-persistence-race/v1';
const flags = ['-race', '-short', '-p=1'];
const limits = ['-count=1', '-timeout=60m'];
const buildKeys = ['GOVERSION', 'GOOS', 'GOARCH', 'GOFLAGS', 'GOEXPERIMENT', 'CGO_ENABLED',
  'GOAMD64', 'GOARM64', 'GO386', 'GOARM', 'GOTOOLCHAIN', 'CC', 'CXX',
  'CGO_CFLAGS', 'CGO_CPPFLAGS', 'CGO_CXXFLAGS', 'CGO_LDFLAGS'];
const family = /^Test(?:Revision2|SchemaSwitch|SchemaMigration)/u;
const namePattern = /^(?:Test|Fuzz|Benchmark|Example)(?:[A-Z0-9_][A-Za-z0-9_]*)?$/u;
const hash = value => createHash('sha256').update(JSON.stringify(value)).digest('hex');
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
function requireThat(condition, message) { if (!condition) throw new Error(message); }
function command(executable, args) {
  const result = spawnSync(executable, args, { encoding: 'utf8', maxBuffer: 16 * 1024 * 1024 });
  if (result.stderr) process.stderr.write(result.stderr);
  requireThat(!result.error && result.status === 0, `${executable} ${args[0]} failed: ${result.error?.message ?? result.status}`);
  return result.stdout;
}
function sourceIdentity() {
  return {
    revision: command('git', ['rev-parse', 'HEAD']).trim(),
    tree: command('git', ['rev-parse', 'HEAD^{tree}']).trim(),
    status: command('git', ['status', '--porcelain=v1', '--untracked-files=all']).trimEnd(),
  };
}
function buildIdentity() {
  const value = JSON.parse(command('go', ['env', '-json', ...buildKeys]));
  requireThat(buildKeys.every(k => typeof value[k] === 'string'), 'incomplete Go build identity');
  return Object.fromEntries(buildKeys.map(k => [k, value[k]]));
}
function closedLines(text, pattern, label) {
  requireThat(text.endsWith('\n'), `${label} discovery must end with a newline`);
  const values = text.slice(0, -1).split('\n');
  requireThat(values.length > 0 && values.every(v => pattern.test(v)), `empty or malformed ${label} discovery`);
  requireThat(new Set(values).size === values.length, `duplicate ${label} discovery`);
  return values.sort();
}
function selection(catalog, lane) {
  const migration = catalog.filter(n => family.test(n));
  const rest = catalog.filter(n => !family.test(n));
  requireThat(migration.length && rest.length, 'empty persistence partition');
  requireThat(new Set([...migration, ...rest]).size === catalog.length, 'partition is not disjoint');
  return lane === 'migration' ? migration : rest;
}
function validateManifest(m) {
  requireThat(m.schema === schema && ['migration', 'rest'].includes(m.lane), 'invalid partition manifest');
  requireThat(m.package === pkg && same(m.flags, [...flags, ...limits]), 'changed race flags or package');
  requireThat(m.source && /^[a-f0-9]{40}$/u.test(m.source.revision) && /^[a-f0-9]{40}$/u.test(m.source.tree) && m.source.status === '', 'source must be an exact clean checkout');
  requireThat(m.build && same(Object.keys(m.build), buildKeys) && buildKeys.every(k => typeof m.build[k] === 'string'), 'invalid build identity');
  requireThat(Array.isArray(m.packages) && m.packages.includes(pkg) && same(m.packages, [...new Set(m.packages)].sort()), 'invalid package universe');
  requireThat(Array.isArray(m.catalog) && m.catalog.length && m.catalog.every(n => namePattern.test(n) && !n.startsWith('Benchmark')) && same(m.catalog, [...new Set(m.catalog)].sort()), 'invalid runnable catalog');
  requireThat(Array.isArray(m.benchmarks) && m.benchmarks.every(n => namePattern.test(n) && n.startsWith('Benchmark')) && same(m.benchmarks, [...new Set(m.benchmarks)].sort()), 'invalid benchmark catalog');
  requireThat(m.catalogDigest === hash({ catalog: m.catalog, benchmarks: m.benchmarks }), 'catalog digest mismatch');
  requireThat(same(m.selection, selection(m.catalog, m.lane)), 'selection is not the exact partition');
  requireThat(m.status === 'passed' && m.exitCode === 0 && !m.validationError, 'partition did not pass');
  requireThat(Array.isArray(m.outcomes) && same(m.outcomes.map(r => r.name), m.selection) && m.outcomes.every(r => ['pass', 'skip'].includes(r.status)), 'missing, duplicate, failed or unmatched root outcome');
}
function compare(paths) {
  requireThat(paths.length === 2, 'compare requires two manifest files');
  const values = paths.map(p => JSON.parse(readFileSync(p, 'utf8')));
  values.forEach(validateManifest);
  values.sort((a, b) => a.lane.localeCompare(b.lane));
  requireThat(values[0].lane === 'migration' && values[1].lane === 'rest', 'both distinct lanes are required');
  for (const key of ['source', 'build', 'packages', 'package', 'flags', 'catalog', 'benchmarks', 'catalogDigest']) {
    requireThat(same(values[0][key], values[1][key]), `partition ${key} mismatch`);
  }
  const union = values.flatMap(m => m.selection).sort();
  requireThat(same(union, values[0].catalog), 'partition union differs from runnable catalog');
  console.log(`Verified ${union.length} race entrypoints exactly once at ${values[0].source.revision}`);
}
async function run(lane, listOnly) {
  const manifestPath = process.env.RACE_PARTITION_MANIFEST;
  requireThat(manifestPath, 'RACE_PARTITION_MANIFEST is required');
  const packages = closedLines(readFileSync(0, 'utf8'), /^[A-Za-z0-9][A-Za-z0-9_.~+-]*(?:\/[A-Za-z0-9_.~+-]+)+$/u, 'package');
  requireThat(packages.includes(pkg), 'persistence package missing');
  const source = sourceIdentity();
  const build = buildIdentity();
  const temporary = mkdtempSync(join(tmpdir(), 'vibermate-persistence-race-'));
  let names;
  try {
    const binary = join(temporary, 'catalog.test');
    command('go', ['test', ...flags, pkg, '-c', '-o', binary]);
    names = closedLines(command(binary, ['-test.short', '-test.list=.']), namePattern, 'test');
  } finally { rmSync(temporary, { recursive: true, force: true }); }
  const catalog = names.filter(n => !n.startsWith('Benchmark'));
  const benchmarks = names.filter(n => n.startsWith('Benchmark'));
  const selected = selection(catalog, lane);
  const manifest = { schema, lane, package: pkg, source, build, packages, flags: [...flags, ...limits],
    catalog, benchmarks, catalogDigest: hash({ catalog, benchmarks }), selection: selected,
    status: 'discovered', exitCode: null, outcomes: [] };
  const save = () => {
    mkdirSync(dirname(resolve(manifestPath)), { recursive: true });
    const temporaryPath = `${manifestPath}.${process.pid}.tmp`;
    writeFileSync(temporaryPath, `${JSON.stringify(manifest, null, 2)}\n`);
    renameSync(temporaryPath, manifestPath);
  };
  save();
  if (listOnly) { console.log(selected.join('\n')); return 0; }
  // Catalog names are closed ASCII Go identifiers; anchoring selects whole
  // parents, so all dynamic descendants and ordinary fuzz seeds still run.
  const pattern = `^(${selected.join('|')})$`;
  manifest.status = 'running'; save();
  const starts = new Set();
  const outcomes = new Map();
  let validationError;
  let buffer = '';
  const consume = line => {
    try {
      const event = JSON.parse(line);
      if (!event.Test) return;
      const root = event.Test.split('/')[0];
      requireThat(event.Package === pkg && selected.includes(root), `unselected test outcome: ${event.Test}`);
      if (event.Test !== root) return;
      if (event.Action === 'run') {
        requireThat(!starts.has(root), `duplicate root start: ${root}`); starts.add(root);
      } else if (['pass', 'fail', 'skip'].includes(event.Action)) {
        requireThat(starts.has(root) && !outcomes.has(root), `unmatched or duplicate root outcome: ${root}`);
        outcomes.set(root, event.Action);
      }
    } catch (error) { validationError ??= error.message; }
  };
  const code = await new Promise(resolveExit => {
    const child = spawn('go', ['test', ...flags, pkg, ...limits, '-json', '-run', pattern], { stdio: ['ignore', 'pipe', 'inherit'] });
    child.stdout.setEncoding('utf8');
    child.stdout.on('data', chunk => {
      process.stdout.write(chunk); buffer += chunk.toString();
      let end;
      while ((end = buffer.indexOf('\n')) >= 0) { consume(buffer.slice(0, end)); buffer = buffer.slice(end + 1); }
    });
    child.on('error', error => { validationError ??= error.message; });
    child.on('close', status => resolveExit(status ?? 1));
  });
  if (buffer) validationError ??= 'unterminated test JSON output';
  if (selected.some(n => !starts.has(n) || !outcomes.has(n))) validationError ??= 'selected tests did not all execute';
  if ([...outcomes.values()].includes('fail')) validationError ??= 'failed root outcome';
  if (!same(source, sourceIdentity()) || !same(build, buildIdentity())) validationError ??= 'source or build changed during execution';
  manifest.outcomes = [...outcomes].sort(([a], [b]) => a < b ? -1 : a > b ? 1 : 0).map(([name, status]) => ({ name, status }));
  manifest.validationError = validationError ?? null;
  manifest.exitCode = code;
  manifest.status = code === 0 && !validationError ? 'passed' : 'failed'; save();
  if (validationError) console.error(`race partition: ${validationError}`);
  return code || (validationError ? 1 : 0);
}

try {
  const [mode, ...args] = process.argv.slice(2);
  if (mode === 'compare') compare(args);
  else {
    requireThat(['migration', 'rest'].includes(mode) && (args.length === 0 || (args.length === 1 && args[0] === '--list')), 'usage: persistence-race-partition.mjs migration|rest [--list], or compare file file');
    process.exitCode = await run(mode, args[0] === '--list');
  }
} catch (error) { console.error(`race partition: ${error.message}`); process.exitCode = 1; }
