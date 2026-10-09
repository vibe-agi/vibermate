import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import test from 'node:test';
import { validateVerifierBuildInfo } from './verify-packaged-diagnostic-verifier.mjs';

test('verifier source rejects dirty or different builds', () => {
  const revision = '0'.repeat(40);
  const good = `binary: go1.26.9\n\tbuild\tvcs=git\n\tbuild\tvcs.revision=${revision}\n\tbuild\tvcs.modified=false\n`;
  assert.doesNotThrow(() => validateVerifierBuildInfo(good, revision));
  assert.throws(() => validateVerifierBuildInfo(good.replace('modified=false', 'modified=true'), revision));
  assert.throws(() => validateVerifierBuildInfo(good, '1'.repeat(40)));
});

test('verifier source rejects missing and duplicate settings', () => {
  const revision = '0'.repeat(40);
  const good = `binary: go1.26.9\n\tbuild\tvcs=git\n\tbuild\tvcs.revision=${revision}\n\tbuild\tvcs.modified=false\n`;
  assert.throws(() => validateVerifierBuildInfo(good.replace('\tbuild\tvcs=git\n', ''), revision));
  assert.throws(() => validateVerifierBuildInfo(`${good}\tbuild\tvcs=git\n`, revision));
});

test('verifier CLI prints only a fixed label when Go metadata is unavailable', () => {
  const cliPath = fileURLToPath(new URL('./verify-packaged-diagnostic-verifier.mjs', import.meta.url));
  const result = spawnSync(process.execPath, [cliPath, '/bin/ls', '0'.repeat(40)], { encoding: 'utf8' });
  assert.equal(result.status, 1);
  assert.equal(result.stdout, 'VERIFIER_SOURCE_MISMATCH\n');
  assert.equal(result.stderr, '');
});
