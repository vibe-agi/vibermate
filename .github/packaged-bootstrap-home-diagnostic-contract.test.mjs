import assert from 'node:assert/strict';
import test from 'node:test';
import { readFileSync, writeFileSync, mkdtempSync, mkdirSync, rmSync, existsSync, symlinkSync, unlinkSync, readdirSync, statSync } from 'node:fs';
import { spawnSync, execFileSync } from 'node:child_process';
import { generateKeyPairSync } from 'node:crypto';
import { tmpdir } from 'node:os';
import { join, dirname } from 'node:path';

const workflow = readFileSync(new URL('./workflows/packaged-bootstrap-home-diagnostic.yml', import.meta.url), 'utf8');
const normal = readFileSync(new URL('./workflows/packaged-acceptance.yml', import.meta.url), 'utf8');
const job = (name) => workflow.match(new RegExp(`^  ${name}:\\n(?:(?!^  [\\w-]+:)[\\s\\S])*`, 'mu'))?.[0];
const producer = job('producer');
const consumer = job('consumer');
function shell(id) {
  const block = consumer?.match(new RegExp(`^        id: ${id}\\n(?:(?!^      -)[\\s\\S])*`, 'mu'))?.[0];
  const body = block?.match(/^        run: \|\n((?:          .*\n?)*)/mu)?.[1];
  assert.ok(body, `actual ${id} shell step is required`);
  return body.replace(/^          /gmu, '');
}
function fixture(run) {
  const root = mkdtempSync(join(tmpdir(), 'bootstrap-contract-'));
  try { run(root); } finally { rmSync(root, { recursive: true, force: true }); }
}
function execute(script, env) {
  return spawnSync('/bin/bash', ['-c', script], { env: { ...process.env, PATH: `${dirname(process.execPath)}:${process.env.PATH}`, ...env }, encoding: 'utf8', timeout: 10000 });
}

test('only the authorized dedicated push creates one producer and two fresh named-environment consumers', () => {
  assert.match(workflow, /^on:\n  push:\n    branches:\n      - diagnostic\/packaged-bootstrap-home-20261008\npermissions:/mu);
  assert.doesNotMatch(workflow, /workflow_dispatch|workflow_call|pull_request|schedule:|self-hosted|continue-on-error/u);
  assert.ok(consumer, 'fresh consumer job is required');
  assert.equal((workflow.match(/^  [\w-]+:\n    (?:needs:|environment:)/gmu) ?? []).length, 2);
  for (const value of ['needs: producer', 'fail-fast: false', 'policy: [isolated, login]']) assert.ok(consumer.includes(value), value);
  for (const section of [producer, consumer]) {
    assert.match(section, /environment: packaged-acceptance\n    runs-on: macos-15/u);
    assert.match(section, /DEVELOPER_DIR: \/Applications\/Xcode_16\.2\.app\/Contents\/Developer/u);
    assert.match(section, /ref: \$\{\{ github.sha \}\}/u);
    assert.ok(section.includes('git status --porcelain=v1 --untracked-files=all'));
  }
  assert.ok(consumer.includes('test "$(git rev-parse HEAD)" = "$EXPECTED_REVISION"'));
  assert.ok(consumer.includes('test "$GITHUB_SHA" = "$EXPECTED_REVISION"'));
  assert.match(workflow, /cancel-in-progress: false/u);
});

