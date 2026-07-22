// internal/sim/asc.go
package sim

import (
	"sync"
	"time"

	signalcontrolv1 "github.com/openits/openits-models/pkg/proto/openits/signal_control/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ce-types published by the ASC device, following the vikasa.<service>.<event>.v<n>
// convention enforced by internal/events.Subject/ServiceEvent.
const (
	ceTypePhaseStateChange        = "vikasa.signal-control.phase-state-change.v1"
	ceTypeDetectorTransition      = "vikasa.signal-control.detector-transition.v1"
	ceTypeOperationalStatusReport = "vikasa.signal-control.operational-status-report.v1"
	ceTypeControllerFaultEvent    = "vikasa.signal-control.controller-fault-event.v1"
	ceTypePedestrianEvent         = "vikasa.signal-control.pedestrian-event.v1"
	ceTypeCoordinationChange      = "vikasa.signal-control.coordination-change.v1"
)

type phaseDef struct {
	number   uint32
	greenSec int
	approach string
	channel  uint32
}

// Fixed 4-phase ring [2, 4, 6, 8]; splits sum with yellow+all-red to a 90s cycle
// (30+10+20+10 green = 70s, + 4*(3+2) = 20s of yellow/all-red = 90s).
var plan = []phaseDef{
	{2, 30, "nb", 1}, {4, 10, "eb", 2}, {6, 20, "sb", 3}, {8, 10, "wb", 4},
}

const (
	yellowSec = 3
	allRedSec = 2

	presenceDur    = 1500 * time.Millisecond
	statusPeriod   = 60 * time.Second
	pedCoordCycles = 5 // emit pedestrian + coordination events every N completed cycles

	// Demand-driven MAX_OUT probability model (see maxOutProbability): a
	// termination is drawn MAX_OUT with probability p, rather than the old
	// unreachable greenCount>=greenSec/2 threshold (at realistic arrival
	// rates — a couple arrivals per phase per green — greenCount never got
	// anywhere near greenSec/2, so terminations were always GAP_OUT).
	//
	// referenceCapacityVPH is picked so a corridor cabinet's base_vph (~900)
	// at a peak hour (curve ~1.0) saturates to ~1.0, while a side-street
	// cabinet (~400-600) or any cabinet off-peak sits well below 1.0.
	referenceCapacityVPH = 900.0
	maxOutBaseP          = 0.05 // p at zero demand (floor of the sat-driven term)
	maxOutSatWeight      = 0.45 // weight of demand saturation (VolumePerHour/referenceCapacityVPH, clamped to [0,1])
	maxOutArrivalWeight  = 0.10 // weight of observed-vs-expected arrivals during the just-ended green
	maxOutProbFloor      = 0.03
	maxOutProbCeil       = 0.6
)

type faultEvent struct {
	id     string
	raised bool
}

// ASC simulates a NEMA-style actuated signal controller: a fixed 4-phase ring
// driven purely by Tick(now, em) — no goroutines or timers. It shares a *Demand
// with the other devices in the cabinet so vehicle counts stay consistent.
type ASC struct {
	mu         sync.Mutex
	controller string
	demand     *Demand
	seq        uint64

	started     bool
	idx         int       // index into plan for the currently active phase
	state       string    // "GREEN" | "YELLOW" | "RED"
	stateUntil  time.Time // when the current state ends
	greenCount  int       // arrivals on the active green phase, for termination reason
	cycles      int       // completed cycles (idx wrapped back to 0)
	wasFlashing bool      // flashing() as of the previous Tick, to detect the clear edge

	// pedCadenceMult scales how fast `cycles` advances toward the
	// pedCoordCycles threshold: 1 (or unset, since the zero value is treated
	// as 1) is the normal rate; the ped-surge scenario hook
	// (SetPedCadenceMultiplier) raises it to emit pedestrian/coordination
	// events proportionally more often, without touching phase cycling.
	pedCadenceMult int

	nextArrival time.Time            // next shared vehicle arrival across all approaches
	arrivalIdx  int                  // round-robins arrivals across approaches
	pendingOff  map[uint32]time.Time // channel -> detector OFF due time

	nextStatus time.Time

	faults        map[string]bool
	pendingFaults []faultEvent
}

// NewASC creates an ASC device for controller, sharing d with the rest of the cabinet.
func NewASC(controller string, d *Demand) *ASC {
	return &ASC{
		controller: controller,
		demand:     d,
		pendingOff: map[uint32]time.Time{},
		faults:     map[string]bool{},
	}
}

// InjectFault raises a controller fault: a ControllerFaultEvent is queued for the
// next Tick and phase cycling switches to flash (heartbeats and detector arrivals
// keep running).
func (a *ASC) InjectFault(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.faults[id] = true
	a.pendingFaults = append(a.pendingFaults, faultEvent{id: id, raised: true})
}

// ClearFault reverses InjectFault: queues a cleared ControllerFaultEvent and, once
// no faults remain, resumes phase cycling on the next Tick.
func (a *ASC) ClearFault(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.faults, id)
	a.pendingFaults = append(a.pendingFaults, faultEvent{id: id, raised: false})
}

