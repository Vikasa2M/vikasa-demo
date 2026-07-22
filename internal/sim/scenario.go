// internal/sim/scenario.go
package sim

import (
	"fmt"
	"time"
)

// Devices bundles a cabinet's simulated devices plus the shared Demand model
// they all read from. main.go builds one Devices per cabinet, ticks each
// device on it every 250ms, and passes it to Scenario for /inject dispatch.
type Devices struct {
	Demand *Demand
	ASC    *ASC
	Cam1   *Camera
	Cam2   *Camera
	Lidar1 *Lidar
	DMS1   *DMS
	GW     *Gateway
	// Reversible is non-nil only on the I-75 South reversible-lane cabinet
	// (Config.Reversible); it adds a scheduled reversible express-lane segment.
	Reversible *ReversibleLane
}

// All returns the devices in the order main.go should Tick them. The optional
// reversible-lane device (present only on the I-75 South cabinet) ticks last.
func (d Devices) All() []Device {
	devs := []Device{d.ASC, d.Cam1, d.Cam2, d.Lidar1, d.DMS1, d.GW}
	if d.Reversible != nil {
		devs = append(devs, d.Reversible)
	}
	return devs
}

// Auto-clear delays and constants for the /inject scenarios below.
const (
	conflictFlashClearAfter    = 90 * time.Second
	detectorFaultClearAfter    = 60 * time.Second
	corridorIncidentClearAfter = 120 * time.Second
	pedSurgeClearAfter         = 120 * time.Second
	pedSurgeMultiplier         = 3

	// corridorIncidentID is used on BOTH cam-1 and lidar-1: Demand's
	// incident state is refcounted per id (see traffic.go), so pairing
	// inject/clear symmetrically across both devices under the same id is
	// correct and safe — the shared speed degradation only lifts once both
	// holders have cleared it.
	corridorIncidentID = "inc-1"
)

// Scenario dispatches a named POST /inject/{name} scenario against devices:
// it mutates device state for the devices' next Tick (each device hook is
// itself mutex-guarded, so this is safe to call concurrently with the
// ticker goroutine driving Tick) and schedules an auto-clear via
// time.AfterFunc after the scenario's hold duration. An unknown name
// returns an error, which main.go surfaces as an HTTP 404.
func Scenario(name string, devices Devices) error {
	switch name {
	case "conflict-flash":
		devices.ASC.InjectFault("conflict-flash")
		time.AfterFunc(conflictFlashClearAfter, func() {
			devices.ASC.ClearFault("conflict-flash")
		})

	case "detector-fault":
		devices.ASC.InjectFault("detector-fault")
		time.AfterFunc(detectorFaultClearAfter, func() {
			devices.ASC.ClearFault("detector-fault")
		})

	case "corridor-incident":
		devices.Demand.SetIncident(true)
		devices.Cam1.InjectIncident(corridorIncidentID)
		devices.Lidar1.InjectIncident(corridorIncidentID)
		devices.DMS1.PostAdvisory()
		time.AfterFunc(corridorIncidentClearAfter, func() {
			devices.Cam1.ClearIncident(corridorIncidentID)
			devices.Lidar1.ClearIncident(corridorIncidentID)
			devices.DMS1.ClearAdvisory()
			devices.Demand.SetIncident(false)
		})

	case "ped-surge":
		devices.ASC.SetPedCadenceMultiplier(pedSurgeMultiplier)
		time.AfterFunc(pedSurgeClearAfter, func() {
			devices.ASC.SetPedCadenceMultiplier(1)
		})

	default:
		return fmt.Errorf("unknown scenario %q", name)
	}
	return nil
}
