# Vikasa Demo — Recording Walkthrough (Run-of-Show)

A shot-by-shot script for recording the demo. The recording is a **silent
screen capture that a presenter narrates live** — step through at your own
pace, pausing whenever you like. Nothing here is auto-narrated; the "SAY"
lines are talking-point prompts, not a script to read verbatim.

For bring-up, URLs, ports, scenarios, and the two-mode `democtl tour`, see
`docs/RUNBOOK.md`. This file is the *sequence* to record.

---

## Setup (before you hit record)

1. **Bring the stack up clean** (`make demo`, ~6–8 min) and confirm the
   pre-record gate is green — this is non-negotiable:
   ```sh
   make demo-tour        # democtl tour --verify: all 6 phases must PASS
   ```
   If any phase fails, fix the root cause and re-run — a failing assertion
   means the effect you'll narrate won't be visible on screen.
2. **Warm the stack ~10 min** so the timeseries have history to show, and run
   one cut/restore cycle so Resilience Lab's cumulative staircase has shape:
   ```sh
   go run ./cmd/democtl cut --cabinet cab-i85-001 ; sleep 30
   go run ./cmd/democtl restore --cabinet cab-i85-001
   ```
3. **Browser**: Chrome full-screen on `http://localhost:3000`. Every dashboard
   URL below appends `?kiosk` (hides Grafana chrome) and a time range. Anon
   access is Admin — no login prompt.
4. **Terminal**: keep one terminal *off-camera* (second monitor or hidden) for
   `democtl` commands (paced tour, cut/restore, AI setup).
5. **Screen recorder**: QuickTime (⌘⇧5) or OBS, framed on the Grafana window
   only. Record silent; the presenter narrates live over playback, or narrate
   live into the recorder — your choice.
6. **Post-production**: `ffmpeg` (8.x present) to trim/segment, e.g.
   `ffmpeg -i take.mov -ss 00:00:05 -c copy trimmed.mp4`, or split per act with
   `-ss/-to`. Keep the source `.mov`; export MP4 (H.264) for sharing.

Set every dashboard's refresh to **10s** and the time range as noted so panels
stay live on camera.

---

## Act 1 — The pitch: three sovereign DOTs, one corridor (~2 min)

Lead with the executive dashboards. This is the "why."

**1a. Executive · Corridor Federation**
`http://localhost:3000/d/vikasa-exec-federation/?kiosk&from=now-30m&to=now`
- **SAY:** Georgia, North Carolina, and South Carolina each run their own
  ~33-cabinet network, their own NATS tiers, their own ClickHouse — no shared
  control plane. Vikasa federates their I-85 corridor in real time through a
  one-way DMZ. Every cabinet stays sovereign at the edge and visible at the
  center.
- **POINT AT:** the three freshness hero stats (all a few seconds), the
  per-DOT "cabinets reporting = 33" grid, and the federation-throughput chart.

**1b. Executive · Multi-Vendor / Open Standards**
`http://localhost:3000/d/vikasa-exec-multi-vendor/?kiosk&from=now-30m&to=now`
- **SAY:** NTCIP 1202, SAE J2735, ARC-IT — open standards, any vendor.
  Econolite and McCain controllers plug into the same pipeline, same schema,
  same analytics. No translation shims, no lock-in.
- **POINT AT:** the Econolite ASC3 (5) vs McCain MaxTime (4) split and the
  vendor event coverage.

---

## Act 2 — The network is real (~1.5 min)

**2a. Fleet Health**
`http://localhost:3000/d/vikasa-fleet-health/?kiosk&from=now-15m&to=now&var-dot=gdot`
- **SAY:** Ninety-nine cabinets across three states, one operations view. The
  map shows the whole network; the pie the vendor split; the fault table and
  drill-downs take an operator from "which state has a problem" to "which
  controller" without leaving the page.
- **POINT AT:** the three 33-count clusters on the map, the vendor pie, the
  "cabinets reporting" liveness line.

**2b. Demo Tour**
`http://localhost:3000/d/vikasa-demo-tour/?kiosk&from=now-15m&to=now&var-dot=gdot`
- **SAY:** The whole pipeline on one screen — 99 cabinets to ClickHouse in
  seconds. Ingest rate, worst-case lag, events by service, dead letters at
  zero, all nine NATS tiers green.

---

## Act 3 — Resilience & operations (the driven tour, ~6–8 min)

