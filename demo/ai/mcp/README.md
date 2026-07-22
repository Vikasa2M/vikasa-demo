# Wiring the AI segment's MCP servers

The demo's "models-only" AI segment hands an external AI exactly two tools —
a read-only ClickHouse server and a Grafana server scoped to one folder —
plus the YANG models pack as an attached file. Both servers are official,
upstream, third-party projects; this repo doesn't vendor or modify either
one. This doc is about *wiring them into an MCP client*, not building them.

Server invocation/env var names below were confirmed live against each
project's current upstream README on 2026-07-15 (`grafana/mcp-grafana` and
`ClickHouse/mcp-clickhouse` on GitHub). MCP servers evolve — if a client
rejects the config below, re-check those READMEs before assuming this repo
is wrong.

## 1. `democtl ai-setup` first

```sh
go run ./cmd/democtl ai-setup
```

This is idempotent (re-run it before every take — see `docs/RUNBOOK.md`)
and prints a ready-to-paste env block:

```
GRAFANA_URL=http://localhost:3000
GRAFANA_SERVICE_ACCOUNT_TOKEN=glsa_...
GRAFANA_API_KEY=glsa_...
CLICKHOUSE_HOST=localhost
CLICKHOUSE_PORT=8123
CLICKHOUSE_USER=ai_readonly
CLICKHOUSE_PASSWORD=vikasa-ai
```

`GRAFANA_SERVICE_ACCOUNT_TOKEN` and `GRAFANA_API_KEY` are the same token
value printed twice under both names -- `mcp-grafana` deprecated
`GRAFANA_API_KEY` in favor of `GRAFANA_SERVICE_ACCOUNT_TOKEN`, but still
honors the old name, and MCP client versions differ in which one they
expect. Use whichever your client's config recognizes.

The token can only create/edit dashboards in the "AI Built" folder (see
`internal/ai`) -- if the AI (or a typo in your MCP config)
tries to touch anything else in Grafana, it gets a 403.

## 2. The Grafana server: `grafana/mcp-grafana`

- **Image**: `grafana/mcp-grafana` (Docker Hub / GHCR).
- **Env**: `GRAFANA_URL`, `GRAFANA_SERVICE_ACCOUNT_TOKEN` (or the
  deprecated `GRAFANA_API_KEY` alias).
- **Stdio invocation** (what every local MCP client below uses):

  ```sh
  docker run --rm -i \
    -e GRAFANA_URL -e GRAFANA_SERVICE_ACCOUNT_TOKEN \
    grafana/mcp-grafana -t stdio
  ```

  `-t stdio` is required -- the image defaults to SSE mode otherwise.
  `GRAFANA_URL` must be reachable *from inside the container*:
  `http://host.docker.internal:3000` on macOS/Windows Docker Desktop, not
  `http://localhost:3000` (that resolves to the container itself). On Linux
  Docker, use `--network host` and `http://localhost:3000` instead, or the
  Docker bridge gateway IP.
