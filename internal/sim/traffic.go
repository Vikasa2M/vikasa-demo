// internal/sim/traffic.go
package sim

import (
	"math"
	"math/rand"
	"sync"
	"time"
)

// hourly demand multipliers (index = hour UTC); AM/PM peaks, busy overnight
// floor. The demo runs at real wall-clock time and gets recorded at
// unpredictable hours, so the trough is kept high (~0.58-0.60, not the
// ~0.08-0.15 a literal commute curve would have) — otherwise an overnight
// recording shows every cabinet idle and the ATSPM/Signal-Performance
// dashboards (Purdue matrix, split-failure) go dead. Peaks still reach 1.00
// so AM/PM rush hour remains visibly busier than the rest of the day.
var hourCurve = [24]float64{
	0.62, 0.60, 0.58, 0.58, 0.60, 0.68, 0.80, 0.92,
	1.00, 0.90, 0.80, 0.78, 0.80, 0.80, 0.82, 0.88,
	0.95, 1.00, 0.93, 0.82, 0.75, 0.70, 0.66, 0.64,
}

const (
	freeFlowKmh  = 65.0  // vf
	jamDensityKm = 120.0 // kj, veh/km/lane
)

// Autonomous congestion-episode tuning: a bounded, seeded schedule of short
// congestion dips layered on top of the demand baseline (see CongestionLevel
// / generateCongestionEpisodes) so dashboards show organic dips/recoveries
// at baseline, not a flat line — without touching the binary incident
// mechanism the scripted tour's corridor-incident scenario uses.
const (
	congestionCyclePeriod = 20 * time.Minute // the fixed per-cabinet schedule repeats every cyclePeriod
	congestionRampMin     = 25 * time.Second
	congestionRampMax     = 60 * time.Second
	congestionHoldMin     = 60 * time.Second
	congestionHoldMax     = 180 * time.Second
	congestionPeakMin     = 0.50 // an episode's peak congestion level is drawn once, in [Min,Max)
	congestionPeakMax     = 0.85

	// congestionSpeedCoeff scales how much an active congestion level cuts
	// SpeedKmh: v *= 1 - level*congestionSpeedCoeff. At the maximum possible
	// level*coeff (0.85*0.55 = 0.4675) this leaves ~53% of free-flow speed —
	// materially milder than the scripted corridor-incident's flat 0.4x
	// (leaves 40%), so an injected incident always reads as the more
	// dramatic event even if it happens to land during an autonomous
	// episode.
	congestionSpeedCoeff = 0.55
)

// Gentle short-period demand fluctuation: a small seeded sine wiggle on top
// of hourCurve so volume/speed still isn't dead-flat between congestion
// episodes. Amplitude and period are drawn once per cabinet (see NewDemand)
// so the wiggle is staggered across cabinets, not synchronized.
const (
	demandNoisePeriodMin    = 5 * time.Minute
	demandNoisePeriodMax    = 9 * time.Minute
	demandNoiseAmplitudeMin = 0.04
	demandNoiseAmplitudeMax = 0.08
)

// congestionEpisode is one entry in a cabinet's fixed, seeded congestion
// schedule: a ramp-up/hold/ramp-down window recurring every
// congestionCyclePeriod, starting at offset within the cycle.
type congestionEpisode struct {
	offset   time.Duration
	rampUp   time.Duration
	hold     time.Duration
	rampDown time.Duration
	peak     float64
}

// levelAt returns this episode's congestion contribution at elapsed (time
// since the start of the current congestionCyclePeriod), wrapping around the
// cycle boundary so an episode whose offset sits near the end of the cycle
// still ramps/holds/recovers continuously into the next cycle instead of
// being truncated.
func (e congestionEpisode) levelAt(elapsed time.Duration) float64 {
	dt := elapsed - e.offset
	if dt < 0 {
		dt += congestionCyclePeriod
	}
	total := e.rampUp + e.hold + e.rampDown
	switch {
	case dt >= total:
		return 0
	case dt < e.rampUp:
		if e.rampUp <= 0 {
			return e.peak
		}
		return e.peak * float64(dt) / float64(e.rampUp)
	case dt < e.rampUp+e.hold:
		return e.peak
	default:
		if e.rampDown <= 0 {
			return 0
		}
		rd := dt - e.rampUp - e.hold
		return e.peak * (1 - float64(rd)/float64(e.rampDown))
	}
}

