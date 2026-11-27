// Configuration, sign-in and the failures that come from in front of byge
// rather than from it: a redirect to the Access login, an HTML 403, a timeout.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { mkdtempSync, writeFileSync, chmodSync, readFileSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import {
  loadConfig, makeClient, makeHandlers, makeServer, jwtExpiry, pick, mergeSubtasks,
  NotSignedIn, ApiError, cardView, TOOLS,
} from '../lib.js';

const dir = mkdtempSync(join(tmpdir(), 'byge-mcp-test-'));
const jwt = exp => ['e30', Buffer.from(JSON.stringify({ exp })).toString('base64url'), 'sig'].join('.');

// A stand-in for cloudflared: prints a token for `access token`, and for
// `access login` prints the URL a person would open.
function fakeCloudflared(name, { token = '', fail = false, login = '' } = {}) {
  const p = join(dir, name);
  writeFileSync(p, `#!/bin/sh
echo "$@" >> "${p}.calls"
if [ "$2" = "login" ]; then ${login || 'echo "A browser window should have opened at the following URL:"; echo; echo "https://board.example.org/cdn-cgi/access/cli?aud=x&token=y"; sleep 5'}; exit 0; fi
${fail ? 'echo "Unable to find token for provided application." >&2; exit 1' : `echo "${token}"`}
`);
  chmodSync(p, 0o755);
  return p;
}

let srv;
let base;
const seen = [];
before(async () => {
  srv = createServer((req, res) => {
    seen.push({ url: req.url, headers: req.headers });
    if (req.url === '/api/v1/redirect') { res.writeHead(302, { Location: 'https://example.cloudflareaccess.com/cdn-cgi/access/login?x' }); return res.end(); }
    if (req.url === '/api/v1/html403') { res.writeHead(403, { 'Content-Type': 'text/html' }); return res.end('<html>Forbidden</html>'); }
    if (req.url === '/api/v1/json403') { res.writeHead(403, { 'Content-Type': 'application/json' }); return res.end('{"error":"forbidden"}'); }
    if (req.url === '/api/v1/text500') { res.writeHead(500, { 'Content-Type': 'text/plain' }); return res.end('boom'); }
    if (req.url === '/api/v1/html200') { res.writeHead(200, { 'Content-Type': 'text/html' }); return res.end('<html></html>'); }
    if (req.url === '/api/v1/empty') { res.writeHead(204); return res.end(); }
    res.writeHead(200, { 'Content-Type': 'application/json' });
    res.end(JSON.stringify({ path: req.url }));
  });
  await new Promise(r => srv.listen(0, '127.0.0.1', r));
  base = `http://127.0.0.1:${srv.address().port}`;
});
after(() => { srv.close(); rmSync(dir, { recursive: true, force: true }); });

test('loadConfig picks the auth from what is set', () => {
  assert.equal(loadConfig({ BYGE_URL: 'https://board.example.org///' }).url, 'https://board.example.org');
  assert.equal(loadConfig({ BYGE_URL: 'https://board.example.org' }).auth, 'cloudflared');
  assert.equal(loadConfig({ BYGE_URL: 'http://localhost:1' }).auth, 'none');
  assert.equal(loadConfig({ BYGE_URL: 'https://x', CF_ACCESS_CLIENT_ID: 'a', CF_ACCESS_CLIENT_SECRET: 'b' }).auth, 'service-token');
  assert.equal(loadConfig({ BYGE_URL: 'https://x', BYGE_AUTH: 'NONE' }).auth, 'none');
  assert.deepEqual(loadConfig({ BYGE_HEADERS: '{"X-User":"a@example.org"}' }).headers, { 'X-User': 'a@example.org' });
  assert.deepEqual(loadConfig({ BYGE_HEADERS: '  ' }).headers, {});
  assert.throws(() => loadConfig({ BYGE_HEADERS: '{' }), /not JSON/);
  assert.throws(() => loadConfig({ BYGE_HEADERS: '[]' }), /object of string values/);
  assert.throws(() => loadConfig({ BYGE_HEADERS: '{"a":1}' }), /object of string values/);
  assert.throws(() => loadConfig({ BYGE_AUTH: 'magic' }), /not one of/);
  assert.throws(() => loadConfig({ BYGE_AUTH: 'service-token' }), /needs CF_ACCESS_CLIENT_ID/);
});

