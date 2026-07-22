#!/usr/bin/env bash
set -euo pipefail

# lint-dashboards.sh — static checks over deploy/grafana/dashboards/*.json.
#
# Usage: scripts/lint-dashboards.sh [dashboard-dir]
# Run standalone or via CI's dashboard-lint job (.github/workflows/ci.yaml).
#
# Rules enforced:
#   1. Every dashboard JSON file must be valid JSON.
#   2. No panel target's query may use `match(` — multi-value template
#      variables must use IN, never regex match(). match() on a
#      LowCardinality column is the vikasa anti-pattern this repo
#      deliberately avoids.
#   3. Every panel target's datasource uid must be one of the known,
#      provisioned datasource uids (clickhouse-vikasa, prometheus) — a
#      typo'd uid renders a silently-broken panel.
#   4. No generated topology output is committed under
#      deploy/topology/rendered/ — that tree is git-ignored `make topology`
#      output, not source.
#
# Deliberately NOT checked: $__timeFilter presence. Several dashboards
# (Fleet Health, Resilience Lab, DMS Status, ...) legitimately use
# `now() - INTERVAL ...` snapshots for latest-state/active-fault panels
# instead of honoring the time picker — that's correct, not a lint
# violation, so this script doesn't second-guess it.

DASHBOARD_DIR="${1:-deploy/grafana/dashboards}"
KNOWN_DATASOURCE_UIDS=("clickhouse-vikasa" "prometheus")

fail=0

shopt -s nullglob
files=("$DASHBOARD_DIR"/*.json)
if [ ${#files[@]} -eq 0 ]; then
  echo "lint-dashboards: no dashboard JSON files found under $DASHBOARD_DIR" >&2
  exit 1
fi

for f in "${files[@]}"; do
  echo "== $f =="

  # 1. valid JSON
  if ! jq empty "$f" >/dev/null 2>&1; then
    echo "  FAIL: invalid JSON"
    fail=1
    continue
  fi

  # 2. no match( in any panel target's query (rawSql for ClickHouse, expr for
  # Prometheus — check both, plus the raw target object as a fallback).
  bad_match=$(jq -r '
    [.panels[]? | .targets[]? | (.rawSql // .expr // "") | select(contains("match("))] | length
  ' "$f")
  if [ "$bad_match" != "0" ]; then
    echo "  FAIL: $bad_match panel target(s) use match( — use IN instead"
    fail=1
  fi

  # 3. every panel target's datasource uid is known
  uids=$(jq -r '[.panels[]? | .targets[]? | .datasource?.uid // empty] | unique | .[]' "$f")
  while IFS= read -r uid; do
    [ -z "$uid" ] && continue
    known=0
    for k in "${KNOWN_DATASOURCE_UIDS[@]}"; do
      [ "$uid" = "$k" ] && known=1 && break
    done
    if [ "$known" -ne 1 ]; then
      echo "  FAIL: unknown panel target datasource uid '$uid'"
      fail=1
    fi
  done <<< "$uids"
done

# 4. no committed rendered topology output
rendered="$(git ls-files 'deploy/topology/rendered/*' 2>/dev/null || true)"
if [ -n "$rendered" ]; then
  echo "FAIL: committed generated topology output found:"
  echo "$rendered"
  fail=1
fi

if [ "$fail" -ne 0 ]; then
  echo "lint-dashboards: FAILED"
  exit 1
fi
echo "lint-dashboards: all checks passed (${#files[@]} dashboards)"
