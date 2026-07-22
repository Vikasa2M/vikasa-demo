// internal/sim/dms.go
package sim

import (
	"sync"
	"time"

	commonv1 "github.com/openits/openits-models/pkg/proto/openits/common/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ce-types published by the DMS device.
const (
	ceTypeModeChanged  = "vikasa.dms.mode-changed.v1"
	ceTypeFaultRaised  = "vikasa.dms.fault-raised.v1"
	ceTypeFaultCleared = "vikasa.dms.fault-cleared.v1"
)

const (
	dmsModeNormal   = "normal"
	dmsModeAdvisory = "advisory"
)

// Autonomous DMS activity tuning: independent of the scripted
// PostAdvisory/InjectFault hooks, a DMS re-asserts its current mode
// periodically (so time-windowed dashboard panels always have recent data)
// and posts/clears an advisory tied to its cabinet's own autonomous traffic
// congestion (see (*Demand).CongestionLevel), separate from the scripted
// tour's corridor-incident scenario.
const (
	// dmsPeriodicReassertPeriod is how often the DMS re-emits a ModeChanged
	// for its CURRENT mode (prior==current) even with nothing new to report.
	dmsPeriodicReassertPeriod = 100 * time.Second

	// dmsCongestionAdvisoryThreshold is the CongestionLevel above which the
	// DMS autonomously posts an advisory — only "significant" congestion,
	// not every minor wiggle, changes the sign.
	dmsCongestionAdvisoryThreshold = 0.5

	reasonCongestionAhead   = "congestion ahead"
	reasonCongestionCleared = "congestion cleared"
	reasonPeriodic          = "periodic"
)

// dmsModeEvent is a queued ModeChanged transition.
type dmsModeEvent struct {
	prior, current, reason string
}

// dmsFaultEvent is a queued FaultRaised/FaultCleared transition.
type dmsFaultEvent struct {
	id     string
	raised bool
}

// DMS simulates a dynamic message sign. Besides the scripted scenario hooks
// — PostAdvisory/ClearAdvisory toggle its display mode, InjectFault/
// ClearFault raise and clear device faults — it also has bounded autonomous
// activity: a periodic mode re-assertion so it's never "silent" for long,
// and an advisory tied to its own cabinet's congestion episodes (see
// checkCongestion). Same Tick-driven, no-goroutine design as ASC.
type DMS struct {
	mu         sync.Mutex
	controller string
	demand     *Demand // congestion source for autonomous advisories; nil is valid (periodic-only, no congestion tie-in) for callers that don't need it
	seq        uint64

	mode string
	// scripted is true while a manually-posted (tour scenario) advisory is
	// active. It suppresses checkCongestion entirely, so the scripted
	// PostAdvisory/ClearAdvisory hooks always win over autonomous behavior
	// and the two mechanisms can never fight over d.mode.
	scripted bool
	// autoAdvisory is true when the CURRENT advisory was set by
	// checkCongestion (not PostAdvisory), so only checkCongestion's own
	// advisory gets auto-cleared once the episode ends.
	autoAdvisory bool

	started       bool
	nextPeriodic  time.Time
	pendingModes  []dmsModeEvent
	pendingFaults []dmsFaultEvent
}

// NewDMS creates a DMS device for controller, starting in normal mode and
// sharing d with the rest of the cabinet so its autonomous advisory can tie
// into the cabinet's own congestion episodes. d may be nil for callers that
// don't need the congestion tie-in (checkCongestion becomes a no-op).
func NewDMS(controller string, d *Demand) *DMS {
	return &DMS{controller: controller, demand: d, mode: dmsModeNormal}
}

// PostAdvisory switches the sign to advisory mode, queuing a ModeChanged for
// the next Tick. A no-op (on the mode-changed emission — scripted is still
// (re)armed) if already in advisory mode. Marks the advisory as
// scripted-owned, which both suppresses checkCongestion until ClearAdvisory
// and disclaims any prior autonomous ownership so a later autonomous clear
// can't fire redundantly once this scripted advisory is cleared.
func (d *DMS) PostAdvisory() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.scripted = true
	d.autoAdvisory = false
	if d.mode == dmsModeAdvisory {
		return
	}
	d.pendingModes = append(d.pendingModes, dmsModeEvent{prior: d.mode, current: dmsModeAdvisory, reason: "advisory-posted"})
	d.mode = dmsModeAdvisory
}

// ClearAdvisory reverses PostAdvisory, returning the sign to normal mode and
// releasing scripted ownership so autonomous congestion advisories can
// resume.
func (d *DMS) ClearAdvisory() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.scripted = false
	if d.mode == dmsModeNormal {
		return
	}
	d.pendingModes = append(d.pendingModes, dmsModeEvent{prior: d.mode, current: dmsModeNormal, reason: "advisory-cleared"})
	d.mode = dmsModeNormal
}

