#!/bin/sh
# Concatenates the YANG modules the AI segment's models-only prompt depends
# on (signal-control, traffic-sensor, perception, dms, common/types) from
# the PINNED sibling models repo into one attachable file:
# demo/ai/models-pack.generated.yang (gitignored -- regenerate with
# `make ai-models-pack`).
#
# MODELS_DIR defaults to ../../openits-models-pinned, NOT ../../openits-models:
# the demo is pinned to openits-models@75f1fdb (see docs/MODELS-PIN.md) --
# the live openits-models checkout is on a churning branch with a breaking
# change (signal-control cut-3a) this repo hasn't absorbed yet, and the AI's
# models pack must describe the SAME schema the running demo's ClickHouse
# tables were generated from, or its "correlate YANG leaves to columns"
# method (see demo/ai/prompts/system-models-only.md) would be working from
# a doc that doesn't match the data.
set -eu
MODELS_DIR="${MODELS_DIR:-../../openits-models-pinned}"
out=demo/ai/models-pack.generated.yang
mkdir -p "$(dirname "$out")"
: > "$out"

# Every pattern below is anchored on "vikasa-<domain>*", so vendor-specific
# modules (vikasa-vendor-econolite-signal-control-types.yang,
# vikasa-vendor-trafficvision-*.yang) are naturally excluded -- they don't
# match any of these prefixes and the AI never needs vendor-specific leaves
# to build the dashboard.
for pattern in 'vikasa-types*' 'vikasa-common*' 'vikasa-signal-control*' \
               'vikasa-traffic-sensor*' 'vikasa-perception*' 'vikasa-dms*'; do
  for f in "$MODELS_DIR"/yang/$pattern.yang; do
    [ -f "$f" ] || continue
    printf '\n// ===== %s =====\n' "$(basename "$f")" >> "$out"
    cat "$f" >> "$out"
  done
done

size=$(wc -c < "$out" | tr -d ' ')
echo "wrote $out ($size bytes)"

# ~300KB is comfortably inside every current frontier model's context window
# alongside the MCP tool-call traffic this segment also generates, but a
# growing models repo could push past that. If it does, trim to the
# *-events + *-types modules (the notifications and their leaf types --
# what the dashboard actually needs) and drop the base config/state modules
# (vikasa-signal-control.yang, vikasa-perception.yang, etc. -- containers
# for the device's own config/operational state, not events) and any
# vendor-specific ones, by hand-editing the pattern list above.
if [ "$size" -gt 307200 ]; then
  echo "WARNING: $out is ${size} bytes (> 300KB). Trim the pattern list in" >&2
  echo "tools/ai/gen-models-pack.sh to *-events + *-types modules only." >&2
fi
