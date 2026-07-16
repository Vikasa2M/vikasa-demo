package tour

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Vikasa2M/vikasa-demo/internal/verify"
)

// Grafana dashboard uids (see deploy/grafana/dashboards/*.json's top-level
// "uid" field — provisioned by Task 15/16's file-based dashboard provider).
const (
	uidDemoTour         = "vikasa-demo-tour"
	uidFleetHealth      = "vikasa-fleet-health"
	uidResilienceLab    = "vikasa-resilience-lab"
	uidSignalPerf       = "vikasa-signal-performance"
	uidCorridorI85      = "vikasa-corridor-i85"
	uidPerceptionFusion = "vikasa-perception-fusion"
	uidReversibleLanes  = "vikasa-reversible-lanes"
)

// allDOTs is the fleet's 3 DOTs, in the fixed order the baseline phase
// checks them.
var allDOTs = []string{"gdot", "ncdot", "scdot"}

// gdotSiblingCabinets are cab-i85-001's two siblings in the gdot database —
// used by the wan-cut phase to prove the cut was targeted, not fleetwide.
var gdotSiblingCabinets = []string{"cab-002", "cab-003"}

// Config parameterizes BuildPhases: where to find the live stack.
type Config struct {
	CH          string // ClickHouse HTTP base URL, e.g. http://localhost:8123
	Grafana     string // Grafana base URL, e.g. http://localhost:3000
	ComposeFile string // compose file democtl cut/restore resolves the "vikasa" WAN network against
	FleetPath   string // path to deploy/topology/fleet.yaml
	DOT         string // primary DOT for the cabinet-scoped phases (wan-cut/restore/fault/corridor); baseline always checks all 3
}

// withDefaults fills in the same defaults cmd/democtl's other subcommands
// use, for callers (tests, other tools) that don't set every field.
func (c Config) withDefaults() Config {
	if c.CH == "" {
		c.CH = "http://localhost:8123"
	}
	if c.Grafana == "" {
		c.Grafana = "http://localhost:3000"
	}
	if c.ComposeFile == "" {
		c.ComposeFile = "deploy/compose/docker-compose.yml"
	}
	if c.FleetPath == "" {
		c.FleetPath = "deploy/topology/fleet.yaml"
	}
	if c.DOT == "" {
		c.DOT = "gdot"
	}
	return c
}

// cutCabinet is gdot's I-85 corridor cabinet — the one every phase after
// baseline centers on: cut, restored, and (for the corridor phase) the
// source of the perception incident that crosses the DMZ.
const cutCabinet = "cab-i85-001"

// faultCabinet is gdot's non-corridor cabinet used for the signal-fault
// phase. Deliberately NOT a corridor cabinet: its fault must be asserted
// against the per-DOT database (vikasa_gdot), not vikasa_federation,
// since non-corridor cabinets never cross the DMZ — see ActiveFaultCountQuery.
const faultCabinet = "cab-002"

// BuildPhases assembles the 5-phase tour against cfg's live stack. Fails
// only if fleet.yaml can't be read/parsed or doesn't declare the cabinets
// the tour needs.
func BuildPhases(cfg Config) ([]Phase, error) {
	cfg = cfg.withDefaults()

	fleet, err := LoadFleetInfo(cfg.FleetPath)
	if err != nil {
		return nil, err
	}
	cutPort, ok := fleet.SimPorts[cutCabinet]
	if !ok {
		return nil, fmt.Errorf("tour: %s not found in %s's cabinet list", cutCabinet, cfg.FleetPath)
	}
	faultPort, ok := fleet.SimPorts[faultCabinet]
	if !ok {
		return nil, fmt.Errorf("tour: %s not found in %s's cabinet list", faultCabinet, cfg.FleetPath)
	}

	db := verify.Database(cfg.DOT)

	return []Phase{
		buildBaselinePhase(cfg),
		buildWANCutPhase(cfg, db, cutCabinet, cutPort),
		buildRestorePhase(cfg, db, cutCabinet),
		buildFaultPhase(cfg, db, faultCabinet, faultPort),
		buildCorridorPhase(cfg, cutCabinet, cutPort, fleet.CorridorCabinets),
		buildReversiblePhase(cfg),
	}, nil
}

// --- Phase 1: baseline ---

