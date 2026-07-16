// internal/sim/camera.go
package sim

import (
	"hash/fnv"
	"math"
	"math/rand"
	"strconv"
	"sync"
	"time"

	perceptionv1 "github.com/openits/openits-models/pkg/proto/openits/perception/v1"
	trafficsensorv1 "github.com/openits/openits-models/pkg/proto/openits/traffic_sensor/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ce-types published by the Camera: it straddles the traffic-sensor service
// (lane-level interval/status/queue data) and the perception service (zone
// incident detection), matching the "camera" device in the reference card.
const (
	ceTypeTrafficIntervalReport     = "vikasa.traffic-sensor.traffic-interval-report.v1"
	ceTypeTrafficSensorStatusReport = "vikasa.traffic-sensor.traffic-sensor-status-report.v1"
	ceTypeQueueStateChanged         = "vikasa.traffic-sensor.queue-state-changed.v1"
	ceTypeZoneIncidentDetected      = "vikasa.perception.zone-incident-detected.v1"
	ceTypeZoneIncidentCleared       = "vikasa.perception.zone-incident-cleared.v1"
)

const (
	cameraIntervalPeriod = 5 * time.Second
	cameraStatusPeriod   = 60 * time.Second

	// congestionQueueThreshold is the (*Demand).CongestionLevel above which
	// the camera reports a building queue (QueueStateChanged) even with no
	// scenario-injected incident active — a leading indicator, so it's more
	// sensitive than dmsCongestionAdvisoryThreshold in dms.go.
	congestionQueueThreshold = 0.35
)

// incidentEvent is a queued ZoneIncidentDetected/ZoneIncidentCleared transition,
// shared by Camera and Lidar (both perception-class sensors that can independently
// observe the same injected incident) — mirrors ASC's faultEvent pattern.
type incidentEvent struct {
	id     string
	raised bool
}

// deviceSeed derives a deterministic, device-specific RNG seed from the
// controller id, so each simulated sensor gets its own reproducible-but-distinct
// noise stream without requiring a seed parameter on every constructor.
func deviceSeed(controller string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(controller))
	return int64(h.Sum64())
}

// poissonDraw draws a Poisson-distributed count with mean lambda using Knuth's
// algorithm: multiply uniform draws until the running product drops below
// exp(-lambda); the number of draws taken (minus one) is Poisson(lambda).
func poissonDraw(rng *rand.Rand, lambda float64) uint32 {
	if lambda <= 0 {
		return 0
	}
	l := math.Exp(-lambda)
	k := uint32(0)
	p := 1.0
	for {
		k++
		p *= rng.Float64()
		if p <= l {
			break
		}
	}
	return k - 1
}

// Camera simulates a combined traffic-sensor + perception camera: per-lane
// interval reports at 5s cadence, a status heartbeat every 60s, and, while a
// scenario incident is injected, zone incident detection plus a growing queue.
// It shares a *Demand with the rest of the cabinet so its counts stay
// consistent with the ASC and lidar.
type Camera struct {
	mu         sync.Mutex
	controller string
	demand     *Demand
	lanes      int
	rng        *rand.Rand
	seq        uint64
	zoneID     string

	nextInterval time.Time
	nextStatus   time.Time

	incidents        map[string]bool
	pendingIncidents []incidentEvent
	queueStart       time.Time

	// congestionQueueStart anchors QueueStateChanged's duration while an
	// autonomous congestion episode (not a scenario-injected incident) is
	// significant — zero when no such queue is currently being tracked.
	congestionQueueStart time.Time
}

// NewCamera creates a Camera device for controller, watching lanes lanes and
// sharing d with the rest of the cabinet.
func NewCamera(controller string, d *Demand, lanes int) *Camera {
	return &Camera{
		controller: controller,
		demand:     d,
		lanes:      lanes,
		rng:        rand.New(rand.NewSource(deviceSeed(controller))),
		zoneID:     controller + "-zone-1",
		incidents:  map[string]bool{},
	}
}

