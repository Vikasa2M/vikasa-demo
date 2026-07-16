INFRA_DIR  ?= ../../vikasa-infra
# Pinned to openits-models@75f1fdb pending signal-control cut-3a stabilization;
# see docs/MODELS-PIN.md.
MODELS_DIR ?= ../../openits-models-pinned
COMPOSE     = docker compose -f deploy/compose/docker-compose.yml
# COMPOSE_ALL merges the static shared-infra file with gen-compose's
# generated per-DOT/per-cabinet services; every DOT/cabinet-specific compose
# operation (bring-up, stream-init, sink) must use this, not COMPOSE alone.
COMPOSE_ALL = docker compose -f deploy/compose/docker-compose.yml -f deploy/compose/docker-compose.generated.yml
IMAGE       = vikasa-demo:dev

.PHONY: build test topology gen-compose docker-build demo-slice demo demo-down verify demo-tour demo-tour-paced ai-models-pack

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
# this renders gdot/ncdot/scdot unconditionally.
topology: gen-compose
	cd $(INFRA_DIR) && go run ./cmd/gen \
	  -spec $(CURDIR)/deploy/topology/specs/gdot.json \
	  -cabinets $(CURDIR)/deploy/topology/inventories/gdot-cabinets.json \
	  -out $(CURDIR)/deploy/topology/rendered/gdot
	cd $(INFRA_DIR) && go run ./cmd/gen \
	  -spec $(CURDIR)/deploy/topology/specs/ncdot.json \
	  -cabinets $(CURDIR)/deploy/topology/inventories/ncdot-cabinets.json \
	  -out $(CURDIR)/deploy/topology/rendered/ncdot
	cd $(INFRA_DIR) && go run ./cmd/gen \
	  -spec $(CURDIR)/deploy/topology/specs/scdot.json \
	  -cabinets $(CURDIR)/deploy/topology/inventories/scdot-cabinets.json \
	  -out $(CURDIR)/deploy/topology/rendered/scdot

# Build context is the parent dir of both repos (not this repo root): go.mod's
# `replace github.com/openits/openits-models => ../../openits-models-pinned`
# needs the sibling checkout present in the build context to resolve. See
# Dockerfile and docs/MODELS-PIN.md.
docker-build:
	cd ../.. && docker build -f vikasa/vikasa-demo/Dockerfile -t $(IMAGE) .

# Brings up infra + the four-tier NATS chain, renders topology, applies the
# rendered stream configs (stream-init-gdot), applies ClickHouse migrations +
# dimension seed (migrate), then starts the one cabinet sim. Order matters:
# streams must exist before the sim starts publishing so the cabinet's
# VIKASA_BUFFER stream (created by the sim itself) and the regional/central/
# dmz sourcing chain are both ready to move data end to end; migrate runs
# after clickhouse is healthy and before any sink (none exist yet) so the
# schema/dims are in place before data starts flowing.
# gdot-sink comes up last, alongside the sim: it depends on both the stream
# chain (stream-init-gdot) and the schema (migrate) already existing, so it
# must start after both `run --rm` steps complete, not merely be declared
# with a compose depends_on (depends_on only orders container start, not
# these one-shot `run --rm` steps).
# Uses COMPOSE_ALL: gdot-regional/central/dmz/gdot-{regional,central,dmz}-
# exporter/gdot-cab-i85-001-nats/stream-init-gdot/cab-i85-001-sim/gdot-sink
# all now live in the gen-compose generated file, not the static one
# (topology's gen-compose prerequisite already regenerates it, so this
# target doesn't need its own gen-compose step).
demo-slice: docker-build topology
	$(COMPOSE_ALL) up -d clickhouse grafana prometheus gdot-regional gdot-central gdot-dmz \
		gdot-regional-exporter gdot-central-exporter gdot-dmz-exporter gdot-cab-i85-001-nats
	$(COMPOSE_ALL) run --rm stream-init-gdot
	$(COMPOSE_ALL) run --rm migrate
	$(COMPOSE_ALL) up -d cab-i85-001-sim gdot-sink

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

# Unattended rehearsal / take-QA: runs all 5 tour phases against the live
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