test('helpers', () => {
  assert.equal(jwtExpiry(jwt(100)), 100000);
  assert.equal(jwtExpiry('nonsense'), 0);
  assert.equal(jwtExpiry(['e30', Buffer.from('{}').toString('base64url'), 's'].join('.')), 0);
  const list = [{ id: 'a1', name: 'Done' }, { id: 'b2', name: 'Same' }, { id: 'c3', name: 'same' }];
  assert.equal(pick(list, 'a1', 'column', 'b').name, 'Done');
  assert.equal(pick(list, ' done ', 'column', 'b').id, 'a1');
  assert.throws(() => pick(list, 'SAME', 'column', 'b'), /2 columns on b are called "SAME". Use the id: b2, c3/);
  assert.throws(() => pick([], 'x', 'label', 'b'), /There are: none/);
  assert.throws(() => pick(list, '', 'label', 'b'), /No label given/);
  assert.deepEqual(mergeSubtasks([{ id: 's1', title: 'A', done: true }], ['a', { id: 's9', title: 'B' }, { title: 'C', done: 1 }]),
    [{ title: 'a', id: 's1', done: true }, { title: 'B', done: false }, { title: 'C', done: true }]);
  const board = { slug: 'b', columns: [], labels: [] };
  const card = { id: 'c', title: 't', column_id: 'gone', position: 1, assignee: '', due_date: '', labels: ['l1'], subtasks: [], description: 'x'.repeat(200), archived_at: '2030-01-01T00:00:00Z', updated_at: 'u', created_at: 'c' };
  const v = cardView(card, board, { full: false });
  assert.equal(v.column, 'gone');
  assert.deepEqual(v.labels, ['l1']);
  assert.equal(v.description.length, 160);
  assert.equal(v.archived_at, card.archived_at);
  assert.equal(cardView({ ...card, description: 'short' }, board, { full: false }).description, 'short');
  assert.ok(TOOLS.every(t => t.name.startsWith('byge_')));
});

test('requests carry the configured headers and the cloudflared token', async () => {
  const tok = jwt(Math.floor(Date.now() / 1000) + 3600);
  const cf = fakeCloudflared('cf-ok', { token: tok });
  const cfg = { ...loadConfig({ BYGE_URL: base, BYGE_AUTH: 'cloudflared', BYGE_CLOUDFLARED: cf }), headers: { 'X-Extra': 'yes' } };
  const api = makeClient(cfg);
  assert.deepEqual(await api.get('/api/v1/one'), { path: '/api/v1/one' });
  await api.post('/api/v1/two', { a: 1 });
  const [a, b] = seen.slice(-2);
  assert.equal(a.headers['cf-access-token'], tok);
  assert.equal(a.headers['x-extra'], 'yes');
  assert.equal(b.headers['content-type'], 'application/json');
  // The token is cached until it is about to expire.
  assert.equal(readFileSync(`${cf}.calls`, 'utf8').trim().split('\n').length, 1);
  assert.equal(await api.del('/api/v1/empty'), null);
  assert.deepEqual(await api.put('/api/v1/three', {}), { path: '/api/v1/three' });
  assert.deepEqual(await api.patch('/api/v1/four', {}), { path: '/api/v1/four' });
});

test('a service token goes out as the two Access headers', async () => {
  const cfg = loadConfig({ BYGE_URL: base, CF_ACCESS_CLIENT_ID: 'id.access', CF_ACCESS_CLIENT_SECRET: 'secret' });
  await makeClient(cfg).get('/api/v1/st');
  const h = seen.at(-1).headers;
  assert.equal(h['cf-access-client-id'], 'id.access');
  assert.equal(h['cf-access-client-secret'], 'secret');
});

test('failures in front of byge read as sign-in problems', async () => {
  const cfg = loadConfig({ BYGE_URL: base, BYGE_AUTH: 'none' });
  const api = makeClient(cfg);
  await assert.rejects(api.get('/api/v1/redirect'), e => e instanceof NotSignedIn && /redirected to sign-in \(https:\/\/example.cloudflareaccess.com\/cdn-cgi\/access\/login\)/.test(e.message));
  await assert.rejects(api.get('/api/v1/html403'), e => e instanceof NotSignedIn && /not JSON/.test(e.message));
  await assert.rejects(api.get('/api/v1/json403'), e => e instanceof ApiError && e.status === 403 && /forbidden/.test(e.message));
  await assert.rejects(api.get('/api/v1/text500'), /500 boom/);
  await assert.rejects(api.get('/api/v1/html200'), /expected JSON, got text\/html/);
  await assert.rejects(makeClient(loadConfig({})).get('/api/v1/'), /BYGE_URL is not set/);
  // A port that was free a moment ago, so nothing answers on it.
  const closed = await new Promise(r => { const s = createServer(); s.listen(0, '127.0.0.1', () => { const { port } = s.address(); s.close(() => r(port)); }); });
  await assert.rejects(makeClient(loadConfig({ BYGE_URL: `http://127.0.0.1:${closed}` })).get('/api/v1/'), /GET \/api\/v1\/: ECONNREFUSED/);
  const slow = () => new Promise((_, rej) => setTimeout(() => rej(Object.assign(new Error('t'), { name: 'TimeoutError' })), 1));
  await assert.rejects(makeClient(cfg, undefined, slow).get('/api/v1/'), /no answer within 20s/);
});