// InjectIncident raises a zone incident: a ZoneIncidentDetected is queued for
// the next Tick, and QueueStateChanged reports fire every interval thereafter
// until cleared. It also registers this device's hold on id in the shared
// Demand (degrading speed), so ASC and lidar observe the same slowdown. The
// demand-side Add is gated on this device not already holding id, so a
// repeated InjectIncident call for an id already raised on this device (the
// local map assignment below is itself idempotent) doesn't inflate Demand's
// per-id holder count.
func (c *Camera) InjectIncident(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.incidents[id] {
		c.demand.AddIncident(id)
	}
	c.incidents[id] = true
	c.pendingIncidents = append(c.pendingIncidents, incidentEvent{id: id, raised: true})
}

// ClearIncident reverses InjectIncident: queues a ZoneIncidentCleared and, if
// this device was actually holding id, releases that hold in the shared
// Demand. The shared speed degradation only lifts once every device holding
// id (e.g. a lidar that independently detected the same incident) has also
// called RemoveIncident.
func (c *Camera) ClearIncident(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.incidents[id] {
		c.demand.RemoveIncident(id)
	}
	delete(c.incidents, id)
	c.pendingIncidents = append(c.pendingIncidents, incidentEvent{id: id, raised: false})
}

func (c *Camera) nextSeq() uint64 {
	c.seq++
	return c.seq
}

// Tick advances the Camera to now, emitting any events that came due.
func (c *Camera) Tick(now time.Time, em Emitter) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fireIncidentEvents(now, em)
	c.fireIntervals(now, em)
	c.fireStatus(now, em)
}

// fireIncidentEvents drains queued incident transitions from
// InjectIncident/ClearIncident, each as its own ZoneIncidentDetected or
// ZoneIncidentCleared. Detection also anchors queueStart for QueueStateChanged.
func (c *Camera) fireIncidentEvents(now time.Time, em Emitter) {
	for _, ie := range c.pendingIncidents {
		if ie.raised {
			c.queueStart = now
			msg := &perceptionv1.ZoneIncidentDetected{
				SourceDeviceId: c.controller,
				IncidentId:     ie.id,
				ZoneId:         c.zoneID,
				Type:           "stopped-vehicle",
				Severity:       perceptionv1.FaultSeverity_FAULT_SEVERITY_MAJOR,
				ObjectClass:    "car",
				SpeedKmh:       "0.0",
				OccurredAt:     timestamppb.New(now),
				Sequence:       c.nextSeq(),
			}
			em.Emit(c.controller, ceTypeZoneIncidentDetected, now, msg)
		} else {
			msg := &perceptionv1.ZoneIncidentCleared{
				SourceDeviceId: c.controller,
				IncidentId:     ie.id,
				ZoneId:         c.zoneID,
				OccurredAt:     timestamppb.New(now),
				Sequence:       c.nextSeq(),
			}
			em.Emit(c.controller, ceTypeZoneIncidentCleared, now, msg)
		}
	}
	c.pendingIncidents = nil
}

// fireIntervals emits a TrafficIntervalReport every 5s and, while an
// incident is active OR the cabinet's autonomous congestion is significant,
// a QueueStateChanged alongside it (growing queue duration). An injected
// incident always takes priority over autonomous congestion — and resets
// the congestion queue tracker, so a queue resumed after the incident
// clears starts fresh rather than reporting a duration that spans the
// incident window.
func (c *Camera) fireIntervals(now time.Time, em Emitter) {
	if c.nextInterval.IsZero() {
		c.nextInterval = now.Add(cameraIntervalPeriod)
	}
	for !now.Before(c.nextInterval) {
		at := c.nextInterval
		c.emitInterval(at, em)
		switch {
		case len(c.incidents) > 0:
			c.congestionQueueStart = time.Time{}
			c.emitQueueState(at, c.queueStart, em)
		case c.demand.CongestionLevel(at) >= congestionQueueThreshold:
			if c.congestionQueueStart.IsZero() {
				c.congestionQueueStart = at
			}
			c.emitQueueState(at, c.congestionQueueStart, em)
		default:
			c.congestionQueueStart = time.Time{}
		}
		c.nextInterval = at.Add(cameraIntervalPeriod)
	}
}

