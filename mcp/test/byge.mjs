// Starts a real byge from this repository for the tests: the memory store, so
// nothing outlives the run, and proxy auth, so a test can be somebody by
// sending a header. BYGE_BIN names a binary built elsewhere; without it the
// server is built from ../cmd/byge, which needs Go.
import { spawn, execFileSync } from 'node:child_process';
import { createServer } from 'node:net';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const repo = join(dirname(fileURLToPath(import.meta.url)), '..', '..');

function freePort() {
  return new Promise((resolve, reject) => {
    const s = createServer();
    s.on('error', reject);
    s.listen(0, '127.0.0.1', () => {
      const { port } = s.address();
      s.close(() => resolve(port));
    });
  });
}

let built = '';
function binary() {
  if (process.env.BYGE_BIN) return process.env.BYGE_BIN;
  if (!built) {
    const dir = mkdtempSync(join(tmpdir(), 'byge-mcp-'));
    process.on('exit', () => rmSync(dir, { recursive: true, force: true }));
    built = join(dir, 'byge');
    execFileSync('go', ['build', '-o', built, './cmd/byge'], { cwd: repo, stdio: 'inherit' });
  }
  return built;
}

export const OWNER = 'owner@example.org';

export async function startByge() {
  const port = await freePort();
  const child = spawn(binary(), [], {
    env: {
      PATH: process.env.PATH,
      HOME: process.env.HOME,
      STORAGE: 'memory',
      LISTEN_ADDR: `127.0.0.1:${port}`,
      AUTH_MODE: 'proxy',
      AUTH_HEADER: 'X-User',
      AUTH_BOOTSTRAP_OWNER: OWNER,
      LOG_LEVEL: 'error',
    },
    stdio: ['ignore', 'ignore', 'pipe'],
  });
  let log = '';
  let failed = null;
  child.on('error', e => { failed = e; });
  child.stderr.on('data', d => { log += d; });
  const url = `http://127.0.0.1:${port}`;
  for (let i = 0; i < 100; i++) {
    try {
      const r = await fetch(`${url}/readyz`);
      if (r.ok) return { url, stop: () => child.kill(), log: () => log };
    } catch { /* not up yet */ }
    if (failed) throw new Error(`byge did not start from ${binary()}: ${failed.code || failed.message}`);
    if (child.exitCode !== null) break;
    await new Promise(r => setTimeout(r, 100));
  }
  child.kill();
  throw new Error(`byge did not start: ${log}`);
}