// generateCongestionEpisodes draws a cabinet's fixed congestion schedule
// once from rng: the episode count scales with baseVPH (corridor cabinets —
// higher base_vph — get a few more episodes per cycle than quiet side
// streets, so they congest "a bit more often"), and each episode's
// offset/ramp/hold/peak is drawn independently, so overlapping episodes are
// possible (and harmless — CongestionLevel just takes the max) rather than
// requiring fragile non-overlap bucketing.
func generateCongestionEpisodes(rng *rand.Rand, baseVPH float64) []congestionEpisode {
	sat := clampFloat(baseVPH/referenceCapacityVPH, 0.3, 1.3)
	count := int(math.Round(1 + 2*sat))
	if count < 1 {
		count = 1
	}
	if count > 4 {
		count = 4
	}
	episodes := make([]congestionEpisode, count)
	for i := range episodes {
		episodes[i] = congestionEpisode{
			offset:   time.Duration(rng.Float64() * float64(congestionCyclePeriod)),
			rampUp:   congestionRampMin + time.Duration(rng.Float64()*float64(congestionRampMax-congestionRampMin)),
			hold:     congestionHoldMin + time.Duration(rng.Float64()*float64(congestionHoldMax-congestionHoldMin)),
			rampDown: congestionRampMin + time.Duration(rng.Float64()*float64(congestionRampMax-congestionRampMin)),
			peak:     congestionPeakMin + rng.Float64()*(congestionPeakMax-congestionPeakMin),
		}
	}
	return episodes
}

// mod64 is a non-negative modulo for int64 (Go's % can return negative
// results for negative a; t.Unix() is always non-negative for any sane
// simulated time, but this keeps CongestionLevel well-defined regardless).
func mod64(a, m int64) int64 {
	r := a % m
	if r < 0 {
		r += m
	}
	return r
}

// Demand is the single per-cabinet source of traffic truth. All devices read it
// so ASC detector counts, camera intervals, and lidar counts agree.
type Demand struct {
	mu      sync.Mutex
	rng     *rand.Rand
	baseVPH float64
	// incidents is a per-id holder count, not a mere presence set: the same
	// incident id can be independently held by more than one device (e.g. a
	// camera and a lidar that both detect the same real-world incident), so a
	// plain map[string]struct{} can't distinguish "1 holder" from "2 holders"
	// — the first RemoveIncident would delete the entry either way. Speed
	// degradation stays active while any id's count is > 0.
	incidents map[string]int

	// episodes is this cabinet's fixed, seeded congestion-episode schedule
	// (see generateCongestionEpisodes), generated once at construction and
	// read-only thereafter, so CongestionLevel needs no locking and stays
	// consistent across repeated calls at the same t (required for the
	// q=k·v identity DensityPerKm/SpeedKmh must preserve).
	episodes []congestionEpisode

	// demandNoiseFactor's fixed, seeded sine parameters, also drawn once at
	// construction and read-only thereafter.
	noisePeriod    time.Duration
	noisePhase     float64
	noiseAmplitude float64
}

// NewDemand creates a new seeded traffic demand model with a given base volume.
func NewDemand(seed int64, baseVPH float64) *Demand {
	rng := rand.New(rand.NewSource(seed))
	return &Demand{
		rng:            rng,
		baseVPH:        baseVPH,
		incidents:      map[string]int{},
		episodes:       generateCongestionEpisodes(rng, baseVPH),
		noisePeriod:    demandNoisePeriodMin + time.Duration(rng.Float64()*float64(demandNoisePeriodMax-demandNoisePeriodMin)),
		noisePhase:     rng.Float64() * 2 * math.Pi,
		noiseAmplitude: demandNoiseAmplitudeMin + rng.Float64()*(demandNoiseAmplitudeMax-demandNoiseAmplitudeMin),
	}
}

// CongestionLevel returns the cabinet's current autonomous congestion level
// at t, in [0, congestionPeakMax): 0 when no episode is active, otherwise
// the max across any episode in the fixed schedule that's ramping
// up/holding/ramping down at t (see congestionEpisode.levelAt). It is a
// pure function of t and the immutable schedule fixed at construction, so
// it needs no locking and is safe to call concurrently.
func (d *Demand) CongestionLevel(t time.Time) float64 {
	elapsed := time.Duration(mod64(t.Unix(), int64(congestionCyclePeriod/time.Second))) * time.Second
	level := 0.0
	for _, e := range d.episodes {
		if l := e.levelAt(elapsed); l > level {
			level = l
		}
	}
	return level
}

