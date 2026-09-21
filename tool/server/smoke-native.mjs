// Builds and runs a real standalone Server against disposable private data.
// Requires the Web build: flutter build web --release --no-pub.
import assert from 'node:assert/strict';
import { execFile, spawn } from 'node:child_process';
import { cp, mkdtemp, realpath, rm, stat } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { createInterface } from 'node:readline';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import { verifyWebSetup } from './web-setup-smoke.mjs';

const execute = promisify(execFile);
const root = fileURLToPath(new URL('../../', import.meta.url));
const web = join(root, 'ui/flutter_app/build/web');
assert.ok((await stat(join(web, 'index.html'))).isFile(), 'Build the Web UI first');
const fixture = await realpath(await mkdtemp(join(tmpdir(), 'vibermate-native-smoke-')));
const executable = join(fixture, 'vibermated');
const dataDirectory = join(fixture, 'runtime-data');
let child;
let closed;
let origin;

async function start() {
  // No --web-root or --transport: exercise adjacent packaged assets and the
  // native HTTP default. Port zero avoids touching an existing local Runtime.
  child = spawn(executable, [
    'server', '--data-dir', dataDirectory, '--listen', '127.0.0.1:0',
  ], { cwd: fixture, stdio: ['ignore', 'pipe', 'pipe'] });
  const process = child;
  closed = new Promise(resolve => process.once('close', resolve));
  process.stderr.resume(); // Do not dump potentially secret-bearing logs.
  const lines = createInterface({ input: process.stdout });
  try {
    const status = await new Promise((resolve, reject) => {
      const timer = setTimeout(() => reject(new Error('Native Server startup timed out')), 30_000);
      const cleanup = () => clearTimeout(timer);
      process.once('error', () => { cleanup(); reject(new Error('Native Server spawn failed')); });
      process.once('close', () => { cleanup(); reject(new Error('Native Server exited before readiness')); });
      lines.once('line', line => {
        cleanup();
        try { resolve(JSON.parse(line)); }
        catch { reject(new Error('Invalid Server readiness record')); }
      });
    });
    assert.equal(status.ready, true);
    assert.equal(status.scheme, 'http');
    assert.equal(status.managementUi, true);
    assert.match(status.listenAddress, /^127\.0\.0\.1:\d+$/);
    origin = `http://${status.listenAddress}`;
  } finally {
    lines.close();
    process.stdout.resume();
  }
}

async function stop() {
  if (!child) return;
  if (child.exitCode == null && child.signalCode == null) child.kill('SIGTERM');
  const timer = setTimeout(() => child.kill('SIGKILL'), 30_000);
  try { await closed; }
  finally { clearTimeout(timer); child = undefined; }
}

try {
  await execute('go', ['build', '-o', executable, './cmd/vibermated'], {
    cwd: root, timeout: 180_000, maxBuffer: 1024 * 1024,
  });
  await cp(web, join(fixture, 'vibermate-web'), { recursive: true });
  await start();
  await verifyWebSetup({
    origin: () => origin,
    readRecoveryKey: async () => {
      try {
        const { stdout } = await execute(executable, [
          'server', 'recovery-key', '--data-dir', dataDirectory,
        ], { timeout: 10_000 });
        return stdout.trim();
      } catch {
        throw new Error('Could not read the fixture recovery key');
      }
    },
    restart: async () => { await stop(); await start(); },
  });
  console.log('PASS: native loopback HTTP default, adjacent Web assets, guarded owner setup and Proxy CA export, login and CA persistence across process restart.');
} finally {
  await stop();
  // This exact mkdtemp-created path contains only this test's fixtures.
  await rm(fixture, { recursive: true });
  console.log('Cleaned up the disposable native Server and its fixture directory.');
}
