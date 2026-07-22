// internal/sim/asc_test.go
package sim

import (
	"testing"
	"time"

	signalcontrolv1 "github.com/openits/openits-models/pkg/proto/openits/signal_control/v1"
	"google.golang.org/protobuf/proto"
)

type capture struct {
	types []string
	msgs  []proto.Message
}

func (c *capture) Emit(_, ceType string, _ time.Time, m proto.Message) {
	c.types = append(c.types, ceType)
	c.msgs = append(c.msgs, m)
}

func drive(dev Device, em Emitter, from time.Time, dur, step time.Duration) {
	for t := from; t.Before(from.Add(dur)); t = t.Add(step) {
		dev.Tick(t, em)
	}
}

func TestASCCyclesPhases(t *testing.T) {
	asc := NewASC("asc-1", NewDemand(7, 600))
	c := &capture{}
	start := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	drive(asc, c, start, 3*time.Minute, 100*time.Millisecond)
	var greens int
	for i, ct := range c.types {
		if ct != "vikasa.signal-control.phase-state-change.v1" {
			continue
		}
		if psc := c.msgs[i].(*signalcontrolv1.PhaseStateChange); psc.ToState == signalcontrolv1.ToState(signalcontrolv1.ToState_value["TO_STATE_GREEN"]) {
			greens++
		}
	}
	// 90s cycle × 4 phases → 2 full cycles in 3min → ≥ 8 green onsets
	if greens < 8 {
		t.Fatalf("expected >= 8 green onsets, got %d", greens)
	}
}

func TestASCEmitsDetectorTransitionsAtDemandRate(t *testing.T) {
	asc := NewASC("asc-1", NewDemand(7, 1200))
	c := &capture{}
	start := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	drive(asc, c, start, 5*time.Minute, 100*time.Millisecond)
	var ons int
	for _, ct := range c.types {
		if ct == "vikasa.signal-control.detector-transition.v1" {
			ons++
		}
	}
	// 1200 vph × curve(8h)=1.0 ≈ 100 arrivals in 5min, ON+OFF each → ~200; accept wide band
	if ons < 100 || ons > 400 {
		t.Fatalf("detector transitions out of band: %d", ons)
	}
}

func TestASCFaultInjection(t *testing.T) {
	asc := NewASC("asc-1", NewDemand(7, 600))
	c := &capture{}
	now := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	asc.InjectFault("conflict-flash")
	asc.Tick(now, c)
	found := false
	for _, ct := range c.types {
		if ct == "vikasa.signal-control.controller-fault-event.v1" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected controller-fault-event after InjectFault")
	}
}

// TestASCFaultClearDoesNotBackdatePhaseEvents guards against the ASC replaying its
// frozen phase clock after a long flash: a.stateUntil is only advanced by
// advanceState, which never runs while flashing() is true, so without a re-anchor on
// clear a single post-flash Tick would fire a catch-up loop that fabricates a burst
// of PhaseStateChange events backdated across the whole flash window — contradicting
// the OperationalStatusReport stream, which correctly reported Mode="flash"
// throughout.
func TestASCFaultClearDoesNotBackdatePhaseEvents(t *testing.T) {
	asc := NewASC("asc-1", NewDemand(7, 600))
	c := &capture{}
	start := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)

	// Drive a little before the fault so the ring has already started cycling.
	drive(asc, c, start, 30*time.Second, 100*time.Millisecond)

	faultAt := start.Add(30 * time.Second)
	asc.InjectFault("x")

	// Drive across a LONG flash window (10 simulated minutes) — long enough that,
	// pre-fix, the frozen stateUntil would be many phases in the past by the time
	// the fault clears.
	drive(asc, c, faultAt, 10*time.Minute, 100*time.Millisecond)
	clearAt := faultAt.Add(10 * time.Minute)

	asc.ClearFault("x")

	before := len(c.types)
	asc.Tick(clearAt, c)

	var phaseEventsInClearingTick int
	for i := before; i < len(c.types); i++ {
		if c.types[i] != ceTypePhaseStateChange {
			continue
		}
		phaseEventsInClearingTick++
		psc := c.msgs[i].(*signalcontrolv1.PhaseStateChange)
		occurredAt := psc.OccurredAt.AsTime()
		if !occurredAt.Before(faultAt) && occurredAt.Before(clearAt) {
			t.Fatalf("PhaseStateChange backdated into flash window: occurredAt=%v flash=[%v, %v)", occurredAt, faultAt, clearAt)
		}
	}
	if phaseEventsInClearingTick > 1 {
		t.Fatalf("expected at most 1 PhaseStateChange in the clearing Tick, got %d (frozen-clock catch-up loop likely reintroduced)", phaseEventsInClearingTick)
	}
}