func (c *Camera) emitInterval(at time.Time, em Emitter) {
	lanes := make([]*trafficsensorv1.TrafficIntervalReportLane, c.lanes)
	for i := 0; i < c.lanes; i++ {
		lanes[i] = c.laneReport(at, uint32(i+1))
	}
	msg := &trafficsensorv1.TrafficIntervalReport{
		SourceDeviceId: c.controller,
		Lane:           lanes,
		OccurredAt:     timestamppb.New(at),
		Sequence:       c.nextSeq(),
	}
	em.Emit(c.controller, ceTypeTrafficIntervalReport, at, msg)
}

// laneReport draws one lane's 5s interval: volume is a Poisson draw from the
// shared Demand's hourly rate (5s = 1/720h) split evenly across lanes; speed
// carries ±5% seeded noise off Demand.SpeedKmh; occupancy and density are
// derived from volume/speed so q = k·v holds by construction.
func (c *Camera) laneReport(at time.Time, laneID uint32) *trafficsensorv1.TrafficIntervalReportLane {
	lambda := c.demand.VolumePerHour(at) / 720 / float64(c.lanes)
	vol := poissonDraw(c.rng, lambda)

	speed := c.demand.SpeedKmh(at) * (1 + (c.rng.Float64()-0.5)*0.1)
	if speed < 1 {
		speed = 1
	}

	flowVph := vol * 720
	density := float64(flowVph) / speed // q = k·v => k = q/v
	occupancy := density / jamDensityKm * 100
	if occupancy > 100 {
		occupancy = 100
	}

	return &trafficsensorv1.TrafficIntervalReportLane{
		LaneId:            laneID,
		Volume:            vol,
		SpeedAverageKmh:   strconv.FormatFloat(speed, 'f', 1, 64),
		Occupancy:         strconv.FormatFloat(occupancy, 'f', 1, 64),
		Density:           strconv.FormatFloat(density, 'f', 1, 64),
		FlowRateVph:       flowVph,
		IntervalDurationS: 5,
	}
}

// emitQueueState reports the queue growing since start (either c.queueStart,
// for a scenario-injected incident, or c.congestionQueueStart, for
// autonomous congestion — see fireIntervals); there is no queue-length field
// on the wire message, so QueueDurationS (and Queueing=true) is how the demo
// models a queue that keeps building.
//
// at is a catching-up interval boundary and can be EARLIER than start:
// queueStart/congestionQueueStart are reset to `now`-ish when the queue
// begins, but fireIntervals replays any interval boundaries that fell due
// between the previous Tick and now, some of which can precede that reset.
// A negative duration cast straight to uint32 would wrap to ~4.29e9 seconds,
// so clamp to zero.
func (c *Camera) emitQueueState(at, start time.Time, em Emitter) {
	d := at.Sub(start)
	if d < 0 {
		d = 0
	}
	msg := &trafficsensorv1.QueueStateChanged{
		SourceDeviceId: c.controller,
		ZoneId:         c.zoneID,
		Queueing:       true,
		QueueDurationS: uint32(d.Seconds()),
		QueueStart:     timestamppb.New(start),
		OccurredAt:     timestamppb.New(at),
		Sequence:       c.nextSeq(),
	}
	em.Emit(c.controller, ceTypeQueueStateChanged, at, msg)
}

// fireStatus emits a healthy TrafficSensorStatusReport every 60s.
func (c *Camera) fireStatus(now time.Time, em Emitter) {
	if c.nextStatus.IsZero() {
		c.nextStatus = now.Add(cameraStatusPeriod)
	}
	for !now.Before(c.nextStatus) {
		at := c.nextStatus
		msg := &trafficsensorv1.TrafficSensorStatusReport{
			SourceDeviceId:    c.controller,
			Name:              c.controller,
			OperationalStatus: trafficsensorv1.OperationalStatus_OPERATIONAL_STATUS_ACTIVE,
			Latitude:          "39.739200",
			Longitude:         "-104.990300",
			OccurredAt:        timestamppb.New(at),
			Sequence:          c.nextSeq(),
		}
		em.Emit(c.controller, ceTypeTrafficSensorStatusReport, at, msg)
		c.nextStatus = at.Add(cameraStatusPeriod)
	}
}
