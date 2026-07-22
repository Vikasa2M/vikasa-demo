# Vikasa Demo

[![CI](https://github.com/Vikasa2M/vikasa-demo/actions/workflows/ci.yaml/badge.svg)](https://github.com/Vikasa2M/vikasa-demo/actions/workflows/ci.yaml)
[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

A faithful, laptop-sized miniature of the Vikasa reference architecture:
three independent state DOTs (Marren/Velia/Sabine), each running the full
cabinet → regional → central → DMZ NATS JetStream chain with real
leafnode connections, per-cabinet JetStream domains, cross-domain stream
sourcing, and DMZ subject transforms — federated into one shared
ClickHouse + Grafana view. Every hop is the real mechanism; nothing is
faked at the transport layer. Fake data is injected once, at the
CloudEvents level, by `cabinet-sim`.

Operating the live stack (bring-up, recording a take, teardown, troubleshooting):
[`docs/RUNBOOK.md`](docs/RUNBOOK.md).

## Architecture

```
per DOT (x3: mardot, veldot, sabdot):

  cabinet (x33):  cabinet-sim --publish--> leaf nats-server
                  (ASC+2cam+DMS+lidar)     JS domain = <cab-id>
                                           stream VIKASA_BUFFER
                        |  leafnode (outbound) + cross-domain source
  regional (x1):  VIKASA_<DOT>_D1_D1_0    (sources every cabinet buffer in
                                            the DOT, filterSubject per cabinet)
                        |  cross-domain source
  central (x1):   VIKASA_<DOT>_CENTRAL_D1_D1_0
                        |                        `--> central-sink --> ClickHouse vikasa_<dot>
                        |  cross-domain source + subjectTransform
  dmz (x1):       VIKASA_<DOT>_DMZ        (corridor cabinets remapped to
                                            vikasa.<dot>.share.i85.>)

shared:
  federation-sink  (JS consumers on all 3 DMZ streams) --> ClickHouse vikasa_federation
  ClickHouse (one instance, 4 databases), Grafana, Prometheus, NATS exporters
```

~226 containers total: 99 cabinet-sims + 99 cabinet leaf NATS servers
(33 cabinets per DOT), 9 NATS tier servers (3 regional + 3 central + 3
DMZ), 3 central-sinks, 1 federation-sink, ClickHouse, Grafana, Prometheus,
plus exporter sidecars (9 tier exporters + one per hero cabinet).
Single-node (R1) NATS everywhere instead of R3 clusters — topology fidelity
without laptop-melting replica counts. NATS configs are generated from
`deploy/topology/fleet.yaml` (the fleet SSOT) via `vikasa-infra`'s topology
renderer, not hand-written.

The cross-DOT story flows through the DMZ: each DOT's I-85 corridor
cabinets sit on a shared corridor; their events are subject-transformed
into `vikasa.<dot>.share.i85.>` and consumed by the federation sink — the
DMZ doing its actual job, not a bypassed parallel consumer set.

## Quickstart

**New here?** [`docs/GETTING-STARTED.md`](docs/GETTING-STARTED.md) is the
from-scratch on-ramp — installing Docker and Go, bringing the stack up, and
running the AI segment, with each step explained.

Prerequisites, ports, and full troubleshooting live in
[`docs/RUNBOOK.md`](docs/RUNBOOK.md). A fresh clone needs only Docker (with
~12 GB of RAM allocated) and Go — no sibling checkouts. The pinned models
packages are committed under `vendor/`; the topology renderer is fetched as a
pinned release (`github.com/Vikasa2M/vikasa-infra@v0.1.0`) by `make topology` on
first run, then cached (see [`docs/MODELS-PIN.md`](docs/MODELS-PIN.md)). Short
version:

```sh
make demo            # large: build image, generate topology, bring up all 3 DOTs (~6-8 min)
make demo-small      # small: 3 cabinets (~34 containers) — federation + resilience phases
make demo-medium     # medium: 18 cabinets (~64 containers) — all 6 tour phases
make demo-tour       # unattended rehearsal: runs all 6 tour phases, asserts each, prints PASS/FAIL
make demo-tour-paced # presenter/recording mode: narrates each phase, waits for Enter
make demo-down       # tear down every container/network/volume
```

Pick a size by machine capacity; small/medium are lighter and faster to bring
up. Tour the same size you brought up (e.g. `SIZE=medium make demo-tour`).
See the RUNBOOK's Deployment sizes section.

`democtl tour --verify` (what `make demo-tour` runs) is the take-QA gate —
run it clean before recording. `democtl tour --paced` (what `make
demo-tour-paced` runs) is the narrated mode for live presentation or
screen-capture. The six phases are baseline, wan-cut, restore, fault,
corridor, and reversible; an optional AI segment (`--ai`) adds two more.

## Platform support

Actively developed and **validated on macOS** (Apple Silicon and Intel). The
stack is plain Docker containers driven by `make`, `bash`, and Go, so **Linux**
and **Windows (via WSL2)** use the identical toolchain and *should* work — but
neither has been **validated yet**. If you run it on either, issue/PR reports
are welcome. Windows setup lives in
[`docs/GETTING-STARTED.md`](docs/GETTING-STARTED.md#running-on-windows-wsl2).

## Dashboards (Grafana, Vikasa folder)

The two **Executive** dashboards are the pitch/lead views (narrative
headers, hero stats); the rest are operational. Full URLs are in the
[RUNBOOK](docs/RUNBOOK.md#dashboards-grafana-vikasa-folder).

| Dashboard | uid |
|---|---|
| Executive · Corridor Federation | `vikasa-exec-federation` |
| Executive · Multi-Vendor / Open Standards | `vikasa-exec-multi-vendor` |
| Demo Tour | `vikasa-demo-tour` |
| Fleet Health | `vikasa-fleet-health` |
| Resilience Lab | `vikasa-resilience-lab` |
| Signal Performance (ATSPM) | `vikasa-signal-performance` |
| Corridor I-85 | `vikasa-corridor-i85` |
| Perception & Fusion | `vikasa-perception-fusion` |
| I-75 South Reversible Express Lanes | `vikasa-reversible-lanes` |
| DMS Status | `vikasa-dms-status` |
| Infra | `vikasa-infra` |

## Repo map

```
cmd/
  cabinet-sim/      one binary per simulated roadside cabinet (ASC + 2 cameras + DMS + lidar)
  central-sink/     durable JetStream consumer -> ClickHouse, per DOT
  federation-sink/  durable JetStream consumer on all 3 DMZ streams -> vikasa_federation
  democtl/          demo control CLI: tour, cut, restore, verify, ai-setup, ai-reset
internal/
  sim/              cabinet-sim's traffic model, device emitters, scenario injection
  events/           CloudEvents envelope + subject helpers shared by sim/sink
  sink/             shared sink library: table-driven ce-type -> ClickHouse handler registry
  tour/             the 6-phase demo tour (baseline, wan-cut, restore, fault, corridor, reversible) + AI phases
  ai/               AI-segment setup: scoped Grafana service account, ai_readonly ClickHouse user
  verify/           ClickHouse/compose assertions used by democtl verify and the tour
tools/
  gen-compose/      renders fleet.yaml -> compose overlay, NATS confs, sim configs, CH seed
  ai/               gen-models-pack.sh: concatenates the pinned YANG modules into the models pack
deploy/
  topology/         fleet.yaml (SSOT), per-DOT specs, generated inventories/rendered (gitignored)
  compose/          static docker-compose.yml + generated overlay (gitignored)
  clickhouse/       schema migrations + generated dimension seed (gitignored)
  grafana/          dashboards (committed JSON) + provisioning
  prometheus/       scrape config + generated targets (gitignored)
demo/
  ai/               "models-only" AI segment: MCP wiring (mcp/README.md), prompts, generated models pack
docs/
  GETTING-STARTED.md from-scratch on-ramp (install, bring-up, AI segment), each step explained
  RUNBOOK.md         operating the live stack (bring-up, recording, teardown, troubleshooting)
  MODELS-PIN.md      why/how the models dependency is pinned
scripts/
  demo-up.sh          batched full-fleet bring-up (called by make demo)
  lint-dashboards.sh  Grafana dashboard static checks (run by CI)
.github/workflows/
  ci.yaml             tests + static checks (no compose stack — see below)
```

## CI

`.github/workflows/ci.yaml` runs on every push/PR: `go vet` + `go test`,
a `gen-compose` drift check (fleet.yaml still renders cleanly), the
dashboard lint (`scripts/lint-dashboards.sh`), and a check that no
generated artifact ever gets committed. It never brings up the
docker-compose stack — that's a deliberate scope cut, not an oversight:
the live/integration story (does the whole cross-DOT pipeline actually
move data, cut/restore without loss, etc.) is verified manually via
`make demo` + `democtl tour --verify` per the Runbook, not in CI. Getting
~226 containers healthy inside a GitHub-hosted runner is its own project;
this repo's CI stays fast and dependency-free instead.

### The models dependency in CI

`go.mod` has a **local replace**,
`github.com/openits/openits-models => ../../openits-models-pinned`,
pointing at a sibling checkout that exists on dev machines but not on a
stock CI runner (see [`docs/MODELS-PIN.md`](docs/MODELS-PIN.md) for why
the pin exists). Rather than clone the models repo at the pinned commit
on every run, or scope Go CI to a self-hosted runner with the sibling
present, this repo **commits `vendor/`** (`go mod vendor`, which also
pulls in the replaced module's needed packages) and CI builds/tests with
`GOFLAGS=-mod=vendor`. That makes CI fully self-contained: no sibling
checkout, no extra clone step, no module-proxy fetch. Regenerate and
recommit `vendor/` (`go mod vendor`) whenever `go.mod`/`go.sum` changes or
the models pin moves.

Committing `vendor/` also makes a fresh clone self-contained beyond CI: the
Docker image build (`make docker-build`) runs in vendor mode too, so no
sibling checkout has to be present to build it (see
[`docs/MODELS-PIN.md`](docs/MODELS-PIN.md)). A dev machine that *does* have
the insulated checkout next door can still `go build ./...` / `go test ./...`
unvendored against it.

## Models pin note

This demo pins `openits-models@75f1fdb`, one commit before a signal-control
change ("cut-3a") that reworked the event model; this demo's event
synthesis/classification is written against the pre-cut-3a model. See
[`docs/MODELS-PIN.md`](docs/MODELS-PIN.md) for the rationale and the
insulated-clone mechanism.

## Self-contained clone

A fresh clone needs no sibling checkout to run `make demo`: the pinned
openits-models packages are committed under `vendor/`, and the topology renderer
is fetched as a pinned release (`github.com/Vikasa2M/vikasa-infra@v0.1.0`) by
`make topology` on first run — so the base demo needs network access only that
once (it's then cached), and no sibling checkouts (see
[`docs/MODELS-PIN.md`](docs/MODELS-PIN.md)). The other exception is the optional
AI segment: regenerating the YANG models pack (`make ai-models-pack`) still reads
`../../openits-models-pinned` via `MODELS_DIR`.
