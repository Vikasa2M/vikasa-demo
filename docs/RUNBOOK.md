# Vikasa Demo Runbook

This runbook covers bringing up the full 3-DOT Vikasa demo stack, recording
a silent screen-capture narrated live by a presenter, and tearing the stack
back down. It targets `democtl tour` (`internal/tour`, `cmd/democtl`) — the
scripted **6-phase** tour: baseline, wan-cut, restore, fault, corridor, and
reversible (the I-75 South reversible express lanes).

The fleet is a ~100-cabinet distributed network (33 per DOT). GDOT is shaped
into two real Atlanta corridors: a string of I-85 perception stations
(`cab-i85-001`…`010`) and the I-75 South Metro Express Lanes reversible
segment (`cab-i75s-01`…`05`); NCDOT/SCDOT are a metro spread with one I-85
hero each.

The AI "the model is all you need" segment adds further phases gated behind
`democtl tour --ai`, covered in its own section below (the base tour is
unaffected — it never runs the AI phases unless `--ai` is passed).

## Prerequisites

- Docker Desktop (or another Docker Engine) ≥ 24, with **at least ~12 GB RAM**
  allocated (steady state is ~6 GB, but leave headroom). The full stack is
  ~226 containers: 99 cabinet sims + 99 cabinet leaf NATS (3 hero cabinets
  also get an exporter sidecar), 3×3 regional/central/dmz NATS tier servers +
  their 9 exporters, 3 per-DOT sinks, federation-sink, ClickHouse, Grafana,
  Prometheus. `make demo` brings these up in **batches** (`scripts/demo-up.sh`)
  — a single `up -d --wait` over that many containers disconnects the Docker
  Desktop daemon; the batched bring-up is reliable.
- Go (see `go.mod` for the pinned version).
- Two sibling checkouts next to this repo (i.e. `../../<repo>` relative to
  `vikasa-demo/`):
  - `../../openits-models-pinned` — a pinned clone of `openits-models` (see
    `docs/MODELS-PIN.md`). `go.mod`'s `replace` directive points here.
  - `../../vikasa-infra` — used by `make topology` to render NATS/stream
    configs from `deploy/topology/specs/*.json`.
- Ports `3000`, `8123`, `9000`, `9090`, and `18081`–`18084` free on
  localhost.

## Bring-up

```sh
make demo
```

One command, roughly **6–8 minutes** (batched bring-up of ~226 containers).
It builds the demo image, generates
the compose overlay + topology + ClickHouse seed from
`deploy/topology/fleet.yaml` (`make gen-compose` / `make topology`), brings
up shared infra, applies the ClickHouse schema and dimension seed
(`make migrate`), starts every NATS tier server + cabinet leaf + sim, applies
each DOT's stream configs, and finally starts the 3 per-DOT sinks and
federation-sink once their streams exist. See the Makefile's `demo` target
for the exact ordering rationale — it matters (streams must exist before
anything binds to them).

Confirm it came up clean:

```sh
docker compose -f deploy/compose/docker-compose.yml -f deploy/compose/docker-compose.generated.yml ps
go run ./cmd/democtl verify baseline --dot gdot
go run ./cmd/democtl verify baseline --dot ncdot
go run ./cmd/democtl verify baseline --dot scdot
```

Every container should be `Up`/`healthy`, and all three `verify baseline`
runs should print `[PASS]` for `recent-events`, `heartbeat-freshness`, and
`dead-letters`.

### SCALE=1 mode

For a lighter/faster bring-up (e.g. iterating on dashboards, not recording),
set `SCALE=1` before generating the compose overlay:

```sh
SCALE=1 make gen-compose
make demo
```

This restricts every DOT to just its I-85 hero cabinet (3 cabinets total
instead of 99) — the same corridor cabinets the tour's wan-cut, restore, and
corridor-incident phases already target, so those phases still run. The
signal-fault phase (`cab-002`), the reversible phase (`cab-i75s-01`), and the
GDOT I-85 perception corridor (`cab-i85-002`…`010`) target non-hero cabinets
that don't exist in SCALE=1 — use `--only` to skip them, or don't record with
SCALE=1. Regenerate with plain `make gen-compose` (no `SCALE`) to go back to
the full 99-cabinet fleet.

