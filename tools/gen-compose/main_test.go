package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Vikasa2M/vikasa-demo/internal/fleetsize"
	"gopkg.in/yaml.v3"
)

// fleetPath is the real, committed fleet.yaml — the golden test runs
// gen-compose against the actual SSOT, not a fixture, so a fleet.yaml edit
// that breaks generation fails this test immediately.
const fleetPath = "../../deploy/topology/fleet.yaml"

var dots = []string{"mardot", "veldot", "sabdot"}

// testCab is one cabinet flattened with its DOT and hero classification, for
// the scale-invariant assertions below. The fleet grew from 9 hand-listed
// cabinets to ~100 (distributed-network SCALE), so the tests derive every
// cabinet fact from the parsed SSOT rather than a hand-maintained fixture.
type testCab struct {
	Dot  string
	ID   string
	Hero bool // corridor != "" — the one I-85 cabinet per DOT (WAN-cut demo)
}

// fleetCabinets returns every cabinet in fleet.yaml order, tagged with its DOT
// and hero flag. Hero cabinets (corridor != "") get a private net + exporter +
// volume; non-hero scale cabinets are leaf+sim on "vikasa" only.
func fleetCabinets(t *testing.T) []testCab {
	t.Helper()
	fleet, err := loadFleet(fleetPath)
	if err != nil {
		t.Fatalf("loadFleet: %v", err)
	}
	var out []testCab
	for _, d := range fleet.Dots {
		for _, c := range d.Cabinets {
			out = append(out, testCab{Dot: d.Dot, ID: c.ID, Hero: c.Corridor != ""})
		}
	}
	return out
}

// TestFleetCabinetCount asserts the fleet's structural invariants: exactly 3
// DOTs, every DOT has at least one cabinet, and every DOT has exactly one hero
// (corridor) cabinet — TestTopologySpecsMatchFleet and the SCALE=1 filter both
// depend on there being precisely one corridor cabinet per DOT.
func TestFleetCabinetCount(t *testing.T) {
	fleet, err := loadFleet(fleetPath)
	if err != nil {
		t.Fatalf("loadFleet: %v", err)
	}
	if len(fleet.Dots) != 3 {
		t.Fatalf("got %d dots, want 3", len(fleet.Dots))
	}
	for _, d := range fleet.Dots {
		if len(d.Cabinets) == 0 {
			t.Errorf("%s has no cabinets", d.Dot)
		}
		heroes := 0
		for _, c := range d.Cabinets {
			if c.Corridor != "" {
				heroes++
			}
		}
		if heroes != 1 {
			t.Errorf("%s has %d corridor (hero) cabinets, want exactly 1", d.Dot, heroes)
		}
	}
}

