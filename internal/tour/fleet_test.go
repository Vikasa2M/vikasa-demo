package tour

import "testing"

func TestLoadFleetInfoDerivesSimPortsAndCorridorCabinets(t *testing.T) {
	info, err := LoadFleetInfo(fleetFixturePath)
	if err != nil {
		t.Fatalf("LoadFleetInfo: %v", err)
	}

	// Matches tools/gen-compose's writeCompose port assignment: 18081 + a
	// running index over the PORT-MAPPED cabinets only (every hero/corridor
	// cabinet + any Expose-flagged non-hero cabinet, e.g. gdot's cab-002
	// fault cabinet), in fleet.yaml order. Non-port-mapped scale cabinets are
	// internal-only and absent from SimPorts. Cross-checked against the live
	// docker-compose.generated.yml port mappings.
	want := map[string]int{
		"cab-i85-001": 18081, // gdot hero
		"cab-002":     18082, // gdot fault cabinet (expose:true)
		"cab-i85-101": 18083, // ncdot hero
		"cab-i85-201": 18084, // scdot hero
	}
	if len(info.SimPorts) != len(want) {
		t.Fatalf("got %d sim ports, want %d: %+v", len(info.SimPorts), len(want), info.SimPorts)
	}
	for cabinet, port := range want {
		if got := info.SimPorts[cabinet]; got != port {
			t.Errorf("SimPorts[%q] = %d, want %d", cabinet, got, port)
		}
	}
	// A non-port-mapped scale cabinet must be absent.
	if p, ok := info.SimPorts["cab-003"]; ok {
		t.Errorf("SimPorts unexpectedly contains internal-only cabinet cab-003 = %d", p)
	}

	wantCorridor := []string{"cab-i85-001", "cab-i85-101", "cab-i85-201"}
	if len(info.CorridorCabinets) != len(wantCorridor) {
		t.Fatalf("got %v corridor cabinets, want %v", info.CorridorCabinets, wantCorridor)
	}
	for i, c := range wantCorridor {
		if info.CorridorCabinets[i] != c {
			t.Errorf("CorridorCabinets[%d] = %q, want %q", i, info.CorridorCabinets[i], c)
		}
	}
}

func TestLoadFleetInfoMissingFileErrors(t *testing.T) {
	if _, err := LoadFleetInfo("does/not/exist.yaml"); err == nil {
		t.Error("want error for a missing fleet file, got nil")
	}
}

// TestLoadFleetInfoScale1FiltersToCorridorCabinets asserts LoadFleetInfo
// mirrors gen-compose's SCALE=1 corridor-only filter: under SCALE=1 only each
// DOT's corridor (hero) cabinet exists in the generated compose, so only those
// get host ports — 18081/18082/18083 — and no non-corridor cabinet (even an
// Expose-flagged one like cab-002) appears. Without this, the tour's derived
// ports drift from the compose the way `SCALE=1 make demo` actually generates.
func TestLoadFleetInfoScale1FiltersToCorridorCabinets(t *testing.T) {
	t.Setenv("SCALE", "1")
	info, err := LoadFleetInfo(fleetFixturePath)
	if err != nil {
		t.Fatalf("LoadFleetInfo: %v", err)
	}
	want := map[string]int{"cab-i85-001": 18081, "cab-i85-101": 18082, "cab-i85-201": 18083}
	if len(info.SimPorts) != len(want) {
		t.Fatalf("SCALE=1 got %d sim ports, want %d: %+v", len(info.SimPorts), len(want), info.SimPorts)
	}
	for cab, p := range want {
		if got := info.SimPorts[cab]; got != p {
			t.Errorf("SCALE=1 SimPorts[%q] = %d, want %d", cab, got, p)
		}
	}
	for _, absent := range []string{"cab-002", "cab-003", "cab-i75s-01"} {
		if p, ok := info.SimPorts[absent]; ok {
			t.Errorf("SCALE=1 SimPorts unexpectedly contains non-corridor %q = %d", absent, p)
		}
	}
}