If you run `democtl tour` under SCALE=1, keep `SCALE=1` exported for it too: `LoadFleetInfo` reads the same env var so its derived cabinet->port table matches the SCALE=1 generated compose.

## URLs

| Service | URL | Notes |
|---|---|---|
| Grafana | http://localhost:3000 | Anonymous access enabled with Admin role — no login prompt |
| ClickHouse (HTTP) | http://localhost:8123 | Used by `democtl verify`/`tour` |
| ClickHouse (native) | localhost:9000 | Used by Grafana's ClickHouse datasource |
| Prometheus | http://localhost:9090 | NATS JetStream/exporter metrics |

### Dashboards (Grafana, Vikasa folder)

| Dashboard | uid | URL |
|---|---|---|
| Executive · Corridor Federation | `vikasa-exec-federation` | http://localhost:3000/d/vikasa-exec-federation |
| Executive · Multi-Vendor / Open Standards | `vikasa-exec-multi-vendor` | http://localhost:3000/d/vikasa-exec-multi-vendor |
| Demo Tour | `vikasa-demo-tour` | http://localhost:3000/d/vikasa-demo-tour |
| Fleet Health | `vikasa-fleet-health` | http://localhost:3000/d/vikasa-fleet-health |
| Resilience Lab | `vikasa-resilience-lab` | http://localhost:3000/d/vikasa-resilience-lab |
| Signal Performance (ATSPM) | `vikasa-signal-performance` | http://localhost:3000/d/vikasa-signal-performance |
| Corridor I-85 | `vikasa-corridor-i85` | http://localhost:3000/d/vikasa-corridor-i85 |
| Perception & Fusion (GDOT I-85 corridor) | `vikasa-perception-fusion` | http://localhost:3000/d/vikasa-perception-fusion |
| I-75 South Reversible Express Lanes | `vikasa-reversible-lanes` | http://localhost:3000/d/vikasa-reversible-lanes |
| DMS Status | `vikasa-dms-status` | http://localhost:3000/d/vikasa-dms-status |
| Infra | `vikasa-infra` | http://localhost:3000/d/vikasa-infra |

The two **Executive** dashboards are the pitch/lead views (narrative headers,
hero stats); the rest are operational.

Dashboards with a `$dot` (and, for two of them, `$cabinet`) template
variable accept `?var-dot=gdot` / `&var-cabinet=cab-002` query params to
pre-select a scope when linking directly.

### Sim inject/health ports

Only a handful of cabinets are host-mapped — every hero (I-85 corridor)
cabinet plus any non-hero cabinet flagged `expose: true` in `fleet.yaml`. The
other ~95 cabinets are internal (vikasa-network) only, which is what keeps
~100 cabinets from binding ~100 host ports. Ports are assigned as `18081 + a
running index over the port-mapped cabinets in fleet.yaml order` (see
`tools/gen-compose/main.go`'s `writeCompose`/`portMapped`;
`internal/tour.LoadFleetInfo` derives the same mapping at runtime):

| Cabinet | DOT | Port | Role |
|---|---|---|---|
| cab-i85-001 | gdot | 18081 | I-85 hero — cut/restore + corridor incident |
| cab-002 | gdot | 18082 | signal-fault target (`expose: true`) |
| cab-i85-101 | ncdot | 18083 | I-85 hero |
| cab-i85-201 | scdot | 18084 | I-85 hero |

Available scenarios (`internal/sim/scenario.go`): `conflict-flash`,
`detector-fault`, `corridor-incident`, `ped-surge`. All auto-clear on their
own (60–120s) — no manual clear step needed.

## `democtl tour`: two modes

`cmd/democtl`'s `tour` subcommand (built on `internal/tour`) drives the same
6-phase table two ways:

