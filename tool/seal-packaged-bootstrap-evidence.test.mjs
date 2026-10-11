import assert from 'node:assert/strict';
import test from 'node:test';
import { generateKeyPairSync, privateDecrypt, createDecipheriv, constants } from 'node:crypto';
import { readFileSync, writeFileSync, mkdtempSync, rmSync, statSync, symlinkSync, existsSync } from 'node:fs';
import { spawnSync } from 'node:child_process';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { sealDiagnosticEvidence } from './seal-packaged-bootstrap-evidence.mjs';

const schema = 'vibermate.private-bootstrap-evidence/v1';
const recipient = generateKeyPairSync('rsa', { modulusLength: 3072 });
const wrongRecipient = generateKeyPairSync('rsa', { modulusLength: 3072 });
const publicPem = recipient.publicKey.export({ type: 'spki', format: 'pem' });
const sentinel = Buffer.from('SYNTHETIC-PRIVATE-SENTINEL\u0000exact bytes');
const cli = fileURLToPath(new URL('./seal-packaged-bootstrap-evidence.mjs', import.meta.url));
function decrypt(bytes, key = recipient.privateKey) {
  const envelope = JSON.parse(bytes);
  assert.deepEqual(Object.keys(envelope).sort(), ['ciphertext', 'nonce', 'schema', 'tag', 'wrappedKey']);
  assert.equal(envelope.schema, schema);
  const aesKey = privateDecrypt({ key, padding: constants.RSA_PKCS1_OAEP_PADDING, oaepHash: 'sha256' }, Buffer.from(envelope.wrappedKey, 'base64'));
  assert.equal(aesKey.length, 32);
  assert.equal(Buffer.from(envelope.nonce, 'base64').length, 12);
  assert.equal(Buffer.from(envelope.tag, 'base64').length, 16);
  const decipher = createDecipheriv('aes-256-gcm', aesKey, Buffer.from(envelope.nonce, 'base64'));
  decipher.setAAD(Buffer.from(envelope.schema));
  decipher.setAuthTag(Buffer.from(envelope.tag, 'base64'));
  return Buffer.concat([decipher.update(Buffer.from(envelope.ciphertext, 'base64')), decipher.final()]);
}
function fixture(run) {
  const root = mkdtempSync(join(tmpdir(), 'seal-bootstrap-'));
  try {
    const input = join(root, 'input.tar.gz'); const output = join(root, 'sealed.json'); const key = join(root, 'public.pem');
    writeFileSync(input, sentinel, { mode: 0o600 }); writeFileSync(key, publicPem, { mode: 0o600 });
    run({ root, input, output, key });
  } finally { rmSync(root, { recursive: true, force: true }); }
}
function invoke(input, output, key) {
  return spawnSync(process.execPath, [cli, input, output, key], { encoding: 'utf8', timeout: 10000 });
}
function failed(result) {
  assert.equal(result.status, 1);
  assert.equal(result.stdout, 'EVIDENCE_SEAL_FAILED\n');
  assert.equal(result.stderr, '');
}

test('real envelope hides plaintext and authenticates exact bytes to only its recipient', () => {
  const sealed = sealDiagnosticEvidence(sentinel, publicPem);
  assert.notDeepEqual(sealed, sentinel, 'forwarding plaintext does not seal evidence');
  assert.equal(sealed.includes('SYNTHETIC-PRIVATE-SENTINEL'), false);
  assert.deepEqual(decrypt(sealed), sentinel);
  assert.throws(() => decrypt(sealed, wrongRecipient.privateKey));
  const second = sealDiagnosticEvidence(sentinel, publicPem);
  assert.notDeepEqual(second, sealed, 'each seal uses fresh randomness');
});

test('real authentication rejects ciphertext, tag, nonce and schema tampering', () => {
  const sealed = sealDiagnosticEvidence(sentinel, publicPem);
  assert.notDeepEqual(sealed, sentinel);
  for (const field of ['ciphertext', 'tag', 'nonce', 'wrappedKey']) {
    const envelope = JSON.parse(sealed); const bytes = Buffer.from(envelope[field], 'base64');
    bytes[0] ^= 1; envelope[field] = bytes.toString('base64');
    assert.throws(() => decrypt(JSON.stringify(envelope)), field);
  }
  const envelope = JSON.parse(sealed); envelope.schema = 'other-schema';
  assert.throws(() => decrypt(JSON.stringify(envelope)));
});

test('sealer admits bounded bytes and only RSA public recipients of at least 3072 bits', () => {
  assert.deepEqual(decrypt(sealDiagnosticEvidence(Buffer.alloc(1024 * 1024), publicPem)), Buffer.alloc(1024 * 1024));
  assert.throws(() => sealDiagnosticEvidence(Buffer.alloc(1024 * 1024 + 1), publicPem));
  assert.throws(() => sealDiagnosticEvidence('not byte input', publicPem));
  const weak = generateKeyPairSync('rsa', { modulusLength: 2048 });
  const ec = generateKeyPairSync('ec', { namedCurve: 'prime256v1' });
  for (const key of [weak.publicKey, ec.publicKey, recipient.privateKey, 'invalid PEM', recipient.privateKey.export({ type: 'pkcs8', format: 'pem' })]) {
    assert.throws(() => sealDiagnosticEvidence(sentinel, key));
  }
});

test('CLI creates one exclusive mode-0600 encrypted file with fixed output', () => fixture(({ input, output, key }) => {
  const result = invoke(input, output, key);
  assert.equal(result.status, 0);
  assert.equal(result.stdout, 'EVIDENCE_SEALED\n'); assert.equal(result.stderr, '');
  assert.equal(statSync(output).mode & 0o777, 0o600);
  assert.deepEqual(decrypt(readFileSync(output)), sentinel);
  const original = readFileSync(output);
  failed(invoke(input, output, key));
  assert.deepEqual(readFileSync(output), original, 'collision must preserve existing output');
}));

test('CLI refuses symlink/nonregular inputs, oversized files and invalid public keys without output', () => {
  for (const boundary of ['input-symlink', 'key-symlink', 'input-directory', 'key-directory', 'input-big', 'key-big', 'private-key', 'missing-input', 'output-symlink']) fixture(({ root, input, output, key }) => {
    if (boundary === 'input-symlink') { const link = join(root, 'input-link'); symlinkSync(input, link); input = link; }
    if (boundary === 'key-symlink') { const link = join(root, 'key-link'); symlinkSync(key, link); key = link; }
    if (boundary === 'input-directory') input = root;
    if (boundary === 'key-directory') key = root;
    if (boundary === 'input-big') writeFileSync(input, Buffer.alloc(1024 * 1024 + 1));
    if (boundary === 'key-big') writeFileSync(key, Buffer.alloc(16 * 1024 + 1));
    if (boundary === 'private-key') writeFileSync(key, recipient.privateKey.export({ type: 'pkcs8', format: 'pem' }));
    if (boundary === 'missing-input') input = join(root, 'absent');
    if (boundary === 'output-symlink') symlinkSync(input, output);
    failed(invoke(input, output, key));
    if (boundary !== 'output-symlink') assert.equal(existsSync(output), false, boundary);
    if (boundary === 'output-symlink') assert.deepEqual(readFileSync(output), sentinel, 'output link target is never overwritten');
  });
  failed(spawnSync(process.execPath, [cli], { encoding: 'utf8' }));
});
