// internal/sim/traffic_test.go
package sim

import (
	"math"
	"testing"
	"time"
)

func TestDemandDeterministic(t *testing.T) {
	a, b := NewDemand(42, 600), NewDemand(42, 600)
	at := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	for i := 0; i < 100; i++ {
		if a.NextHeadway(at) != b.NextHeadway(at) {
			t.Fatal("same seed must give same headways")
		}
	}
}

func TestPeakExceedsOvernight(t *testing.T) {
	// The demand floor is intentionally lifted (see hourCurve) so the demo
	// stays busy at any hour, but AM/PM peaks must still read as visibly
	// busier than the overnight trough: assert a real gap (>=1.4x) without
	// demanding the old near-zero-overnight ratio (>=3x), which the lifted
	// floor no longer produces by design.
	d := NewDemand(1, 600)
	peak := d.VolumePerHour(time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC))  // 08:00
	night := d.VolumePerHour(time.Date(2026, 7, 14, 3, 0, 0, 0, time.UTC)) // 03:00
	if peak <= night {
		t.Fatalf("peak %f should exceed overnight %f", peak, night)
	}
	if peak < 1.4*night {
		t.Fatalf("peak %f should be >= 1.4x overnight %f", peak, night)
	}
}

func TestFundamentalDiagramConsistency(t *testing.T) {
	d := NewDemand(1, 600)
	at := time.Date(2026, 7, 14, 17, 30, 0, 0, time.UTC)
	q, k, v := d.VolumePerHour(at), d.DensityPerKm(at), d.SpeedKmh(at)
	if math.Abs(q-k*v) > 1e-6 {
		t.Fatalf("q=k*v violated: q=%f k=%f v=%f", q, k, v)
	}
}

func TestIncidentSlowsTraffic(t *testing.T) {
	d := NewDemand(1, 600)
	at := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	before := d.SpeedKmh(at)
	d.SetIncident(true)
	if d.SpeedKmh(at) >= before {
		t.Fatal("incident must reduce speed")
	}
}

// congestionStats scans [start, start+window) at step, returning the
// timestamp/level of the highest CongestionLevel seen (peakAt/peak) and the
// first timestamp where it was exactly zero (zeroAt/foundZero). Shared by
// traffic_test.go, dms_test.go, and devices_test.go so each can locate a
// concrete "definitely congested" / "definitely clear" instant without
// hardcoding episode timing, which depends on the seeded schedule.
func congestionStats(d *Demand, start time.Time, window, step time.Duration) (peakAt time.Time, peak float64, zeroAt time.Time, foundZero bool) {
	for tt := start; tt.Before(start.Add(window)); tt = tt.Add(step) {
		lvl := d.CongestionLevel(tt)
		if lvl > peak {
			peak = lvl
			peakAt = tt
		}
		if lvl == 0 && !foundZero {
			zeroAt = tt
			foundZero = true
		}
	}
	return
}

// TestCongestionEpisodesStartAndEnd guards the autonomous episode scheduler
// being bounded and occasional rather than always-on or never-firing: over
// one full congestionCyclePeriod, the schedule must reach a nonzero peak
// (at least one episode fires) AND spend some time back at exactly zero
// (episodes end/recover, they don't run permanently).
func TestCongestionEpisodesStartAndEnd(t *testing.T) {
	d := NewDemand(5, 900)
	start := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	_, peak, _, foundZero := congestionStats(d, start, congestionCyclePeriod, time.Second)
	if peak <= 0 {
		t.Fatal("expected at least one congestion episode with level > 0 within one full cycle")
	}
	if !foundZero {
		t.Fatal("expected congestion to be exactly zero (recovered) at some point within the cycle — episodes must be occasional, not constant")
	}
}

