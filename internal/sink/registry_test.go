package sink

import (
	"testing"
	"time"

	"github.com/Vikasa2M/vikasa-demo/internal/events"
	signalcontrolv1 "github.com/openits/openits-models/pkg/proto/openits/signal_control/v1"
)

func TestRowsPhaseStateChange(t *testing.T) {
	at := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	env, err := events.New("mardot", "d1", "cab-i85-001", "asc-1",
		"vikasa.signal-control.phase-state-change.v1", at,
		&signalcontrolv1.PhaseStateChange{SourceDeviceId: "asc-1", PhaseNumber: 2})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := Rows(env)
	if err != nil {
		t.Fatal(err)
	}
	// 1 typed row + 1 events_raw row
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	if rows[0].Table != "phase_state_change" || rows[1].Table != "events_raw" {
		t.Fatalf("bad tables: %s %s", rows[0].Table, rows[1].Table)
	}
	h := Registry["vikasa.signal-control.phase-state-change.v1"]
	if len(rows[0].Values) != len(h.Columns) {
		t.Fatalf("row width %d != columns %d", len(rows[0].Values), len(h.Columns))
	}
}

func TestRowsShareSubjectFallback(t *testing.T) {
	at := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	env, err := events.New("mardot", "d1", "cab-i85-001", "asc-1",
		"vikasa.signal-control.phase-state-change.v1", at,
		&signalcontrolv1.PhaseStateChange{SourceDeviceId: "asc-1", PhaseNumber: 2})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the DMZ subject transform: federation-sink's messages arrive
	// on the 8-token "share" form, not the 7-token internal one ParseSubject
	// expects. Rows must fall back to ParseShareSubject instead of erroring.
	env.Subject = "vikasa.mardot.share.i85.cab-i85-001.signal-control.asc-1.phase-state-change"

	rows, err := Rows(env)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	raw := rows[1]
	if raw.Table != "events_raw" {
		t.Fatalf("want events_raw, got %s", raw.Table)
	}
	// eventsRawColumns: ce_id, ce_time, dot, district, cabinet_id, device_id, service, event.
	if got := raw.Values[3]; got != "share-i85" {
		t.Fatalf("events_raw.district = %v, want share-i85", got)
	}
	if got := raw.Values[4]; got != "cab-i85-001" {
		t.Fatalf("events_raw.cabinet_id = %v, want cab-i85-001", got)
	}
}

func TestRowsUnknownTypeErrors(t *testing.T) {
	env := &events.Envelope{Type: "vikasa.bogus.thing.v1",
		Subject: "vikasa.mardot.d1.cab-002.bogus.x-1.thing", ID: "x", Time: time.Now()}
	if _, err := Rows(env); err == nil {
		t.Fatal("unknown ce-type must error (dead-letter path)")
	}
}

func TestEveryEmittedTypeHasHandler(t *testing.T) {
	// The 17 ce-types the sim emits (reference card). Registry must cover all.
	emitted := []string{
		"vikasa.signal-control.phase-state-change.v1",
		"vikasa.signal-control.detector-transition.v1",
		"vikasa.signal-control.pedestrian-event.v1",
		"vikasa.signal-control.coordination-change.v1",
		"vikasa.signal-control.preemption-event.v1",
		"vikasa.signal-control.controller-fault-event.v1",
		"vikasa.signal-control.operational-status-report.v1",
		"vikasa.traffic-sensor.traffic-interval-report.v1",
		"vikasa.traffic-sensor.traffic-sensor-status-report.v1",
		"vikasa.traffic-sensor.queue-state-changed.v1",
		"vikasa.perception.zone-interval-report.v1",
		"vikasa.perception.zone-incident-detected.v1",
		"vikasa.perception.zone-incident-cleared.v1",
		"vikasa.dms.mode-changed.v1",
		"vikasa.dms.fault-raised.v1",
		"vikasa.dms.fault-cleared.v1",
		"vikasa.reversible-lane.lane-state-changed.v1",
		"vikasa.gateway.heartbeat.v1",
	}
	for _, ct := range emitted {
		if _, ok := Registry[ct]; !ok {
			t.Errorf("no handler for %s", ct)
		}
	}
}
