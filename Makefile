# Topology renderer. The NATS/JetStream stream configs are rendered by
# github.com/Vikasa2M/vikasa-infra's cmd/gen, pinned to a released version and
# fetched by `go run` on first use (then cached in the Go module cache) — see
# docs/MODELS-PIN.md. Override INFRA_GEN to point at a local checkout's
# ./cmd/gen (e.g. INFRA_GEN=../vikasa-infra/cmd/gen) when iterating on the
# renderer or building air-gapped.
INFRA_VERSION ?= v0.1.0
INFRA_GEN     ?= github.com/Vikasa2M/vikasa-infra/cmd/gen@$(INFRA_VERSION)
# Pinned to openits-models@75f1fdb pending signal-control cut-3a stabilization;
# see docs/MODELS-PIN.md.
MODELS_DIR ?= ../../openits-models-pinned
COMPOSE     = docker compose -f deploy/compose/docker-compose.yml
# COMPOSE_ALL merges the static shared-infra file with gen-compose's
# generated per-DOT/per-cabinet services; every DOT/cabinet-specific compose
# operation (bring-up, stream-init, sink) must use this, not COMPOSE alone.
COMPOSE_ALL = docker compose -f deploy/compose/docker-compose.yml -f deploy/compose/docker-compose.generated.yml
IMAGE       = vikasa-demo:dev

.PHONY: build test topology gen-compose docker-build demo-slice demo demo-small demo-medium demo-down verify demo-tour demo-tour-paced ai-models-pack ai-wire-claude ai-wire-codex

build:
	go build ./...

test:
	go test ./...

# fleet.yaml -> deploy/compose/{docker-compose.generated.yml,nats/*.conf,
# sims/*.yaml}, deploy/topology/inventories/{dot}-cabinets.json,
# deploy/clickhouse/seed.generated.sql. All gitignored; regenerate whenever
# fleet.yaml changes. SCALE=1 restricts every DOT to its corridor cabinet.
gen-compose:
	go run ./tools/gen-compose -fleet deploy/topology/fleet.yaml -out deploy/compose

# topology depends on gen-compose since it now consumes gen-compose's
# generated inventories (deploy/topology/inventories/{dot}-cabinets.json)
# rather than a hand-committed file. All 3 DOT specs exist as of Task 14, so
# this renders mardot/veldot/sabdot unconditionally.
topology: gen-compose
	GOFLAGS= go run $(INFRA_GEN) \
	  -spec deploy/topology/specs/mardot.json \
	  -cabinets deploy/topology/inventories/mardot-cabinets.json \
	  -out deploy/topology/rendered/mardot
	GOFLAGS= go run $(INFRA_GEN) \
	  -spec deploy/topology/specs/veldot.json \
	  -cabinets deploy/topology/inventories/veldot-cabinets.json \
	  -out deploy/topology/rendered/veldot
	GOFLAGS= go run $(INFRA_GEN) \
	  -spec deploy/topology/specs/sabdot.json \
	  -cabinets deploy/topology/inventories/sabdot-cabinets.json \
	  -out deploy/topology/rendered/sabdot

# Self-contained: the build context is this repo root. The image builds in
# vendor mode (`-mod=vendor`) against the committed vendor/ tree, which
# already contains the pinned openits-models packages, so no sibling checkout
# is needed in the build context. See Dockerfile and docs/MODELS-PIN.md.
docker-build:
	docker build -f Dockerfile -t $(IMAGE) .