// TestCongestionReducesSpeedAndRaisesDensity guards the core congestion
// mechanism: at the schedule's peak instant, speed must be materially lower,
// and density (q/v — see DensityPerKm) materially higher, than at a nearby
// instant where the schedule is exactly clear.
func TestCongestionReducesSpeedAndRaisesDensity(t *testing.T) {
	d := NewDemand(5, 900)
	start := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	peakAt, peak, zeroAt, foundZero := congestionStats(d, start, congestionCyclePeriod, time.Second)
	if peak <= 0 || !foundZero {
		t.Fatalf("test fixture needs both a nonzero peak and a zero instant within one cycle to compare — got peak=%.3f foundZero=%v", peak, foundZero)
	}

	speedAtPeak := d.SpeedKmh(peakAt)
	speedAtZero := d.SpeedKmh(zeroAt)
	if speedAtPeak >= speedAtZero {
		t.Fatalf("expected congestion to reduce speed: at peak (level=%.2f) speed=%.1f, at clear speed=%.1f", peak, speedAtPeak, speedAtZero)
	}

	densAtPeak := d.DensityPerKm(peakAt)
	densAtZero := d.DensityPerKm(zeroAt)
	if densAtPeak <= densAtZero {
		t.Fatalf("expected congestion to raise density: at peak density=%.2f, at clear density=%.2f", densAtPeak, densAtZero)
	}
}

// TestCongestionBounded guards against an unbounded/runaway congestion
// level: scanning densely across a full cycle, the level must never reach
// or exceed 1 (SpeedKmh would go non-positive) and must never leave speed
// below a sane floor.
func TestCongestionBounded(t *testing.T) {
	d := NewDemand(6, 900)
	start := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	for tt := start; tt.Before(start.Add(congestionCyclePeriod)); tt = tt.Add(5 * time.Second) {
		lvl := d.CongestionLevel(tt)
		if lvl < 0 || lvl >= 1 {
			t.Fatalf("congestion level out of bounds at %v: %.3f (want [0,1))", tt, lvl)
		}
		if v := d.SpeedKmh(tt); v < 5 {
			t.Fatalf("speed dropped below sane floor at %v: %.2f km/h (level=%.3f)", tt, v, lvl)
		}
	}
}

// TestCongestionDeterministic guards reproducibility: two Demands built from
// the same seed must report identical CongestionLevel at every sampled
// instant.
func TestCongestionDeterministic(t *testing.T) {
	a, b := NewDemand(9, 700), NewDemand(9, 700)
	start := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	for i := 0; i < 300; i++ {
		tt := start.Add(time.Duration(i) * 7 * time.Second)
		if a.CongestionLevel(tt) != b.CongestionLevel(tt) {
			t.Fatalf("congestion level mismatch at %v for identical seeds", tt)
		}
	}
}

// TestCongestionSpeedCutMilderThanIncident guards the "tour must still stand
// out" requirement: even at its absolute maximum (peak=congestionPeakMax),
// autonomous congestion's speed cut must stay clearly milder than the
// scripted incident's flat 0.4x — otherwise an unlucky autonomous episode
// could out-dramatize the injected corridor-incident.
func TestCongestionSpeedCutMilderThanIncident(t *testing.T) {
	maxCut := congestionPeakMax * congestionSpeedCoeff
	const incidentCut = 0.6 // incident leaves 0.4x, i.e. cuts 60%
	if maxCut >= incidentCut {
		t.Fatalf("autonomous congestion's max possible cut (%.3f) must stay below the scripted incident's cut (%.3f) so the incident always reads as more dramatic", maxCut, incidentCut)
	}
}

// TestCorridorCongestsMoreOftenThanSideStreet guards the "corridor cabinets
// (higher base_vph) should congest a bit more often" requirement at the
// schedule-generation level (episode count), which is a deterministic
// function of baseVPH — not the realized congested-seconds total, which
// also depends on per-episode random hold durations and would make this
// comparison seed-sensitive.
func TestCorridorCongestsMoreOftenThanSideStreet(t *testing.T) {
	corridor := NewDemand(11, 900) // cab-i85-001-like
	side := NewDemand(12, 400)     // quiet side-street-like
	if len(corridor.episodes) <= len(side.episodes) {
		t.Fatalf("expected corridor (base_vph=900) to schedule more episodes per cycle than a side street (base_vph=400): corridor=%d side=%d",
			len(corridor.episodes), len(side.episodes))
	}
}