func buildBaselinePhase(cfg Config) Phase {
	return Phase{
		Name: "baseline",
		Narration: strings.TrimSpace(`
All three DOTs -- Georgia, North Carolina, South Carolina -- are live: about
a hundred cabinets, thirty-three per DOT, streaming detector calls, phase
changes, and heartbeats through each DOT's regional/central/DMZ NATS tiers
into ClickHouse in real time. Pull up the Demo Tour dashboard and let it
breathe for a second: ingest rate climbing across all three DOTs, per-cabinet
freshness sitting at a few seconds, dead letters flat at zero. This is the
steady state everything else in the tour perturbs and then returns to.`),
		Dashboards: []DashboardRef{
			{Title: "Demo Tour", URL: dashboardURL(cfg.Grafana, uidDemoTour)},
		},
		WatchFor: "Ingest rate by DOT climbing for all three DOTs; worst-case ingest lag low/green; dead letters at zero.",
		Action:   nil,
		Settle:   0,
		Assert: func(ctx context.Context) error {
			for _, dot := range allDOTs {
				if err := assertBaseline(ctx, cfg.CH, verify.Database(dot)); err != nil {
					return fmt.Errorf("%s: %w", dot, err)
				}
			}
			return nil
		},
	}
}

// --- Phase 2: wan-cut ---

func buildWANCutPhase(cfg Config, db, cabinet string, port int) Phase {
	return Phase{
		Name: "wan-cut",
		Narration: fmt.Sprintf(strings.TrimSpace(`
Now we cut %s's WAN uplink -- simulating a fiber cut or carrier outage
between the cabinet and GDOT's regional network. The cabinet doesn't stop
working: its local JetStream buffer keeps every event it generates, it just
can't ship them upstream. Switch to the Resilience Lab dashboard to watch
%s's buffer depth start climbing, and the Demo Tour dashboard to watch that
one cabinet's freshness gap grow while its siblings (cab-002, cab-003) and
the other two DOTs stay completely untouched.`), cabinet, cabinet),
		Dashboards: []DashboardRef{
			{Title: "Resilience Lab", URL: dashboardURL(cfg.Grafana, uidResilienceLab)},
			{Title: "Demo Tour", URL: dashboardURL(cfg.Grafana, uidDemoTour) + "?var-dot=" + cfg.DOT},
		},
		WatchFor: fmt.Sprintf(
			"%s's buffer-depth line climbing on Resilience Lab; its freshness bar on Demo Tour climbing into the red while the other 8 cabinets stay flat and green.",
			cabinet),
		Action: func(ctx context.Context) error {
			return runDemoctl(ctx, "cut", "--cabinet", cabinet, "--compose-file", cfg.ComposeFile)
		},
		Settle: 90 * time.Second,
		Assert: func(ctx context.Context) error {
			// 1. The cut cabinet's own heartbeat gap must be visibly stale.
			age, err := queryScalar(ctx, cfg.CH, CabinetHeartbeatFreshnessQuery(db, cabinet))
			if err != nil {
				return fmt.Errorf("%s heartbeat freshness: %w", cabinet, err)
			}
			if age <= verify.HeartbeatFreshnessThresholdSeconds {
				return fmt.Errorf("%s heartbeat age = %ds, want > %ds (the cut should be visible from ClickHouse)",
					cabinet, age, verify.HeartbeatFreshnessThresholdSeconds)
			}

			// 2. Its siblings in the same DOT must still be fresh: this was
			// a targeted cut, not a fleetwide outage.
			for _, sibling := range gdotSiblingCabinets {
				sAge, err := queryScalar(ctx, cfg.CH, CabinetHeartbeatFreshnessQuery(db, sibling))
				if err != nil {
					return fmt.Errorf("sibling %s heartbeat freshness: %w", sibling, err)
				}
				if sAge > verify.HeartbeatFreshnessThresholdSeconds {
					return fmt.Errorf("sibling %s heartbeat age = %ds, want <= %ds (should be unaffected by %s's cut)",
						sibling, sAge, verify.HeartbeatFreshnessThresholdSeconds, cabinet)
				}
			}

			// 3. ncdot/scdot are entirely untouched.
			for _, dot := range []string{"ncdot", "scdot"} {
				if err := assertBaseline(ctx, cfg.CH, verify.Database(dot)); err != nil {
					return fmt.Errorf("%s (should be unaffected): %w", dot, err)
				}
			}

			// 4. Zero loss at the sim's own vantage point: everything the
			// cabinet generated during the cut is still buffered locally,
			// not dropped.
			dropped, err := simDropped(ctx, port)
			if err != nil {
				return fmt.Errorf("%s sim dropped counter: %w", cabinet, err)
			}
			if dropped != 0 {
				return fmt.Errorf("%s sim reports dropped=%d, want 0 (buffering should absorb the cut, not drop)", cabinet, dropped)
			}
			return nil
		},
	}
}

