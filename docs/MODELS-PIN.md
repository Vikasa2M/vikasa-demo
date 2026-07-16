# openits-models pin

**Status:** demo is pinned to `openits-models@75f1fdb` pending signal-control
cut-3a stabilization.

## Why

The sibling `openits-models` repo is on a churning branch
(`signal-control-cut3a`) and landed a breaking change ("cut 3a",
commit `ab71d1a` and follow-ups, merged at `25e681e`) that drops the
parallel signal-control event enums:

- `PhaseStateChange.ToState` / `TerminationReason`
- `DetectorTransition.Transition`
- `PedestrianEvent.Event`
- `CoordinationChange.ChangeKind`
- `PreemptionEvent.Stage`

Cut-3a moves to `Kind` as the sole event classifier instead of these
parallel enums. That broke this repo's build: `internal/sim/asc.go` (event
synthesis) and `internal/sink/handlers.go` (event classification/routing)
both reference the dropped fields directly.

Rather than race a live, still-churning branch on the models side, the
demo is pinned to the last commit *before* cut-3a landed:

```
75f1fdb  Merge signal-control config-completeness cut 2d: NTCIP 1201 timebase (TOD schedule + clock)
```

## Mechanism

The pin is implemented as an **insulated clone**, not a reference into the
live `../../openits-models` checkout (that repo is owned by another
in-flight session and must not be touched):

```
git clone ~/GolandProjects/openits-models ~/GolandProjects/openits-models-pinned
git -C ~/GolandProjects/openits-models-pinned checkout 75f1fdb
```

This produces a detached-HEAD checkout at `../../openits-models-pinned`
(sibling of both `vikasa-demo` and `openits-models`, i.e.
`~/GolandProjects/openits-models-pinned`) that is
outside this repo and **not tracked by vikasa-demo's git history**. It
will not move even if the live `openits-models` branch keeps churning.

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
  whitelist — the old machine-local `~/GolandProjects/.dockerignore` allowlist
  is obsolete.

The `replace` directive stays in `go.mod` for local, unvendored development
against the insulated checkout (and for the deferred re-sync below), but it is
inert under vendor mode. Regenerate and recommit `vendor/` (`go mod vendor`)
whenever `go.mod`/`go.sum` changes or the pin moves.

The one path that still reads the checkout is the optional AI segment:
`make ai-models-pack` regenerates the (gitignored) YANG pack from
`../../openits-models-pinned` via `MODELS_DIR`. The base demo does not.

## Topology renderer — also vendored in-repo

The other former sibling dependency, `github.com/Vikasa2M/vikasa-infra` (the
NATS/stream topology renderer that `make topology` invokes), is likewise
copied into the repo under `third_party/vikasa-infra/` as a self-contained
nested module — see `third_party/vikasa-infra/README.md` for its provenance
commit and refresh procedure. Both this and the models vendoring are stopgaps
until the respective repos publish consumable GitHub releases.

## Deferred follow-up

Re-syncing to cut-3a (or whatever lands after it stabilizes) is deferred
work, tracked separately. When it's picked up:

1. Point the replace back at `../../openits-models` (or a new pinned
   commit past cut-3a).
2. Rewrite `internal/sim/asc.go` event synthesis to stop setting
   `ToState`/`TerminationReason`/`Transition`/`Event`/`ChangeKind`/`Stage`
   and instead encode everything through `Kind`.
3. Rewrite `internal/sink/handlers.go` classification/routing to read
   `Kind` instead of the dropped per-message enum getters.
4. Check whether the ClickHouse DDL / dimension tables encode any of the
   dropped enums and update accordingly.
5. Delete `../../openits-models-pinned` once no longer needed (it's a
   throwaway insulated clone, not a permanent fixture).
