// The tools against a real byge. Every call goes through the same client the
// server uses, so what is checked here is what an agent gets.
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { loadConfig, makeClient, makeHandlers } from '../lib.js';
import { startByge, OWNER } from './byge.mjs';

let srv;
let h;
let anon;

before(async () => {
  srv = await startByge();
  const cfg = loadConfig({ BYGE_URL: srv.url + '/', BYGE_HEADERS: JSON.stringify({ 'X-User': OWNER }) });
  h = makeHandlers(cfg, makeClient(cfg));
  const acfg = loadConfig({ BYGE_URL: srv.url });
  anon = makeHandlers(acfg, makeClient(acfg));
});
after(() => srv?.stop());

test('whoami reports the server and its boards', async () => {
  const w = await h.byge_whoami();
  assert.equal(w.api, 'ok');
  assert.equal(w.auth, 'none');
  assert.deepEqual(w.boards, []);
});

test('the whole card lifecycle, by names', async () => {
  const b = await h.byge_create_board({ name: 'Example Agents', columns: ['To Do', 'Waiting', 'Done'] });
  assert.equal(b.slug, 'example-agents');
  assert.equal(b.url, `${srv.url}/b/example-agents`);
  assert.deepEqual(b.columns.map(c => c.name), ['To Do', 'Waiting', 'Done']);

  const l = await h.byge_create_label({ board: 'Example Agents', name: 'Example_Label', color: '#3b82f6' });
  assert.equal(l.name, 'Example_Label');
  await h.byge_create_label({ board: b.slug, name: 'Other' });

  const boards = await h.byge_list_boards();
  assert.equal(boards.length, 1);
  assert.equal((await h.byge_get_board({ board: b.id })).slug, b.slug);

  const c = await h.byge_create_card({
    board: 'example-agents', title: 'Example card', column: 'waiting', description: 'first',
    due_date: '2030-01-31', assignee: '@me', labels: ['example_label'],
    subtasks: ['one', { title: 'two', done: true }],
  });
  assert.equal(c.column, 'Waiting');
  assert.equal(c.assignee, OWNER);
  assert.deepEqual(c.labels, ['Example_Label']);
  assert.deepEqual(c.subtasks.map(s => [s.title, s.done]), [['one', false], ['two', true]]);
  const firstIds = c.subtasks.map(s => s.id);

  // A partial update keeps every field it does not name.
  const u = await h.byge_update_card({ card: c.id, title: 'Renamed', append_description: 'second', add_labels: ['Other'] });
  assert.equal(u.title, 'Renamed');
  assert.equal(u.description, 'first\n\nsecond');
  assert.equal(u.due_date, '2030-01-31');
  assert.equal(u.assignee, OWNER);
  assert.deepEqual(u.labels.sort(), ['Example_Label', 'Other']);
  assert.deepEqual(u.subtasks.map(s => s.id), firstIds);

  const r = await h.byge_update_card({ card: c.id, remove_labels: ['Example_Label'], due_date: '', assignee: '' });
  assert.deepEqual(r.labels, ['Other']);
  assert.equal(r.due_date, undefined);
  assert.equal(r.assignee, undefined);

  const r2 = await h.byge_update_card({ card: c.id, labels: [], subtasks: ['two', 'three'], description: 'only' });
  assert.equal(r2.labels, undefined);
  assert.equal(r2.description, 'only');
  assert.equal(r2.subtasks[0].id, firstIds[1], 'a line kept by title keeps its id');
  assert.equal(r2.subtasks[0].done, true);
  const r3 = await h.byge_update_card({ card: c.id, append_description: 'appended to nothing', description: '' });
  assert.equal(r3.description, 'appended to nothing');

  const t = await h.byge_set_subtask({ card: c.id, subtask: 'THREE', done: true });
  assert.equal(t.subtasks.find(s => s.title === 'three').done, true);
  const t2 = await h.byge_set_subtask({ card: c.id, subtask: 'four', done: false, add: true });
  assert.equal(t2.subtasks.length, 3);
  await assert.rejects(h.byge_set_subtask({ card: c.id, subtask: 'five', done: true }), /No subtask "five".*"two"/);

  // Moving: to the bottom by default, to the top on request.
  const d = await h.byge_create_card({ board: b.slug, title: 'Second', column: 'Done' });
  const m = await h.byge_move_card({ card: c.id, column: 'Done' });
  assert.equal(m.column, 'Done');
  assert.equal(m.position, 2);
  const m2 = await h.byge_move_card({ card: c.id, column: 'done', position: 'top' });
  assert.equal(m2.position, 1);
  const moved = await h.byge_update_card({ card: d.id, column: 'To Do' });
  assert.equal(moved.column, 'To Do');

  const list = await h.byge_list_cards({ board: b.slug });
  assert.deepEqual(list.columns.map(x => [x.name, x.count]), [['To Do', 1], ['Waiting', 0], ['Done', 1]]);
  assert.equal(list.columns[2].cards[0].subtasks, '2/3 done');
  const only = await h.byge_list_cards({ board: b.slug, column: 'Done' });
  assert.deepEqual(only.columns.map(x => x.name), ['Done']);
  const found = await h.byge_list_cards({ board: b.slug, q: 'Renamed' });
  assert.equal(found.columns.reduce((n, x) => n + x.count, 0), 1);

  const cm = await h.byge_comment({ card: c.id, body: 'Example comment' });
  assert.equal(cm.author, OWNER);
  const full = await h.byge_get_card({ card: c.id });
  assert.equal(full.comments.length, 1);
  assert.equal(full.comments[0].body, 'Example comment');
  assert.equal(full.board, b.slug);
  assert.equal((await h.byge_get_card({ card: c.id, comments: false })).comments, undefined);

  const a = await h.byge_archive_card({ card: c.id });
  assert.ok(a.id);
  const arch = await h.byge_list_cards({ board: b.slug, archived: true });
  assert.equal(arch.archived.length, 1);
  await h.byge_archive_card({ card: c.id, restore: true });
  assert.equal((await h.byge_list_cards({ board: b.slug, archived: true })).archived.length, 0);

  await assert.rejects(h.byge_delete_card({ card: d.id }), /confirm: true/);
  assert.deepEqual(await h.byge_delete_card({ card: d.id, confirm: true }), { deleted: d.id });
  await assert.rejects(h.byge_get_card({ card: d.id }), /404/);
});

