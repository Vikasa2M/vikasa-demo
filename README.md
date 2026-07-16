# Vikasa Demo

A faithful, laptop-sized miniature of the Vikasa production architecture:
three independent state DOTs (GA/NC/SC), each running the full
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
per DOT (x3: gdot, ncdot, scdot):

  cabinet (x3):   cabinet-sim --publish--> leaf nats-server
                  (ASC+2cam+DMS+lidar)     JS domain = <cab-id>
                                           stream VIKASA_BUFFER
                        |  leafnode (outbound) + cross-domain source
  regional (x1):  VIKASA_<DOT>_D1_D1_0    (sources the 3 cabinet buffers,
                                            filterSubject per cabinet)
                        |  cross-domain source
  central (x1):   VIKASA_<DOT>_CENTRAL_D1_D1_0
                        |                        `--> central-sink --> ClickHouse vikasa_<dot>
                        |  cross-domain source + subjectTransform
  dmz (x1):       VIKASA_<DOT>_DMZ        (corridor cabinet remapped to
                                            vikasa.<dot>.share.i85.>)

shared:
  federation-sink  (JS consumers on all 3 DMZ streams) --> ClickHouse vikasa_federation
  ClickHouse (one instance, 4 databases), Grafana, Prometheus, NATS exporters
```

~35 containers total: 18 NATS servers (9 cabinet leafs + 3 regional + 3
central + 3 DMZ), 9 cabinet-sims, 3 central-sinks, 1 federation-sink,
ClickHouse, Grafana, Prometheus, plus exporter sidecars. Single-node (R1)
NATS everywhere instead of R3 clusters — topology fidelity without
laptop-melting replica counts. NATS configs are generated from
`deploy/topology/fleet.yaml` (the fleet SSOT) via `vikasa-infra`'s
topology renderer, not hand-written.

The cross-DOT story flows through the DMZ: one cabinet per DOT sits on a
shared I-85 corridor; its events are subject-transformed into
`vikasa.<dot>.share.i85.>` and consumed by the federation sink — the DMZ
doing its actual job, not a bypassed parallel consumer set.

## Quickstart

Prerequisites, ports, and full troubleshooting live in
[`docs/RUNBOOK.md`](docs/RUNBOOK.md). Short version:

```sh
make demo            # build image, generate topology, bring up all 3 DOTs (~3-5 min)
make demo-tour       # unattended rehearsal: runs all 5 tour phases, asserts each, prints PASS/FAIL
make demo-tour-paced # presenter/recording mode: narrates each phase, waits for Enter
make demo-down       # tear down every container/network/volume
```

`democtl tour --verify` (what `make demo-tour` runs) is the take-QA gate —
run it clean before recording. `democtl tour --paced` (what `make
demo-tour-paced` runs) is the narrated mode for live presentation or
screen-capture.

## Dashboards (Grafana, Vikasa folder)

| Dashboard | uid |
|---|---|
| Demo Tour | `vikasa-demo-tour` |
| Fleet Health | `vikasa-fleet-health` |
| Resilience Lab | `vikasa-resilience-lab` |
| Signal Performance (ATSPM) | `vikasa-signal-performance` |
| Corridor I-85 | `vikasa-corridor-i85` |
| Perception & Fusion | `vikasa-perception-fusion` |
| DMS Status | `vikasa-dms-status` |
| Infra | `vikasa-infra` |

## Repo map

```
cmd/
  cabinet-sim/      one binary per simulated roadside cabinet (ASC + 2 cameras + DMS + lidar)
  central-sink/     durable JetStream consumer -> ClickHouse, per DOT
  federation-sink/  durable JetStream consumer on all 3 DMZ streams -> vikasa_federation
  democtl/          demo control CLI: tour, cut, restore, verify
internal/
  sim/              cabinet-sim's traffic model, device emitters, scenario injection
  events/           CloudEvents envelope + subject helpers shared by sim/sink
  sink/             shared sink library: table-driven ce-type -> ClickHouse handler registry
  tour/             the 5-phase demo tour (baseline, wan-cut, restore, fault, corridor)
  verify/           ClickHouse/compose assertions used by democtl verify and the tour
tools/
  gen-compose/      renders fleet.yaml -> compose overlay, NATS confs, sim configs, CH seed
deploy/
  topology/         fleet.yaml (SSOT), per-DOT specs, generated inventories/rendered (gitignored)
  compose/          static docker-compose.yml + generated overlay (gitignored)
  clickhouse/       schema migrations + generated dimension seed (gitignored)
  grafana/          dashboards (committed JSON) + provisioning
  prometheus/       scrape config + generated targets (gitignored)
docs/
  RUNBOOK.md         operating the live stack
  MODELS-PIN.md      why/how the models dependency is pinned
scripts/
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
40+ containers healthy inside a GitHub-hosted runner is its own project;
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

On a dev machine with the sibling checkout present, `go build ./...` and
`go test ./...` work exactly as before, unvendored — vendoring is purely
a CI convenience, not the default local mode.

## Models pin note

This demo is pinned to `openits-models@75f1fdb`, one commit before a
still-churning breaking change on the models side ("signal-control
cut-3a") that would otherwise break event synthesis/classification here.
See [`docs/MODELS-PIN.md`](docs/MODELS-PIN.md) for the full rationale, the
insulated-clone mechanism, and the deferred re-sync follow-up.

## Commit signing / secrets

Commits in this repo are made with signing disabled for this branch of
work (no 1Password/GPG signing configured in this environment). The
`../../openits-models-pinned` and `../../vikasa-infra` sibling checkouts
referenced above are local-machine paths, not part of this repo's git
history — a fresh clone of `vikasa-demo` needs both siblings checked out
next to it (see Prerequisites in [`docs/RUNBOOK.md`](docs/RUNBOOK.md))
before `make demo` will run; CI does not need them (see above).
