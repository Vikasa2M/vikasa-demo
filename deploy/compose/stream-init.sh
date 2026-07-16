#!/bin/sh
set -eu
# usage: stream-init.sh <dot> ; rendered specs mounted at /rendered/<dot>
#
# Applies each rendered stream JSON to the NATS server that owns its cluster.
# Idempotent (info/update-or-add). Rewrites num_replicas to 1 before applying:
# partition (regional) streams are always rendered at num_replicas=3 (fixed
# regardless of the topology spec's replicas field), which fails to form a
# quorum on a single-node compose NATS server (no peers). Central/dmz streams
# already render at num_replicas=1 for this slice, so the rewrite is a no-op
# for them, but it is applied uniformly for safety.
#
# Also caps max_bytes at 1 GiB: the rendered topology bakes in
# production-sized limits (regional=50GiB, central=20GiB, dmz=10GiB), which
# exceed the JetStream storage ceiling NATS auto-detects from a single
# compose host's free disk (observed ~35.5GB in Docker Desktop), so
# `stream add` fails with "insufficient storage resources available" before
# a single byte is ever written. 1 GiB is ample for a one-cabinet demo slice.
DOT="$1"
trap 'rm -f "$TMP"' EXIT
apply() { # $1 server url, $2 stream json path
  name=$(jq -r .name "$2")
  TMP=$(mktemp)
  jq '.num_replicas = 1 | .max_bytes = 1073741824' "$2" > "$TMP"
  if nats --server "$1" stream info "$name" >/dev/null 2>&1; then
    nats --server "$1" stream update "$name" --config "$TMP" --force
  else
    nats --server "$1" stream add "$name" --config "$TMP"
  fi
  rm -f "$TMP"
}
for f in /rendered/"$DOT"/clusters/d1a/streams/*.json;  do [ -e "$f" ] || continue; apply "nats://${DOT}-regional:4222" "$f"; done
for f in /rendered/"$DOT"/clusters/core/streams/*.json; do [ -e "$f" ] || continue; apply "nats://${DOT}-central:4222"  "$f"; done
for f in /rendered/"$DOT"/clusters/dmz/streams/*.json;  do [ -e "$f" ] || continue; apply "nats://${DOT}-dmz:4222"      "$f"; done
echo "streams initialized for $DOT"
