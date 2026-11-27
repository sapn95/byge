# byge-mcp

An [MCP](https://modelcontextprotocol.io) server for
[byge](https://github.com/sapn95/byge), the self-hosted kanban board. It lets an
agent read boards and file, move, tick off and comment on cards through byge's
JSON API.

It lives in the byge repository, next to the server, and its tests run against
a byge built from the same commit. A change to the API that would break the
tools fails CI there.

## Install

Claude Code:

``` bash
claude mcp add byge --scope user \
  --env BYGE_URL=https://kanban.example.org \
  -- npx -y byge-mcp
```

Any other MCP client takes the same command and environment:

``` json
{
  "mcpServers": {
    "byge": {
      "command": "npx",
      "args": ["-y", "byge-mcp"],
      "env": { "BYGE_URL": "https://kanban.example.org" }
    }
  }
}
```

Node 20.19 or newer.

## Signing in

The API has no tokens of its own. It grants what the board grants to whoever
reaches it, so the server signs in the way the board is protected.

| `BYGE_AUTH` | What it sends | When |
|---|---|---|
| `cloudflared` | `cf-access-token` from `cloudflared access token -app=$BYGE_URL` | Default for an `https://` URL. The agent acts as the person who signed in, so `@me` and comment authors are that person. |
| `service-token` | `CF-Access-Client-Id` and `CF-Access-Client-Secret` | Default when `CF_ACCESS_CLIENT_ID` and `CF_ACCESS_CLIENT_SECRET` are set. A service token has no address: `@me` is a `403` for it, and it sees a board only once the board grants it. |
| `none` | nothing | Default for an `http://` URL, such as a board on localhost or reached inside a cluster. |

With `cloudflared`, sign in once with `cloudflared access login $BYGE_URL`, or
let the agent call `byge_login`, which starts that and returns the URL to open.
The token is cached until a minute before it expires and is fetched again from
cloudflared after that. When the sign-in has run out, every tool says so and
names `byge_login`.

`BYGE_HEADERS` adds headers to every request, as a JSON object. Behind a proxy
that reads identity from a header (`AUTH_MODE=proxy`), that is
`{"X-Forwarded-Email":"someone@example.org"}`.
`BYGE_CLOUDFLARED` points at a cloudflared that is not on `PATH`.

## Tools

| Tool | Does |
|---|---|
| `byge_whoami` | Which server, which auth, whether the API answers, which boards |
| `byge_login` | Starts `cloudflared access login` and returns the URL to open |
| `byge_list_boards`, `byge_get_board` | Boards with their columns, WIP limits and labels |
| `byge_create_board` | A new board; the caller owns it |
| `byge_list_cards` | Cards by column, with the board search (`label:`, `assignee:`, `due:`, `is:archived`) |
| `byge_get_card` | One card with its checklist and comments |
| `byge_create_card` | A card, with column and labels by name |
| `byge_update_card` | Changes the fields given and keeps the rest |
| `byge_move_card` | Moves a card to the top or bottom of a column |
| `byge_set_subtask` | Ticks or unticks one checklist line |
| `byge_comment` | A comment, authored by the caller |
| `byge_archive_card` | Archives a card, or restores one |
| `byge_delete_card` | Deletes a card. Needs `confirm: true` |
| `byge_create_label` | A label on a board |
| `byge_api` | Any `/api/v1/` route, for columns, layout, SLA and bulk changes. `DELETE` needs `confirm: true` |

Boards are named by slug, id or name, and columns and labels by id or name in
any case. A name that matches nothing gets an error that lists the names that
exist.

Where the tools behave differently from the API:

- `PUT /cards/{id}` replaces the whole card and clears what it is not sent.
  `byge_update_card` reads the card first and sends back the merged result, so
  an agent that only changes the title keeps the description.
- The API toggles labels and subtasks. The tools take the state you want, so
  sending the same call twice gives the same card.
- Checklist lines given by title keep their id when the title already exists,
  so a rewritten checklist does not replace lines that were already there.

## Develop

``` bash
cd mcp
npm ci
npm run gate    # syntax, lint, protocol smoke test, tests with coverage
```

The tests build byge from `../cmd/byge` with Go and run it with the memory
store and proxy auth. To use a binary built elsewhere, set `BYGE_BIN`.

## Release

Tags `mcp-v*` publish to npm through `.github/workflows/mcp-release.yml`, with
npm Trusted Publishing and no token. Tags `v*` release the board and do not
touch this package.

``` bash
cd mcp && npm version patch --no-git-tag-version
# commit, open a pull request, merge, then on the merged commit:
git tag mcp-v$(node -p 'require("./package.json").version') && git push origin --tags
```

## Licence

MIT, as the rest of byge.
