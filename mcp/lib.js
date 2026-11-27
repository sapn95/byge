// byge-mcp: an MCP server for byge, the kanban board in this repository.
//
// It speaks to byge's JSON API (/api/v1, see internal/api/openapi.json) and
// nothing else, so every rule about WIP limits, grants and comment authors
// stays where it is written down: in the service behind that API.
//
// What it adds over calling the API directly:
//   - columns and labels by name as well as by id, so an agent can say
//     "Done" instead of reading the board first;
//   - updates that change only the fields given. The API replaces the whole
//     card on PUT and clears every field left out; this server reads the card
//     first and sends it back merged;
//   - label and subtask changes that state the wanted end state, where the
//     API only toggles;
//   - a sign-in through cloudflared when the board sits behind Cloudflare
//     Access, so the caller acts as the person who signed in.
//
// Deleting anything needs confirm:true. It cannot be undone.

import { Server } from '@modelcontextprotocol/sdk/server/index.js';
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js';
import { ListToolsRequestSchema, CallToolRequestSchema } from '@modelcontextprotocol/sdk/types.js';
import { readFileSync, openSync, closeSync, mkdtempSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { execFile, spawn } from 'node:child_process';

// Name and version come from package.json: `npm version` bumps only that file,
// and a second copy here would advertise a stale version to every client.
export const PKG = JSON.parse(readFileSync(new URL('./package.json', import.meta.url), 'utf8'));

const TIMEOUT_MS = 20000;

// ---------------------------------------------------------------- config

export function loadConfig(env = process.env) {
  let url = (env.BYGE_URL ?? '').trim();
  while (url.endsWith('/')) url = url.slice(0, -1);
  let headers = {};
  if (env.BYGE_HEADERS !== undefined && env.BYGE_HEADERS.trim() !== '') {
    try {
      headers = JSON.parse(env.BYGE_HEADERS);
    } catch {
      throw new Error('BYGE_HEADERS is not JSON. Expected an object such as {"X-User":"someone@example.org"}.');
    }
    if (headers === null || typeof headers !== 'object' || Array.isArray(headers)
      || Object.values(headers).some(v => typeof v !== 'string')) {
      throw new Error('BYGE_HEADERS must be a JSON object of string values.');
    }
  }
  const clientId = (env.CF_ACCESS_CLIENT_ID ?? '').trim();
  const clientSecret = (env.CF_ACCESS_CLIENT_SECRET ?? '').trim();
  let auth = (env.BYGE_AUTH ?? 'auto').trim().toLowerCase();
  if (!['auto', 'cloudflared', 'service-token', 'none'].includes(auth)) {
    throw new Error(`BYGE_AUTH "${auth}" is not one of auto, cloudflared, service-token, none.`);
  }
  if (auth === 'auto') {
    if (clientId && clientSecret) auth = 'service-token';
    else if (url.startsWith('https://')) auth = 'cloudflared';
    else auth = 'none';
  }
  if (auth === 'service-token' && !(clientId && clientSecret)) {
    throw new Error('BYGE_AUTH=service-token needs CF_ACCESS_CLIENT_ID and CF_ACCESS_CLIENT_SECRET.');
  }
  const cloudflared = (env.BYGE_CLOUDFLARED ?? 'cloudflared').trim();
  return { url, headers, auth, clientId, clientSecret, cloudflared };
}

// ---------------------------------------------------------------- auth

function run(cmd, args, timeout = 15000) {
  return new Promise(resolve => {
    execFile(cmd, args, { timeout, encoding: 'utf8' }, (err, stdout, stderr) => {
      resolve({ ok: !err, stdout: (stdout || '').trim(), stderr: (stderr || '').trim(), err });
    });
  });
}

// The expiry of a JWT, in milliseconds, or 0 when it cannot be read. Nothing
// here trusts the token: the edge checks it. This only decides when to ask
// cloudflared for a fresh one.
export function jwtExpiry(token) {
  try {
    const payload = JSON.parse(Buffer.from(token.split('.')[1], 'base64url').toString('utf8'));
    return typeof payload.exp === 'number' ? payload.exp * 1000 : 0;
  } catch { return 0; }
}

export class NotSignedIn extends Error {}

function makeAuth(cfg) {
  // One object, so the cache is written as properties: two calls racing for
  // a token both store a valid one, and either may win.
  const tok = { value: '', expires: 0 };
  return {
    async headers() {
      if (cfg.auth === 'service-token') {
        return { 'CF-Access-Client-Id': cfg.clientId, 'CF-Access-Client-Secret': cfg.clientSecret };
      }
      if (cfg.auth !== 'cloudflared') return {};
      if (tok.value && Date.now() < tok.expires - 60000) return { 'cf-access-token': tok.value };
      const r = await run(cfg.cloudflared, ['access', 'token', `-app=${cfg.url}`]);
      if (r.err && r.err.code === 'ENOENT') {
        throw new NotSignedIn(`cloudflared is not installed (looked for "${cfg.cloudflared}"). Install it, or set BYGE_AUTH=none or a service token.`);
      }
      if (!r.ok || !r.stdout || r.stdout.includes(' ')) {
        throw new NotSignedIn(`Not signed in to ${cfg.url}. Call byge_login, or run: cloudflared access login ${cfg.url}`);
      }
      tok.value = r.stdout;
      tok.expires = jwtExpiry(r.stdout) || Date.now() + 10 * 60000;
      return { 'cf-access-token': r.stdout };
    },
    forget() { tok.value = ''; tok.expires = 0; },
  };
}

// Start `cloudflared access login` and hand back the URL it prints. The
// process is left running: the token lands in that process once the person
// has signed in, and killing it while they do loses it. Its output goes to a
// file, not a pipe, so it survives this server exiting: a pipe with nobody
// reading it would kill cloudflared on its next write.
function startLogin(cfg) {
  return new Promise(resolve => {
    const log = join(mkdtempSync(join(tmpdir(), 'byge-mcp-login-')), 'cloudflared.log');
    const fd = openSync(log, 'w', 0o600);
    let done = false;
    const finish = r => {
      if (done) return;
      done = true;
      clearInterval(poll);
      clearTimeout(timer);
      resolve(r);
    };
    const child = spawn(cfg.cloudflared, ['access', 'login', cfg.url], { stdio: ['ignore', fd, fd], detached: true });
    closeSync(fd);
    const scan = () => {
      let out;
      try { out = readFileSync(log, 'utf8'); } catch { return ''; }
      // The login page is /cdn-cgi/access/cli on the application's own host.
      const m = out.match(/https:\/\/\S+\/cdn-cgi\/access\/cli\S*/);
      if (m) finish({ started: true, url: m[0] });
      else if (/Successfully fetched your token/i.test(out)) finish({ started: true, url: '', already: true });
      return out;
    };
    const poll = setInterval(scan, 100);
    const timer = setTimeout(() => finish({ started: true, url: '' }), 8000);
    child.on('error', e => finish({ started: false, error: e.code === 'ENOENT' ? `cloudflared not found ("${cfg.cloudflared}")` : String(e.message) }));
    child.on('exit', code => {
      const out = scan();
      finish({ started: code === 0, url: '', already: code === 0, error: code === 0 ? '' : out.trim().slice(-400) });
    });
    child.unref();
  });
}

// ---------------------------------------------------------------- client

export class ApiError extends Error {
  constructor(status, message) {
    super(message);
    this.status = status;
  }
}

export function makeClient(cfg, auth = makeAuth(cfg), fetchImpl = fetch) {
  async function request(method, path, body) {
    if (!cfg.url) throw new Error('BYGE_URL is not set. Point it at the board, e.g. https://kanban.example.org');
    const headers = { Accept: 'application/json', ...cfg.headers, ...(await auth.headers()) };
    const init = { method, headers, redirect: 'manual', signal: AbortSignal.timeout(TIMEOUT_MS) };
    if (body !== undefined) {
      headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(body);
    }
    let res;
    try {
      res = await fetchImpl(cfg.url + path, init);
    } catch (e) {
      // undici puts the reason in cause, and an AggregateError there when it
      // tried both address families.
      const why = e.name === 'TimeoutError'
        ? `no answer within ${TIMEOUT_MS / 1000}s`
        : (e.cause?.code || e.cause?.errors?.[0]?.code || e.cause?.message || e.message);
      throw new Error(`${method} ${path}: ${why}`, { cause: e });
    }
    // Cloudflare Access answers a request it does not accept with a redirect
    // to its sign-in page, or a 403 page in HTML. Neither is byge talking.
    if (res.status >= 300 && res.status < 400) {
      auth.forget?.();
      throw new NotSignedIn(`${method} ${path}: redirected to sign-in (${res.headers.get('location')?.split('?')[0] || 'no location'}). Call byge_login, or run: cloudflared access login ${cfg.url}`);
    }
    const text = await res.text();
    const type = res.headers.get('content-type') || '';
    let data;
    if (text && type.includes('json')) {
      try { data = JSON.parse(text); } catch { data = undefined; }
    }
    if (!res.ok) {
      if (data === undefined && (res.status === 401 || res.status === 403)) {
        auth.forget?.();
        throw new NotSignedIn(`${method} ${path}: ${res.status} from in front of byge (not JSON). The sign-in was refused or has expired. Call byge_login.`);
      }
      const msg = data && typeof data.error === 'string' ? data.error : text.trim().slice(0, 300) || res.statusText;
      throw new ApiError(res.status, `${method} ${path}: ${res.status} ${msg}`);
    }
    if (res.status === 204 || !text) return null;
    if (data === undefined) throw new Error(`${method} ${path}: expected JSON, got ${type || 'no content type'}`);
    return data;
  }
  return {
    get: p => request('GET', p),
    post: (p, b) => request('POST', p, b),
    put: (p, b) => request('PUT', p, b),
    patch: (p, b) => request('PATCH', p, b),
    del: p => request('DELETE', p),
    request,
  };
}

// ---------------------------------------------------------------- helpers

const enc = encodeURIComponent;
const norm = s => String(s ?? '').trim().toLowerCase();

// A column or label named by id, or by its name in any case. An ambiguous
// name is refused rather than guessed.
export function pick(list, ref, what, where) {
  const want = String(ref ?? '').trim();
  if (!want) throw new Error(`No ${what} given.`);
  const byId = list.find(x => x.id === want);
  if (byId) return byId;
  const hits = list.filter(x => norm(x.name) === norm(want));
  if (hits.length === 1) return hits[0];
  if (hits.length > 1) throw new Error(`${hits.length} ${what}s on ${where} are called "${want}". Use the id: ${hits.map(h => h.id).join(', ')}`);
  const names = list.map(x => `"${x.name}"`).join(', ') || 'none';
  throw new Error(`No ${what} "${want}" on ${where}. There are: ${names}`);
}

function makeBoards(api) {
  const cache = { list: null, at: 0 };
  async function all(fresh = false) {
    if (!cache.list || fresh || Date.now() - cache.at > 30000) {
      const list = await api.get('/api/v1/boards');
      cache.list = list;
      cache.at = Date.now();
    }
    return cache.list;
  }
  // The API's board paths take the slug. Accept the id and the name too,
  // because a card only knows its board_id.
  async function slugOf(ref) {
    const want = String(ref ?? '').trim();
    if (!want) throw new Error('No board given.');
    for (const fresh of [false, true]) {
      const list = await all(fresh);
      const hit = list.find(b => b.slug === want || b.id === want)
        || list.find(b => norm(b.name) === norm(want) || b.slug === norm(want));
      if (hit) return hit.slug;
    }
    return want; // let the API answer 404 with its own words
  }
  async function board(ref) {
    return api.get(`/api/v1/boards/${enc(await slugOf(ref))}`);
  }
  return { all, slugOf, board, drop() { cache.list = null; } };
}

export function cardView(card, board, { full = true } = {}) {
  const col = board?.columns.find(c => c.id === card.column_id);
  const labels = card.labels.map(id => board?.labels.find(l => l.id === id)?.name ?? id);
  const v = {
    id: card.id,
    title: card.title,
    column: col ? col.name : card.column_id,
    position: card.position,
    assignee: card.assignee || undefined,
    due_date: card.due_date || undefined,
    labels: labels.length ? labels : undefined,
  };
  if (card.subtasks.length) {
    v.subtasks = full
      ? card.subtasks.map(s => ({ id: s.id, title: s.title, done: s.done }))
      : `${card.subtasks.filter(s => s.done).length}/${card.subtasks.length} done`;
  }
  if (full) v.description = card.description || undefined;
  else if (card.description) v.description = card.description.length > 160 ? card.description.slice(0, 157) + '...' : card.description;
  if (card.archived_at) v.archived_at = card.archived_at;
  v.updated_at = card.updated_at;
  if (full) {
    v.created_at = card.created_at;
    v.board = board?.slug;
  }
  return v;
}

function boardView(b, url) {
  return {
    id: b.id,
    slug: b.slug,
    name: b.name,
    url: url ? `${url}/b/${b.slug}` : undefined,
    columns: b.columns.map(c => ({ id: c.id, name: c.name, ...(c.wip_limit ? { wip_limit: c.wip_limit } : {}) })),
    labels: b.labels.map(l => ({ id: l.id, name: l.name, ...(l.color ? { color: l.color } : {}) })),
  };
}

// The body a PUT /cards/{id} needs to leave everything but `change` as it is.
export function cardInput(card) {
  return {
    column_id: card.column_id,
    title: card.title,
    description: card.description,
    due_date: card.due_date,
    assignee: card.assignee,
    labels: [...card.labels],
    subtasks: card.subtasks.map(s => ({ id: s.id, title: s.title, done: s.done })),
  };
}

// Subtasks given as titles (strings) or {title, done}. A title that matches an
// existing line keeps that line's id, so its history is not replaced.
export function mergeSubtasks(existing, given) {
  const left = [...existing];
  return given.map(g => {
    const s = typeof g === 'string' ? { title: g } : g;
    const i = left.findIndex(e => (s.id && e.id === s.id) || norm(e.title) === norm(s.title));
    const prior = i >= 0 ? left.splice(i, 1)[0] : null;
    const out = { title: String(s.title ?? prior?.title ?? '') };
    if (prior) out.id = prior.id;
    out.done = s.done !== undefined ? Boolean(s.done) : Boolean(prior?.done);
    return out;
  });
}

const needConfirm = (args, what) => {
  if (args.confirm !== true) throw new Error(`${what} cannot be undone. Call again with confirm: true.`);
};

// ---------------------------------------------------------------- tools

const str = (description, extra = {}) => ({ type: 'string', description, ...extra });
const CARD = str('Card id.');
const BOARD = str('Board slug, id or name.');
const CONFIRM = { type: 'boolean', description: 'Must be true. Deleting cannot be undone.' };
const LABELS = { type: 'array', items: { type: 'string' }, description: 'Label names or ids on the board.' };
const SUBTASKS = {
  type: 'array',
  description: 'The whole checklist, in order. Strings are titles; objects may set done. A title matching an existing line keeps it.',
  items: {
    anyOf: [
      { type: 'string' },
      { type: 'object', properties: { title: { type: 'string' }, done: { type: 'boolean' } }, required: ['title'] },
    ],
  },
};

export const TOOLS = [
  {
    name: 'byge_login',
    description: 'Sign in to a board behind Cloudflare Access. Starts `cloudflared access login` and returns the URL the person has to open. Only needed when another tool says the caller is not signed in.',
    inputSchema: { type: 'object', properties: {} },
  },
  {
    name: 'byge_whoami',
    description: 'Which board server this talks to, how it authenticates, the server version, and whether the API answers.',
    inputSchema: { type: 'object', properties: {} },
  },
  {
    name: 'byge_list_boards',
    description: 'The boards the caller may read: slug, name, columns and labels of each.',
    inputSchema: { type: 'object', properties: {} },
  },
  {
    name: 'byge_get_board',
    description: 'One board with its columns (and WIP limits) and labels. Use it to see which column and label names exist.',
    inputSchema: { type: 'object', properties: { board: BOARD }, required: ['board'] },
  },
  {
    name: 'byge_create_board',
    description: 'Create a board. The caller becomes its owner. Left without columns it gets To Do, In Progress and Done.',
    inputSchema: {
      type: 'object',
      properties: {
        name: str('Board name.'),
        slug: str('URL slug, lower case and dashes. Derived from the name when left out.'),
        columns: { type: 'array', items: { type: 'string' }, description: 'Column names, left to right.' },
      },
      required: ['name'],
    },
  },
  {
    name: 'byge_list_cards',
    description: 'Cards on a board, grouped by column in board order. Descriptions are shortened; use byge_get_card for the whole card. q uses the board search: words, "quoted phrases", label:<name>, assignee:<part>|none, due:none|overdue|today|week, is:archived.',
    inputSchema: {
      type: 'object',
      properties: {
        board: BOARD,
        q: str('Search, as on the board page.'),
        column: str('Only this column (name or id).'),
        archived: { type: 'boolean', description: 'List the archive instead of the board.' },
      },
      required: ['board'],
    },
  },
  {
    name: 'byge_get_card',
    description: 'One card in full: description, checklist with subtask ids, labels by name, and its comments.',
    inputSchema: {
      type: 'object',
      properties: { card: CARD, comments: { type: 'boolean', description: 'Include the comment thread. Default true.' } },
      required: ['card'],
    },
  },
  {
    name: 'byge_create_card',
    description: 'File a card. Column and labels by name or id; the card goes to the bottom of the first column when no column is given.',
    inputSchema: {
      type: 'object',
      properties: {
        board: BOARD,
        title: str('Title, up to 200 characters.'),
        column: str('Column name or id.'),
        description: str('Markdown.'),
        due_date: str('YYYY-MM-DD.'),
        assignee: str('An address, or "@me" for the signed-in caller.'),
        labels: LABELS,
        subtasks: SUBTASKS,
      },
      required: ['board', 'title'],
    },
  },
  {
    name: 'byge_update_card',
    description: 'Change some fields of a card and keep the rest. Give only what changes. An empty string clears description, due_date or assignee. labels replaces the set; add_labels and remove_labels change it. subtasks replaces the checklist. column moves the card (a WIP limit may refuse with 409).',
    inputSchema: {
      type: 'object',
      properties: {
        card: CARD,
        title: str('New title.'),
        description: str('New description, Markdown.'),
        append_description: str('Text added to the end of the description, after a blank line.'),
        column: str('Move to this column (name or id).'),
        due_date: str('YYYY-MM-DD, or "" to remove.'),
        assignee: str('An address, "@me", or "" to unassign.'),
        labels: LABELS,
        add_labels: LABELS,
        remove_labels: LABELS,
        subtasks: SUBTASKS,
      },
      required: ['card'],
    },
  },
  {
    name: 'byge_move_card',
    description: 'Move a card to another column, at the bottom or the top. A WIP limit may refuse with 409.',
    inputSchema: {
      type: 'object',
      properties: {
        card: CARD,
        column: str('Column name or id.'),
        position: str('"bottom" (default) or "top".', { enum: ['bottom', 'top'] }),
      },
      required: ['card', 'column'],
    },
  },
  {
    name: 'byge_set_subtask',
    description: 'Tick or untick one checklist line, by subtask id or title. Adds the line when no line has that title and add is true.',
    inputSchema: {
      type: 'object',
      properties: {
        card: CARD,
        subtask: str('Subtask id or title.'),
        done: { type: 'boolean', description: 'true to tick, false to untick.' },
        add: { type: 'boolean', description: 'Add the line if it does not exist.' },
      },
      required: ['card', 'subtask', 'done'],
    },
  },
  {
    name: 'byge_comment',
    description: 'Write a comment on a card. Comments cannot be edited afterwards; the author is the signed-in caller.',
    inputSchema: {
      type: 'object',
      properties: { card: CARD, body: str('Comment text, up to 5000 characters.') },
      required: ['card', 'body'],
    },
  },
  {
    name: 'byge_archive_card',
    description: 'Take a card off the board and keep it in the archive, or put an archived card back with restore: true.',
    inputSchema: {
      type: 'object',
      properties: { card: CARD, restore: { type: 'boolean', description: 'Restore instead of archive.' } },
      required: ['card'],
    },
  },
  {
    name: 'byge_delete_card',
    description: 'Delete a card and its comments for good. Needs confirm: true. Prefer byge_archive_card.',
    inputSchema: { type: 'object', properties: { card: CARD, confirm: CONFIRM }, required: ['card', 'confirm'] },
  },
  {
    name: 'byge_create_label',
    description: 'Create a label on a board.',
    inputSchema: {
      type: 'object',
      properties: { board: BOARD, name: str('Label name.'), color: str('Hex colour such as #3b82f6.') },
      required: ['board', 'name'],
    },
  },
  {
    name: 'byge_api',
    description: 'Call any byge JSON API route directly, for what the other tools do not cover (columns, layout, SLA, bulk, board rename). GET /api/v1/openapi.json lists every route. DELETE needs confirm: true.',
    inputSchema: {
      type: 'object',
      properties: {
        method: str('HTTP method.', { enum: ['GET', 'POST', 'PUT', 'PATCH', 'DELETE'] }),
        path: str('Path starting with /api/v1/.'),
        body: { type: 'object', description: 'JSON body for POST, PUT and PATCH.' },
        confirm: { type: 'boolean', description: 'Must be true for DELETE.' },
      },
      required: ['method', 'path'],
    },
  },
];

export function makeHandlers(cfg, api, { login = () => startLogin(cfg) } = {}) {
  const boards = makeBoards(api);

  async function cardWithBoard(id) {
    const card = await api.get(`/api/v1/cards/${enc(id)}`);
    const list = await boards.all();
    let meta = list.find(b => b.id === card.board_id);
    if (!meta) meta = (await boards.all(true)).find(b => b.id === card.board_id);
    const board = meta ? await api.get(`/api/v1/boards/${enc(meta.slug)}`) : null;
    return { card, board };
  }
  const labelIds = (board, refs) => refs.map(r => pick(board.labels, r, 'label', `board "${board.slug}"`).id);
  const columnId = (board, ref) => pick(board.columns, ref, 'column', `board "${board.slug}"`).id;

  return {
    async byge_login() {
      if (cfg.auth !== 'cloudflared') {
        return { note: `BYGE_AUTH is ${cfg.auth}; there is nothing to sign in to.` };
      }
      const r = await login();
      if (!r.started) return { error: r.error || 'cloudflared did not start' };
      if (r.already) return { signed_in: true };
      return {
        open_this: r.url || '(cloudflared opened the browser itself; no URL was printed)',
        then: 'Sign in in the browser. cloudflared keeps waiting and stores the token; call byge_whoami afterwards to check.',
      };
    },

    async byge_whoami() {
      const out = { url: cfg.url, auth: cfg.auth, client: `${PKG.name} ${PKG.version}` };
      try {
        const index = await api.get('/api/v1/');
        out.api = index ? 'ok' : 'empty';
        out.boards = (await boards.all(true)).map(b => b.slug);
      } catch (e) {
        out.api = e.message;
        out.signed_in = !(e instanceof NotSignedIn);
      }
      return out;
    },

    async byge_list_boards() {
      return (await boards.all(true)).map(b => boardView(b, cfg.url));
    },

    async byge_get_board({ board }) {
      return boardView(await boards.board(board), cfg.url);
    },

    async byge_create_board({ name, slug, columns }) {
      const body = { name };
      if (slug) body.slug = slug;
      if (columns) body.columns = columns;
      const b = await api.post('/api/v1/boards', body);
      boards.drop();
      return boardView(b, cfg.url);
    },

    async byge_list_cards({ board, q, column, archived }) {
      const b = await boards.board(board);
      let path = `/api/v1/boards/${enc(b.slug)}/${archived ? 'archive' : 'cards'}`;
      if (q) path += `?q=${enc(q)}`;
      let cards = await api.get(path);
      const only = column ? columnId(b, column) : '';
      if (only) cards = cards.filter(c => c.column_id === only);
      if (archived) return { board: b.slug, archived: cards.map(c => cardView(c, b, { full: false })) };
      const out = { board: b.slug, columns: [] };
      for (const col of b.columns) {
        if (only && col.id !== only) continue;
        const here = cards.filter(c => c.column_id === col.id).sort((x, y) => x.position - y.position);
        out.columns.push({
          name: col.name,
          count: here.length,
          ...(col.wip_limit ? { wip_limit: col.wip_limit } : {}),
          cards: here.map(c => {
            const v = cardView(c, b, { full: false });
            delete v.column;
            return v;
          }),
        });
      }
      return out;
    },

    async byge_get_card({ card, comments = true }) {
      const { card: c, board } = await cardWithBoard(card);
      const v = cardView(c, board);
      if (comments) {
        const thread = await api.get(`/api/v1/cards/${enc(c.id)}/comments`);
        v.comments = thread.map(t => ({ id: t.id, author: t.author || 'anonymous', at: t.created_at, body: t.body }));
      }
      return v;
    },

    async byge_create_card({ board, title, column, description, due_date, assignee, labels, subtasks }) {
      const b = await boards.board(board);
      const body = { title };
      if (column) body.column_id = columnId(b, column);
      if (description !== undefined) body.description = description;
      if (due_date !== undefined) body.due_date = due_date;
      if (assignee !== undefined) body.assignee = assignee;
      if (labels) body.labels = labelIds(b, labels);
      if (subtasks) body.subtasks = mergeSubtasks([], subtasks);
      const c = await api.post(`/api/v1/boards/${enc(b.slug)}/cards`, body);
      return cardView(c, b);
    },

    async byge_update_card(args) {
      const { card: c, board } = await cardWithBoard(args.card);
      if (!board) throw new Error(`Card ${c.id} is on a board the caller cannot list.`);
      const body = cardInput(c);
      if (args.title !== undefined) body.title = args.title;
      if (args.description !== undefined) body.description = args.description;
      if (args.append_description) {
        body.description = body.description ? `${body.description.replace(/\s+$/, '')}\n\n${args.append_description}` : args.append_description;
      }
      if (args.column !== undefined) body.column_id = columnId(board, args.column);
      if (args.due_date !== undefined) body.due_date = args.due_date;
      if (args.assignee !== undefined) body.assignee = args.assignee;
      if (args.labels) body.labels = labelIds(board, args.labels);
      if (args.add_labels) for (const id of labelIds(board, args.add_labels)) if (!body.labels.includes(id)) body.labels.push(id);
      if (args.remove_labels) {
        const gone = new Set(labelIds(board, args.remove_labels));
        body.labels = body.labels.filter(id => !gone.has(id));
      }
      if (args.subtasks) body.subtasks = mergeSubtasks(c.subtasks, args.subtasks);
      const updated = await api.put(`/api/v1/cards/${enc(c.id)}`, body);
      return cardView(updated, board);
    },

    async byge_move_card({ card, column, position = 'bottom' }) {
      const { card: c, board } = await cardWithBoard(card);
      if (!board) throw new Error(`Card ${c.id} is on a board the caller cannot list.`);
      const target = columnId(board, column);
      // The order route moves a card into the column it names and places it
      // where the list says, in one request that a WIP limit checks once.
      const cards = await api.get(`/api/v1/boards/${enc(board.slug)}/cards`);
      const others = cards.filter(x => x.column_id === target && x.id !== c.id)
        .sort((x, y) => x.position - y.position).map(x => x.id);
      const order = position === 'top' ? [c.id, ...others] : [...others, c.id];
      await api.put(`/api/v1/boards/${enc(board.slug)}/columns/${enc(target)}/cards/order`, { order });
      return cardView(await api.get(`/api/v1/cards/${enc(c.id)}`), board);
    },

    async byge_set_subtask({ card, subtask, done, add = false }) {
      const { card: c, board } = await cardWithBoard(card);
      const body = cardInput(c);
      const want = String(subtask).trim();
      let line = body.subtasks.find(s => s.id === want) || body.subtasks.find(s => norm(s.title) === norm(want));
      if (!line) {
        if (!add) {
          const have = c.subtasks.map(s => `"${s.title}"`).join(', ') || 'none';
          throw new Error(`No subtask "${want}" on card ${c.id}. There are: ${have}. Pass add: true to add it.`);
        }
        line = { title: want, done: false };
        body.subtasks.push(line);
      }
      line.done = Boolean(done);
      const updated = await api.put(`/api/v1/cards/${enc(c.id)}`, body);
      return cardView(updated, board);
    },

    async byge_comment({ card, body }) {
      const t = await api.post(`/api/v1/cards/${enc(card)}/comments`, { body });
      return { id: t.id, card: t.card_id, author: t.author || 'anonymous', at: t.created_at };
    },

    async byge_archive_card({ card, restore = false }) {
      const c = await api.post(`/api/v1/cards/${enc(card)}/${restore ? 'restore' : 'archive'}`);
      return c ? { id: c.id, title: c.title, archived_at: c.archived_at } : { id: card, done: true };
    },

    async byge_delete_card(args) {
      needConfirm(args, 'Deleting a card');
      await api.del(`/api/v1/cards/${enc(args.card)}`);
      return { deleted: args.card };
    },

    async byge_create_label({ board, name, color }) {
      const slug = await boards.slugOf(board);
      const body = { name };
      if (color) body.color = color;
      return api.post(`/api/v1/boards/${enc(slug)}/labels`, body);
    },

    async byge_api({ method, path, body, confirm }) {
      const m = String(method).toUpperCase();
      if (!['GET', 'POST', 'PUT', 'PATCH', 'DELETE'].includes(m)) throw new Error(`Method ${method} is not supported.`);
      if (!String(path).startsWith('/api/v1/')) throw new Error('path must start with /api/v1/.');
      if (m === 'DELETE') needConfirm({ confirm }, `DELETE ${path}`);
      const r = await api.request(m, path, m === 'GET' || m === 'DELETE' ? undefined : (body ?? {}));
      if (m !== 'GET') boards.drop();
      return r ?? { status: 'done' };
    },
  };
}

// ---------------------------------------------------------------- server

export function makeServer(cfg, handlers) {
  const server = new Server({ name: PKG.name, version: PKG.version }, { capabilities: { tools: {} } });
  server.setRequestHandler(ListToolsRequestSchema, async () => ({ tools: TOOLS }));
  server.setRequestHandler(CallToolRequestSchema, async req => {
    const { name, arguments: args = {} } = req.params;
    const h = handlers[name];
    if (!h) return { isError: true, content: [{ type: 'text', text: `unknown tool: ${name}` }] };
    try {
      const out = await h(args);
      return { content: [{ type: 'text', text: JSON.stringify(out, null, 2) }] };
    } catch (e) {
      return { isError: true, content: [{ type: 'text', text: e.message || String(e) }] };
    }
  });
  return server;
}

export async function main() {
  let cfg;
  try {
    cfg = loadConfig();
  } catch (e) {
    process.stderr.write(`byge-mcp: ${e.message}\n`);
    process.exit(2);
  }
  const api = makeClient(cfg);
  const server = makeServer(cfg, makeHandlers(cfg, api));
  await server.connect(new StdioServerTransport());
  // A client that closes stdin is done with us. Without this the open
  // transport keeps the process alive after the client has gone.
  process.stdin.on('close', () => setTimeout(() => process.exit(0), 50).unref());
}
