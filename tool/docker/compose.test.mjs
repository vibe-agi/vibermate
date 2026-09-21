import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

const root = fileURLToPath(new URL('../../', import.meta.url));

test('container image consumes only the current prepared source artifacts', () => {
  const dockerfile = readFileSync(new URL('../../Dockerfile', import.meta.url), 'utf8');
  assert.doesNotMatch(dockerfile, /releases\/download/u);
  assert.doesNotMatch(dockerfile, / AS distribution/u);
  assert.match(dockerfile, /COPY dist\/docker\/ \/opt\/vibermate\//u);
  assert.match(dockerfile, /org\.opencontainers\.image\.version="0\.1\.11"/u);
});

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
      VIBERMATE_IMAGE: 'vibermate-runtime:0.1.11-local',
      VIBERMATE_LOCAL_DATA_VOLUME: 'vibermate-local-data',
      VIBERMATE_ACCESS_ADDRESS: 'vibermate.home.arpa:9666',
      VIBERMATE_PRIVATE_BIND_ADDRESS: '192.0.2.20',
      VIBERMATE_PRIVATE_DATA_VOLUME: 'vibermate-private-data',
      VIBERMATE_PUBLIC_HOST: 'runtime.example.com',
      VIBERMATE_ACME_EMAIL: 'admin@example.com',
      VIBERMATE_PUBLIC_BIND_ADDRESS: '0.0.0.0',
      VIBERMATE_PUBLIC_DATA_VOLUME: 'vibermate-public-data',
      VIBERMATE_TEAM_BIND_ADDRESS: '192.0.2.10',
      VIBERMATE_TEAM_ACCESS_ADDRESS: 'runtime.example.com:9666',
      VIBERMATE_TEAM_DATA_VOLUME: 'vibermate-team-data',
      VIBERMATE_TLS_CERT_FILE: '/tmp/vibermate-fixture/fullchain.pem',
      VIBERMATE_TLS_KEY_FILE: '/tmp/vibermate-fixture/privkey.pem',
      ...overrides,
    },
  });
  assert.equal(result.status, 0, result.stderr || String(result.error));
  return JSON.parse(result.stdout);
}

test('local HTTP cannot inherit a remote bind address or the HTTPS volume', () => {
  const value = config('compose.yaml', {
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
  assert.equal(service.build.context, root.replace(/\/$/u, ''));
  assert.equal(service.build.dockerfile, 'Dockerfile');
  assert.equal(service.build.target, 'local');
  assert.equal(value.volumes['runtime-data'].name, 'vibermate-local-data');
});

test('private HTTPS binds an explicit interface and derives one advertised identity', () => {
  const value = config('compose.private.yaml');
  const service = value.services.vibermate;
  assert.deepEqual(service.command.slice(-4), [
    '--web-root', '/opt/vibermate/vibermate-web',
    '--transport', 'private_ca_tls',
  ]);
  assert.equal(service.command[service.command.indexOf('--access-address') + 1], 'vibermate.home.arpa:9666');
  assert.equal(service.ports[0].host_ip, '192.0.2.20');
  assert.equal(value.volumes['runtime-data'].name, 'vibermate-private-data');
  assert.equal(service.pull_policy, 'never');
});

test('automatic public HTTPS has an explicit name, terms and TLS challenge', () => {
  const value = config('compose.public.yaml');
  const service = value.services.vibermate;
  assert.equal(service.command[service.command.indexOf('--access-address') + 1], 'runtime.example.com:443');
  assert.equal(service.command[service.command.indexOf('--transport') + 1], 'automatic_tls');
  assert.ok(service.command.includes('--acme-agree-terms'));
  assert.equal(service.command[service.command.indexOf('--acme-challenge') + 1], 'tls_alpn_01');
  assert.equal(service.ports[0].published, '443');
  assert.equal(service.ports[0].target, 9666);
  assert.equal(value.volumes['runtime-data'].name, 'vibermate-public-data');
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