- Dashboard create/update ("write") tools are enabled by default; no extra
  flag needed. (`--disable-write` would turn them off -- don't pass it.)

## 3. The ClickHouse server: `mcp-clickhouse` (ClickHouse/mcp-clickhouse)

- **Package**: `mcp-clickhouse` (PyPI). No local install needed with `uv`.
- **Env**: `CLICKHOUSE_HOST`, `CLICKHOUSE_PORT`, `CLICKHOUSE_USER`,
  `CLICKHOUSE_PASSWORD`, `CLICKHOUSE_SECURE` (`"false"` for our plain-HTTP
  local ClickHouse on :8123 -- there's no TLS in this demo stack).
- **Invocation**:

  ```sh
  uv run --with mcp-clickhouse --python 3.10 mcp-clickhouse
  ```

  (requires [`uv`](https://docs.astral.sh/uv/) installed; it fetches the
  package into an ephemeral env on first run.)
- **Belt and suspenders**: `mcp-clickhouse` itself defaults to
  `CLICKHOUSE_ALLOW_WRITE_ACCESS=false` (read-only at the MCP-tool level).
  Don't set it to `true` for this demo -- combined with the `ai_readonly`
  ClickHouse user (`readonly=1` server-side, see
  `deploy/clickhouse/users.d/ai-readonly.xml`), a mutating query is
  rejected twice over: once by the MCP server's own tool gating, and again
  by ClickHouse itself even if that gating were bypassed.

See `mcp-config.example.json` in this directory for both servers in one
config block (Claude Desktop / Claude Code JSON format).

## Per-client setup

**Shortcut for Claude Code / Codex:** `tools/ai/wire-mcp.sh [claude|codex]`
(or `make ai-wire-claude` / `make ai-wire-codex`) does the whole thing in one
step — runs `democtl ai-setup` for a fresh token and registers both servers
with the client. Add `--dry-run` to preview the exact commands first. The manual
steps below are what that script automates, and are still the reference for
Claude Desktop and ChatGPT.

### Claude Code

```sh
claude mcp add --env GRAFANA_URL=http://host.docker.internal:3000 \
  --env GRAFANA_SERVICE_ACCOUNT_TOKEN=<token from ai-setup> \
  --transport stdio grafana \
  -- docker run --rm -i -e GRAFANA_URL -e GRAFANA_SERVICE_ACCOUNT_TOKEN grafana/mcp-grafana -t stdio

claude mcp add --env CLICKHOUSE_HOST=localhost --env CLICKHOUSE_PORT=8123 \
  --env CLICKHOUSE_USER=ai_readonly --env CLICKHOUSE_PASSWORD=vikasa-ai \
  --env CLICKHOUSE_SECURE=false \
  --transport stdio clickhouse \
  -- uv run --with mcp-clickhouse --python 3.10 mcp-clickhouse
```

Default scope (`local`) is fine for a demo rehearsal -- it's private to
this checkout and machine. Attach `demo/ai/models-pack.generated.yang` to
the chat, paste `system-models-only.md` as the system prompt (or the
start of the conversation) and `task-corridor.md` as the task, same as
any other file+prompt in Claude Code.

### Claude Desktop

Edit the app's MCP config file (macOS:
`~/Library/Application Support/Claude/claude_desktop_config.json`; Windows:
`%APPDATA%\Claude\claude_desktop_config.json`) and merge in
`mcp-config.example.json`'s `mcpServers` block, substituting the real token
from `democtl ai-setup`. Restart Claude Desktop to pick it up. Attach the
models pack as a file in the chat the same way.

### Codex CLI (OpenAI)

Codex is OpenAI's local coding-agent CLI. Like Claude Code, it runs MCP servers
as **local stdio subprocesses -- no tunnel needed** (unlike the ChatGPT app
below, whose MCP client runs in OpenAI's cloud). Add the two servers once; they
persist in `~/.codex/config.toml`:

```sh
codex mcp add grafana \
  --env GRAFANA_URL=http://host.docker.internal:3000 \
  --env GRAFANA_SERVICE_ACCOUNT_TOKEN=<token from ai-setup> \
  -- docker run --rm -i -e GRAFANA_URL -e GRAFANA_SERVICE_ACCOUNT_TOKEN grafana/mcp-grafana -t stdio

codex mcp add clickhouse \
  --env CLICKHOUSE_HOST=localhost --env CLICKHOUSE_PORT=8123 \
  --env CLICKHOUSE_USER=ai_readonly --env CLICKHOUSE_PASSWORD=vikasa-ai \
  --env CLICKHOUSE_SECURE=false \
  -- uv run --with mcp-clickhouse --python 3.10 mcp-clickhouse
```

Confirm both are connected with `/mcp` in the Codex TUI. Codex reads files by
path from its working directory, so there is no "attach" step -- run Codex from
the repo and point it at `demo/ai/models-pack.generated.yang`,
`demo/ai/prompts/system-models-only.md`, and `demo/ai/prompts/task-corridor.md`.
Two Codex-specific notes: it is a coding agent, so tell it explicitly to build
the dashboard via MCP and not edit repo files (or run it in a scratch dir); and
it prompts for approval before each tool call by default -- use its auto-approval
mode for an uninterrupted take.

### ChatGPT (Developer Mode / Apps)

ChatGPT's MCP support is **remote-only** (HTTPS, Developer Mode under
Settings -> Apps) -- it cannot spawn a local `docker run`/`uv run` process
the way Claude Code/Desktop do. To use these same two servers with ChatGPT
you must additionally:

1. Run each server in HTTP/SSE mode instead of stdio (drop `-t stdio` for
   `mcp-grafana`; set `CLICKHOUSE_MCP_SERVER_TRANSPORT=http` for
   `mcp-clickhouse`).
2. Expose each locally-running server over HTTPS with a tunnel (e.g.
   `ngrok`, or ChatGPT's own Secure MCP Tunnel) -- both servers listening
   on `localhost` are not reachable from ChatGPT's servers otherwise.
3. Add each tunnel URL as a custom connector under Developer Mode, and
   re-enable it per chat.

This is meaningfully more setup than the two local-client paths above and
wasn't exercised for the recorded take -- budget real time for it before
relying on ChatGPT for a live recording.

## A note on drift

Both servers are fast-moving upstream projects. Before a real recording
session, sanity-check this doc against:

- <https://github.com/grafana/mcp-grafana>
- <https://github.com/ClickHouse/mcp-clickhouse>

particularly the env var names and default-enabled tool categories, in
case either project has renamed something since 2026-07-15.
