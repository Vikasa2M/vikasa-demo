#!/bin/sh
# Wire the AI segment's two MCP servers (Grafana + ClickHouse) into a local MCP
# client in one step: issue fresh credentials via `democtl ai-setup`, then
# register both servers with the client using the correct stdio commands.
#
# Usage:
#   tools/ai/wire-mcp.sh claude          # wire Claude Code
#   tools/ai/wire-mcp.sh codex           # wire Codex CLI
#   tools/ai/wire-mcp.sh claude --dry-run # print the commands, change nothing
#
# The client (`claude` or `codex`) must be installed and on PATH, and the demo
# stack must be up (`make demo` / `make demo-small`) so ai-setup can reach
# Grafana. See demo/ai/mcp/README.md for the manual equivalent and per-client
# notes, and docs/GETTING-STARTED.md for the full AI-segment walkthrough.
#
# Note: `democtl ai-setup` ROTATES the Grafana token each run, so wiring a
# second client invalidates the first's token. Wire the one client you'll
# record with.
set -eu

CLIENT="${1:-}"
DRY=""
[ "${2:-}" = "--dry-run" ] && DRY=1

case "$CLIENT" in
  claude|codex) ;;
  *) echo "usage: $0 [claude|codex] [--dry-run]" >&2; exit 2 ;;
esac

# Run from the repo root regardless of where this is invoked (script lives in
# tools/ai/).
REPO="$(CDPATH= cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO"

# Skip the install check under --dry-run so the commands can be previewed
# without the client present.
[ -n "$DRY" ] || command -v "$CLIENT" >/dev/null 2>&1 || {
  echo "error: '$CLIENT' is not on your PATH — install it first." >&2; exit 1; }

GRAFANA_URL="http://host.docker.internal:3000"

# ClickHouse env is fixed (read-only user provisioned in the ClickHouse config).
CH_ENV="CLICKHOUSE_HOST=localhost CLICKHOUSE_PORT=8123 CLICKHOUSE_USER=ai_readonly CLICKHOUSE_PASSWORD=vikasa-ai CLICKHOUSE_SECURE=false"

run() {
  if [ -n "$DRY" ]; then printf '  %s\n' "$*"; else "$@"; fi
}

echo ">> issuing a fresh Grafana token via democtl ai-setup ..."
if [ -n "$DRY" ]; then
  TOKEN="<token from: go run ./cmd/democtl ai-setup>"
else
  TOKEN="$(go run ./cmd/democtl ai-setup | awk -F= '/^GRAFANA_SERVICE_ACCOUNT_TOKEN=/{print $2; exit}')"
  [ -n "$TOKEN" ] || { echo "error: could not read a Grafana token from ai-setup (is the stack up?)" >&2; exit 1; }
fi

echo ">> wiring $CLIENT (grafana + clickhouse) ..."
if [ "$CLIENT" = "claude" ]; then
  run claude mcp remove grafana    2>/dev/null || true
  run claude mcp remove clickhouse 2>/dev/null || true
  run claude mcp add grafana \
    -e GRAFANA_URL="$GRAFANA_URL" -e GRAFANA_SERVICE_ACCOUNT_TOKEN="$TOKEN" \
    -- docker run --rm -i -e GRAFANA_URL -e GRAFANA_SERVICE_ACCOUNT_TOKEN grafana/mcp-grafana -t stdio
  # shellcheck disable=SC2086
  run claude mcp add clickhouse \
    $(for kv in $CH_ENV; do printf -- '-e %s ' "$kv"; done) \
    -- uv run --with mcp-clickhouse --python 3.10 mcp-clickhouse
  echo ">> done. Check with: claude mcp list"
else
  run codex mcp remove grafana    2>/dev/null || true
  run codex mcp remove clickhouse 2>/dev/null || true
  run codex mcp add grafana \
    --env GRAFANA_URL="$GRAFANA_URL" --env GRAFANA_SERVICE_ACCOUNT_TOKEN="$TOKEN" \
    -- docker run --rm -i -e GRAFANA_URL -e GRAFANA_SERVICE_ACCOUNT_TOKEN grafana/mcp-grafana -t stdio
  # shellcheck disable=SC2086
  run codex mcp add clickhouse \
    $(for kv in $CH_ENV; do printf -- '--env %s ' "$kv"; done) \
    -- uv run --with mcp-clickhouse --python 3.10 mcp-clickhouse
  echo ">> done. Check with: codex mcp list (or /mcp in the Codex TUI)"
fi

echo ">> next: make ai-models-pack, then attach demo/ai/models-pack.generated.yang,"
echo "   send demo/ai/prompts/system-models-only.md as the system prompt and"
echo "   demo/ai/prompts/task-corridor.md as the task. See docs/GETTING-STARTED.md."