// SetPedCadenceMultiplier scales how often PedestrianEvent/CoordinationChange
// fire (normally once every pedCoordCycles completed ring cycles): a
// multiplier of m makes `cycles` advance m per completed ring cycle instead
// of 1, so events recur roughly m times as often. m <= 0 is treated as 1
// (the default rate). Used by the ped-surge scenario hook to temporarily
// raise pedestrian event cadence.
func (a *ASC) SetPedCadenceMultiplier(m int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pedCadenceMult = m
}

// Tick advances the ASC to now, emitting any events that came due.
func (a *ASC) Tick(now time.Time, em Emitter) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if !a.started {
		a.start(now, em)
	}
	a.fireFaults(now, em)
	a.fireArrivals(now, em)
	a.fireStatus(now, em)

	nowFlashing := a.flashing()
	if a.wasFlashing && !nowFlashing {
		// The last fault cleared since the previous Tick. While flashing,
		// advanceState never ran, so a.stateUntil is still anchored at whenever
		// flash began — possibly minutes ago. Replaying the catch-up loop below
		// against that frozen deadline would fabricate a burst of PhaseStateChange
		// events backdated across the whole flash window, contradicting the
		// OperationalStatusReport stream (which correctly reported Mode="flash"
		// throughout). Re-anchor instead: resume the current phase fresh from now,
		// emitting nothing for the time spent flashing.
		a.resumeFromFlash(now)
	}
	a.wasFlashing = nowFlashing
	if nowFlashing {
		return // no phase cycling while faulted
	}
	for !now.Before(a.stateUntil) {
		a.advanceState(em)
	}
}

func (a *ASC) flashing() bool { return len(a.faults) > 0 }

// resumeFromFlash re-anchors the phase clock after the last fault clears. It keeps
// the controller in whatever state it was in when flash began (GREEN/YELLOW/RED for
// the same phase index) but restarts that state's full duration from now, rather
// than replaying the frozen interval. It also resets greenCount so the next
// termination reason is computed from real post-flash arrivals only, not arrivals
// that landed during the frozen flash interval.
func (a *ASC) resumeFromFlash(now time.Time) {
	var dur time.Duration
	switch a.state {
	case "GREEN":
		dur = time.Duration(plan[a.idx].greenSec) * time.Second
	case "YELLOW":
		dur = yellowSec * time.Second
	case "RED":
		dur = allRedSec * time.Second
	}
	a.stateUntil = now.Add(dur)
	a.greenCount = 0
}

func (a *ASC) nextSeq() uint64 {
	a.seq++
	return a.seq
}

// start puts phase index 0 into GREEN at `now` and emits its onset.
func (a *ASC) start(now time.Time, em Emitter) {
	a.started = true
	a.idx = 0
	a.state = "GREEN"
	ph := plan[a.idx]
	a.stateUntil = now.Add(time.Duration(ph.greenSec) * time.Second)
	a.emitPhase(em, ph.number, "TO_STATE_GREEN", "", now)
}

// advanceState fires exactly one state boundary: GREEN->YELLOW->RED->next GREEN.
func (a *ASC) advanceState(em Emitter) {
	at := a.stateUntil
	switch a.state {
	case "GREEN":
		ph := plan[a.idx]
		reason := "TERMINATION_REASON_GAP_OUT"
		if a.demand.Draw() < a.maxOutProbability(at, ph) {
			reason = "TERMINATION_REASON_MAX_OUT"
		}
		a.greenCount = 0
		a.state = "YELLOW"
		a.stateUntil = at.Add(yellowSec * time.Second)
		a.emitPhase(em, ph.number, "TO_STATE_YELLOW", reason, at)
	case "YELLOW":
		ph := plan[a.idx]
		a.state = "RED"
		a.stateUntil = at.Add(allRedSec * time.Second)
		a.emitPhase(em, ph.number, "TO_STATE_RED", "", at)
	case "RED":
		a.idx = (a.idx + 1) % len(plan)
		if a.idx == 0 {
			mult := a.pedCadenceMult
			if mult <= 0 {
				mult = 1
			}
			for i := 0; i < mult; i++ {
				a.cycles++
				if a.cycles%pedCoordCycles == 0 {
					a.emitPedestrianAndCoordination(em, at)
				}
			}
		}
		ph := plan[a.idx]
		a.state = "GREEN"
		a.stateUntil = at.Add(time.Duration(ph.greenSec) * time.Second)
		a.emitPhase(em, ph.number, "TO_STATE_GREEN", "", at)
	}
}

