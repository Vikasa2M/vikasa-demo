package tour

import (
	"bufio"
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// fleetFixturePath is the real fleet.yaml, reachable from this package's
// test working directory (go test runs with cwd = the package dir).
const fleetFixturePath = "../../deploy/topology/fleet.yaml"

func testConfig() Config {
	return Config{
		CH:          "http://localhost:8123",
		Grafana:     "http://localhost:3000",
		ComposeFile: "deploy/compose/docker-compose.yml",
		FleetPath:   fleetFixturePath,
		DOT:         "mardot",
	}
}

func TestBuildPhasesNamesUniqueAndAssertsPresent(t *testing.T) {
	phases, err := BuildPhases(testConfig())
	if err != nil {
		t.Fatalf("BuildPhases: %v", err)
	}
	if len(phases) != 6 {
		t.Fatalf("got %d phases, want 6", len(phases))
	}

	seen := map[string]bool{}
	for _, p := range phases {
		if p.Name == "" {
			t.Error("phase has empty Name")
		}
		if seen[p.Name] {
			t.Errorf("duplicate phase name %q", p.Name)
		}
		seen[p.Name] = true

		if strings.TrimSpace(p.Narration) == "" {
			t.Errorf("phase %q has empty Narration", p.Name)
		}
		if strings.TrimSpace(p.WatchFor) == "" {
			t.Errorf("phase %q has empty WatchFor", p.Name)
		}
		if len(p.Dashboards) == 0 {
			t.Errorf("phase %q has no Dashboards", p.Name)
		}
		for _, d := range p.Dashboards {
			if d.Title == "" || d.URL == "" {
				t.Errorf("phase %q has a Dashboard with an empty Title/URL: %+v", p.Name, d)
			}
		}
		// Every phase must define an assertion — verify mode has nothing
		// to check otherwise (paced mode never calls it, but the table
		// itself must always carry one; see tour.go's Phase.Assert doc).
		if p.Assert == nil {
			t.Errorf("phase %q has a nil Assert", p.Name)
		}
		if p.Settle < 0 {
			t.Errorf("phase %q has a negative Settle: %s", p.Name, p.Settle)
		}
	}

	// Phase 1 (baseline) leads the tour.
	if phases[0].Name != "baseline" {
		t.Errorf("phases[0].Name = %q, want %q", phases[0].Name, "baseline")
	}
	// Observe-only phases showcase steady-state / autonomous behavior and
	// carry no Action; every other phase perturbs the stack and must have one.
	observeOnly := map[string]bool{"baseline": true, "reversible": true}
	for _, p := range phases {
		if observeOnly[p.Name] {
			if p.Action != nil {
				t.Errorf("observe-only phase %q should have a nil Action", p.Name)
			}
		} else if p.Action == nil {
			t.Errorf("phase %q has a nil Action", p.Name)
		}
	}
}

func TestBuildPhasesOrder(t *testing.T) {
	phases, err := BuildPhases(testConfig())
	if err != nil {
		t.Fatalf("BuildPhases: %v", err)
	}
	want := []string{"baseline", "wan-cut", "restore", "fault", "corridor", "reversible"}
	if len(phases) != len(want) {
		t.Fatalf("got %d phases, want %d", len(phases), len(want))
	}
	for i, name := range want {
		if phases[i].Name != name {
			t.Errorf("phases[%d].Name = %q, want %q", i, phases[i].Name, name)
		}
	}
}

func TestBuildPhasesMissingFleetFileErrors(t *testing.T) {
	cfg := testConfig()
	cfg.FleetPath = "does/not/exist.yaml"
	if _, err := BuildPhases(cfg); err == nil {
		t.Error("want error for a missing fleet file, got nil")
	}
}

func TestFilterPhasesPreservesDeclaredOrder(t *testing.T) {
	phases, err := BuildPhases(testConfig())
	if err != nil {
		t.Fatalf("BuildPhases: %v", err)
	}
	// Ask for restore before wan-cut; the result should still come back in
	// the table's declared order (wan-cut, then restore).
	got, err := FilterPhases(phases, []string{"restore", "wan-cut"})
	if err != nil {
		t.Fatalf("FilterPhases: %v", err)
	}
	if len(got) != 2 || got[0].Name != "wan-cut" || got[1].Name != "restore" {
		names := make([]string, len(got))
		for i, p := range got {
			names[i] = p.Name
		}
		t.Errorf("got %v, want [wan-cut restore]", names)
	}
}

func TestFilterPhasesUnknownNameErrors(t *testing.T) {
	phases, err := BuildPhases(testConfig())
	if err != nil {
		t.Fatalf("BuildPhases: %v", err)
	}
	if _, err := FilterPhases(phases, []string{"not-a-real-phase"}); err == nil {
		t.Error("want error for an unknown phase name, got nil")
	}
}

func TestFilterPhasesEmptyReturnsAll(t *testing.T) {
	phases, err := BuildPhases(testConfig())
	if err != nil {
		t.Fatalf("BuildPhases: %v", err)
	}
	got, err := FilterPhases(phases, nil)
	if err != nil {
		t.Fatalf("FilterPhases: %v", err)
	}
	if len(got) != len(phases) {
		t.Errorf("got %d phases, want all %d", len(got), len(phases))
	}
}

// TestRunPacedGatesOnEnter drives a 2-phase table through paced mode with a
// scripted stdin (two newlines, then EOF) and confirms: (1) it prints both
// phases' narration blocks and dashboard URLs before stopping, (2) it never
// calls Assert (paced mode has no PASS/FAIL notion), and (3) once stdin is
// exhausted it stops the tour outright rather than treating EOF as an
// implicit Enter press — a real, caught bug: the first implementation
// looped through every remaining phase's live action (cut/restore/inject)
// back-to-back the instant stdin closed, which is exactly backwards for a
// presenter tool.
func TestRunPacedGatesOnEnterAndNeverAsserts(t *testing.T) {
	var actionCalls, assertCalls int
	phases := []Phase{
		{
			Name:       "one",
			Narration:  "narration for phase one",
			Dashboards: []DashboardRef{{Title: "Dash One", URL: "http://localhost:3000/d/one"}},
			WatchFor:   "watch for one",
			Action:     func(context.Context) error { actionCalls++; return nil },
			Assert:     func(context.Context) error { assertCalls++; return nil },
		},
		{
			Name:       "two",
			Narration:  "narration for phase two",
			Dashboards: []DashboardRef{{Title: "Dash Two", URL: "http://localhost:3000/d/two"}},
			WatchFor:   "watch for two",
			Action:     func(context.Context) error { actionCalls++; return nil },
			Assert:     func(context.Context) error { assertCalls++; return nil },
		},
	}

	// Two Enter presses gate phase one all the way through (narration ->
	// Enter -> action -> watch-for -> Enter); stdin then hits EOF right as
	// phase two asks to be triggered, which must stop the run before
	// phase two's action ever fires.
	in := bytes.NewBufferString("\n\n")
	var out bytes.Buffer

	err := Run(context.Background(), phases, RunOptions{Paced: true, In: in, Out: &out})
	if err != nil {
		t.Fatalf("Run: unexpected error: %v", err)
	}

	if actionCalls != 1 {
		t.Errorf("actionCalls = %d, want 1 (only phase one's action should have fired; phase two's Enter-gate hit EOF first)", actionCalls)
	}
	if assertCalls != 0 {
		t.Errorf("assertCalls = %d, want 0 (paced mode must never run assertions)", assertCalls)
	}

	printed := out.String()
	for _, want := range []string{
		"Phase 1/2: one", "narration for phase one", "Dash One", "http://localhost:3000/d/one", "watch for one",
		"Phase 2/2: two", "narration for phase two", "Dash Two", "http://localhost:3000/d/two",
		"stdin closed before this phase was triggered — stopping the tour",
	} {
		if !strings.Contains(printed, want) {
			t.Errorf("paced output missing %q; full output:\n%s", want, printed)
		}
	}
	if strings.Contains(printed, "watch for two") {
		t.Errorf("phase two's action must not have run, so its watch-for text should never print; full output:\n%s", printed)
	}
	if strings.Contains(printed, "PASS") || strings.Contains(printed, "FAIL") {
		t.Errorf("paced output should never print PASS/FAIL; full output:\n%s", printed)
	}
}

func TestRunVerifyModeReportsPassAndFail(t *testing.T) {
	phases := []Phase{
		{Name: "ok", Assert: func(context.Context) error { return nil }},
		{Name: "broken", Assert: func(context.Context) error { return errFake }},
	}
	var out bytes.Buffer
	err := Run(context.Background(), phases, RunOptions{Out: &out})
	if err == nil {
		t.Fatal("Run: want error when a phase fails, got nil")
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Errorf("error %q does not name the failing phase", err.Error())
	}
	printed := out.String()
	if !strings.Contains(printed, "[PASS] ok") {
		t.Errorf("output missing PASS line for ok phase:\n%s", printed)
	}
	if !strings.Contains(printed, "[FAIL] broken") {
		t.Errorf("output missing FAIL line for broken phase:\n%s", printed)
	}
}

func TestRunVerifyModeSettleOverride(t *testing.T) {
	var slept time.Duration
	phases := []Phase{{
		Name:   "slow",
		Settle: 10 * time.Minute, // would time out the test if not overridden
		Assert: func(context.Context) error { return nil },
	}}
	start := time.Now()
	if err := Run(context.Background(), phases, RunOptions{Settle: 10 * time.Millisecond}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	slept = time.Since(start)
	if slept > time.Second {
		t.Errorf("Run took %s; --settle override does not appear to have applied", slept)
	}
}

var errFake = &fakeError{"fake assertion failure"}

type fakeError struct{ msg string }

func (e *fakeError) Error() string { return e.msg }

// sanity: waitForEnter is exercised indirectly above via bufio, but confirm
// directly too that it doesn't block once the reader is exhausted, and that
// exhaustion is reported as stop=true (never silently treated as an Enter
// press — see runPaced's doc comment on this exact bug).
func TestWaitForEnterEOFDoesNotBlock(t *testing.T) {
	r := bufio.NewReader(strings.NewReader(""))
	var out bytes.Buffer
	type result struct {
		stop bool
		err  error
	}
	done := make(chan result, 1)
	go func() {
		stop, err := waitForEnter(r, &out, "prompt")
		done <- result{stop, err}
	}()
	select {
	case res := <-done:
		if res.err != nil {
			t.Errorf("waitForEnter: unexpected error: %v", res.err)
		}
		if !res.stop {
			t.Error("waitForEnter: want stop=true on EOF, got false")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitForEnter blocked on an exhausted reader instead of returning")
	}
}