// demandNoiseFactor is a small seeded sine wiggle in
// [1-noiseAmplitude, 1+noiseAmplitude] so volume/speed isn't dead-flat
// between congestion episodes either. Pure function of t and the cabinet's
// fixed, seeded period/phase/amplitude.
func (d *Demand) demandNoiseFactor(t time.Time) float64 {
	secs := float64(t.Unix())
	return 1 + d.noiseAmplitude*math.Sin(2*math.Pi*secs/d.noisePeriod.Seconds()+d.noisePhase)
}

// VolumePerHour returns baseVPH × time-of-day curve × a gentle seeded noise
// factor.
func (d *Demand) VolumePerHour(t time.Time) float64 {
	return d.baseVPH * hourCurve[t.UTC().Hour()] * d.demandNoiseFactor(t)
}

// SpeedKmh solves Greenshields for the current demand: q = k·vf·(1-k/kj);
// pick the uncongested root, then apply incident degradation or, absent an
// incident, the cabinet's current autonomous congestion level. Incident and
// congestion are mutually exclusive (not compounded): the scripted tour's
// incident is meant to be the dramatic, unmistakable event, so it fully
// overrides whatever mild autonomous congestion happens to be active rather
// than stacking with it.
func (d *Demand) SpeedKmh(t time.Time) float64 {
	q := d.VolumePerHour(t)
	qmax := freeFlowKmh * jamDensityKm / 4
	if q > qmax {
		q = qmax
	}
	// k = kj/2 * (1 - sqrt(1 - q/qmax)) — uncongested branch
	k := jamDensityKm / 2 * (1 - math.Sqrt(1-q/qmax))
	v := freeFlowKmh * (1 - k/jamDensityKm)
	d.mu.Lock()
	inc := len(d.incidents) > 0
	d.mu.Unlock()
	switch {
	case inc:
		v *= 0.4
	default:
		if level := d.CongestionLevel(t); level > 0 {
			v *= 1 - level*congestionSpeedCoeff
		}
	}
	return v
}

// DensityPerKm returns k = q / v (so q = k·v holds by construction).
func (d *Demand) DensityPerKm(t time.Time) float64 {
	return d.VolumePerHour(t) / d.SpeedKmh(t)
}

// NextHeadway returns the exponential inter-arrival time for the next vehicle.
func (d *Demand) NextHeadway(t time.Time) time.Duration {
	vph := d.VolumePerHour(t)
	if vph < 1 {
		vph = 1
	}
	d.mu.Lock()
	u := d.rng.ExpFloat64()
	d.mu.Unlock()
	return time.Duration(u * float64(time.Hour) / vph)
}

// Draw returns a uniform random float64 in [0, 1) from the Demand's seeded
// rng, guarded by the same mutex as NextHeadway. It lets other per-cabinet
// device logic (e.g. the ASC's phase-termination-reason mix) draw
// additional deterministic randomness from the same seed, instead of
// reaching for a fresh, non-deterministic rand source.
func (d *Demand) Draw() float64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.rng.Float64()
}

// AddIncident registers a hold on incident id, incrementing its holder count.
// Multiple devices (camera, lidar) can independently hold the same id; speed
// stays degraded until every holder calls RemoveIncident, so one device
// clearing its own copy of a shared incident can't prematurely restore speed
// for the others. Callers are expected to call this at most once per "not
// held -> held" transition on their end (Camera/Lidar guard this via their
// own local incident membership check) so repeated Inject calls for an
// already-held id don't inflate the count.
func (d *Demand) AddIncident(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.incidents[id]++
}

// RemoveIncident releases one hold on incident id, decrementing its holder
// count and deleting the entry once it reaches zero. Speed only recovers once
// no ids have any remaining holders (see IncidentActive).
func (d *Demand) RemoveIncident(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.incidents[id] <= 1 {
		delete(d.incidents, id)
	} else {
		d.incidents[id]--
	}
}

// SetIncident is a legacy manual toggle for the incident scenario hook,
// implemented on top of AddIncident/RemoveIncident via a reserved "_manual"
// id so it composes correctly with device-driven incidents (e.g. it won't
// clear an incident a device is still holding, and a device clearing its own
// incident won't clear a manually-set one).
func (d *Demand) SetIncident(active bool) {
	if active {
		d.AddIncident("_manual")
	} else {
		d.RemoveIncident("_manual")
	}
}

// IncidentActive returns whether any incident id currently holds the shared
// speed degradation.
func (d *Demand) IncidentActive() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.incidents) > 0
}