// TestASCPedSurgeIncreasesCadence guards against a proven no-op: with the
// additive `a.cycles += mult; if a.cycles%pedCoordCycles == 0` formulation,
// mult=3 against pedCoordCycles=5 lands on exactly the same real-cycle
// boundaries as mult=1 (gcd(3,5)=1), so the ped-surge scenario's
// SetPedCadenceMultiplier(3) silently changed nothing. The fix steps the
// counter one at a time in a loop, checking the threshold on every step, so a
// 3x multiplier fires the threshold ~3x as often over the same span. This
// test drives two fresh ASCs across an identical, long-enough simulated
// window (30 simulated minutes = 20 ring cycles, a multiple of both
// pedCoordCycles=5 and the 3x multiplier, so the expected ratio is exact and
// not an artifact of where the window happens to end) — one at the default
// (unset) multiplier, one with SetPedCadenceMultiplier(3) set from the
// start — and asserts the surge run emits materially more
// PedestrianEvents.
func TestASCPedSurgeIncreasesCadence(t *testing.T) {
	countPedEvents := func(mult int) int {
		asc := NewASC("asc-1", NewDemand(7, 600))
		if mult > 0 {
			asc.SetPedCadenceMultiplier(mult)
		}
		c := &capture{}
		start := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
		// 30 simulated minutes = 20 completed 90s ring cycles.
		drive(asc, c, start, 30*time.Minute, time.Second)
		n := 0
		for _, ct := range c.types {
			if ct == ceTypePedestrianEvent {
				n++
			}
		}
		return n
	}

	n1 := countPedEvents(0) // default: multiplier unset, treated as 1
	n2 := countPedEvents(3) // ped-surge: multiplier 3, set from the start
	t.Logf("pedestrian events over 30 simulated minutes: default(N1)=%d ped-surge(N2)=%d", n1, n2)

	if n1 == 0 {
		t.Fatalf("expected at least one pedestrian event at default cadence over 30 simulated minutes, got 0")
	}
	// Assert >= 2x rather than hardcoding exact counts: this leaves slack
	// around the boundary-alignment arithmetic while still failing hard
	// against the additive-no-op bug (pre-fix, N1 == N2 exactly).
	if n2 < 2*n1 {
		t.Fatalf("expected ped-surge multiplier to materially increase cadence: default=%d surge=%d (want surge >= 2x default)", n1, n2)
	}
}

// countTerminations drives a fresh ASC (seed, baseVPH) across [start, start+dur)
// and tallies YELLOW-onset PhaseStateChange events by termination reason.
func countTerminations(seed int64, baseVPH float64, start time.Time, dur time.Duration) (maxOut, gapOut int) {
	asc := NewASC("asc-1", NewDemand(seed, baseVPH))
	c := &capture{}
	drive(asc, c, start, dur, 100*time.Millisecond)
	yellow := signalcontrolv1.ToState(signalcontrolv1.ToState_value["TO_STATE_YELLOW"])
	maxOutReason := signalcontrolv1.TerminationReason(signalcontrolv1.TerminationReason_value["TERMINATION_REASON_MAX_OUT"])
	gapOutReason := signalcontrolv1.TerminationReason(signalcontrolv1.TerminationReason_value["TERMINATION_REASON_GAP_OUT"])
	for i, ct := range c.types {
		if ct != ceTypePhaseStateChange {
			continue
		}
		psc := c.msgs[i].(*signalcontrolv1.PhaseStateChange)
		if psc.ToState != yellow {
			continue
		}
		switch psc.TerminationReason {
		case maxOutReason:
			maxOut++
		case gapOutReason:
			gapOut++
		}
	}
	return maxOut, gapOut
}

// TestASCTerminationMixIsDemandDriven guards the fix to the previously-dead
// termination-reason branch: the old code set MAX_OUT only when
// greenCount >= greenSec/2 (e.g. >= 15 arrivals on a 30s green), which at
// realistic arrival rates (a couple arrivals per phase per green) never
// happened, so terminations were always GAP_OUT — flattening the
// ATSPM/Signal-Performance MAX_OUT-vs-GAP_OUT dashboards. The fix draws
// MAX_OUT with a probability that scales with demand saturation (see
// (*ASC).maxOutProbability), off the ASC's shared, seeded Demand rng.
//
// This test drives a HIGH-demand ASC (base_vph=1200, well above the
// referenceCapacityVPH=900 corridor reference) and a LOW-demand ASC
// (base_vph=300) over the same 40-simulated-minute span at a peak hour
// (08:00 UTC, hourCurve=1.00), and asserts: the high-demand MAX_OUT fraction
// is materially above zero (busy conditions MAX_OUT a meaningful share of
// the time), the low-demand fraction is lower (demand-driven ordering
// holds), and re-running the high-demand case with the same seed reproduces
// identical counts (deterministic, off the seeded rng — not a fresh/unseeded
// random source).
func TestASCTerminationMixIsDemandDriven(t *testing.T) {
	start := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC) // peak hour: hourCurve[8] = 1.00
	dur := 40 * time.Minute

	const seed = 7
	highMax, highGap := countTerminations(seed, 1200, start, dur)
	lowMax, lowGap := countTerminations(seed, 300, start, dur)

	highTotal := highMax + highGap
	lowTotal := lowMax + lowGap
	if highTotal == 0 || lowTotal == 0 {
		t.Fatalf("expected YELLOW terminations over 40 simulated minutes: high=%d low=%d", highTotal, lowTotal)
	}
	highFrac := float64(highMax) / float64(highTotal)
	lowFrac := float64(lowMax) / float64(lowTotal)
	t.Logf("peak hour, 40 simulated minutes: high(base_vph=1200) MAX_OUT=%d/%d (%.3f); low(base_vph=300) MAX_OUT=%d/%d (%.3f)",
		highMax, highTotal, highFrac, lowMax, lowTotal, lowFrac)

	if highFrac < 0.15 || highFrac > 0.7 {
		t.Fatalf("high-demand MAX_OUT fraction out of expected [0.15, 0.7] band: %.3f", highFrac)
	}
	if lowFrac >= highFrac {
		t.Fatalf("expected low-demand MAX_OUT fraction (%.3f) to be lower than high-demand (%.3f) — mix should be demand-driven", lowFrac, highFrac)
	}

	// Determinism: same seed -> identical counts across two independent runs,
	// confirming the mix is drawn from the ASC's seeded Demand rng and not a
	// fresh/non-deterministic source.
	highMax2, highGap2 := countTerminations(seed, 1200, start, dur)
	if highMax2 != highMax || highGap2 != highGap {
		t.Fatalf("non-deterministic termination mix for same seed: run1 max=%d gap=%d, run2 max=%d gap=%d", highMax, highGap, highMax2, highGap2)
	}
}
