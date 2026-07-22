// internal/sim/lidar.go
package sim

import (
	"math"
	"math/rand"
	"strconv"
	"sync"
	"time"

	perceptionv1 "github.com/openits/openits-models/pkg/proto/openits/perception/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	ceTypeZoneIntervalReport = "vikasa.perception.zone-interval-report.v1"
	lidarIntervalPeriod      = 5 * time.Second

	// truckFraction is the class split applied to lidar's total zone count.
	truckFraction = 0.10
)

// Lidar simulates a perception-service lidar unit: a single-zone interval
// report every 5s with a car/truck class split, whose totals track the same
// shared *Demand as the Camera but through an independent RNG stream (± 10%
// noise) so fusion-agreement panels have a small, realistic gap to display.
// It also supports the same incident scenario hooks as Camera, since a lidar
// unit independently detects incidents in its zone.
type Lidar struct {
	mu         sync.Mutex
	controller string
	demand     *Demand
	rng        *rand.Rand
	seq        uint64
	zoneID     string

	nextInterval time.Time

	incidents        map[string]bool
	pendingIncidents []incidentEvent
}

// NewLidar creates a Lidar device for controller, sharing d with the rest of
// the cabinet.
func NewLidar(controller string, d *Demand) *Lidar {
	return &Lidar{
		controller: controller,
		demand:     d,
		rng:        rand.New(rand.NewSource(deviceSeed(controller))),
		zoneID:     controller + "-zone-1",
		incidents:  map[string]bool{},
	}
}

// InjectIncident raises a zone incident from the lidar's own perspective: a
// ZoneIncidentDetected is queued for the next Tick, and this device's hold on
// id is registered in the shared Demand (same effect as Camera.InjectIncident,
// so ASC/camera/lidar agree on the slowdown even if only one device's
// scenario hook was called). The demand-side Add is gated on this device not
// already holding id, so a repeated InjectIncident call for an id already
// raised on this device doesn't inflate Demand's per-id holder count.
func (l *Lidar) InjectIncident(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.incidents[id] {
		l.demand.AddIncident(id)
	}
	l.incidents[id] = true
	l.pendingIncidents = append(l.pendingIncidents, incidentEvent{id: id, raised: true})
}

// ClearIncident reverses InjectIncident: queues a ZoneIncidentCleared and, if
// this device was actually holding id, releases that hold in the shared
// Demand. The shared speed degradation only lifts once every device holding
// id (e.g. a camera that independently detected the same incident) has also
// called RemoveIncident.
func (l *Lidar) ClearIncident(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.incidents[id] {
		l.demand.RemoveIncident(id)
	}
	delete(l.incidents, id)
	l.pendingIncidents = append(l.pendingIncidents, incidentEvent{id: id, raised: false})
}

func (l *Lidar) nextSeq() uint64 {
	l.seq++
	return l.seq
}

// Tick advances the Lidar to now, emitting any events that came due.
func (l *Lidar) Tick(now time.Time, em Emitter) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fireIncidentEvents(now, em)
	l.fireIntervals(now, em)
}

func (l *Lidar) fireIncidentEvents(now time.Time, em Emitter) {
	for _, ie := range l.pendingIncidents {
		if ie.raised {
			msg := &perceptionv1.ZoneIncidentDetected{
				SourceDeviceId: l.controller,
				IncidentId:     ie.id,
				ZoneId:         l.zoneID,
				Type:           "stopped-vehicle",
				Severity:       perceptionv1.FaultSeverity_FAULT_SEVERITY_MAJOR,
				ObjectClass:    "car",
				SpeedKmh:       "0.0",
				OccurredAt:     timestamppb.New(now),
				Sequence:       l.nextSeq(),
			}
			em.Emit(l.controller, ceTypeZoneIncidentDetected, now, msg)
		} else {
			msg := &perceptionv1.ZoneIncidentCleared{
				SourceDeviceId: l.controller,
				IncidentId:     ie.id,
				ZoneId:         l.zoneID,
				OccurredAt:     timestamppb.New(now),
				Sequence:       l.nextSeq(),
			}
			em.Emit(l.controller, ceTypeZoneIncidentCleared, now, msg)
		}
	}
	l.pendingIncidents = nil
}

// fireIntervals emits a ZoneIntervalReport every 5s.
func (l *Lidar) fireIntervals(now time.Time, em Emitter) {
	if l.nextInterval.IsZero() {
		l.nextInterval = now.Add(lidarIntervalPeriod)
	}
	for !now.Before(l.nextInterval) {
		at := l.nextInterval
		l.emitInterval(at, em)
		l.nextInterval = at.Add(lidarIntervalPeriod)
	}
}

// emitInterval draws the zone's total count from the same Demand model the
// Camera uses (5s = 1/720h of the hourly rate), then layers independent ±10%
// seeded noise on top so the lidar's total is close to, but not identical to,
// the camera's — the gap fusion-agreement panels are meant to surface.
func (l *Lidar) emitInterval(at time.Time, em Emitter) {
	lambda := l.demand.VolumePerHour(at) / 720
	base := float64(poissonDraw(l.rng, lambda))
	noisy := base * (1 + (l.rng.Float64()-0.5)*0.2) // ±10%
	if noisy < 0 {
		noisy = 0
	}
	total := uint32(math.Round(noisy))

	truck := uint32(math.Round(float64(total) * truckFraction))
	if truck > total {
		truck = total
	}
	car := total - truck

	speed := l.demand.SpeedKmh(at) * (1 + (l.rng.Float64()-0.5)*0.1)
	if speed < 1 {
		speed = 1
	}

	zone := &perceptionv1.ZoneIntervalReportZone{
		ZoneId:            l.zoneID,
		IntervalStart:     timestamppb.New(at.Add(-lidarIntervalPeriod)),
		IntervalDurationS: 5,
		AverageSpeedKmh:   strconv.FormatFloat(speed, 'f', 1, 64),
		ClassCount: []*perceptionv1.ClassCount{
			{Class: "car", Count: car},
			{Class: "truck", Count: truck},
		},
	}
	msg := &perceptionv1.ZoneIntervalReport{
		SourceDeviceId: l.controller,
		Zone:           []*perceptionv1.ZoneIntervalReportZone{zone},
		OccurredAt:     timestamppb.New(at),
		Sequence:       l.nextSeq(),
	}
	em.Emit(l.controller, ceTypeZoneIntervalReport, at, msg)
}
