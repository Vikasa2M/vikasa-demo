// internal/sim/devices_test.go
package sim

import (
	"testing"
	"time"

	perceptionv1 "github.com/openits/openits-models/pkg/proto/openits/perception/v1"
	trafficsensorv1 "github.com/openits/openits-models/pkg/proto/openits/traffic_sensor/v1"
)

func countType(c *capture, ceType string) int {
	n := 0
	for _, t := range c.types {
		if t == ceType {
			n++
		}
	}
	return n
}

func TestCameraEmitsIntervalReports(t *testing.T) {
	cam := NewCamera("cam-1", NewDemand(3, 600), 2)
	c := &capture{}
	start := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	drive(cam, c, start, time.Minute, time.Second)
	n := countType(c, "vikasa.traffic-sensor.traffic-interval-report.v1")
	if n < 10 || n > 13 { // ~12 in 60s at 5s cadence
		t.Fatalf("expected ~12 interval reports, got %d", n)
	}
	for i, ct := range c.types {
		if ct == "vikasa.traffic-sensor.traffic-interval-report.v1" {
			r := c.msgs[i].(*trafficsensorv1.TrafficIntervalReport)
			if len(r.GetLane()) != 2 { // real generated getter is GetLane(), not GetLanes()
				t.Fatalf("expected 2 lanes, got %d", len(r.GetLane()))
			}
		}
	}
}

func TestCameraLidarAgreement(t *testing.T) {
	d := NewDemand(3, 1200)
	cam, lid := NewCamera("cam-1", d, 2), NewLidar("lidar-1", d)
	cc, lc := &capture{}, &capture{}
	start := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	drive(cam, cc, start, 10*time.Minute, time.Second)
	drive(lid, lc, start, 10*time.Minute, time.Second)
	camTotal, lidTotal := 0, 0
	for i, ct := range cc.types {
		if ct == "vikasa.traffic-sensor.traffic-interval-report.v1" {
			for _, ln := range cc.msgs[i].(*trafficsensorv1.TrafficIntervalReport).GetLane() {
				camTotal += int(ln.GetVolume())
			}
		}
	}
	for i, ct := range lc.types {
		if ct == "vikasa.perception.zone-interval-report.v1" {
			for _, z := range lc.msgs[i].(*perceptionv1.ZoneIntervalReport).GetZone() {
				for _, cls := range z.GetClassCount() {
					lidTotal += int(cls.GetCount())
				}
			}
		}
	}
	if camTotal == 0 || lidTotal == 0 {
		t.Fatalf("no counts: cam=%d lidar=%d", camTotal, lidTotal)
	}
	ratio := float64(lidTotal) / float64(camTotal)
	if ratio < 0.7 || ratio > 1.3 {
		t.Fatalf("camera/lidar disagreement too large: ratio %f", ratio)
	}
}

func TestGatewayHeartbeatCadence(t *testing.T) {
	gw := NewGateway("gw")
	c := &capture{}
	start := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	drive(gw, c, start, time.Minute, time.Second)
	n := countType(c, "vikasa.gateway.heartbeat.v1")
	if n < 3 || n > 5 { // 15s cadence → 4
		t.Fatalf("expected ~4 heartbeats, got %d", n)
	}
}

// TestCameraIncidentQueueDurationNonNegative guards against the queueStart/at
// ordering hazard in emitQueueState: queueStart is reset to `now` in
// fireIncidentEvents, but fireIntervals can replay interval boundaries (`at`)
// that fell due before that reset — most easily forced by a single large Tick
// gap that leaves several 5s boundaries to catch up on at once. Pre-fix, any
// such boundary earlier than queueStart made `at.Sub(queueStart)` negative,
// and casting that straight to uint32 wrapped to ~4.29e9 seconds.
func TestCameraIncidentQueueDurationNonNegative(t *testing.T) {
	cam := NewCamera("cam-1", NewDemand(3, 600), 2)
	c := &capture{}
	start := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)

	// Establish nextInterval before the incident, with a coarse, irregular
	// cadence (not a clean multiple of the 5s interval period).
	drive(cam, c, start, 17*time.Second, 3*time.Second)

	cam.InjectIncident("incident-1")

	// A single large gap forces fireIntervals into a multi-boundary catch-up
	// loop, replaying several `at` values in one Tick — including some that
	// can land before queueStart once InjectIncident is folded in.
	last := start.Add(17 * time.Second)
	cam.Tick(last.Add(90*time.Second), c)

	// Continue with more irregular gaps to exercise the steady-state path too.
	drive(cam, c, last.Add(90*time.Second), 2*time.Minute, 7*time.Second)

	found := false
	for i, ct := range c.types {
		if ct != ceTypeQueueStateChanged {
			continue
		}
		found = true
		qsc := c.msgs[i].(*trafficsensorv1.QueueStateChanged)
		d := qsc.GetQueueDurationS()
		if d >= 3600 {
			t.Fatalf("QueueDurationS out of sane range (likely uint32 wrap from a negative duration): got %d", d)
		}
	}
	if !found {
		t.Fatal("expected at least one QueueStateChanged after InjectIncident")
	}
}

