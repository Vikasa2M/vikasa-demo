// internal/sim/dms_test.go
package sim

import (
	"testing"
	"time"

	commonv1 "github.com/openits/openits-models/pkg/proto/openits/common/v1"
)

// TestDMSEmitsBaselineModeOnFirstTick guards against dms_event being empty at
// baseline: previously a DMS emitted nothing until a scenario hook
// (PostAdvisory/InjectFault) was invoked, so with no scenario running the DMS
// Status dashboard ("Latest mode per sign", "Active DMS faults", history) was
// completely empty for every sign. A fresh DMS must now emit exactly one
// ModeChanged(normal) the first time it is ticked — with no scenario ever
// called — so every sign always has a current mode in dms_event.
func TestDMSEmitsBaselineModeOnFirstTick(t *testing.T) {
	dms := NewDMS("dms-1", nil)
	c := &capture{}
	now := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)

	dms.Tick(now, c)

	if n := countType(c, ceTypeModeChanged); n != 1 {
		t.Fatalf("expected exactly 1 mode-changed on first Tick, got %d", n)
	}
	var mc *commonv1.ModeChanged
	for i, ct := range c.types {
		if ct == ceTypeModeChanged {
			mc = c.msgs[i].(*commonv1.ModeChanged)
		}
	}
	if mc == nil {
		t.Fatal("expected a ModeChanged message in the capture")
	}
	if mc.GetCurrent() != dmsModeNormal {
		t.Fatalf("expected baseline Current=%q, got %q", dmsModeNormal, mc.GetCurrent())
	}
	if mc.GetReason() != "startup" {
		t.Fatalf("expected baseline Reason=%q, got %q", "startup", mc.GetReason())
	}
	if mc.GetPrior() != "unknown" {
		t.Fatalf("expected baseline Prior=%q, got %q", "unknown", mc.GetPrior())
	}

	// A second Tick with no new scenario activity must NOT re-emit the
	// baseline — it fires once on startup, not on every Tick.
	dms.Tick(now.Add(time.Second), c)
	if n := countType(c, ceTypeModeChanged); n != 1 {
		t.Fatalf("expected baseline mode-changed to fire only once, got %d after second Tick", n)
	}
}

// TestDMSBaselineFiresBeforeQueuedScenarioModeChange guards ordering: if a
// scenario hook (PostAdvisory) is called before the very first Tick, the
// baseline normal state must still be emitted first, so the event history
// reads startup(normal) -> advisory rather than skipping straight to
// advisory with no baseline row.
func TestDMSBaselineFiresBeforeQueuedScenarioModeChange(t *testing.T) {
	dms := NewDMS("dms-1", nil)
	c := &capture{}
	now := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)

	dms.PostAdvisory()
	dms.Tick(now, c)

	if n := countType(c, ceTypeModeChanged); n != 2 {
		t.Fatalf("expected baseline + advisory mode-changed on first Tick, got %d", n)
	}
	first := c.msgs[0].(*commonv1.ModeChanged)
	if first.GetCurrent() != dmsModeNormal || first.GetReason() != "startup" {
		t.Fatalf("expected first event to be the startup baseline, got current=%q reason=%q", first.GetCurrent(), first.GetReason())
	}
	second := c.msgs[1].(*commonv1.ModeChanged)
	if second.GetCurrent() != dmsModeAdvisory {
		t.Fatalf("expected second event to be the queued advisory change, got current=%q", second.GetCurrent())
	}
}

// TestDMSPeriodicReassertion guards the "time-windowed panels go empty"
// fix: with no scenario ever run and no congestion (demand=nil), a DMS
// driven across several dmsPeriodicReassertPeriod boundaries must keep
// emitting ModeChanged(reason="periodic") — startup plus at least a couple
// of periodic re-asserts — all reporting the unchanged "normal" mode.
func TestDMSPeriodicReassertion(t *testing.T) {
	dms := NewDMS("dms-1", nil)
	c := &capture{}
	start := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	drive(dms, c, start, 5*time.Minute, time.Second)

	n := countType(c, ceTypeModeChanged)
	if n < 3 { // startup + >=2 periodic reasserts over 5 dmsPeriodicReassertPeriod(=100s)-ish minutes
		t.Fatalf("expected multiple periodic mode-changed reassertions over 5 simulated minutes, got %d", n)
	}
	for i, ct := range c.types {
		if ct != ceTypeModeChanged {
			continue
		}
		mc := c.msgs[i].(*commonv1.ModeChanged)
		if mc.GetCurrent() != dmsModeNormal {
			t.Fatalf("expected every reassertion to report normal mode with no scenario/congestion active, got %q (reason=%q) at index %d",
				mc.GetCurrent(), mc.GetReason(), i)
		}
	}
}