- **`democtl tour --paced`** — presenter/recording mode. For each phase it
  prints a narration block (what's about to happen, which dashboard(s) to
  show and their URLs, what the audience will see), then **waits for
  Enter**. Once you press Enter it triggers the phase's action, prints what
  to watch for on the dashboard, and waits for Enter again before moving on.
  It never runs assertions or prints PASS/FAIL — the CLI is off-camera, the
  dashboard is on-camera. If stdin is closed (e.g. you Ctrl-D or your
  terminal session ends) the tour stops immediately and **does not** run
  any further phase's action — an exhausted/closed stdin is never treated
  as an implicit Enter press.
- **`democtl tour` (default) / `democtl tour --verify`** — rehearsal / take-
  QA mode. Runs all 6 phases unattended: trigger the action, sleep the
  phase's settle duration, run its assertion, print `[PASS]`/`[FAIL]`.
  Exits nonzero if any phase fails. This is what you run **before** a take
  to confirm it will be clean.

Useful flags (both modes): `--dot` (default `gdot`; the cabinet-scoped
phases target this DOT's corridor/non-corridor cabinets — baseline always
checks all 3 DOTs regardless), `--ch`/`--grafana` (base URLs, for
overriding non-default ports), `--compose-file`, `--fleet`. Verify-mode-only:
`--settle <duration>` (override every phase's settle — for rehearsing one
phase faster than its production wait allows) and `--only
<phase,phase,...>` (run a subset by name — `baseline`, `wan-cut`, `restore`, `fault`, `corridor`, `reversible` — for
rehearsing one phase at a time; useful since
`restore` expects a prior `wan-cut` and can be run as its own later
invocation).

`make demo-tour` runs verify mode; `make demo-tour-paced` runs paced mode.

## Recording checklist

1. `make demo` and wait for it to finish (~6–8 min); confirm baseline PASS
   for all 3 DOTs (see Bring-up above).
2. Run a cut/restore cycle once before recording so Resilience Lab's
   cumulative-events staircase and buffer-depth history have something to
   show on first render (Grafana's dashboards otherwise start with an empty
   window):
   ```sh
   go run ./cmd/democtl cut --cabinet cab-i85-001
   sleep 30
   go run ./cmd/democtl restore --cabinet cab-i85-001
   ```
3. `democtl tour --verify` (or `make demo-tour`) — confirm all 6 phases
   PASS. **Do not start recording until this is clean.**
4. Start your silent screen recorder, framed on the Grafana browser window.
5. Run `democtl tour --paced` (or `make demo-tour-paced`) in a terminal kept
   off-camera (or in a second monitor). Step through it phase by phase,
   narrating live: read the narration block, switch Grafana to the named
   dashboard(s), press Enter to trigger the phase, point out what's
   changing per the "WATCH FOR" line, press Enter again once it's visibly
   landed and move to the next phase. Pause as long as you like between
   phases — paced mode has no timeout.
6. If `--verify` ever fails on a rehearsal, **fix the root cause and re-run
   verify** rather than recording anyway — a failing assertion means the
   effect the narration promises won't actually be visible on screen.

### Per-phase cues

| # | Phase | Dashboard(s) to show | Say | What lands |
|---|---|---|---|---|
| 1 | baseline | Demo Tour | All 3 DOTs live, ~100 cabinets (33/DOT) streaming | Ingest rate climbing, freshness low, 0 dead letters |
| 2 | wan-cut | Resilience Lab, Demo Tour | Cutting cab-i85-001's uplink; it keeps buffering locally, nothing is lost | Buffer depth climbs; that cabinet's freshness gap grows while siblings/other DOTs stay flat |
| 3 | restore | Resilience Lab | Reconnecting; buffer drains, zero loss, zero duplicates | Buffer depth falls; cumulative-events staircase catches back up |
| 4 | fault | Fleet Health, Signal Performance | Injecting a conflict-flash fault at an intersection | Controller mode flips to flash; phase activity goes quiet (auto-clears ~90s) |
| 5 | corridor | Corridor I-85, Perception & Fusion | A perception incident on the I-85 corridor cabinet crosses the DMZ into the shared federation view | Incident + DMS advisory appear in the corridor/federation view; only corridor cabinets ever show there (auto-clears ~2min) |
| 6 | reversible | I-75 South Reversible Express Lanes | GDOT's I-75 South express lanes reverse on a schedule — northbound for the AM peak, southbound for the PM | The direction tile flips through an in-transition sweep; the reversal timeline bands blue/orange; every segment-map marker flips color together |

