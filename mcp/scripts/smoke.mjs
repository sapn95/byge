#!/usr/bin/env node
// Offline protocol smoke test. Starts the server over stdio the way a client
// does, through the bin entry, completes the MCP handshake, lists the tools
// and checks what breaks quietly: a version that drifted from package.json, a
// tool advertised but not dispatched (or the reverse), a malformed schema.
//
// No network and no credentials: BYGE_URL is empty and BYGE_AUTH none, so a
// tool call can only fail before it reaches anything.
import { spawn } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import readline from 'node:readline';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const pkg = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8'));
const entry = join(root, pkg.bin['byge-mcp']);

const fail = [];
const check = (ok, what) => { console.log(`${ok ? 'ok  ' : 'FAIL'}  ${what}`); if (!ok) fail.push(what); };

const child = spawn(process.execPath, [entry], {
  stdio: ['pipe', 'pipe', 'pipe'],
  env: { ...process.env, BYGE_URL: '', BYGE_AUTH: 'none', BYGE_HEADERS: '' },
});
let stderr = '';
child.stderr.on('data', d => { stderr += d.toString(); });

const pending = new Map();
let id = 1;
readline.createInterface({ input: child.stdout }).on('line', line => {
  line = line.trim();
  if (!line) return;
  let m;
  try { m = JSON.parse(line); } catch { fail.push(`non-JSON on stdout: ${line.slice(0, 80)}`); return; }
  const p = pending.get(m.id);
  if (p) { pending.delete(m.id); p(m); }
});
const send = (method, params) => new Promise((resolve, reject) => {
  const i = id++;
  pending.set(i, resolve);
  child.stdin.write(JSON.stringify({ jsonrpc: '2.0', id: i, method, params }) + '\n');
  setTimeout(() => reject(new Error(`${method} timed out`)), 20000).unref();
});

try {
  const init = await send('initialize', {
    protocolVersion: '2025-06-18', capabilities: {}, clientInfo: { name: 'smoke', version: '1' },
  });
  child.stdin.write(JSON.stringify({ jsonrpc: '2.0', method: 'notifications/initialized' }) + '\n');

  const info = init?.result?.serverInfo;
  check(info?.name === pkg.name, `serverInfo.name "${info?.name}" matches package.json "${pkg.name}"`);
  check(info?.version === pkg.version, `serverInfo.version "${info?.version}" matches package.json "${pkg.version}"`);

  const tools = (await send('tools/list'))?.result?.tools;
  check(Array.isArray(tools) && tools.length > 0, `tools/list returns tools (${tools?.length ?? 0})`);
  const names = (tools || []).map(t => t.name);
  check(new Set(names).size === names.length, 'no duplicate tool names');

  for (const t of tools || []) {
    const s = t.inputSchema;
    check(/^byge_[a-z_]+$/.test(t.name), `${t.name}: named byge_*`);
    check(typeof t.description === 'string' && t.description.length > 20, `${t.name}: has a usable description`);
    check(s?.type === 'object' && typeof s.properties === 'object', `${t.name}: inputSchema is an object schema`);
    for (const req of s?.required || []) {
      check(Object.hasOwn(s.properties || {}, req), `${t.name}: required "${req}" is declared`);
    }
  }
  // "unknown tool" is the one answer that proves no branch handles a name.
  for (const n of names) {
    const r = await send('tools/call', { name: n, arguments: {} });
    check(!/unknown tool/i.test(JSON.stringify(r?.result ?? r)), `${n}: reaches a handler`);
  }
  const bogus = await send('tools/call', { name: 'byge_nope', arguments: {} });
  check(bogus?.result?.isError === true, 'an unknown tool is an error');

  check(!stderr.trim(), `clean stderr${stderr ? ` (got: ${stderr.slice(0, 160)})` : ''}`);
} catch (e) {
  check(false, `handshake: ${e.message}`);
} finally {
  child.stdin.end();
  child.kill();
}

console.log(fail.length ? `\n${fail.length} failed` : '\nall checks passed');
process.exit(fail.length ? 1 : 0);