test('cloudflared without a token, or missing, says how to sign in', async () => {
  const cfg = loadConfig({ BYGE_URL: base, BYGE_AUTH: 'cloudflared', BYGE_CLOUDFLARED: fakeCloudflared('cf-none', { fail: true }) });
  await assert.rejects(makeClient(cfg).get('/api/v1/'), e => e instanceof NotSignedIn && /cloudflared access login/.test(e.message));
  const gone = loadConfig({ BYGE_URL: base, BYGE_AUTH: 'cloudflared', BYGE_CLOUDFLARED: join(dir, 'does-not-exist') });
  await assert.rejects(makeClient(gone).get('/api/v1/'), /cloudflared is not installed/);
  const w = await makeHandlers(cfg, makeClient(cfg)).byge_whoami();
  assert.equal(w.signed_in, false);
});

test('byge_login hands back the URL cloudflared prints', async () => {
  const cfg = loadConfig({ BYGE_URL: base, BYGE_AUTH: 'cloudflared', BYGE_CLOUDFLARED: fakeCloudflared('cf-login') });
  const r = await makeHandlers(cfg, makeClient(cfg)).byge_login();
  assert.equal(r.open_this, 'https://board.example.org/cdn-cgi/access/cli?aud=x&token=y');
  const done = loadConfig({ BYGE_URL: base, BYGE_AUTH: 'cloudflared', BYGE_CLOUDFLARED: fakeCloudflared('cf-done', { login: 'echo "Successfully fetched your token:"' }) });
  assert.deepEqual(await makeHandlers(done, makeClient(done)).byge_login(), { signed_in: true });
  const bad = loadConfig({ BYGE_URL: base, BYGE_AUTH: 'cloudflared', BYGE_CLOUDFLARED: fakeCloudflared('cf-bad', { login: 'echo nope; exit 3' }) });
  await assert.rejects(makeHandlers(bad, makeClient(bad)).byge_login(), /did not start: nope/);
  const none = loadConfig({ BYGE_URL: base, BYGE_AUTH: 'cloudflared', BYGE_CLOUDFLARED: join(dir, 'nothing-here') });
  await assert.rejects(makeHandlers(none, makeClient(none)).byge_login(), /cloudflared not found/);
  const fake = makeHandlers(cfg, makeClient(cfg), { login: async () => ({ started: true, url: '' }) });
  const p = await fake.byge_login();
  assert.equal(p.pending, true);
  assert.match(p.note, /cloudflared access login http/);
  // --quiet is what keeps the JWT out of the log.
  assert.match(readFileSync(join(dir, 'cf-login.calls'), 'utf8'), /^access login --quiet http/);
});

test('every advertised tool has a handler, and the other way round', () => {
  const cfg = loadConfig({ BYGE_URL: base, BYGE_AUTH: 'none' });
  const keys = Object.keys(makeHandlers(cfg, makeClient(cfg))).sort();
  assert.deepEqual(keys, TOOLS.map(t => t.name).sort());
});

test('a configured header in another case is replaced, not doubled', async () => {
  const cfg = loadConfig({ BYGE_URL: base, BYGE_AUTH: 'none', BYGE_HEADERS: '{"content-type":"text/plain","accept":"text/html"}' });
  await makeClient(cfg).post('/api/v1/case', { a: 1 });
  const h = seen.at(-1).headers;
  assert.equal(h['content-type'], 'application/json');
  assert.equal(h.accept, 'application/json');
});

test('byge_api keeps the path under /api/v1/ after parsing', async () => {
  const cfg = loadConfig({ BYGE_URL: base, BYGE_AUTH: 'none' });
  const h = makeHandlers(cfg, makeClient(cfg));
  await assert.rejects(h.byge_api({ method: 'GET', path: '/api/v1/../../secret' }), /stay under it/);
  await assert.rejects(h.byge_api({ method: 'GET', path: '//evil.example/api/v1/' }), /stay under it/);
  await assert.rejects(h.byge_api({ method: 'GET', path: 'api/v1/' }), /stay under it/);
  assert.deepEqual(await h.byge_api({ method: 'GET', path: '/api/v1/x?q=a%20b' }), { path: '/api/v1/x?q=a%20b' });
});

test('the server turns a thrown error into an error result', async () => {
  const cfg = loadConfig({ BYGE_URL: base, BYGE_AUTH: 'none' });
  const server = makeServer(cfg, { byge_x: async () => { throw new Error('nope'); }, byge_y: async () => ({ ok: 1 }) });
  const call = server._requestHandlers.get('tools/call');
  const ctx = { signal: new AbortController().signal, requestId: 1, sendNotification: async () => {}, sendRequest: async () => {} };
  const x = await call({ method: 'tools/call', params: { name: 'byge_x', arguments: {} } }, ctx);
  assert.equal(x.isError, true);
  assert.equal(x.content[0].text, 'nope');
  const y = await call({ method: 'tools/call', params: { name: 'byge_y' } }, ctx);
  assert.deepEqual(JSON.parse(y.content[0].text), { ok: 1 });
  const inherited = await call({ method: 'tools/call', params: { name: 'toString' } }, ctx);
  assert.match(inherited.content[0].text, /unknown tool: toString/);
});