// --- Phase 3: restore ---

func buildRestorePhase(cfg Config, db, cabinet string) Phase {
	return Phase{
		Name: "restore",
		Narration: fmt.Sprintf(strings.TrimSpace(`
Reconnect %s. Its buffered backlog drains upstream immediately -- watch the
Resilience Lab buffer-depth line fall back to baseline and the cumulative-
events staircase catch back up to where it would have been without the cut.
Nothing generated during the outage is lost, and nothing gets double-
counted on redelivery: ClickHouse dedupes on event id.`), cabinet),
		Dashboards: []DashboardRef{
			{Title: "Resilience Lab", URL: dashboardURL(cfg.Grafana, uidResilienceLab)},
		},
		WatchFor: fmt.Sprintf(
			"%s's buffer depth draining back to near-zero; the cumulative-events line resuming its climb with no permanent gap.",
			cabinet),
		Action: func(ctx context.Context) error {
			return runDemoctl(ctx, "restore", "--cabinet", cabinet, "--compose-file", cfg.ComposeFile)
		},
		Settle: 60 * time.Second,
		Assert: func(ctx context.Context) error {
			if err := assertBaseline(ctx, cfg.CH, db); err != nil {
				return fmt.Errorf("baseline: %w", err)
			}

			dedup, err := verify.RunDedup(ctx, verify.Deps{CH: cfg.CH, DB: db})
			if err != nil {
				return fmt.Errorf("dedup: %w", err)
			}
			if dedup.Diff != 0 {
				return fmt.Errorf("dedup: %d duplicate ce_id row(s) (count=%d distinct=%d)", dedup.Diff, dedup.Count, dedup.Distinct)
			}

			n, err := queryScalar(ctx, cfg.CH, PhaseStateChangeCountQuery(db, cabinet, 15))
			if err != nil {
				return fmt.Errorf("phase_state_change backfill: %w", err)
			}
			if n == 0 {
				return fmt.Errorf("%s has 0 phase_state_change rows in the last 15m -- the buffered backlog doesn't appear to have landed", cabinet)
			}
			return nil
		},
	}
}

// --- Phase 4: signal fault ---

func buildFaultPhase(cfg Config, db, cabinet string, port int) Phase {
	return Phase{
		Name: "fault",
		Narration: fmt.Sprintf(strings.TrimSpace(`
Inject a conflict-flash fault at %s: the controller detects a safety-
critical conflict and drops straight to flash -- every approach red or
yellow flash, no coordinated phasing, until a technician clears it. Switch
to the Fleet Health dashboard (controller mode / active faults) and Signal
Performance: %s's phase activity should visibly go quiet mid-cycle.`), cabinet, cabinet),
		Dashboards: []DashboardRef{
			{Title: "Fleet Health", URL: dashboardURL(cfg.Grafana, uidFleetHealth)},
			{Title: "Signal Performance", URL: dashboardURL(cfg.Grafana, uidSignalPerf) + "?var-dot=" + cfg.DOT + "&var-cabinet=" + cabinet},
		},
		WatchFor: fmt.Sprintf(
			"%s's controller mode flipping to flash on Fleet Health; its phase-termination activity going quiet on Signal Performance. It auto-clears itself in about 90s -- no action needed.",
			cabinet),
		Action: func(ctx context.Context) error {
			return injectScenario(ctx, port, "conflict-flash")
		},
		Settle: 20 * time.Second,
		Assert: func(ctx context.Context) error {
			faults, err := queryScalar(ctx, cfg.CH, ActiveFaultCountQuery(db, cabinet))
			if err != nil {
				return fmt.Errorf("%s active faults: %w", cabinet, err)
			}
			if faults > 0 {
				return nil
			}
			modeOut, err := verify.Query(ctx, cfg.CH, LatestOperationalModeQuery(db, cabinet))
			if err != nil {
				return fmt.Errorf("%s operational mode: %w", cabinet, err)
			}
			mode := strings.TrimSpace(modeOut)
			if mode != "flash" {
				return fmt.Errorf("%s: no active controller_fault_event AND operational_status mode = %q (want an active fault or mode=flash)",
					cabinet, mode)
			}
			return nil
		},
	}
}

// --- Phase 5: corridor incident ---

