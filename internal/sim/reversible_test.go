// internal/sim/reversible_test.go
package sim

import (
	"testing"
	"time"

	reversiblelanev1 "github.com/openits/openits-models/pkg/proto/openits/reversible_lane/v1"
)

// TestReversibleLaneEmitsOnFirstTick guards against reversible_lane_state being
// empty at demo start: the first Tick must publish the initial state (OPEN,
// NORTHBOUND = AM inbound).
func TestReversibleLaneEmitsOnFirstTick(t *testing.T) {
	rl := NewReversibleLane("rev-1")
	c := &capture{}
	rl.Tick(time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC), c)

	if got := countType(c, ceTypeLaneStateChanged); got != 1 {
		t.Fatalf("expected 1 lane-state-changed on first tick, got %d", got)
	}
	m := c.msgs[0].(*reversiblelanev1.LaneStateChanged)
	if m.GetNewState() != reversiblelanev1.LaneFlowState_LANE_FLOW_STATE_OPEN {
		t.Errorf("initial state = %v, want OPEN", m.GetNewState())
	}
	if m.GetNewDirection() != reversiblelanev1.TravelDirection_TRAVEL_DIRECTION_NORTHBOUND {
		t.Errorf("initial direction = %v, want NORTHBOUND (AM inbound)", m.GetNewDirection())
	}
}

// TestReversibleLaneFlipsDirectionOnSchedule drives the device across several
// scheduled cycles and asserts it passes through the IN_TRANSITION barrier
// phase and flips between NORTHBOUND and SOUTHBOUND.
func TestReversibleLaneFlipsDirectionOnSchedule(t *testing.T) {
	rl := NewReversibleLane("rev-1")
	c := &capture{}
	start := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	// one full cycle = 2*(revOpenDur+revTransitionDur); drive ~10 min for
	// multiple flips regardless of the exact durations.
	drive(rl, c, start, 10*time.Minute, time.Second)

	if countType(c, ceTypeLaneStateChanged) == 0 {
		t.Fatal("expected lane-state-changed events, got 0")
	}

	var sawOpen, sawTransition, sawNB, sawSB bool
	for i, ct := range c.types {
		if ct != ceTypeLaneStateChanged {
			continue
		}
		m := c.msgs[i].(*reversiblelanev1.LaneStateChanged)
		switch m.GetNewState() {
		case reversiblelanev1.LaneFlowState_LANE_FLOW_STATE_OPEN:
			sawOpen = true
		case reversiblelanev1.LaneFlowState_LANE_FLOW_STATE_IN_TRANSITION:
			sawTransition = true
		}
		switch m.GetNewDirection() {
		case reversiblelanev1.TravelDirection_TRAVEL_DIRECTION_NORTHBOUND:
			sawNB = true
		case reversiblelanev1.TravelDirection_TRAVEL_DIRECTION_SOUTHBOUND:
			sawSB = true
		}
	}
	if !sawOpen || !sawTransition {
		t.Errorf("expected both OPEN and IN_TRANSITION; open=%v transition=%v", sawOpen, sawTransition)
	}
	if !sawNB || !sawSB {
		t.Errorf("expected a flip between NB and SB; nb=%v sb=%v", sawNB, sawSB)
	}
}
