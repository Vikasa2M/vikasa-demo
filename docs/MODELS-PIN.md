# openits-models pin

The demo pins `github.com/Vikasa2M/openits-models` at commit `75f1fdb` and
vendors it, so a fresh clone builds and runs fully offline.

## Why this commit

A later revision of the signal-control event model ("cut-3a") replaced the
parallel per-event enums with a single `Kind` string classifier, dropping:

- `PhaseStateChange.ToState` / `TerminationReason`
- `DetectorTransition.Transition`
- `PedestrianEvent.Event`
- `CoordinationChange.ChangeKind`
- `PreemptionEvent.Stage`

This demo's event synthesis (`internal/sim/asc.go`) and classification
(`internal/sink/handlers.go`) — and the ClickHouse columns and Grafana panels
keyed on those enum values — are written against the older enum model, so the
demo pins the last commit before cut-3a:

```
75f1fdb  Merge signal-control config-completeness cut 2d: NTCIP 1201 timebase (TOD schedule + clock)
```

Moving to a current `openits-models` release means porting those code paths (and
the enum-keyed columns and dashboards) to the `Kind` model.

## Mechanism

The pin is implemented as an **insulated clone** — a dedicated checkout at a
fixed commit, kept separate from any live working checkout of the models repo:

```
git clone https://github.com/Vikasa2M/openits-models ../../openits-models-pinned
git -C ../../openits-models-pinned checkout 75f1fdb
```

This produces a detached-HEAD checkout at `../../openits-models-pinned`
(a sibling of this repo) that is outside this repo and **not tracked by its
git history**. It will not move even if the upstream `openits-models` branch
keeps changing.

`go.mod`'s replace directive points at it:

```
replace github.com/openits/openits-models => ../../openits-models-pinned
```

## Self-contained builds — no sibling needed

A fresh clone does **not** need the insulated checkout to build or run the
demo. The pinned models packages are committed under `vendor/` (`go mod
vendor` pulls in the replaced module's packages), and every build path uses
vendor mode:

- Host `go build`/`go test` auto-select vendor mode because `vendor/` is
  present, so they never consult the `replace` path.
- CI sets `GOFLAGS=-mod=vendor` explicitly (see `.github/workflows/ci.yaml`).
- `make docker-build` builds with the **repo root as the Docker context** and
  `go build -mod=vendor` inside the image (see `Dockerfile`). It no longer
  copies `openits-models-pinned` into the context, so there's nothing to
  whitelist — the old machine-local `.dockerignore` allowlist is obsolete.

The `replace` directive stays in `go.mod` for local, unvendored development
against the insulated checkout, but it is inert under vendor mode. Regenerate
and recommit `vendor/` (`go mod vendor`) whenever `go.mod`/`go.sum` changes or
the pin moves.

The one path that still reads the checkout is the optional AI segment:
`make ai-models-pack` regenerates the (gitignored) YANG pack from
`../../openits-models-pinned` via `MODELS_DIR`. The base demo does not.

## Topology renderer — a pinned released module

The NATS/JetStream stream configs are rendered by
`github.com/Vikasa2M/vikasa-infra`'s `cmd/gen`, which `make topology` runs via
`go run …@v0.1.0` (pinned in the Makefile as `INFRA_VERSION`). Go fetches it from
the module proxy on first use and caches it thereafter, so `make demo` needs
network access only on the first run. To build fully offline (or to iterate on
the renderer), point `INFRA_GEN` at a local checkout's `./cmd/gen`.