// maxOutProbability computes the probability that the green phase just ending
// on ph (at simulated time `at`) terminates MAX_OUT rather than GAP_OUT. It
// combines two demand-driven signals, both derived from the ASC's shared,
// seeded Demand so the result stays deterministic:
//
//   - saturation: current VolumePerHour relative to referenceCapacityVPH,
//     clamped to [0,1]. This is the primary driver — busy corridor cabinets
//     at peak hours saturate toward 1 and MAX_OUT a meaningful fraction of
//     the time; quiet side-street/off-peak cabinets sit well below 1 and
//     mostly GAP_OUT.
//   - arrival pressure: how the actual arrival count observed on this green
//     (a.greenCount) compared to what steady-state demand predicts for a
//     phase of this duration, given arrivals round-robin evenly across the
//     4 approaches. A busier-than-expected green nudges p up; a quieter one
//     nudges it down.
//
// The result is clamped to [maxOutProbFloor, maxOutProbCeil] so no cabinet is
// ever purely one reason.
func (a *ASC) maxOutProbability(at time.Time, ph phaseDef) float64 {
	vph := a.demand.VolumePerHour(at)
	sat := vph / referenceCapacityVPH

	// Expected arrivals for this specific approach during its own green:
	// arrivals round-robin across len(plan) approaches, so this approach
	// gets roughly a 1/len(plan) share of the overall arrival rate.
	expected := vph / 3600 * float64(ph.greenSec) / float64(len(plan))
	arrivalPressure := 0.0
	if expected > 0 {
		arrivalPressure = float64(a.greenCount)/expected - 1
	}

	p := maxOutBaseP +
		maxOutSatWeight*clampFloat(sat, 0, 1) +
		maxOutArrivalWeight*clampFloat(arrivalPressure, -1, 1)
	return clampFloat(p, maxOutProbFloor, maxOutProbCeil)
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (a *ASC) emitPhase(em Emitter, phaseNumber uint32, toState, reason string, at time.Time) {
	msg := &signalcontrolv1.PhaseStateChange{
		SourceDeviceId: a.controller,
		PhaseNumber:    phaseNumber,
		ToState:        signalcontrolv1.ToState(signalcontrolv1.ToState_value[toState]),
		OccurredAt:     timestamppb.New(at),
		Sequence:       a.nextSeq(),
	}
	if reason != "" {
		msg.TerminationReason = signalcontrolv1.TerminationReason(signalcontrolv1.TerminationReason_value[reason])
	}
	em.Emit(a.controller, ceTypePhaseStateChange, at, msg)
}

// isActiveGreen reports whether phaseNumber is the currently-served green phase.
func (a *ASC) isActiveGreen(phaseNumber uint32) bool {
	return a.state == "GREEN" && plan[a.idx].number == phaseNumber
}

// fireArrivals draws vehicle arrivals from the single shared Demand stream (so ASC
// detector counts, camera intervals, and lidar counts stay consistent with each
// other) and round-robins them across the four approaches. Each arrival emits a
// DetectorTransition ON, followed by an OFF ~1.5s later once presence clears.
func (a *ASC) fireArrivals(now time.Time, em Emitter) {
	if a.nextArrival.IsZero() {
		a.nextArrival = now.Add(a.demand.NextHeadway(now))
	}
	for !now.Before(a.nextArrival) {
		arriveAt := a.nextArrival
		ph := plan[a.arrivalIdx%len(plan)]
		a.arrivalIdx++

		if off, ok := a.pendingOff[ph.channel]; ok {
			// Rare short-headway edge case: clear the stale presence before the
			// new arrival so ON/OFF stay paired.
			a.emitDetector(em, ph, "TRANSITION_OFF", off)
		}
		a.emitDetector(em, ph, "TRANSITION_ON", arriveAt)
		a.pendingOff[ph.channel] = arriveAt.Add(presenceDur)
		if a.isActiveGreen(ph.number) {
			a.greenCount++
		}
		a.nextArrival = arriveAt.Add(a.demand.NextHeadway(arriveAt))
	}
	for ch, off := range a.pendingOff {
		if !now.Before(off) {
			a.emitDetector(em, plan[ch-1], "TRANSITION_OFF", off)
			delete(a.pendingOff, ch)
		}
	}
}

func (a *ASC) emitDetector(em Emitter, ph phaseDef, transition string, at time.Time) {
	msg := &signalcontrolv1.DetectorTransition{
		SourceDeviceId: a.controller,
		Channel:        ph.channel,
		Transition:     signalcontrolv1.Transition(signalcontrolv1.Transition_value[transition]),
		Lane:           "1",
		Approach:       ph.approach,
		PhaseServed:    ph.number,
		OccurredAt:     timestamppb.New(at),
		Sequence:       a.nextSeq(),
	}
	em.Emit(a.controller, ceTypeDetectorTransition, at, msg)
}

// fireStatus emits an OperationalStatusReport every 60s: mode "coordinated"
// normally, or "flash" while any fault is active.
func (a *ASC) fireStatus(now time.Time, em Emitter) {
	if a.nextStatus.IsZero() {
		a.nextStatus = now.Add(statusPeriod)
	}
	for !now.Before(a.nextStatus) {
		at := a.nextStatus
		mode := "coordinated"
		flashActive := false
		if a.flashing() {
			mode = "flash"
			flashActive = true
		}
		msg := &signalcontrolv1.OperationalStatusReport{
			SourceDeviceId: a.controller,
			Mode:           mode,
			FlashActive:    flashActive,
			OccurredAt:     timestamppb.New(at),
			Sequence:       a.nextSeq(),
		}
		em.Emit(a.controller, ceTypeOperationalStatusReport, at, msg)
		a.nextStatus = at.Add(statusPeriod)
	}
}

// fireFaults drains queued fault transitions from InjectFault/ClearFault, each as
// its own ControllerFaultEvent.
func (a *ASC) fireFaults(now time.Time, em Emitter) {
	for _, fe := range a.pendingFaults {
		sev := signalcontrolv1.FaultSeverity(signalcontrolv1.FaultSeverity_value["FAULT_SEVERITY_MAJOR"])
		flashCause := ""
		if fe.raised {
			flashCause = fe.id
		}
		msg := &signalcontrolv1.ControllerFaultEvent{
			SourceDeviceId: a.controller,
			FaultId:        fe.id,
			Severity:       sev,
			Raised:         fe.raised,
			FlashCause:     flashCause,
			OccurredAt:     timestamppb.New(now),
			Sequence:       a.nextSeq(),
		}
		em.Emit(a.controller, ceTypeControllerFaultEvent, now, msg)
	}
	a.pendingFaults = nil
}

// emitPedestrianAndCoordination fires once every pedCoordCycles completed cycles:
// one PedestrianEvent (a call served) and one CoordinationChange (a split update),
// both at the plan boundary.
func (a *ASC) emitPedestrianAndCoordination(em Emitter, at time.Time) {
	ph := plan[0]

	ped := &signalcontrolv1.PedestrianEvent{
		SourceDeviceId:  a.controller,
		PhaseNumber:     ph.number,
		DetectorChannel: ph.channel,
		Event: signalcontrolv1.OpenitsSignalControlPedestrianEventsEvent(
			signalcontrolv1.OpenitsSignalControlPedestrianEventsEvent_value["OPENITS_SIGNAL_CONTROL_PEDESTRIAN_EVENTS_EVENT_CALL_REGISTERED"],
		),
		OccurredAt: timestamppb.New(at),
		Sequence:   a.nextSeq(),
	}
	em.Emit(a.controller, ceTypePedestrianEvent, at, ped)

	cc := &signalcontrolv1.CoordinationChange{
		SourceDeviceId: a.controller,
		ChangeKind:     signalcontrolv1.ChangeKind(signalcontrolv1.ChangeKind_value["CHANGE_KIND_SPLIT"]),
		SplitNumber:    ph.number,
		PreviousValue:  int32(ph.greenSec),
		NewValue:       int32(ph.greenSec),
		OccurredAt:     timestamppb.New(at),
		Sequence:       a.nextSeq(),
	}
	em.Emit(a.controller, ceTypeCoordinationChange, at, cc)
}
