import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const root = fileURLToPath(new URL('../../', import.meta.url));

function config(file, overrides = {}) {
  const result = spawnSync('docker', [
    'compose', '--env-file', '.env.example', '-f', file,
    'config', '--format', 'json',
  ], {
    cwd: root,
    encoding: 'utf8',
    env: {
      ...process.env,
      VIBERMATE_BIND_ADDRESS: '127.0.0.1',
      VIBERMATE_PORT: '9666',
      VIBERMATE_IMAGE: 'vibermate-runtime:0.1.10-local',
      VIBERMATE_LOCAL_DATA_VOLUME: 'vibermate-local-data',
      VIBERMATE_TEAM_BIND_ADDRESS: '192.0.2.10',
      VIBERMATE_TLS_CERT_FILE: '/tmp/vibermate-fixture/fullchain.pem',
      VIBERMATE_TLS_KEY_FILE: '/tmp/vibermate-fixture/privkey.pem',
      ...overrides,
    },
  });
  assert.equal(result.status, 0, result.stderr || String(result.error));
  return JSON.parse(result.stdout);
}

test('local HTTP cannot inherit a remote bind address or the HTTPS volume', () => {
  const value = config('compose.local.yaml', {
    VIBERMATE_BIND_ADDRESS: '0.0.0.0',
    VIBERMATE_PORT: '19666',
  });
  const service = value.services.vibermate;
  assert.deepEqual(service.ports.map(p => [p.host_ip, p.published, p.target]), [
    ['127.0.0.1', '19666', 9666],
  ]);
  assert.equal(service.command.at(-1), 'http');
  assert.equal(service.healthcheck.test.at(-1), 'http://127.0.0.1:9666/api/v1/server/web-auth');
  assert.equal(service.read_only, true);
  assert.deepEqual(service.cap_drop, ['ALL']);
  assert.equal(service.pull_policy, 'never');
  assert.equal(value.volumes['runtime-data'].name, 'vibermate-local-data');
});

test('existing Compose installations remain HTTPS with the same data volume', () => {
  const value = config('compose.yaml');
  assert.equal(value.services.vibermate.command.at(-1), 'self_signed_tls');
  assert.equal(value.volumes['runtime-data'].name, 'vibermate-runtime-data');
  assert.equal(value.services.vibermate.ports[0].host_ip, '127.0.0.1');
});

test('team HTTPS requires operator-owned read-only server certificate files', () => {
  const value = config('compose.team.yaml');
  const service = value.services.vibermate;
  assert.deepEqual(service.command.slice(-6), [
    '--transport', 'tls_files', '--tls-cert', '/certs/fullchain.pem',
    '--tls-key', '/certs/privkey.pem',
  ]);
  assert.equal(service.ports[0].host_ip, '192.0.2.10');
  assert.equal(value.volumes['runtime-data'].name, 'vibermate-team-data');
  const files = service.volumes.filter(v => v.type === 'bind');
  assert.equal(files.length, 2);
  for (const file of files) {
    assert.equal(file.read_only, true);
    assert.equal(file.bind.create_host_path, false);
  }
});