test('one pinned producer authenticates a shared immutable payload', () => {
  for (const value of [
    'artifact_id: ${{ steps.upload.outputs.artifact-id }}',
    'archive_sha256: ${{ steps.archive.outputs.sha256 }}',
    'revision: ${{ steps.source.outputs.revision }}',
    'build_macos_app.sh live', '-buildvcs=true -trimpath',
    'ditto --norsrc --noextattr --noacl --noqtn -X',
    '/usr/bin/tar -czf "$INPUT_ARCHIVE" -C "$PAYLOAD_ROOT" .',
    'COPYFILE_DISABLE=1', 'verify-packaged-diagnostic-verifier.mjs',
    'if-no-files-found: error', 'PRODUCER_EXITS',
  ]) assert.ok(producer.includes(value), value);
  assert.equal((producer.match(/build_macos_app\.sh live/gu) ?? []).length, 1);
  for (const command of ['./cmd/vibermate-acceptance\n', './cmd/vibermate-acceptance-verify\n']) assert.ok(producer.includes(command));
  for (const label of ['client_catalog', 'app_build', 'app_verify', 'harness_build', 'verifier_build', 'verifier_source', 'archive']) assert.ok(producer.includes(`${label}=%d`), label);
  const clientInstall = normal.match(/^          npm install[\s\S]*?^          printf 'FIXED_CLAUDE_PATH[^\n]*/mu)?.[0];
  assert.ok(producer.includes(clientInstall), 'normal fixed client installation and checks are reused verbatim');
  assert.doesNotMatch(producer, /--deterministic-only|--bootstrap-diagnostic/u);
  assert.match(consumer, /artifact-ids: \$\{\{ needs.producer.outputs.artifact_id \}\}/u);
  assert.doesNotMatch(consumer, /npm install|go build|build_macos_app|install_ci_cocoapods|codesign|chmod/u);
  const pins = new Set([
    'actions/checkout@11d5960a326750d5838078e36cf38b85af677262',
    'actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16',
    'actions/setup-node@a0853c24544627f65ddf259abe73b1d18a591444',
    'actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02',
    'actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093',
  ]);
  for (const [, action] of workflow.matchAll(/uses: ([^\s]+)/gu)) assert.ok(pins.has(action), action);
  assert.equal((workflow.match(/retention-days: 7/gu) ?? []).length, 3);
  for (const [, name] of workflow.matchAll(/          name: ((?:diagnostic-|encrypted-)[^\n]+)/gu)) {
    for (const identity of ['github.run_id', 'github.run_attempt', 'github.sha']) assert.ok(name.includes(identity), identity);
  }
});

test('public App builder inherits 0022 while parent statuses remain private under 0077', () => {
  const block = producer.match(/^          set \+e\n          [^\n]*build_macos_app\.sh live[^\n]*\n(?:          [^\n]*\n){4}/mu)?.[0];
  assert.ok(block, 'actual App invocation and parent exit capture are required');
  fixture((root) => {
    const tool = join(root, 'ui/flutter_app/tool');
    mkdirSync(tool, { recursive: true, mode: 0o700 });
    writeFileSync(join(tool, 'build_macos_app.sh'), `#!/bin/bash
set -euo pipefail
test "$#" -eq 1 && test "$1" = live
umask > "$PROBE_ROOT/child-mask"
"$PROBE_NODE" -e 'require("node:fs").writeFileSync(process.argv[1], "synthetic executable", {mode:0o777})' "$PROBE_ROOT/gui"
`, { mode: 0o755 });
    const statuses = join(root, 'producer-exits.txt');
    const result = execute(`set -euo pipefail
umask 077
cd "$PROBE_ROOT"
: > "$PRODUCER_EXITS"
${block.replace(/^          /gmu, '')}
umask > "$PROBE_ROOT/parent-mask"
`, { PROBE_ROOT: root, PROBE_NODE: process.execPath, PRODUCER_EXITS: statuses });
    assert.equal(result.status, 0, result.stderr);
    assert.equal(readFileSync(join(root, 'child-mask'), 'utf8').trim(), '0022');
    assert.equal(statSync(join(root, 'gui')).mode & 0o777, 0o755);
    assert.equal(readFileSync(join(root, 'parent-mask'), 'utf8').trim(), '0077');
    assert.equal(statSync(statuses).mode & 0o777, 0o600);
    assert.equal(readFileSync(statuses, 'utf8'), 'app_build=0\n');
  });
});

test('both arms preserve actual harness and fresh V7 verifier outcomes and private evidence', () => {
  assert.ok(consumer, 'consumer required');
  for (const value of [
    'DIAGNOSTIC_POLICY: ${{ matrix.policy }}', '--diagnostic-daemon-home "$DIAGNOSTIC_POLICY"',
    '--bootstrap-diagnostic "$EVIDENCE_ROOT/bootstrap.json"', '--deterministic-only --timeout 10m',
    'harness_exit=$?', 'verifier_exit=$?', "printf 'harness=%d", "printf 'verifier=%d",
    "always() && steps.transfer.outcome == 'success'", '--expected-mode deterministic',
    '--expected-schema vibermate.m0-assembly-acceptance/v7', '--expected-revision "$EXPECTED_REVISION"',
    '--expected-client-id claude-code', '--expected-client-version 2.1.220',
    '--source-root "$GITHUB_WORKSPACE"', '--desktop-app "$PAYLOAD_ROOT/ViberMate.app"',
    '--acceptance-executable "$PAYLOAD_ROOT/vibermate-acceptance"',
    '--client-entrypoint "$PAYLOAD_ROOT/client/node_modules/@anthropic-ai/claude-code-darwin-arm64/claude"',
    '4096', '0o600', 'identity.txt', 'exits.txt', 'diagnostic only',
    'test "$harness_exit" -eq 0', 'test "$verifier_exit" -eq 0',
  ]) assert.ok(consumer.includes(value), value);
  assert.ok(consumer.indexOf('id: transfer') < consumer.indexOf('id: acceptance'));
  assert.ok(consumer.indexOf('id: verifier') < consumer.indexOf('id: outcome'));
  assert.match(consumer, /name: Retain encrypted diagnostic only evidence\n        if: \$\{\{ always\(\) && steps.seal.outcome == 'success' \}\}/u);
  assert.match(consumer, /id: outcome\n        if: \$\{\{ always\(\) \}\}/u);
  assert.match(consumer, /name: encrypted-bootstrap-evidence-.*matrix.policy/u);
  assert.match(consumer, /path: \$\{\{ env.UPLOAD_ROOT \}\}\/evidence.sealed.json/u);
  assert.doesNotMatch(consumer, /path: \$\{\{ env.(?:STAGING_ROOT|EVIDENCE_ROOT) \}\}|path: .*\.tar\.gz|private-bootstrap/u);
  assert.doesNotMatch(workflow, /expected failure passed|release PASS|CFFIXED_USER_HOME|unset HOME|Keychain|stderr|\.log/u);
  assert.doesNotMatch(normal, /diagnostic-daemon-home|bootstrap-diagnostic/u);
});

test('actual seal shell exposes only encrypted upload bytes and no plaintext on sealer failure', () => {
  const script = shell('seal');
  const recipient = generateKeyPairSync('rsa', { modulusLength: 3072 });
  for (const validKey of [true, false]) fixture((root) => {
    const staging = join(root, 'staging'); const upload = join(root, 'upload'); const workspace = join(root, 'workspace'); const tool = join(workspace, 'tool');
    for (const path of [staging, upload, workspace, tool]) mkdirSync(path, { mode: 0o700 });
    writeFileSync(join(staging, 'bootstrap.json'), 'SYNTHETIC-STAGED-SENTINEL', { mode: 0o600 });
    writeFileSync(join(tool, 'seal-packaged-bootstrap-evidence.mjs'), readFileSync(new URL('../tool/seal-packaged-bootstrap-evidence.mjs', import.meta.url)));
    writeFileSync(join(tool, 'packaged-bootstrap-diagnostic-recipient.pem'), validKey ? recipient.publicKey.export({ type: 'spki', format: 'pem' }) : 'invalid synthetic public key');
    const result = execute(script, { TASK_ROOT: root, STAGING_ROOT: staging, UPLOAD_ROOT: upload, GITHUB_WORKSPACE: workspace });
    assert.equal(result.status === 0, validKey, result.stderr);
    assert.equal(existsSync(join(root, 'evidence.tar.gz')), true, 'plaintext archive stays outside upload target');
    assert.deepEqual(readdirSync(upload), validKey ? ['evidence.sealed.json'] : []);
    if (validKey) {
      const output = join(upload, 'evidence.sealed.json');
      assert.equal(statSync(output).mode & 0o777, 0o600);
      const sealed = readFileSync(output);
      assert.equal(sealed.includes('SYNTHETIC-STAGED-SENTINEL'), false);
      assert.equal(JSON.parse(sealed).schema, 'vibermate.private-bootstrap-evidence/v1');
    }
  });
});

test('actual final shell fails closed for nonzero, absent, duplicate or malformed exits', () => {
  const script = shell('outcome');
  const cases = [
    ['harness=0\nverifier=0\n', true],
    ['harness=7\nverifier=0\n', false],
    ['harness=0\nverifier=8\n', false],
    ['harness=0\n', false], ['verifier=0\n', false],
    ['harness=not-run\nverifier=0\n', false], ['harness=0\nverifier=oops\n', false],
    ['harness=0\nverifier=0\nharness=0\n', false],
    ['harness=0\nverifier=0\nextra=0\n', false],
  ];
  for (const [statuses, success] of cases) fixture((root) => {
    writeFileSync(join(root, 'exits.txt'), statuses);
    const result = execute(script, { EVIDENCE_ROOT: root, GITHUB_STEP_SUMMARY: join(root, 'summary') });
    assert.equal(result.status === 0, success, `${JSON.stringify(statuses)}: ${result.stderr}`);
  });
  fixture((root) => assert.notEqual(execute(script, { EVIDENCE_ROOT: root, GITHUB_STEP_SUMMARY: join(root, 'summary') }).status, 0));
});

test('actual archive shell rejects mismatches before extraction and accepts authenticated bytes', () => {
  const script = shell('authenticate');
  fixture((root) => {
    const download = join(root, 'download'); const payload = join(root, 'payload'); const source = join(root, 'source');
    for (const path of [download, payload, source]) mkdirSync(path, { mode: 0o700 });
    writeFileSync(join(source, 'authenticated.txt'), 'same producer bytes');
    const archive = join(download, 'packaged-inputs.tar.gz');
    execFileSync('/usr/bin/tar', ['-czf', archive, '-C', source, '.'], { env: { ...process.env, COPYFILE_DISABLE: '1' } });
    const env = { DOWNLOAD_ROOT: download, PAYLOAD_ROOT: payload, EXPECTED_DIGEST: '0'.repeat(64) };
    assert.notEqual(execute(script, env).status, 0);
    assert.equal(existsSync(join(payload, 'authenticated.txt')), false, 'mismatch must not extract any member');
    env.EXPECTED_DIGEST = execFileSync('/usr/bin/shasum', ['-a', '256', archive], { encoding: 'utf8' }).split(' ')[0];
    const digest = env.EXPECTED_DIGEST;
    env.EXPECTED_DIGEST = 'malformed';
    assert.notEqual(execute(script, env).status, 0);
    env.EXPECTED_DIGEST = digest;
    writeFileSync(join(download, 'unexpected.txt'), 'extra');
    assert.notEqual(execute(script, env).status, 0);
    unlinkSync(join(download, 'unexpected.txt'));
    const bytes = readFileSync(archive);
    unlinkSync(archive);
    assert.notEqual(execute(script, env).status, 0);
    const target = join(root, 'elsewhere.tar.gz');
    writeFileSync(target, bytes);
    symlinkSync(target, archive);
    assert.notEqual(execute(script, env).status, 0);
    assert.equal(existsSync(join(payload, 'authenticated.txt')), false);
    unlinkSync(archive);
    writeFileSync(archive, bytes);
    assert.equal(execute(script, env).status, 0);
    assert.equal(readFileSync(join(payload, 'authenticated.txt'), 'utf8'), 'same producer bytes');
  });
});