## AI segment ("the model is all you need" — Task 19)

Additional phases (`ai-build`, `ai-qa`) after the base six, gated behind `democtl tour --ai`.
Unlike phases 1-5, these are presenter-driven **in every mode** — the
actual work (an LLM exploring ClickHouse and building a dashboard over MCP,
then answering ad hoc questions) happens in an external MCP client
(`democtl` cannot see or drive it), so both phases always narrate and gate
on Enter, whether or not you pass `--paced`. The one automated part is the
take-QA gate (`democtl verify ai-dashboard`), which runs right after you
confirm the AI is done.

There is deliberately **no fallback tier**: if the AI's build fails the
take-QA gate, that take is bad — reset with `democtl ai-reset` and record
it again. Don't narrate over a failing gate.

### One-time setup

- `make ai-models-pack` — regenerate `demo/ai/models-pack.generated.yang`
  from the PINNED models checkout (`../../openits-models-pinned` — see
  `docs/MODELS-PIN.md`) whenever that checkout moves. Confirm the printed
  size (should be comfortably under ~300KB) and that it's fresh before a
  recording session.
- Read `demo/ai/mcp/README.md` once and wire the two MCP servers
  (`grafana/mcp-grafana`, `ClickHouse/mcp-clickhouse`) into whichever
  client you're recording with (Claude Code / Claude Desktop — ChatGPT
  needs a remote tunnel, see that doc's caveat). You'll re-paste the token
  from `democtl ai-setup` each session, but the server wiring itself is
  set up once.

### Recording checklist (AI segment)

1. Bring up the stack and run a cut/restore cycle on `cab-i85-001` **before
   this session** (same as the standard checklist's step 2) — Q1 in
   `demo/ai/prompts/questions.md` ("did we lose any data? Prove it.") needs
   a real outage in the recent past to analyze. Note the wall-clock
   start/end of the cut so you can sanity-check the AI's answer.
2. `democtl ai-reset` — clean slate; the AI Built folder must be empty
   before a take (also run this between retakes).
3. `democtl ai-setup` — prints a fresh env block (folder created, service
   account + token issued/rotated). Confirm your MCP client can already
   reach both servers (a quick "list your tools" prompt is enough) before
   you start recording.
4. Start recording. Run `democtl tour --ai --paced` (or `--only
   ai-build,ai-qa` to rehearse the AI phases alone) in a terminal kept off
   camera. Phase 6 prints the narration, runs `ai-setup` for you, and waits
   for Enter — paste the env block into your MCP client if you haven't
   already, attach `demo/ai/models-pack.generated.yang`, send
   `system-models-only.md` as the system prompt and `task-corridor.md` as
   the task, and let the AI work on camera.
5. Once the AI reports a dashboard URL, switch to it in Grafana on camera,
   then press Enter in the tour terminal. It runs `democtl verify
   ai-dashboard` automatically:
   - **PASS** — the take is good; continue.
   - **FAIL** — "bad take" prints on screen. Stop recording, run `democtl
     ai-reset`, and re-record from step 3 (a fresh token doesn't hurt
     anything, and guarantees no half-built dashboard confuses the retake).
6. Phase 7 prints the ask-the-data preface and every question from
   `demo/ai/prompts/questions.md`, then runs `democtl verify dedup --dot
   gdot` and prints the real count/distinct/diff numbers for Q1's outage
   window — read the AI's answer against these live, on camera, before
   moving to Q2-Q5. Press Enter once the Q&A beat is done.
7. `democtl ai-reset` after the take is in the can, so the folder is clean
   for the next session (or the local-model re-record — see the plan's
   stretch phases).

### Rehearsing without recording

`democtl tour --ai --only ai-build` runs just phase 6 (useful for
rehearsing the take-QA gate against a dashboard you already built by hand,
or checking `verify ai-dashboard` end to end — see this task's report for
the exact pass-then-fail sequence used to prove it). `--only ai-qa` runs
just phase 7's narration + ground truth without touching Grafana at all.

## Teardown

```sh
make demo-down
```

Tears down every container, network, and volume for the project (`docker
compose ... down -v`). The next `make demo` starts completely fresh
(including a fresh ClickHouse — no leftover data from a previous session).

## Troubleshooting

- **Cross-domain stream sources not flowing (a tier shows 0 messages).**
  This is the single highest-risk chain in the stack — sim → cabinet buffer
  → regional → central → DMZ, each hop a cross-domain JetStream source.
  First check leaf connectivity: `curl <server>:8222/leafz` (e.g.
  `curl gdot-regional:8222/leafz`) — confirm the expected leaf remotes are
  listed and connected. If leafz looks fine but a tier's stream still stays
  at 0 messages, check `nats stream info <stream>` on that hop for source
  errors, and confirm the cabinet leaf's JetStream domain matches the
  `$JS.<domain>.API` prefix used in the regional stream's source config — a
  domain mismatch between the cabinet conf and the rendered `external.api`
  prefix is the usual culprit.
- **A dashboard panel is empty / wrong values.** Two likely causes, in
  order: (1) enum literal mismatch — ClickHouse `LowCardinality(String)`
  columns like `to_state`/`transition`/`termination_reason` store the
  models enums' `.String()` output (e.g. `to_state = 'TO_STATE_YELLOW'`,
  not `'YELLOW'` or `'TO_STATE_YELLOW '`) — query one row directly and
  compare the literal before assuming the data is missing:
  `curl -s http://localhost:8123/ --data "SELECT DISTINCT to_state FROM vikasa_gdot.phase_state_change FORMAT TSV"`.
  (2) lazy-load — Grafana lazy-loads panel queries on first paint; scroll
  the panel into view or refresh once before concluding it's broken.
