import { execFileSync } from 'node:child_process';
import { lstatSync } from 'node:fs';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

export function validateVerifierBuildInfo(text, expectedRevision) {
  if (!/^[0-9a-f]{40}$/.test(expectedRevision)) throw new Error('invalid source identity');
  const expected = { vcs: 'git', 'vcs.revision': expectedRevision, 'vcs.modified': 'false' };
  for (const [key, value] of Object.entries(expected)) {
    const settings = [...text.matchAll(/^\s*build\s+(vcs(?:\.revision|\.modified)?)=(\S+)\s*$/gm)]
      .filter(match => match[1] === key);
    if (settings.length !== 1 || settings[0][2] !== value) throw new Error('verifier source mismatch');
  }
}

function verifyPackagedVerifier(binaryPath, expectedRevision) {
  const info = lstatSync(binaryPath);
  if (!info.isFile() || (info.mode & 0o111) === 0) throw new Error('invalid verifier binary');

  const buildInfo = execFileSync('go', ['version', '-m', binaryPath], {
    encoding: 'utf8',
    timeout: 10000,
    maxBuffer: 65536,
    stdio: ['ignore', 'pipe', 'ignore'],
  });
  validateVerifierBuildInfo(buildInfo, expectedRevision);
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  try {
    if (process.argv.length !== 4) throw new Error('invalid arguments');
    verifyPackagedVerifier(process.argv[2], process.argv[3]);
    process.stdout.write('VERIFIER_SOURCE_OK\n');
  } catch {
    process.stdout.write('VERIFIER_SOURCE_MISMATCH\n');
    process.exitCode = 1;
  }
}
