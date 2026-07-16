// internal/sim/reversible.go
package sim

import (
	"sync"
	"time"

	reversiblelanev1 "github.com/openits/openits-models/pkg/proto/openits/reversible_lane/v1"
)

// ceTypeLaneStateChanged is the CloudEvents type for a reversible-lane
// direction/flow-state change (there is no canonical constant in
// openits-models; this follows the vikasa.<service>.<event>.v1 convention).
const ceTypeLaneStateChanged = "vikasa.reversible-lane.lane-state-changed.v1"

// Reversible-lane schedule. A real reversible express-lane segment (the I-75
// South Metro Express Lanes) reverses twice a day — open NORTHBOUND for the
// AM inbound peak, SOUTHBOUND for the PM outbound peak. That is compressed
// here so a full reversal is visible on camera during a minutes-long take:
// each direction stays open for revOpenDur, separated by a revTransitionDur
// IN_TRANSITION barrier sweep. A keepalive re-report every revKeepalive keeps
// the dashboard's current-direction reading fresh between flips.
const (
	revOpenDur       = 120 * time.Second
	revTransitionDur = 20 * time.Second
	revKeepalive     = 20 * time.Second
)

// ReversibleLane simulates a scheduled reversible express-lane segment. Only
// the designated I-75 South cabinet runs one (Config.Reversible). It emits a
// LaneStateChanged on every scheduled phase change (open->transition->open,
// flipping direction) plus periodic keepalives, all with InitiatedBy marking
// the schedule reason so the "why" is visible downstream.
type ReversibleLane struct {
	controller string // device id, e.g. "rev-1"

	mu         sync.Mutex
	started    bool
	phaseStart time.Time
	lastEmit   time.Time
	state      reversiblelanev1.LaneFlowState
	direction  reversiblelanev1.TravelDirection
	seq        uint64
}

// NewReversibleLane builds a reversible-lane device with the given device id.
func NewReversibleLane(controller string) *ReversibleLane {
	return &ReversibleLane{controller: controller}
}

// Tick advances the scheduled reversal cycle and emits LaneStateChanged on any
// phase change, plus a keepalive every revKeepalive while steady.
func (r *ReversibleLane) Tick(now time.Time, em Emitter) {
	r.mu.Lock()
	defer r.mu.Unlock()

	const (
		open       = reversiblelanev1.LaneFlowState_LANE_FLOW_STATE_OPEN
		transition = reversiblelanev1.LaneFlowState_LANE_FLOW_STATE_IN_TRANSITION
		nb         = reversiblelanev1.TravelDirection_TRAVEL_DIRECTION_NORTHBOUND
		sb         = reversiblelanev1.TravelDirection_TRAVEL_DIRECTION_SOUTHBOUND
	)

	if !r.started {
		r.started = true
		r.phaseStart = now
		r.state, r.direction = open, nb
		r.emit(now, open, nb, r.reason(open, nb), em)
		return
	}

	prevState, prevDir := r.state, r.direction
	switch r.state {
	case open:
		if now.Sub(r.phaseStart) >= revOpenDur {
			r.state = transition // close for the barrier sweep; direction holds until it reopens
			r.phaseStart = now
		}
	case transition:
		if now.Sub(r.phaseStart) >= revTransitionDur {
			r.state = open
			if r.direction == nb { // reopen the opposite direction
				r.direction = sb
			} else {
				r.direction = nb
			}
			r.phaseStart = now
		}
	}

	if r.state != prevState || r.direction != prevDir {
		r.emit(now, prevState, prevDir, r.reason(r.state, r.direction), em)
		return
	}
	if now.Sub(r.lastEmit) >= revKeepalive {
		r.emit(now, prevState, prevDir, "schedule-keepalive", em)
	}
}

// reason maps the new (state, direction) to a human-readable InitiatedBy tag.
func (r *ReversibleLane) reason(state reversiblelanev1.LaneFlowState, dir reversiblelanev1.TravelDirection) string {
	switch {
	case state == reversiblelanev1.LaneFlowState_LANE_FLOW_STATE_IN_TRANSITION:
		return "schedule-transition"
	case dir == reversiblelanev1.TravelDirection_TRAVEL_DIRECTION_NORTHBOUND:
		return "schedule-am-inbound"
	default:
		return "schedule-pm-outbound"
	}
}

// emit publishes a LaneStateChanged carrying the prior (state, direction) and
// the device's current (state, direction) as the new values.
func (r *ReversibleLane) emit(now time.Time, prevState reversiblelanev1.LaneFlowState, prevDir reversiblelanev1.TravelDirection, initiatedBy string, em Emitter) {
	r.seq++
	em.Emit(r.controller, ceTypeLaneStateChanged, now, &reversiblelanev1.LaneStateChanged{
		Kind:              "lane-state-changed",
		PreviousState:     prevState,
		PreviousDirection: prevDir,
		NewState:          r.state,
		NewDirection:      r.direction,
		InitiatedBy:       initiatedBy,
		SourceDeviceId:    r.controller,
		Sequence:          r.seq,
	})
	r.lastEmit = now
}