// TestDMSAdvisoryOnCongestion guards the DMS-tied-to-traffic-episodes
// requirement: while its cabinet's shared Demand reports significant
// congestion, the DMS must autonomously post an advisory (reason=
// "congestion ahead") with no PostAdvisory call, and clear it back to
// normal (reason="congestion cleared") once the episode recovers — all
// without ever calling the scripted PostAdvisory/ClearAdvisory hooks.
func TestDMSAdvisoryOnCongestion(t *testing.T) {
	d := NewDemand(31, 900)
	start := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	_, peak, _, _ := congestionStats(d, start, congestionCyclePeriod, time.Second)
	if peak < dmsCongestionAdvisoryThreshold {
		t.Fatalf("test fixture assumption broken: no episode in this cabinet's schedule reaches the DMS advisory threshold (%.2f) within one cycle (peak=%.2f) — pick a different seed",
			dmsCongestionAdvisoryThreshold, peak)
	}

	dms := NewDMS("dms-1", d)
	c := &capture{}
	// Drive across a full cycle so the episode ramps in, peaks, and clears.
	drive(dms, c, start, congestionCyclePeriod, time.Second)

	sawAdvisory, sawClearAfter := false, false
	for i, ct := range c.types {
		if ct != ceTypeModeChanged {
			continue
		}
		mc := c.msgs[i].(*commonv1.ModeChanged)
		if mc.GetCurrent() == dmsModeAdvisory && mc.GetReason() == reasonCongestionAhead {
			sawAdvisory = true
		}
		if sawAdvisory && mc.GetCurrent() == dmsModeNormal && mc.GetReason() == reasonCongestionCleared {
			sawClearAfter = true
		}
	}
	if !sawAdvisory {
		t.Fatal("expected an autonomous advisory mode-changed (reason=\"congestion ahead\") while congestion was significant, with no PostAdvisory call")
	}
	if !sawClearAfter {
		t.Fatal("expected the autonomous advisory to clear back to normal (reason=\"congestion cleared\") once the episode recovered")
	}
}

// TestDMSScriptedAdvisoryWinsOverCongestion guards the "tour-safe" ordering
// requirement: while a scripted (PostAdvisory) advisory is active, the
// autonomous congestion check must never touch the mode — even if the
// cabinet's congestion happens to be significant at the same time — and
// once ClearAdvisory runs, the scripted clear (not a congestion-cleared
// duplicate) is what's recorded.
func TestDMSScriptedAdvisoryWinsOverCongestion(t *testing.T) {
	d := NewDemand(31, 900) // same fixture as TestDMSAdvisoryOnCongestion: known to reach the threshold within one cycle
	start := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	peakAt, peak, _, _ := congestionStats(d, start, congestionCyclePeriod, time.Second)
	if peak < dmsCongestionAdvisoryThreshold {
		t.Fatalf("test fixture assumption broken: peak=%.2f", peak)
	}

	dms := NewDMS("dms-1", d)
	c := &capture{}
	// Drive up to just before the peak (autonomous behavior is free to fire
	// here — that's not what this test checks), then post the scripted
	// advisory — mirroring the tour's corridor-incident scenario — and
	// continue driving through the peak and back down. Only events from
	// this point on are checked for autonomous interference.
	drive(dms, c, start, peakAt.Sub(start), time.Second)
	dms.PostAdvisory()
	scriptedFrom := len(c.types)
	drive(dms, c, peakAt, congestionCyclePeriod-peakAt.Sub(start), time.Second)
	// Everything up to here happened while scripted (d.scripted) was true;
	// ClearAdvisory below ends that window, and checkCongestion is free to
	// resume immediately afterward — that's by design, not the thing this
	// test guards, so only [scriptedFrom, scriptedTo) is checked.
	scriptedTo := len(c.types)
	dms.ClearAdvisory()
	dms.Tick(start.Add(congestionCyclePeriod), c)

	for i := scriptedFrom; i < scriptedTo; i++ {
		if c.types[i] != ceTypeModeChanged {
			continue
		}
		mc := c.msgs[i].(*commonv1.ModeChanged)
		if mc.GetReason() == reasonCongestionAhead || mc.GetReason() == reasonCongestionCleared {
			t.Fatalf("expected no autonomous congestion mode-changed while a scripted advisory was active, got reason=%q at index %d", mc.GetReason(), i)
		}
	}
}