// TestSharedIncidentRefcount guards against the cross-device incident hazard:
// Camera and Lidar each used to call demand.SetIncident(false) the moment
// their OWN incident set emptied, so if both devices independently detected
// the same incident and only one cleared it, the shared speed degradation
// dropped even though the other device still considered the incident active.
// Demand now refcounts by incident id, so degradation should persist until
// every holder has cleared.
func TestSharedIncidentRefcount(t *testing.T) {
	d := NewDemand(3, 600)
	cam := NewCamera("cam-1", d, 2)
	lid := NewLidar("lidar-1", d)

	cam.InjectIncident("shared-incident")
	lid.InjectIncident("shared-incident")
	if !d.IncidentActive() {
		t.Fatal("expected incident active after both devices injected it")
	}

	cam.ClearIncident("shared-incident")
	if !d.IncidentActive() {
		t.Fatal("incident dropped after only ONE of two holders cleared it — refcount regression")
	}

	lid.ClearIncident("shared-incident")
	if d.IncidentActive() {
		t.Fatal("expected incident inactive once both holders cleared it")
	}
}

// TestCameraQueueBuildsDuringCongestion guards the "camera queue-state
// changes fire" requirement for autonomous congestion, distinct from the
// scenario-injected-incident path already covered by
// TestCameraIncidentQueueDurationNonNegative: with no InjectIncident call at
// all, driving the camera through a scheduled congestion peak must still
// produce at least one QueueStateChanged(Queueing=true).
func TestCameraQueueBuildsDuringCongestion(t *testing.T) {
	d := NewDemand(41, 900)
	start := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	peakAt, peak, _, _ := congestionStats(d, start, congestionCyclePeriod, time.Second)
	if peak < congestionQueueThreshold {
		t.Fatalf("test fixture assumption broken: no episode in this cabinet's schedule reaches the camera queue threshold (%.2f) within one cycle (peak=%.2f) — pick a different seed",
			congestionQueueThreshold, peak)
	}
	_ = peakAt

	cam := NewCamera("cam-1", d, 2)
	c := &capture{}
	drive(cam, c, start, congestionCyclePeriod, time.Second)

	found := false
	for i, ct := range c.types {
		if ct != ceTypeQueueStateChanged {
			continue
		}
		qsc := c.msgs[i].(*trafficsensorv1.QueueStateChanged)
		if qsc.GetQueueing() {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected at least one QueueStateChanged(Queueing=true) during a congestion episode (schedule peak level %.2f), got none, with no incident ever injected", peak)
	}
}

func TestDMSScenario(t *testing.T) {
	dms := NewDMS("dms-1", nil)
	c := &capture{}
	now := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	// The very first Tick always seeds a baseline mode-changed(normal), even
	// with no scenario queued yet — see TestDMSEmitsBaselineModeOnFirstTick.
	dms.Tick(now, c)
	if countType(c, "vikasa.dms.mode-changed.v1") != 1 {
		t.Fatal("expected baseline mode-changed on first Tick")
	}
	dms.PostAdvisory()
	dms.Tick(now.Add(time.Second), c)
	if countType(c, "vikasa.dms.mode-changed.v1") != 2 {
		t.Fatal("expected mode-changed after PostAdvisory")
	}
	dms.InjectFault("pixel-failure")
	dms.Tick(now.Add(2*time.Second), c)
	if countType(c, "vikasa.dms.fault-raised.v1") != 1 {
		t.Fatal("expected fault-raised after InjectFault")
	}
}