Switch to the **paced tour** — it narrates each phase, tells you which
dashboard to show and what to watch, and waits for Enter between steps. Run it
in the off-camera terminal:
```sh
make demo-tour-paced        # democtl tour --paced
```
It walks all six phases; read its narration blocks, switch Grafana to the
named dashboard, press Enter to trigger, point out the "WATCH FOR" effect,
press Enter to advance. Pause as long as you like — paced mode has no timeout.

| # | Phase | Show | The moment |
|---|-------|------|-----------|
| 1 | baseline | Demo Tour | steady state — everything green |
| 2 | wan-cut | Resilience Lab + Demo Tour | cut cab-i85-001's uplink; its ingest drops to zero and edge buffer climbs while its siblings and the other DOTs stay flat |
| 3 | restore | Resilience Lab | buffer drains, cumulative staircase catches up — zero loss, zero duplicates |
| 4 | fault | Fleet Health + Signal Performance | inject a conflict-flash on cab-002; controller flips to flash |
| 5 | corridor | Corridor I-85 + Perception | a perception incident on the I-85 corridor cabinet crosses the DMZ into the shared federation view |
| 6 | reversible | I-75 South Reversible Express Lanes | the express lanes flip direction on schedule through an in-transition sweep |

---

## Act 4 — The Atlanta corridors, up close (~2–3 min)

**4a. Perception — GDOT I-85 corridor**
`http://localhost:3000/d/vikasa-perception-fusion/?kiosk&from=now-15m&to=now`
- **SAY:** Ten perception stations strung along the real I-85 through metro
  Atlanta. Two cameras and a lidar per cabinet, fused into one agreement
  ratio. Watch a station turn red the moment its stack flags a stopped
  vehicle.
- **DO (off-camera):** `curl -s -X POST http://localhost:18081/inject/corridor-incident`
  then watch cab-i85-001 go red on the corridor map + the incident feed.

**4b. I-75 South Reversible Express Lanes**
`http://localhost:3000/d/vikasa-reversible-lanes/?kiosk&from=now-15m&to=now`
- **SAY:** GDOT's real barrier-separated reversible lanes south of Atlanta —
  open northbound for the AM inbound peak, southbound for the PM outbound
  peak. A reversible-lane controller is just another device on the same NATS
  pipeline. It flips on schedule (compressed here) — watch the direction tile
  and the whole segment map flip color together.

**4c. Signal Performance (ATSPM)**
`http://localhost:3000/d/vikasa-signal-performance/?kiosk&from=now-15m&to=now&var-dot=gdot&var-cabinet=$__all`
- **SAY:** Classic ATSPM measures — Purdue phase-termination diagrams,
  split-failure detection, green-duration percentiles, a cross-vendor cohort
  comparison — all derived from raw controller events, no vendor's
  proprietary ATSPM module in the loop.

---

## Act 5 — "The model is all you need" (AI segment, ~3–4 min)

Optional finale. Presenter-driven; needs an MCP client (Claude/ChatGPT with
the Grafana + ClickHouse MCP servers) or Claude doing it live. See
`docs/RUNBOOK.md`'s AI section and `demo/ai/`.

```sh
go run ./cmd/democtl tour --ai --only ai-build,ai-qa   # narrates + runs ai-setup, gates on Enter
```
- **SAY:** We hand the model *only* the YANG data model — no schema docs, no
  dashboard to copy. It discovers the ClickHouse schema itself and builds a
  working operations dashboard from first principles, in the "AI Built"
  Grafana folder. Then we ask it operational questions — including "did cabinet
  cab-i85-001 lose any data during its outage? Prove it" — and it independently
  re-verifies zero loss, zero duplicates from the data alone.
- **SHOW:** the AI building the dashboard live in Grafana, then the
  `democtl verify ai-dashboard` gate going green, then the ask-the-data answers
  (SQL + numbers).

---

## Close (~30s)

Return to **Executive · Corridor Federation**.
- **SAY:** Three sovereign agencies, ~100 cabinets, one federated corridor —
  vendor-neutral, open-standard, resilient to a fiber cut, and self-describing
  enough that a model builds its own dashboards from the data model alone.
  That's Vikasa.

---

## Tips

- If a map looks blank on first load, it's tile lazy-load — give it a few
  seconds before the shot, or nudge zoom.
- Keep the CLI off-camera; the dashboard is the star.
- Record in segments (one per act) and stitch with `ffmpeg` — easier to
  re-take one act than the whole thing.
- Don't hammer ClickHouse with side queries mid-record; keep the box quiet so
  freshness stays at a couple of seconds on screen.
