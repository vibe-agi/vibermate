import assert from 'node:assert/strict';
import test from 'node:test';
import { readFileSync } from 'node:fs';

const workflow = readFileSync(new URL('./workflows/packaged-acceptance.yml', import.meta.url), 'utf8');
const ci = readFileSync(new URL('./workflows/ci.yml', import.meta.url), 'utf8');
const make = readFileSync(new URL('../Makefile', import.meta.url), 'utf8');

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
  assert.ok(ci.includes('make check-release-build'));
  assert.ok(ci.includes('govulncheck@v1.6.0 -tags vibermate_native_secrets ./...'));
  assert.ok(ci.includes('make test-store-crash-race'));
});

test('full crash race remains mandatory outside short mode', () => {
  assert.match(ci, /test-store-crash-race/u);
  assert.match(make, /test-store-crash-race:/u);
  assert.match(make, /go test -race \.\/internal\/runtimepersistence -run '\^TestContentSourceProcessCrash'/u);
});

test('full response page race remains an unconditional dedicated gate', () => {
  const job = ci.match(/^  store-response-race:\n(?:(?!^  [\w-]+:)[\s\S])*/mu)?.[0];
  assert.ok(job, 'dedicated full response race job is required');
  assert.match(job, /^    needs: generated-structural$/mu);
  assert.match(job, /^    runs-on: ubuntu-latest$/mu);
  const deadline = job.match(/^    timeout-minutes: (\d+)$/mu)?.[1];
  assert.ok(Number(deadline) > 40 && Number(deadline) <= 60, 'finite job deadline must enclose the 40m test watchdog');
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