- **Empty Grafana panels right after bring-up.** Grafana lazy-loads panel
  queries on first paint; switch dashboards or refresh once. If a panel is
  still empty after a full refresh and the "Recording checklist" step 2
  cut/restore cycle, check the underlying data directly, e.g.:
  `curl -s http://localhost:8123/ --data "SELECT count() FROM vikasa_gdot.events_raw WHERE ce_time > now() - INTERVAL 5 MINUTE FORMAT TSV"`.
- **Sink falling behind (`ce_time` → `ingested_at` lag growing).** Already
  tuned (see progress notes: batch size 8000 / 1s max-wait / ClickHouse
  async_insert) — sustained lag should sit in the 0–20s range under full
  9-cabinet load. If you see minutes of lag, check `docker logs
  <dot>-sink-1` for repeated `fetched N/8000` at N≈8000 (saturated) vs.
  small N (healthy/idle).
- **Consumer durable config mismatch on sink startup.** Self-heals: the
  sink detects a `MaxAckPending` mismatch against an existing durable
  consumer and repairs it in place (falling back to delete+recreate) before
  resuming. No manual intervention needed; if it's still stuck after a
  minute, `docker compose ... restart <dot>-sink`.
- **`ce_time` stuck in the past while `ingested_at` is current** (fresh
  rows keep landing but their event-time is hours old). Seen once during
  this task after a long-lived stack survived a host sleep/suspend — the
  long-running sim processes' event timestamps stopped advancing at
  real-world pace even though the containers' own OS clock was correct.
  `verify baseline`'s `recent-events` check (which filters on `ce_time`)
  will fail while `heartbeat-freshness` (which uses `ingested_at`) still
  passes — that mismatch is the tell. Fix: `make demo-down && make demo`
  for a clean bring-up; don't leave the stack running across a laptop
  sleep/suspend if you plan to record from it afterward.
- **A cabinet won't reconnect after `democtl restore`.** `democtl
  cut`/`restore` resolve the leaf container and the shared `vikasa` WAN
  network live via `docker compose ps` each time (see
  `internal/verify/docker.go`), so they're safe to retry. Confirm with
  `docker network inspect <project>_vikasa` that the leaf's container is
  listed with the right service-name alias (not just its bare container
  name) — a bare `docker network connect` without `--alias` would leave
  other services unable to resolve it by service name, which
  `democtl restore` avoids by construction.