# Brings up infra + the four-tier NATS chain, renders topology, applies the
# rendered stream configs (stream-init-mardot), applies ClickHouse migrations +
# dimension seed (migrate), then starts the one cabinet sim. Order matters:
# streams must exist before the sim starts publishing so the cabinet's
# VIKASA_BUFFER stream (created by the sim itself) and the regional/central/
# dmz sourcing chain are both ready to move data end to end; migrate runs
# after clickhouse is healthy and before any sink (none exist yet) so the
# schema/dims are in place before data starts flowing.
# mardot-sink comes up last, alongside the sim: it depends on both the stream
# chain (stream-init-mardot) and the schema (migrate) already existing, so it
# must start after both `run --rm` steps complete, not merely be declared
# with a compose depends_on (depends_on only orders container start, not
# these one-shot `run --rm` steps).
# Uses COMPOSE_ALL: mardot-regional/central/dmz/mardot-{regional,central,dmz}-
# exporter/mardot-cab-i85-001-nats/stream-init-mardot/cab-i85-001-sim/mardot-sink
# all now live in the gen-compose generated file, not the static one
# (topology's gen-compose prerequisite already regenerates it, so this
# target doesn't need its own gen-compose step).
demo-slice: docker-build topology
	$(COMPOSE_ALL) up -d clickhouse grafana prometheus mardot-regional mardot-central mardot-dmz \
		mardot-regional-exporter mardot-central-exporter mardot-dmz-exporter mardot-cab-i85-001-nats
	$(COMPOSE_ALL) run --rm stream-init-mardot
	$(COMPOSE_ALL) run --rm migrate
	$(COMPOSE_ALL) up -d cab-i85-001-sim mardot-sink

demo-down:
	$(COMPOSE_ALL) down -v

# Full ~100-cabinet, 3-DOT bring-up + cross-DOT federation via the DMZ.
# Delegated to scripts/demo-up.sh, which paces the bring-up in small batches:
# a single `up -d --wait` over the whole fleet (~210 cabinet containers)
# overwhelms the Docker daemon's create path and disconnects it mid-bring-up
# (observed live at 99 cabinets — RAM is fine at ~6 GB, the spike is the
# simultaneous container-create/network-attach storm). The script brings up
# infra + NATS tiers first, then cabinet leaves and sims in BATCH-sized
# groups (each `--wait`ed before the next), then stream-init and the sinks —
# same ordering guarantees as before (streams exist before any sink binds;
# sims race their own VIKASA_BUFFER stream on their leaf), just reliable at
# scale. Set BATCH to tune the group size (default 20).
demo: docker-build gen-compose topology
	sh scripts/demo-up.sh

# Smaller footprints for lighter machines. SIZE flows through gen-compose and
# LoadFleetInfo (see internal/fleetsize): small = 3 corridor heroes (~34
# containers, federation + resilience phases only); medium = 18 cabinets (~64
# containers, all six tour phases). Plain `make demo` is large (99 cabinets).
# Tour the same size you brought up, e.g. `SIZE=medium make demo-tour`.
demo-small:
	$(MAKE) demo SIZE=small

demo-medium:
	$(MAKE) demo SIZE=medium

# Unattended rehearsal / take-QA: runs all 6 tour phases against the live
# stack started by `make demo`, asserting each phase's expected outcome and
# exiting nonzero if any phase fails. Run this before recording a take —
# see docs/RUNBOOK.md's recording checklist.
demo-tour:
	go run ./cmd/democtl tour

# Presenter/recording mode: narrates each phase, names the dashboard to
# show, and waits for Enter before/after triggering it. Meant to be run
# live while narrating over a silent screen recording — see
# docs/RUNBOOK.md.
demo-tour-paced:
	go run ./cmd/democtl tour --paced

# Task 19 AI segment: concatenates the signal-control/traffic-sensor/
# perception/dms/common/types YANG modules from the PINNED models checkout
# (same MODELS_DIR the build/docker-build targets use -- see
# docs/MODELS-PIN.md) into demo/ai/models-pack.generated.yang (gitignored).
# Attach that file to the AI's chat alongside
# demo/ai/prompts/system-models-only.md -- see demo/ai/mcp/README.md for
# the rest of the segment.
ai-models-pack:
	MODELS_DIR=$(MODELS_DIR) ./tools/ai/gen-models-pack.sh

# Wire the AI segment's two MCP servers (Grafana + ClickHouse) into a local MCP
# client in one step: runs democtl ai-setup for a fresh token, then registers
# both servers with the client. Stack must be up. See tools/ai/wire-mcp.sh and
# demo/ai/mcp/README.md.
ai-wire-claude:
	./tools/ai/wire-mcp.sh claude

ai-wire-codex:
	./tools/ai/wire-mcp.sh codex
