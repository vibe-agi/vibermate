import { createPublicKey, KeyObject, randomBytes, createCipheriv, publicEncrypt, constants as cryptoConstants } from 'node:crypto';
import { lstatSync, openSync, fstatSync, closeSync, readSync, writeFileSync, realpathSync, constants as fsConstants } from 'node:fs';
import { pathToFileURL } from 'node:url';

const schema = 'vibermate.private-bootstrap-evidence/v1';
const maxPlaintext = 1024 * 1024;
const maxPublicKey = 16 * 1024;

export function sealDiagnosticEvidence(plaintext, publicKey) {
  if (!Buffer.isBuffer(plaintext) || plaintext.length > maxPlaintext) throw new Error('invalid diagnostic input');
  let recipient;
  if (publicKey instanceof KeyObject) {
    if (publicKey.type !== 'public') throw new Error('public recipient required');
    recipient = publicKey;
  } else {
    if (typeof publicKey !== 'string' && !Buffer.isBuffer(publicKey)) throw new Error('invalid public recipient');
    const pem = Buffer.from(publicKey);
    if (pem.length > maxPublicKey || !/^-----BEGIN PUBLIC KEY-----\r?\n/.test(pem.toString('utf8'))) throw new Error('invalid public recipient');
    recipient = createPublicKey(pem);
  }
  if (recipient.asymmetricKeyType !== 'rsa' || !(recipient.asymmetricKeyDetails?.modulusLength >= 3072)) throw new Error('invalid public recipient');
  const key = randomBytes(32);
  const nonce = randomBytes(12);
  const cipher = createCipheriv('aes-256-gcm', key, nonce);
  cipher.setAAD(Buffer.from(schema));
  const ciphertext = Buffer.concat([cipher.update(plaintext), cipher.final()]);
  const wrappedKey = publicEncrypt({ key: recipient, padding: cryptoConstants.RSA_PKCS1_OAEP_PADDING, oaepHash: 'sha256' }, key);
  return Buffer.from(JSON.stringify({
    schema,
    wrappedKey: wrappedKey.toString('base64'),
    nonce: nonce.toString('base64'),
    tag: cipher.getAuthTag().toString('base64'),
    ciphertext: ciphertext.toString('base64'),
  }));
}

function readBoundedFile(path, limit) {
  if (!lstatSync(path).isFile()) throw new Error('regular file required');
  const fd = openSync(path, fsConstants.O_RDONLY | fsConstants.O_NOFOLLOW);
  try {
    const stat = fstatSync(fd);
    if (!stat.isFile() || stat.size > limit) throw new Error('invalid file size');
    // Read at most limit+1 even if the file changes after fstat.
    const bytes = Buffer.alloc(limit + 1);
    let count = 0;
    while (count < bytes.length) {
      const next = readSync(fd, bytes, count, bytes.length - count, null);
      if (next === 0) break;
      count += next;
    }
    if (count > limit) throw new Error('invalid file size');
    return bytes.subarray(0, count);
  } finally { closeSync(fd); }
}

if (process.argv[1] && import.meta.url === pathToFileURL(realpathSync(process.argv[1])).href) {
  try {
    if (process.argv.length !== 5) throw new Error('invalid arguments');
    const plaintext = readBoundedFile(process.argv[2], maxPlaintext);
    const publicKey = readBoundedFile(process.argv[4], maxPublicKey);
    const sealed = sealDiagnosticEvidence(plaintext, publicKey);
    writeFileSync(process.argv[3], sealed, { flag: 'wx', mode: 0o600 });
    process.stdout.write('EVIDENCE_SEALED\n');
  } catch {
    process.stdout.write('EVIDENCE_SEAL_FAILED\n');
    process.exitCode = 1;
  }
}