// InjectFault raises a device fault, queuing a FaultRaised for the next Tick.
func (d *DMS) InjectFault(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pendingFaults = append(d.pendingFaults, dmsFaultEvent{id: id, raised: true})
}

// ClearFault reverses InjectFault, queuing a FaultCleared.
func (d *DMS) ClearFault(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pendingFaults = append(d.pendingFaults, dmsFaultEvent{id: id, raised: false})
}

func (d *DMS) nextSeq() uint64 {
	d.seq++
	return d.seq
}

// Tick advances the DMS to now, emitting any queued scenario transitions
// plus its own autonomous activity. On its very first Tick a DMS also seeds
// a baseline ModeChanged(normal) so every sign has a current mode in
// dms_event from the start, even if no scenario (PostAdvisory/InjectFault)
// is ever run against it.
func (d *DMS) Tick(now time.Time, em Emitter) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.started {
		d.started = true
		baseline := dmsModeEvent{prior: "unknown", current: dmsModeNormal, reason: "startup"}
		d.pendingModes = append([]dmsModeEvent{baseline}, d.pendingModes...)
		d.nextPeriodic = now.Add(dmsPeriodicReassertPeriod)
	}
	d.checkCongestion(now)
	d.firePeriodic(now)
	d.fireModes(now, em)
	d.fireFaults(now, em)
}

// checkCongestion autonomously posts/clears an advisory tied to the
// cabinet's shared Demand congestion episodes, independent of the scripted
// PostAdvisory/ClearAdvisory hooks: it's a no-op with no Demand wired in,
// and it never touches d.mode while a scripted advisory is active
// (d.scripted), so it can't fight the tour's corridor-incident scenario. It
// only clears the advisory it itself set (d.autoAdvisory), never a scripted
// one.
func (d *DMS) checkCongestion(now time.Time) {
	if d.demand == nil || d.scripted {
		return
	}
	congested := d.demand.CongestionLevel(now) >= dmsCongestionAdvisoryThreshold
	switch {
	case congested && d.mode == dmsModeNormal:
		d.pendingModes = append(d.pendingModes, dmsModeEvent{prior: d.mode, current: dmsModeAdvisory, reason: reasonCongestionAhead})
		d.mode = dmsModeAdvisory
		d.autoAdvisory = true
	case !congested && d.autoAdvisory:
		d.pendingModes = append(d.pendingModes, dmsModeEvent{prior: d.mode, current: dmsModeNormal, reason: reasonCongestionCleared})
		d.mode = dmsModeNormal
		d.autoAdvisory = false
	}
}

// firePeriodic queues a ModeChanged(prior==current==mode, reason="periodic")
// every dmsPeriodicReassertPeriod, so dms_event always has a recent row —
// fixing the time-windowed DMS dashboard panels going empty once the last
// real transition ages out — even when nothing has actually changed.
func (d *DMS) firePeriodic(now time.Time) {
	for !now.Before(d.nextPeriodic) {
		d.pendingModes = append(d.pendingModes, dmsModeEvent{prior: d.mode, current: d.mode, reason: reasonPeriodic})
		d.nextPeriodic = d.nextPeriodic.Add(dmsPeriodicReassertPeriod)
	}
}

func (d *DMS) fireModes(now time.Time, em Emitter) {
	for _, me := range d.pendingModes {
		msg := &commonv1.ModeChanged{
			SourceDeviceId: d.controller,
			Prior:          me.prior,
			Current:        me.current,
			Reason:         me.reason,
			OccurredAt:     timestamppb.New(now),
			Sequence:       d.nextSeq(),
		}
		em.Emit(d.controller, ceTypeModeChanged, now, msg)
	}
	d.pendingModes = nil
}

func (d *DMS) fireFaults(now time.Time, em Emitter) {
	for _, fe := range d.pendingFaults {
		if fe.raised {
			msg := &commonv1.FaultRaised{
				SourceDeviceId: d.controller,
				FaultId:        fe.id,
				Severity:       commonv1.FaultSeverity_FAULT_SEVERITY_MAJOR,
				Description:    "DMS fault: " + fe.id,
				OccurredAt:     timestamppb.New(now),
				Sequence:       d.nextSeq(),
			}
			em.Emit(d.controller, ceTypeFaultRaised, now, msg)
		} else {
			msg := &commonv1.FaultCleared{
				SourceDeviceId: d.controller,
				FaultId:        fe.id,
				OccurredAt:     timestamppb.New(now),
				Sequence:       d.nextSeq(),
			}
			em.Emit(d.controller, ceTypeFaultCleared, now, msg)
		}
	}
	d.pendingFaults = nil
}