// TestGenComposeGoldenFullFleet is the Step-1 golden test: run gen-compose
// against the real fleet.yaml into a temp dir and assert every artifact the
// brief calls out.
func TestGenComposeGoldenFullFleet(t *testing.T) {
	root := t.TempDir()
	outDir := filepath.Join(root, "compose")

	if err := run(fleetPath, outDir, fleetsize.Large); err != nil {
		t.Fatalf("run: %v", err)
	}

	cabs := fleetCabinets(t)

	// NATS confs: one per cabinet + 9 tier (3 dots x {regional,central,dmz}).
	wantConfs := len(cabs) + 9
	natsDir := filepath.Join(outDir, "nats")
	confs, err := os.ReadDir(natsDir)
	if err != nil {
		t.Fatalf("read %s: %v", natsDir, err)
	}
	if len(confs) != wantConfs {
		names := make([]string, len(confs))
		for i, e := range confs {
			names[i] = e.Name()
		}
		t.Fatalf("got %d nats confs, want %d: %v", len(confs), wantConfs, names)
	}
	for _, dot := range dots {
		for _, tier := range []string{"regional", "central", "dmz"} {
			want := dot + "-" + tier + ".conf"
			if _, err := os.Stat(filepath.Join(natsDir, want)); err != nil {
				t.Errorf("missing tier conf %s", want)
			}
		}
	}
	for i, dot := range dots {
		for _, id := range fleetCabinetIDsForDot(t, i) {
			want := dot + "-" + id + ".conf"
			if _, err := os.Stat(filepath.Join(natsDir, want)); err != nil {
				t.Errorf("missing cabinet conf %s", want)
			}
		}
	}

	// One sim YAML per cabinet.
	simsDir := filepath.Join(outDir, "sims")
	sims, err := os.ReadDir(simsDir)
	if err != nil {
		t.Fatalf("read %s: %v", simsDir, err)
	}
	if len(sims) != len(cabs) {
		t.Errorf("got %d sim configs, want %d", len(sims), len(cabs))
	}

	// Generated compose parses as YAML and contains the expected services +
	// per-cabinet networks.
	composeBytes, err := os.ReadFile(filepath.Join(outDir, "docker-compose.generated.yml"))
	if err != nil {
		t.Fatalf("read generated compose: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(composeBytes, &doc); err != nil {
		t.Fatalf("generated compose is not valid YAML: %v", err)
	}
	services, _ := doc["services"].(map[string]any)
	if services == nil {
		t.Fatalf("generated compose has no services map")
	}
	wantServices := []string{
		"mardot-sink", "veldot-sink", "sabdot-sink",
		"stream-init-mardot", "stream-init-veldot", "stream-init-sabdot",
		"federation-sink",
	}
	for _, c := range cabs {
		wantServices = append(wantServices, c.ID+"-sim")
	}
	for _, want := range wantServices {
		if _, ok := services[want]; !ok {
			t.Errorf("generated compose missing service %q", want)
		}
	}

	// NATS exporter sidecars: one per DOT per tier (9 total — e.g.
	// mardot-regional-exporter, veldot-central-exporter, sabdot-dmz-exporter).
	// These used to be hand-written mardot-only entries in the static
	// docker-compose.yml with a depends_on into the generated file, which
	// made the static file invalid standalone; they're generated here now,
	// alongside the tier server each one scrapes.
	for _, dot := range dots {
		for _, tier := range []string{"regional", "central", "dmz"} {
			svcName := dot + "-" + tier + "-exporter"
			svc, ok := services[svcName].(map[string]any)
			if !ok {
				t.Fatalf("generated compose missing exporter service %q", svcName)
			}
			cmd, _ := svc["command"].([]any)
			wantArg := fmt.Sprintf("http://%s-%s:8222", dot, tier)
			found := false
			for _, c := range cmd {
				if fmt.Sprintf("%v", c) == wantArg {
					found = true
				}
			}
			if !found {
				t.Errorf("%s command = %v, want to contain %q", svcName, cmd, wantArg)
			}
			nets := serviceNetworks(t, services, svcName)
			if !containsStr(nets, "vikasa") {
				t.Errorf("%s networks = %v, want to contain %q", svcName, nets, "vikasa")
			}
			deps := serviceDependsOn(t, services, svcName)
			wantDep := dot + "-" + tier
			if !containsStr(deps, wantDep) {
				t.Errorf("%s depends_on = %v, want to contain %q", svcName, deps, wantDep)
			}
			if got := serviceRestart(t, services, svcName); got != "on-failure" {
				t.Errorf("%s restart = %q, want %q", svcName, got, "on-failure")
			}
		}
	}

	// NATS exporter sidecars per cabinet LEAF: ONE PER HERO CABINET only (the
	// corridor cabinets, e.g. mardot-cab-i85-001-nats-exporter), alongside the
	// leaf it scrapes. The WAN-cut/buffering demo's hero metric (JetStream
	// storage bytes) lives on the leaf, not the tier servers, so this is what
	// makes the buffer scrapable at all — including DURING a cut. Unlike the
	// tier exporters (vikasa-only), the cabinet exporter must be on BOTH
	// "vikasa" (so Prometheus can reach it) AND the cabinet's private net
	// (so IT can still reach the leaf once `democtl cut` disconnects the
	// leaf from "vikasa" — confirmed live that an vikasa-only exporter
	// goes blind mid-cut, exactly when the buffer metric needs to be
	// climbing). NON-HERO scale cabinets have NO exporter (nothing to cut,
	// and ~100 dead scrape targets would be pure noise).
	for _, c := range cabs {
		leaf := c.Dot + "-" + c.ID + "-nats"
		svcName := leaf + "-exporter"
		_, exists := services[svcName]
		if !c.Hero {
			if exists {
				t.Errorf("non-hero cabinet %s unexpectedly has an exporter service %q", c.ID, svcName)
			}
			continue
		}
		svc, ok := services[svcName].(map[string]any)
		if !ok {
			t.Fatalf("generated compose missing hero cabinet exporter service %q", svcName)
		}
		cmd, _ := svc["command"].([]any)
		wantArg := fmt.Sprintf("http://%s:8222", leaf)
		found := false
		for _, cc := range cmd {
			if fmt.Sprintf("%v", cc) == wantArg {
				found = true
			}
		}
		if !found {
			t.Errorf("%s command = %v, want to contain %q", svcName, cmd, wantArg)
		}
		nets := serviceNetworks(t, services, svcName)
		wantNet := c.ID + "-net"
		if !containsStr(nets, "vikasa") || !containsStr(nets, wantNet) {
			t.Errorf("%s networks = %v, want to contain both %q and %q", svcName, nets, "vikasa", wantNet)
		}
		deps := serviceDependsOn(t, services, svcName)
		if !containsStr(deps, leaf) {
			t.Errorf("%s depends_on = %v, want to contain %q", svcName, deps, leaf)
		}
		if got := serviceRestart(t, services, svcName); got != "on-failure" {
			t.Errorf("%s restart = %q, want %q", svcName, got, "on-failure")
		}
	}

	// federation-sink is the cross-DOT federation consumer (Task 14): it
	// must depend on every DOT's dmz server (source of the share-subject
	// stream) plus clickhouse (its ClickHouse target), and target
	// vikasa_federation via FEDERATION_CH_DSN.
	fedDeps := serviceDependsOn(t, services, "federation-sink")
	for _, want := range []string{"mardot-dmz", "veldot-dmz", "sabdot-dmz", "clickhouse"} {
		if !containsStr(fedDeps, want) {
			t.Errorf("federation-sink depends_on = %v, want to contain %q", fedDeps, want)
		}
	}
	fedEnv := serviceEnv(t, services, "federation-sink")
	if got := fedEnv["FEDERATION_CH_DSN"]; got != "clickhouse://clickhouse:9000" {
		t.Errorf("federation-sink env FEDERATION_CH_DSN = %q, want %q", got, "clickhouse://clickhouse:9000")
	}

	// CRITICAL property 3 (bring-up robustness): every per-DOT sink and
	// federation-sink must self-recover from a crash (`restart: on-failure`)
	// as a belt-and-suspenders backstop to internal/sink's stream-bind retry
	// loop and the Makefile's stream-init-before-sinks ordering. stream-init
	// jobs are one-shot `run --rm` invocations and must keep `restart: "no"`
	// — auto-restarting them under `up -d` would be wrong.
	for _, svc := range []string{"mardot-sink", "veldot-sink", "sabdot-sink", "federation-sink"} {
		if got := serviceRestart(t, services, svc); got != "on-failure" {
			t.Errorf("%s restart = %q, want %q", svc, got, "on-failure")
		}
	}
	for _, svc := range []string{"stream-init-mardot", "stream-init-veldot", "stream-init-sabdot"} {
		if got := serviceRestart(t, services, svc); got != "no" {
			t.Errorf("%s restart = %q, want %q", svc, got, "no")
		}
	}

	// Only HERO cabinets declare a private network; non-hero scale cabinets
	// share "vikasa" (declaring ~100 private nets would exhaust Docker's
	// default address pool).
	networks, _ := doc["networks"].(map[string]any)
	if networks == nil {
		t.Fatalf("generated compose has no networks map")
	}
	for _, c := range cabs {
		want := c.ID + "-net"
		_, ok := networks[want]
		if c.Hero && !ok {
			t.Errorf("generated compose missing hero cabinet network %q", want)
		}
		if !c.Hero && ok {
			t.Errorf("generated compose unexpectedly declares non-hero cabinet network %q", want)
		}
	}

	// CRITICAL property 1 (WAN-cut/buffering demo depends on this): a
	// cabinet's SIM service is on ONLY its private network — it can never
	// reach the WAN directly — while its LEAF NATS service bridges BOTH the
	// shared "vikasa" WAN network and the private network (that bridge is
	// exactly what a WAN-cut severs). A tier service (e.g. "mardot-central")
	// sits on "vikasa" only and never on a cabinet's private network. Check
	// one corridor cabinet per DOT.
	corridorByDot := map[string]string{
		"mardot": "cab-i85-001",
		"veldot": "cab-i85-101",
		"sabdot": "cab-i85-201",
	}
	for _, dot := range dots {
		cabID := corridorByDot[dot]
		netName := cabID + "-net"

		simName := cabID + "-sim"
		simNets := serviceNetworks(t, services, simName)
		if len(simNets) != 1 || simNets[0] != netName {
			t.Errorf("%s networks = %v, want exactly [%s]", simName, simNets, netName)
		}

		leafName := dot + "-" + cabID + "-nats"
		leafNets := serviceNetworks(t, services, leafName)
		if !containsStr(leafNets, "vikasa") || !containsStr(leafNets, netName) {
			t.Errorf("%s networks = %v, want to contain both %q and %q", leafName, leafNets, "vikasa", netName)
		}
	}

	tierName := "mardot-central"
	tierNets := serviceNetworks(t, services, tierName)
	if !containsStr(tierNets, "vikasa") {
		t.Errorf("%s networks = %v, want to contain %q", tierName, tierNets, "vikasa")
	}
	for _, n := range tierNets {
		if strings.HasSuffix(n, "-net") {
			t.Errorf("%s networks = %v, unexpectedly includes cabinet-private network %q", tierName, tierNets, n)
		}
	}

	// SCALE property (non-hero cabinets): a non-hero cabinet's SIM and its
	// LEAF both sit on "vikasa" ONLY — no private net at all. This is what
	// lets the fleet reach ~100 cabinets without one Docker network each. The
	// sim reaches its leaf over the shared "vikasa" WAN (there is no cut to
	// survive, so no private-net isolation is needed). Check the first
	// non-hero cabinet in each DOT.
	seenNonHero := map[string]bool{}
	for _, c := range cabs {
		if c.Hero || seenNonHero[c.Dot] {
			continue
		}
		seenNonHero[c.Dot] = true

		simNets := serviceNetworks(t, services, c.ID+"-sim")
		if len(simNets) != 1 || simNets[0] != "vikasa" {
			t.Errorf("non-hero %s-sim networks = %v, want exactly [vikasa]", c.ID, simNets)
		}
		leafName := c.Dot + "-" + c.ID + "-nats"
		leafNets := serviceNetworks(t, services, leafName)
		if len(leafNets) != 1 || leafNets[0] != "vikasa" {
			t.Errorf("non-hero %s networks = %v, want exactly [vikasa]", leafName, leafNets)
		}
	}

	// CRITICAL property 2: SINK_STREAM (and related sink env) literal
	// values — the sink's whole job is draining the right central-tier
	// stream into the right ClickHouse database with the right subject
	// filter; a wrong value here silently drops or misroutes demo data.
	wantSinkEnv := map[string]map[string]string{
		"mardot-sink": {
			"SINK_STREAM":      "VIKASA_MARDOT_CENTRAL_D1_D1_0",
			"SINK_CH_DATABASE": "vikasa_mardot",
			"SINK_FILTER":      "vikasa.mardot.>",
		},
		"veldot-sink": {
			"SINK_STREAM":      "VIKASA_VELDOT_CENTRAL_D1_D1_0",
			"SINK_CH_DATABASE": "vikasa_veldot",
			"SINK_FILTER":      "vikasa.veldot.>",
		},
		"sabdot-sink": {
			"SINK_STREAM":      "VIKASA_SABDOT_CENTRAL_D1_D1_0",
			"SINK_CH_DATABASE": "vikasa_sabdot",
			"SINK_FILTER":      "vikasa.sabdot.>",
		},
	}
	for svcName, want := range wantSinkEnv {
		env := serviceEnv(t, services, svcName)
		for k, wantV := range want {
			if got := env[k]; got != wantV {
				t.Errorf("%s env %s = %q, want %q", svcName, k, got, wantV)
			}
		}
	}

	// Seed SQL: one cabinet row per fleet cabinet into vikasa_federation,
	// and per-DOT the count of that DOT's cabinets. Derived from the fleet so
	// this holds at any fleet size.
	perDotCount := map[string]int{}
	for _, c := range cabs {
		perDotCount[c.Dot]++
	}
	seedBytes, err := os.ReadFile(filepath.Join(root, "clickhouse", "seed.generated.sql"))
	if err != nil {
		t.Fatalf("read seed.generated.sql: %v", err)
	}
	seed := string(seedBytes)
	if n := countInsertRows(t, seed, "INSERT INTO vikasa_federation.cabinets"); n != len(cabs) {
		t.Errorf("vikasa_federation.cabinets insert has %d rows, want %d", n, len(cabs))
	}
	for _, dot := range dots {
		prefix := "INSERT INTO vikasa_" + dot + ".cabinets"
		if n := countInsertRows(t, seed, prefix); n != perDotCount[dot] {
			t.Errorf("%s insert has %d rows, want %d", prefix, n, perDotCount[dot])
		}
	}
	if n := countInsertRows(t, seed, "INSERT INTO vikasa_federation.devices"); n != len(cabs)*6 {
		t.Errorf("vikasa_federation.devices insert has %d rows, want %d (%d cabinets x 6 devices)", n, len(cabs)*6, len(cabs))
	}

	// Inventory JSONs match the fleet's cabinets exactly.
	for i, dot := range dots {
		invBytes, err := os.ReadFile(filepath.Join(root, "topology", "inventories", dot+"-cabinets.json"))
		if err != nil {
			t.Fatalf("read %s inventory: %v", dot, err)
		}
		var inv struct {
			Dot      string `json:"dot"`
			Cabinets []struct {
				ID        string `json:"id"`
				Partition string `json:"partition"`
				Filter    string `json:"filter"`
			} `json:"cabinets"`
		}
		if err := json.Unmarshal(invBytes, &inv); err != nil {
			t.Fatalf("%s inventory is not valid JSON: %v", dot, err)
		}
		if inv.Dot != dot {
			t.Errorf("%s inventory dot = %q, want %q", dot, inv.Dot, dot)
		}
		wantIDs := fleetCabinetIDsForDot(t, i)
		if len(inv.Cabinets) != len(wantIDs) {
			t.Fatalf("%s inventory has %d cabinets, want %d", dot, len(inv.Cabinets), len(wantIDs))
		}
		for j, c := range inv.Cabinets {
			id := wantIDs[j]
			wantJSDomain := dot + "-d1-" + id
			wantFilter := "vikasa." + dot + ".d1." + id + ".>"
			if c.ID != wantJSDomain {
				t.Errorf("%s cabinet[%d].id = %q, want %q", dot, j, c.ID, wantJSDomain)
			}
			if c.Filter != wantFilter {
				t.Errorf("%s cabinet[%d].filter = %q, want %q", dot, j, c.Filter, wantFilter)
			}
			if c.Partition != "d1/0" {
				t.Errorf("%s cabinet[%d].partition = %q, want %q", dot, j, c.Partition, "d1/0")
			}
		}
	}

	// Prometheus file-SD targets: 9 nats tier exporter targets (one per DOT
	// per tier) + 9 nats-cabinet leaf exporter targets (one per cabinet) + 4
	// sink targets (3 per-DOT sinks + federation-sink), generated alongside
	// the compose file so prometheus.yml itself can stay static and use
	// file_sd_configs instead of a hardcoded (mardot-only) target list.
	targetsBytes, err := os.ReadFile(filepath.Join(root, "prometheus", "targets.generated.json"))
	if err != nil {
		t.Fatalf("read targets.generated.json: %v", err)
	}
	var groups []promTargetGroup
	if err := json.Unmarshal(targetsBytes, &groups); err != nil {
		t.Fatalf("targets.generated.json is not valid JSON: %v", err)
	}
	var natsTargets, natsCabinetTargets, sinkTargets []string
	for _, g := range groups {
		switch g.Labels["job"] {
		case "nats":
			natsTargets = append(natsTargets, g.Targets...)
		case "nats-cabinet":
			natsCabinetTargets = append(natsCabinetTargets, g.Targets...)
		case "sinks":
			sinkTargets = append(sinkTargets, g.Targets...)
		}
	}
	if len(natsTargets) != 9 {
		t.Errorf("prometheus targets: got %d nats targets, want 9: %v", len(natsTargets), natsTargets)
	}
	for _, dot := range dots {
		for _, tier := range []string{"regional", "central", "dmz"} {
			want := fmt.Sprintf("%s-%s-exporter:7777", dot, tier)
			if !containsStr(natsTargets, want) {
				t.Errorf("prometheus nats targets = %v, want to contain %q", natsTargets, want)
			}
		}
	}
	// nats-cabinet targets: one per HERO cabinet only (non-hero cabinets have
	// no leaf exporter to scrape).
	var heroCabs, nonHeroCabs []testCab
	for _, c := range cabs {
		if c.Hero {
			heroCabs = append(heroCabs, c)
		} else {
			nonHeroCabs = append(nonHeroCabs, c)
		}
	}
	if len(natsCabinetTargets) != len(heroCabs) {
		t.Errorf("prometheus targets: got %d nats-cabinet targets, want %d (hero cabinets): %v", len(natsCabinetTargets), len(heroCabs), natsCabinetTargets)
	}
	for _, c := range heroCabs {
		want := fmt.Sprintf("%s-%s-nats-exporter:7777", c.Dot, c.ID)
		if !containsStr(natsCabinetTargets, want) {
			t.Errorf("prometheus nats-cabinet targets = %v, want to contain %q", natsCabinetTargets, want)
		}
	}
	for _, c := range nonHeroCabs {
		notWant := fmt.Sprintf("%s-%s-nats-exporter:7777", c.Dot, c.ID)
		if containsStr(natsCabinetTargets, notWant) {
			t.Errorf("prometheus nats-cabinet targets unexpectedly contains non-hero target %q", notWant)
		}
	}
	wantSinkTargets := []string{"mardot-sink:9091", "veldot-sink:9091", "sabdot-sink:9091", "federation-sink:9091"}
	if len(sinkTargets) != len(wantSinkTargets) {
		t.Errorf("prometheus targets: got %d sink targets, want %d: %v", len(sinkTargets), len(wantSinkTargets), sinkTargets)
	}
	for _, want := range wantSinkTargets {
		if !containsStr(sinkTargets, want) {
			t.Errorf("prometheus sink targets = %v, want to contain %q", sinkTargets, want)
		}
	}
}

// TestGenComposeCleansStaleOutputAcrossRuns runs a full-fleet generation
// into an -out dir, then re-runs with SCALE=1 into the SAME dir, and asserts
// the non-corridor cabinets' confs/sims from the first run are gone. Without
// clearing natsDir/simsDir before each run, `make gen-compose` would leave
// orphaned generated files behind whenever the fleet or SCALE setting
// shrinks the cabinet set between runs.
func TestGenComposeCleansStaleOutputAcrossRuns(t *testing.T) {
	root := t.TempDir()
	outDir := filepath.Join(root, "compose")

	if err := run(fleetPath, outDir, fleetsize.Large); err != nil {
		t.Fatalf("run (full fleet): %v", err)
	}
	if err := run(fleetPath, outDir, fleetsize.Small); err != nil {
		t.Fatalf("run (SCALE=1): %v", err)
	}

	natsDir := filepath.Join(outDir, "nats")
	confs, err := os.ReadDir(natsDir)
	if err != nil {
		t.Fatalf("read %s: %v", natsDir, err)
	}
	if len(confs) != 12 {
		names := make([]string, len(confs))
		for i, e := range confs {
			names[i] = e.Name()
		}
		t.Fatalf("got %d nats confs after full-then-SCALE=1 rerun, want 12 (stale files left behind): %v", len(confs), names)
	}
	if _, err := os.Stat(filepath.Join(natsDir, "mardot-cab-002.conf")); err == nil {
		t.Error("stale mardot-cab-002.conf from the full-fleet run survived the SCALE=1 rerun")
	}

	simsDir := filepath.Join(outDir, "sims")
	sims, err := os.ReadDir(simsDir)
	if err != nil {
		t.Fatalf("read %s: %v", simsDir, err)
	}
	if len(sims) != 3 {
		t.Errorf("got %d sim configs after full-then-SCALE=1 rerun, want 3", len(sims))
	}
}

// TestGenComposeScale1 asserts SCALE=1 restricts every DOT to its single
// corridor cabinet: 3 cabinet confs (one per DOT) + 9 tier confs, 3 sims,
// 3 cabinet-net networks, and 3-row (not 9-row) federation cabinet insert.
func TestGenComposeScale1(t *testing.T) {
	root := t.TempDir()
	outDir := filepath.Join(root, "compose")

	if err := run(fleetPath, outDir, fleetsize.Small); err != nil {
		t.Fatalf("run: %v", err)
	}

	natsDir := filepath.Join(outDir, "nats")
	confs, err := os.ReadDir(natsDir)
	if err != nil {
		t.Fatalf("read %s: %v", natsDir, err)
	}
	if len(confs) != 12 { // 3 cabinet (1 per DOT) + 9 tier
		t.Fatalf("got %d nats confs under SCALE=1, want 12", len(confs))
	}

	corridorIDs := []string{"cab-i85-001", "cab-i85-101", "cab-i85-201"}
	simsDir := filepath.Join(outDir, "sims")
	sims, err := os.ReadDir(simsDir)
	if err != nil {
		t.Fatalf("read %s: %v", simsDir, err)
	}
	if len(sims) != 3 {
		t.Errorf("got %d sim configs under SCALE=1, want 3", len(sims))
	}

	composeBytes, err := os.ReadFile(filepath.Join(outDir, "docker-compose.generated.yml"))
	if err != nil {
		t.Fatalf("read generated compose: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(composeBytes, &doc); err != nil {
		t.Fatalf("generated compose is not valid YAML: %v", err)
	}
	services, _ := doc["services"].(map[string]any)
	for _, id := range corridorIDs {
		if _, ok := services[id+"-sim"]; !ok {
			t.Errorf("SCALE=1 compose missing corridor sim %q", id+"-sim")
		}
	}
	for _, id := range []string{"cab-002", "cab-003", "cab-102", "cab-103", "cab-202", "cab-203"} {
		if _, ok := services[id+"-sim"]; ok {
			t.Errorf("SCALE=1 compose unexpectedly contains non-corridor sim %q", id+"-sim")
		}
	}

	seedBytes, err := os.ReadFile(filepath.Join(root, "clickhouse", "seed.generated.sql"))
	if err != nil {
		t.Fatalf("read seed.generated.sql: %v", err)
	}
	if n := countInsertRows(t, string(seedBytes), "INSERT INTO vikasa_federation.cabinets"); n != 3 {
		t.Errorf("SCALE=1 vikasa_federation.cabinets insert has %d rows, want 3", n)
	}
}

func TestGenComposeMedium(t *testing.T) {
	outDir := t.TempDir()
	if err := run(fleetPath, outDir, fleetsize.Medium); err != nil {
		t.Fatalf("run(medium): %v", err)
	}
	sims, err := filepath.Glob(filepath.Join(outDir, "sims", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sims) != 18 {
		t.Fatalf("medium generated %d sim configs, want 18", len(sims))
	}
}

// TestTopologySpecsMatchFleet is the brief's "test asserts consistency"
// check for Step 5 (topology specs' DMZ shares reference the corridor
// cabinet): deploy/topology/specs/{mardot,veldot,sabdot}.json are static,
// hand-committed files, so this verifies each one stays in sync with
// fleet.yaml's designated corridor cabinet for that DOT instead of silently
// drifting. Originally mardot-only (veldot/sabdot specs didn't exist until Task
// 14); now covers all 3 DOTs since all 3 specs exist.
func TestTopologySpecsMatchFleet(t *testing.T) {
	fleet, err := loadFleet(fleetPath)
	if err != nil {
		t.Fatalf("loadFleet: %v", err)
	}
	for _, dot := range dots {
		var d *DOT
		for i := range fleet.Dots {
			if fleet.Dots[i].Dot == dot {
				d = &fleet.Dots[i]
			}
		}
		if d == nil {
			t.Fatalf("fleet.yaml has no %s entry", dot)
		}
		var corridor, corridorTag string
		for _, c := range d.Cabinets {
			if c.Corridor != "" {
				corridor = c.ID
				corridorTag = c.Corridor
				break
			}
		}
		if corridor == "" {
			t.Fatalf("fleet.yaml %s has no corridor cabinet", dot)
		}

		specBytes, err := os.ReadFile("../../deploy/topology/specs/" + dot + ".json")
		if err != nil {
			t.Fatalf("read %s.json spec: %v", dot, err)
		}
		spec := string(specBytes)
		wantFrom := "vikasa." + dot + "." + d.District + "." + corridor + ".>"
		wantAs := "vikasa." + dot + ".share." + corridorTag + "." + corridor + ".>"
		if !strings.Contains(spec, wantFrom) {
			t.Errorf("%s.json dmz share \"from\" does not reference fleet corridor cabinet: want substring %q", dot, wantFrom)
		}
		if !strings.Contains(spec, wantAs) {
			t.Errorf("%s.json dmz share \"as\" does not reference fleet corridor cabinet: want substring %q", dot, wantAs)
		}
	}
}

// fleetCabinetIDsForDot returns the cabinet IDs (in fleet.yaml order) for
// the i'th DOT in the real fleet.yaml, so tests can assert against the
// actual SSOT rather than a hand-duplicated fixture.
func fleetCabinetIDsForDot(t *testing.T, i int) []string {
	t.Helper()
	fleet, err := loadFleet(fleetPath)
	if err != nil {
		t.Fatalf("loadFleet: %v", err)
	}
	if i >= len(fleet.Dots) {
		t.Fatalf("dot index %d out of range", i)
	}
	ids := make([]string, len(fleet.Dots[i].Cabinets))
	for j, c := range fleet.Dots[i].Cabinets {
		ids[j] = c.ID
	}
	return ids
}

// serviceNetworks returns the parsed-YAML "networks" list for the named
// service in services (a parsed docker-compose "services" map), handling
// both the flow-sequence form ("networks: [a, b]", []any) and a mapping
// form ("networks: {a: {}}", map[string]any) since compose accepts either.
func serviceNetworks(t *testing.T, services map[string]any, name string) []string {
	t.Helper()
	svc, ok := services[name].(map[string]any)
	if !ok {
		t.Fatalf("service %q not found in generated compose", name)
	}
	raw, ok := svc["networks"]
	if !ok {
		t.Fatalf("service %q has no networks", name)
	}
	var nets []string
	switch v := raw.(type) {
	case []any:
		for _, n := range v {
			nets = append(nets, fmt.Sprintf("%v", n))
		}
	case map[string]any:
		for k := range v {
			nets = append(nets, k)
		}
	default:
		t.Fatalf("service %q networks has unexpected type %T", name, raw)
	}
	return nets
}

// serviceEnv returns the parsed-YAML "environment" for the named service as
// a KEY->VALUE map, handling both the mapping form ("environment: {K: V}",
// map[string]any, what the compose template currently emits) and the list
// form ("environment: [K=V, ...]", []any) since compose accepts either.
func serviceEnv(t *testing.T, services map[string]any, name string) map[string]string {
	t.Helper()
	svc, ok := services[name].(map[string]any)
	if !ok {
		t.Fatalf("service %q not found in generated compose", name)
	}
	raw, ok := svc["environment"]
	if !ok {
		t.Fatalf("service %q has no environment", name)
	}
	env := map[string]string{}
	switch v := raw.(type) {
	case map[string]any:
		for k, val := range v {
			env[k] = fmt.Sprintf("%v", val)
		}
	case []any:
		for _, item := range v {
			s := fmt.Sprintf("%v", item)
			if k, val, ok := strings.Cut(s, "="); ok {
				env[k] = val
			}
		}
	default:
		t.Fatalf("service %q environment has unexpected type %T", name, raw)
	}
	return env
}

// serviceDependsOn returns the named service's "depends_on" keys (service
// names it depends on), handling both the mapping form
// ("depends_on: {svc: {condition: ...}}", what the compose template emits
// for federation-sink/*-sink) and the list form ("depends_on: [svc, ...]",
// what it emits for stream-init-*) since compose accepts either.
func serviceDependsOn(t *testing.T, services map[string]any, name string) []string {
	t.Helper()
	svc, ok := services[name].(map[string]any)
	if !ok {
		t.Fatalf("service %q not found in generated compose", name)
	}
	raw, ok := svc["depends_on"]
	if !ok {
		t.Fatalf("service %q has no depends_on", name)
	}
	var deps []string
	switch v := raw.(type) {
	case map[string]any:
		for k := range v {
			deps = append(deps, k)
		}
	case []any:
		for _, d := range v {
			deps = append(deps, fmt.Sprintf("%v", d))
		}
	default:
		t.Fatalf("service %q depends_on has unexpected type %T", name, raw)
	}
	return deps
}

// serviceRestart returns the named service's "restart" field value ("" if
// unset).
func serviceRestart(t *testing.T, services map[string]any, name string) string {
	t.Helper()
	svc, ok := services[name].(map[string]any)
	if !ok {
		t.Fatalf("service %q not found in generated compose", name)
	}
	raw, ok := svc["restart"]
	if !ok {
		return ""
	}
	return fmt.Sprintf("%v", raw)
}

// containsStr reports whether want is present in list.
func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// countInsertRows counts the value-tuple rows (lines starting with "(")
// belonging to the INSERT statement that starts with prefix, stopping at
// the terminating ";". Fails the test if prefix isn't found.
func countInsertRows(t *testing.T, sql, prefix string) int {
	t.Helper()
	idx := strings.Index(sql, prefix)
	if idx < 0 {
		t.Fatalf("seed SQL has no statement starting with %q", prefix)
	}
	rest := sql[idx:]
	n := 0
	for _, line := range strings.Split(rest, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "(") {
			n++
		}
		if strings.HasSuffix(trimmed, ";") {
			break
		}
	}
	return n
}
