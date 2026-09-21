// Real-container smoke test. Uses a fresh project, random host port and a
// uniquely named disposable data volume; never attaches existing user data.
import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { promisify } from 'node:util';
import { fileURLToPath } from 'node:url';
import { verifyWebSetup } from '../server/web-setup-smoke.mjs';

const execute = promisify(execFile);
const project = `vibermate-setup-smoke-${randomBytes(6).toString('hex')}`;
const volume = `${project}-data`;
const root = fileURLToPath(new URL('../../', import.meta.url));
const env = {
  ...process.env,
  VIBERMATE_IMAGE: process.env.VIBERMATE_IMAGE ?? 'vibermate-runtime:0.1.11-local',
  VIBERMATE_PORT: '0',
  VIBERMATE_BIND_ADDRESS: '0.0.0.0', // The HTTP template must ignore this.
  VIBERMATE_LOCAL_DATA_VOLUME: volume,
};

async function docker(args) {
  try {
    const result = await execute('docker', args, {
      cwd: root, env, timeout: 180_000, maxBuffer: 1024 * 1024,
    });
    return result.stdout.trim();
  } catch (error) {
    // Never dump subprocess output: recovery-key reads are secret-bearing.
    throw new Error(`Docker ${args[0]} failed (exit ${error.code ?? 'unknown'})`);
  }
}

const compose = (...args) => docker([
  'compose', '--env-file', '.env.example', '-p', project,
  '-f', 'compose.yaml', ...args,
]);

await docker(['image', 'inspect', env.VIBERMATE_IMAGE, '--format', '{{.Id}}']);
try {
  await compose('up', '-d', '--wait', '--wait-timeout', '90');
  let published = await compose('port', 'vibermate', '9666');
  assert.match(published, /^127\.0\.0\.1:\d+$/);
  await verifyWebSetup({
    origin: () => `http://${published}`,
    readRecoveryKey: () => compose('exec', '-T', 'vibermate',
      '/opt/vibermate/vibermated', 'server', 'recovery-key', '--data-dir', '/data'),
    restart: async () => {
      await compose('restart', 'vibermate');
      await compose('up', '-d', '--wait', '--wait-timeout', '90');
      // Docker may allocate a different host port when published port is zero.
      published = await compose('port', 'vibermate', '9666');
      assert.match(published, /^127\.0\.0\.1:\d+$/);
    },
  });
  console.log('PASS: loopback HTTP, Web assets, authenticated owner setup, guarded Proxy CA export, login and CA persistence across restart.');
} finally {
  await compose('down', '--timeout', '15');
  const ownedVolumes = await docker(['volume', 'ls', '--filter',
    `label=com.docker.compose.project=${project}`, '--format', '{{.Name}}']);
  if (ownedVolumes.split('\n').includes(volume)) {
    await docker(['volume', 'rm', volume]);
  }
  console.log('Cleaned up the disposable test container, network and data volume.');
}
