package tour

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/Vikasa2M/vikasa-demo/internal/fleetsize"
)

// fleetYAML is the subset of deploy/topology/fleet.yaml's schema the tour
// needs: DOTs in file order, each with its cabinets in file order (id +
// corridor + route + expose).
type fleetYAML struct {
	Dots []struct {
		Dot      string `yaml:"dot"`
		Cabinets []struct {
			ID       string `yaml:"id"`
			Corridor string `yaml:"corridor"`
			Route    string `yaml:"route"`
			Expose   bool   `yaml:"expose"`
		} `yaml:"cabinets"`
	} `yaml:"dots"`
}

// FleetInfo is the fleet.yaml-derived data the tour needs at runtime: each
// cabinet's simulated cabinet's host-mapped HTTP port (for /inject and
// /healthz calls) and which cabinets sit on the shared I-85 corridor (the
// only cabinets whose events cross the DMZ into vikasa_federation).
type FleetInfo struct {
	SimPorts         map[string]int
	CorridorCabinets []string
}

// LoadFleetInfo parses fleet.yaml and reproduces tools/gen-compose's sim
// host-port assignment — 18081 plus a running index over the cabinets that
// get a host-mapped port (every hero/corridor cabinet + any Expose-flagged
// non-hero cabinet), in fleet.yaml order (see tools/gen-compose/main.go's
// writeCompose + portMapped, which do the same `simPort := 18081; ...;
// simPort++` walk over the same predicate) — so the tour never hardcodes a
// cabinet -> port table that could silently drift from the generated compose
// file. Non-port-mapped scale cabinets are internal-only and simply absent
// from SimPorts; the tour only ever looks up cabinets it interacts with.
func LoadFleetInfo(path string) (FleetInfo, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return FleetInfo{}, fmt.Errorf("tour: read fleet file %s: %w", path, err)
	}
	var f fleetYAML
	if err := yaml.Unmarshal(b, &f); err != nil {
		return FleetInfo{}, fmt.Errorf("tour: parse fleet file %s: %w", path, err)
	}

	// Apply the same size filter gen-compose does (see internal/fleetsize):
	// cabinets dropped at this size never exist in the generated compose, so
	// this walk must skip them too or the derived cabinet->port table drifts.
	// Reading the same SIZE/SCALE env keeps a SIZE=x bring-up and a SIZE=x
	// tour consistent.
	size, err := fleetsize.Resolve(os.Getenv)
	if err != nil {
		return FleetInfo{}, fmt.Errorf("tour: %w", err)
	}

	info := FleetInfo{SimPorts: map[string]int{}}
	port := 18081
	for _, d := range f.Dots {
		for _, c := range d.Cabinets {
			if !fleetsize.Keep(size, c.Corridor, c.Route, c.Expose) {
				continue // filtered out of the generated compose entirely
			}
			if c.Corridor != "" || c.Expose {
				info.SimPorts[c.ID] = port
				port++
			}
			if c.Corridor != "" {
				info.CorridorCabinets = append(info.CorridorCabinets, c.ID)
			}
		}
	}
	if len(info.SimPorts) == 0 {
		return FleetInfo{}, fmt.Errorf("tour: fleet file %s declared no port-mapped cabinets", path)
	}
	return info, nil
}
