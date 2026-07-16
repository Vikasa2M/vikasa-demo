package tour

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// fleetYAML is the subset of deploy/topology/fleet.yaml's schema the tour
// needs: DOTs in file order, each with its cabinets in file order (id +
// corridor).
type fleetYAML struct {
	Dots []struct {
		Dot      string `yaml:"dot"`
		Cabinets []struct {
			ID       string `yaml:"id"`
			Corridor string `yaml:"corridor"`
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

	// Mirror tools/gen-compose's SCALE=1 filter: when SCALE=1, gen-compose
	// drops every non-corridor cabinet per DOT BEFORE assigning host ports
	// (see gen-compose run()), so the generated compose only maps the corridor
	// cabinets. This walk must apply the same filter or the derived
	// cabinet->port table drifts from the generated compose (wrong ports for
	// every cabinet after the first divergence). Read the same env var
	// gen-compose does, so a SCALE=1 bring-up + SCALE=1 tour stay consistent.
	scale1 := os.Getenv("SCALE") == "1"

	info := FleetInfo{SimPorts: map[string]int{}}
	port := 18081
	for _, d := range f.Dots {
		for _, c := range d.Cabinets {
			if scale1 && c.Corridor == "" {
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