test('names that do not resolve say what exists', async () => {
  await assert.rejects(h.byge_create_card({ board: 'example-agents', title: 'x', column: 'Nope' }),
    /No column "Nope" on board "example-agents". There are: "To Do", "Waiting", "Done"/);
  await assert.rejects(h.byge_create_card({ board: 'example-agents', title: 'x', labels: ['Nope'] }), /No label "Nope"/);
  await assert.rejects(h.byge_get_board({ board: 'no-such-board' }), /404/);
  await assert.rejects(h.byge_get_board({ board: '' }), /No board given/);
});

test('a WIP limit refusal comes back as the API worded it', async () => {
  const b = await h.byge_create_board({ name: 'Example WIP', columns: ['A', 'B'] });
  const col = b.columns.find(c => c.name === 'B');
  await h.byge_api({ method: 'PATCH', path: `/api/v1/boards/${b.slug}/columns/${col.id}`, body: { name: 'B', wip_limit: 1 } });
  await h.byge_create_card({ board: b.slug, title: 'fills B', column: 'B' });
  const c = await h.byge_create_card({ board: b.slug, title: 'wants B' });
  await assert.rejects(h.byge_move_card({ card: c.id, column: 'B' }), /409/);
  await assert.rejects(h.byge_update_card({ card: c.id, column: 'B' }), /409/);
  const view = await h.byge_get_board({ board: b.slug });
  assert.equal(view.columns.find(x => x.name === 'B').wip_limit, 1);
});

test('the raw API tool', async () => {
  const index = await h.byge_api({ method: 'get', path: '/api/v1/' });
  assert.ok(index);
  await assert.rejects(h.byge_api({ method: 'GET', path: '/boards' }), /must start with \/api\/v1\//);
  await assert.rejects(h.byge_api({ method: 'TRACE', path: '/api/v1/' }), /not supported/);
  await assert.rejects(h.byge_api({ method: 'DELETE', path: '/api/v1/boards/example-wip' }), /confirm: true/);
  const gone = await h.byge_api({ method: 'DELETE', path: '/api/v1/boards/example-wip', confirm: true });
  assert.deepEqual(gone, { status: 'done' });
  await assert.rejects(h.byge_api({ method: 'POST', path: '/api/v1/boards', body: { titel: 'x' } }), /400/);
});

test('a caller without a grant sees no boards', async () => {
  assert.deepEqual(await anon.byge_list_boards(), []);
  assert.match((await anon.byge_login()).note, /nothing to sign in to/);
});
