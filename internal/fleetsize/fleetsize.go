// Package fleetsize resolves the demo's deployment size and decides which
// cabinets a given size includes. It is the single source of truth shared by
// tools/gen-compose (which filters fleet.yaml before generating compose) and
// internal/tour (which derives the cabinet->port table at runtime), so a
// SIZE=x bring-up and a SIZE=x tour always agree on which cabinets exist.
package fleetsize

import "fmt"

// Size is a demo deployment footprint.
type Size string

const (
	// Small is each DOT's single I-85 corridor hero (3 cabinets) — the
	// federation + resilience story only. Equivalent to the legacy SCALE=1.
	Small Size = "small"
	// Medium adds every cabinet the demo tour targets (MARDOT's I-85 perception
	// corridor, its I-75S reversible segment, and the fault cabinet) plus each
	// DOT's hero — 18 cabinets, enough for all six tour phases.
	Medium Size = "medium"
	// Large is the full fleet (99 cabinets) — the default.
	Large Size = "large"
)

// Resolve determines the deployment size from environment lookups (pass
// os.Getenv). Order: an explicit SIZE wins (and is validated); otherwise
// SCALE=1 maps to Small for back-compat; otherwise Large.
func Resolve(env func(string) string) (Size, error) {
	switch s := env("SIZE"); s {
	case string(Small):
		return Small, nil
	case string(Medium):
		return Medium, nil
	case string(Large):
		return Large, nil
	case "":
		if env("SCALE") == "1" {
			return Small, nil
		}
		return Large, nil
	default:
		return "", fmt.Errorf("fleetsize: invalid SIZE %q (want small|medium|large)", s)
	}
}

// Keep reports whether a cabinet with the given fleet.yaml tags is included at
// the given size. corridor is the cabinet's `corridor` tag ("" unless it is a
// federation hero), route its physical roadway ("i85"/"i75s"/""), and expose
// whether it is a host-port-mapped non-hero cabinet.
func Keep(size Size, corridor, route string, expose bool) bool {
	switch size {
	case Small:
		return corridor != ""
	case Medium:
		return corridor != "" || route != "" || expose
	default: // Large
		return true
	}
}