func buildCorridorPhase(cfg Config, cabinet string, port int, corridorCabinets []string) Phase {
	dot := cfg.DOT
	return Phase{
		Name: "corridor",
		Narration: fmt.Sprintf(strings.TrimSpace(`
Inject a perception incident at %s, GDOT's I-85 corridor cabinet: camera
and lidar both flag it, speeds degrade, and the DMS posts an advisory.
Because %s sits on the shared corridor, this event crosses the DMZ into the
federation view -- the other DOTs, and a regional TMC watching the whole
corridor, see it too. Switch to the Corridor I-85 dashboard (the incident
should appear) and Perception & Fusion (camera/lidar agreement on the
incident zone).`), cabinet, cabinet),
		Dashboards: []DashboardRef{
			{Title: "Corridor I-85", URL: dashboardURL(cfg.Grafana, uidCorridorI85)},
			{Title: "Perception & Fusion", URL: dashboardURL(cfg.Grafana, uidPerceptionFusion) + "?var-dot=" + dot + "&var-cabinet=" + cabinet},
		},
		WatchFor: strings.TrimSpace(`
The incident appearing in Corridor I-85's active-incidents and DMS-
advisories tables; only I-85 corridor cabinets ever appear there (not
cab-002/003 etc.) -- that's the DMZ boundary holding. Auto-clears after
about 2 minutes.`),
		Action: func(ctx context.Context) error {
			return injectScenario(ctx, port, "corridor-incident")
		},
		Settle: 20 * time.Second,
		Assert: func(ctx context.Context) error {
			incidents, err := queryScalar(ctx, cfg.CH, FederationDetectedIncidentCountQuery(dot, cabinet))
			if err != nil {
				return fmt.Errorf("federation incident: %w", err)
			}
			if incidents == 0 {
				return fmt.Errorf("vikasa_federation.perception_incident: no detected incident for dot=%s cabinet=%s", dot, cabinet)
			}

			advisories, err := queryScalar(ctx, cfg.CH, FederationAdvisoryCountQuery(dot, cabinet))
			if err != nil {
				return fmt.Errorf("federation dms advisory: %w", err)
			}
			if advisories == 0 {
				return fmt.Errorf("vikasa_federation.dms_event: no advisory-mode row for dot=%s cabinet=%s", dot, cabinet)
			}

			leaked, err := queryScalar(ctx, cfg.CH, FederationNonCorridorLeakQuery(dot, corridorCabinets))
			if err != nil {
				return fmt.Errorf("dmz boundary check: %w", err)
			}
			if leaked != 0 {
				return fmt.Errorf("vikasa_federation.perception_incident: %d row(s) from a non-corridor cabinet leaked across the DMZ", leaked)
			}
			return nil
		},
	}
}

// --- Phase 6: reversible lanes ---

func buildReversiblePhase(cfg Config) Phase {
	db := verify.Database(cfg.DOT) // gdot
	return Phase{
		Name: "reversible",
		Narration: strings.TrimSpace(`
GDOT also runs the I-75 South Metro Express Lanes -- barrier-separated
reversible lanes that open northbound for the morning inbound peak and
southbound for the evening outbound peak. Open the I-75 South Reversible
Express Lanes dashboard and watch the big direction tile: on a schedule the
segment sweeps through an in-transition barrier phase and flips direction,
and every station on the segment map flips color together. Same NATS
pipeline, same ClickHouse -- a reversible-lane controller is just another
device publishing standard LaneStateChanged events.`),
		Dashboards: []DashboardRef{
			{Title: "I-75 South Reversible Express Lanes", URL: dashboardURL(cfg.Grafana, uidReversibleLanes)},
		},
		WatchFor: "The current-direction tile flipping between northbound and southbound through an in-transition sweep; the reversal-schedule timeline banding blue/orange; every segment-map marker flipping color together.",
		Action:   nil,
		Settle:   20 * time.Second,
		Assert: func(ctx context.Context) error {
			dirs, err := queryScalar(ctx, cfg.CH, ReversibleDirectionsQuery(db))
			if err != nil {
				return fmt.Errorf("reversible-lane directions: %w", err)
			}
			if dirs < 2 {
				return fmt.Errorf("reversible lane showed %d distinct open directions in the last 5m, want >= 2 (it should flip northbound<->southbound on schedule)", dirs)
			}
			return nil
		},
	}
}

// assertBaseline runs verify.RunBaseline against db and turns the first
// failing check (if any) into an error.
func assertBaseline(ctx context.Context, ch, db string) error {
	results, err := verify.RunBaseline(ctx, verify.Deps{CH: ch, DB: db})
	if err != nil {
		return err
	}
	if fail := firstFailure(results); fail != nil {
		return fmt.Errorf("%s: %s", fail.Name, fail.Detail)
	}
	return nil
}
