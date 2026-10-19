import assert from 'node:assert/strict';
import test from 'node:test';
import { readFileSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const workflow = readFileSync(new URL('./workflows/packaged-acceptance.yml', import.meta.url), 'utf8');
const ci = readFileSync(new URL('./workflows/ci.yml', import.meta.url), 'utf8');
const make = readFileSync(new URL('../Makefile', import.meta.url), 'utf8');

test('test-race dry run invokes every package with finite sequential containment', () => {
  const command = execFileSync('make', ['-n', 'test-race'], {
    cwd: fileURLToPath(new URL('../', import.meta.url)),
    encoding: 'utf8',
    timeout: 10_000,
  });
  assert.equal(command.trim(), 'go test -race -short -p=1 ./... -count=1 -timeout=60m');
});

function assertRaceGate(source) {
  const job = source.match(/^  race:\n(?:(?!^  [\w-]+:)[\s\S])*/mu)?.[0];
  assert.ok(job, 'complete short race job is required');
  assert.match(job, /^    needs: generated-structural$/mu);
  assert.match(job, /^    runs-on: ubuntu-latest$/mu);
  assert.match(job, /^    timeout-minutes: 150$/mu);
  const strategy = job.match(/^    strategy:\n(?:(?!^    \S)[\s\S])*/mu)?.[0];
  assert.equal(strategy?.trimEnd(), '    strategy:\n      fail-fast: false\n      matrix:\n        shard: [native, persistence-migration, persistence-rest, rest]');
  assert.match(job, /^      - run: bash tool\/test-race-shard\.sh \$\{\{ matrix\.shard \}\}$/mu);
  assert.doesNotMatch(job, /continue-on-error|\boptional\b|\bretry\b|\|\||\bGOFLAGS\b/u);
  assert.doesNotMatch(job, /^    if:/mu);
  const steps = job.split(/^      - /mu).slice(1);
  const conditional = steps.filter(step => /^        if:/mu.test(step));
  assert.equal(conditional.length, 2);
  assert.match(conditional[0], /^uses: actions\/setup-node@/u);
  assert.match(conditional[0], /^        if: startsWith\(matrix.shard, 'persistence-'\)$/mu);
  assert.match(conditional[1], /^uses: actions\/upload-artifact@/u);
  assert.match(conditional[1], /^        if: always\(\) && startsWith\(matrix.shard, 'persistence-'\)$/mu);
  const coverage = source.match(/^  race-coverage:\n(?:(?!^  [\w-]+:)[\s\S])*/mu)?.[0];
  assert.ok(coverage, 'persistence manifest comparison gate is required');
  assert.match(coverage, /^    needs: race$/mu);
  assert.match(coverage, /node tool\/persistence-race-partition\.mjs compare/u);
  assert.doesNotMatch(coverage, /\bif:|continue-on-error|\|\|/u);
  for (const value of [
    'actions/checkout@11d5960a326750d5838078e36cf38b85af677262',
    'actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16',
    'go-version-file: go.mod',
    'check-latest: false',
  ]) assert.ok(job.includes(value), value);
}

test('aggregate short race remains an unconditional bounded CI gate with complete persistence partitions', () => {
  assertRaceGate(ci);
});

test('race gate rejects omitted shards, cancellation, optional execution, and changed containment', () => {
  assertRaceGate(ci);
  const mutations = [
    ['[native, persistence-migration, persistence-rest, rest]', '[native, rest]'],
    ['fail-fast: false', 'fail-fast: true'],
    ['    strategy:', '    if: false\n    strategy:'],
    ['    strategy:', '    continue-on-error: true\n    strategy:'],
    ['timeout-minutes: 150', 'timeout-minutes: 180'],
    ['      - run: bash tool/test-race-shard.sh ${{ matrix.shard }}', "      - run: bash tool/test-race-shard.sh ${{ matrix.shard }}\n        if: startsWith(matrix.shard, 'persistence-')"],
    ['bash tool/test-race-shard.sh ${{ matrix.shard }}', 'make test-race'],
    ['bash tool/test-race-shard.sh ${{ matrix.shard }}', 'bash tool/test-race-shard.sh native'],
    ['bash tool/test-race-shard.sh ${{ matrix.shard }}', 'bash tool/test-race-shard.sh ${{ matrix.shard }} || true'],
    ['        shard: [native, persistence-migration, persistence-rest, rest]', '        shard: [native, persistence-migration, persistence-rest, rest]\n        exclude:\n          - shard: persistence-migration'],
  ];
  const job = ci.match(/^  race:\n(?:(?!^  [\w-]+:)[\s\S])*/mu)[0];
  for (const [before, after] of mutations) {
    assert.ok(job.includes(before), `mutation must apply: ${before}`);
    assert.throws(() => assertRaceGate(ci.replace(job, job.replace(before, after))), undefined, after);
  }
  for (const replacement of ['    needs: []', '    needs: race\n    if: false', '    needs: race\n    continue-on-error: true']) {
    assert.throws(() => assertRaceGate(ci.replace('    needs: race\n', replacement+'\n')));
  }
});

test('packaged V7 retains exact gates on disposable hosted inputs', () => {
  assert.match(workflow, /runs-on: macos-15/u);
  assert.doesNotMatch(workflow, /self-hosted|VIBERMATE_CLAUDE_2_1_220_PATH|continue-on-error/u);
  for (const value of [
    'environment: packaged-acceptance',
    '--expected-mode deterministic',
    '--expected-schema vibermate.m0-assembly-acceptance/v7',
    '--expected-revision "${GITHUB_SHA}"',
    '--expected-client-id claude-code',
    '--expected-client-version 2.1.220',
    '--source-root "${GITHUB_WORKSPACE}"',
    '--desktop-app "${DESKTOP_APP}"',
    '--acceptance-executable "${ACCEPTANCE_BIN}"',
    '--client-entrypoint "${FIXED_CLAUDE_PATH}"',
    'build_macos_app.sh live',
    'verify_macos_app.sh "${DESKTOP_APP}" live',
    'if-no-files-found: error',
    '@anthropic-ai/claude-code-darwin-arm64@2.1.220',
    '8addc857f3fe64d5a0368af9ee50321b50afb4a6918ba3ef018ab84f5dbbe081',
  ]) assert.ok(workflow.includes(value), value);
});

test('CI retains hosted workflow and native release checks', () => {
  const check = 'node --test .github/packaged-acceptance-contract.test.mjs';
  assert.ok(ci.includes(check));
  assert.ok(make.includes(check));
  const shards = 'node --test tool/test-race-shard.test.mjs';
  assert.ok(ci.includes(shards));
  assert.ok(make.includes(shards));
  assert.ok(ci.includes('make check-release-build'));
  assert.ok(ci.includes('govulncheck@v1.6.0 -tags vibermate_native_secrets ./...'));
  assert.ok(ci.includes('make test-store-crash-race'));
});

test('full crash race remains mandatory outside short mode', () => {
  assert.match(ci, /test-store-crash-race/u);
  assert.match(make, /test-store-crash-race:/u);
  assert.match(make, /go test -race \.\/internal\/runtimepersistence -run '\^TestContentSourceProcessCrash'/u);
  const job = ci.match(/^  store-crash-race:\n(?:(?!^  [\w-]+:)[\s\S])*/mu)?.[0];
  assert.ok(job, 'dedicated full crash race job is required');
  assert.match(job, /^    timeout-minutes: 20$/mu);
  assert.match(job, /^      - run: make test-store-crash-race$/mu);
  assert.doesNotMatch(job, /\bif:|continue-on-error|\boptional\b|-short/u);
  const target = make.match(/^test-store-crash-race:\n(?:\t[^\n]*\n)+/mu)?.[0];
  assert.equal(target, "test-store-crash-race:\n\tgo test -race ./internal/runtimepersistence -run '^TestContentSourceProcessCrash' -count=1 -parallel=1 -timeout=15m -v\n");
});

test('full response page race remains an unconditional dedicated gate', () => {
  const job = ci.match(/^  store-response-race:\n(?:(?!^  [\w-]+:)[\s\S])*/mu)?.[0];
  assert.ok(job, 'dedicated full response race job is required');
  assert.match(job, /^    needs: generated-structural$/mu);
  assert.match(job, /^    runs-on: ubuntu-latest$/mu);
  const deadline = job.match(/^    timeout-minutes: (\d+)$/mu)?.[1];
  assert.ok(Number(deadline) > 40 && Number(deadline) <= 60, 'finite job deadline must enclose the 40m test watchdog');
  assert.equal(Number(deadline), 55, 'full response race job containment remains unchanged');
  assert.match(job, /^      - run: make test-store-response-race$/mu);
  assert.doesNotMatch(job, /\bif:|continue-on-error|\boptional\b|-short/u);
  for (const value of [
    'actions/checkout@11d5960a326750d5838078e36cf38b85af677262',
    'actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16',
    'go-version-file: go.mod',
    'check-latest: false',
  ]) assert.ok(job.includes(value), value);

  const target = make.match(/^test-store-response-race:\n(?:\t[^\n]*\n)+/mu)?.[0];
  assert.equal(target, "test-store-response-race:\n\tgo test -race -p=1 -parallel=1 ./internal/runtimepersistence -run '^TestContentSourceRealResponseEveryBodyPage$$' -count=1 -timeout=40m -v\n");
  assert.doesNotMatch(target, /-short|\|\||;|\btrue\b/u);
  assert.match(make, /^\.PHONY: .*\btest-store-response-race\b/mu);
});
