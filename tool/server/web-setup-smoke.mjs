// Shared assertions for the same Web contract, regardless of process packaging.
import assert from 'node:assert/strict';
import { randomBytes } from 'node:crypto';

export async function verifyWebSetup({ origin, readRecoveryKey, restart }) {
  async function request(path, { body, token } = {}) {
    return fetch(`${origin()}${path}`, {
      method: body == null ? 'GET' : 'POST',
      headers: {
        Origin: origin(),
        ...(body == null ? {} : { 'Content-Type': 'application/json' }),
        ...(token == null ? {} : { Authorization: `Bearer ${token}` }),
      },
      body: body == null ? undefined : JSON.stringify(body),
      redirect: 'error',
      signal: AbortSignal.timeout(10_000),
    });
  }

  async function jsonAt(path, options, status = 200) {
    const response = await request(path, options);
    assert.equal(response.status, status, `Unexpected status from ${path}`);
    return response.json();
  }

  assert.match(origin(), /^http:\/\/127\.0\.0\.1:\d+$/);
  assert.equal((await jsonAt('/api/v1/server/web-auth')).setupRequired, true);
  const page = await request('/');
  assert.equal(page.status, 200);
  assert.match(await page.text(), /flutter_bootstrap\.js/);
  assert.equal((await request('/flutter_bootstrap.js')).status, 200);
  const unauthenticated = await request('/api/v1/server/root-ca');
  assert.ok([401, 403].includes(unauthenticated.status));

  const password = `fixture-${randomBytes(24).toString('hex')}`;
  const input = {
    schema: 'vibermate-web-setup-v1', recoveryKey: 'x'.repeat(43),
    username: 'setup-smoke-owner', password,
  };
  // Setup deliberately hides whether an owner exists or the key is wrong.
  assert.equal((await request('/api/v1/server/web-setup', { body: input })).status, 409);
  assert.equal((await jsonAt('/api/v1/server/web-auth')).setupRequired, true);
  input.recoveryKey = await readRecoveryKey();
  const session = await jsonAt('/api/v1/server/web-setup', { body: input }, 201);
  input.recoveryKey = '';
  assert.equal(session.principal.role, 'owner');
  const certificate = await jsonAt('/api/v1/server/root-ca', { token: session.readToken });
  assert.equal(certificate.schema, 'vibermate-runtime-root-ca-v1');
  assert.match(certificate.certificatePem, /^-----BEGIN CERTIFICATE-----/);
  assert.equal(certificate.privateKeyPem, undefined);

  await restart();
  assert.equal((await jsonAt('/api/v1/server/web-auth')).setupRequired, false);
  const resumed = await jsonAt('/api/v1/server/web-sessions', { body: {
    schema: 'vibermate-web-login-v1', username: input.username, password,
  } }, 201);
  assert.equal(resumed.principal.id, session.principal.id);
  const retained = await jsonAt('/api/v1/server/root-ca', { token: resumed.readToken });
  assert.equal(retained.fingerprint, certificate.fingerprint);
}
