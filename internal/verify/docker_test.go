package verify

import "testing"

func TestCabinetLeafService(t *testing.T) {
	cases := map[string]string{
		"cab-i85-001": "mardot-cab-i85-001-nats",
		"cab-002":     "mardot-cab-002-nats",
	}
	for cabinet, want := range cases {
		if got := CabinetLeafService(cabinet); got != want {
			t.Errorf("CabinetLeafService(%q) = %q, want %q", cabinet, got, want)
		}
	}
}

// samplePS mirrors the real (NDJSON — one JSON object per line, not a JSON
// array) output of `docker compose -f deploy/compose/docker-compose.yml ps
// --format json`, trimmed to the fields ParseComposePS/ResolveNetwork use.
const samplePS = `{"Name":"vikasa-demo-cab-i85-001-sim-1","Service":"cab-i85-001-sim","Networks":"vikasa-demo_vikasa","Project":"vikasa-demo"}
{"Name":"vikasa-demo-mardot-cab-i85-001-nats-1","Service":"mardot-cab-i85-001-nats","Networks":"vikasa-demo_vikasa","Project":"vikasa-demo"}
{"Name":"vikasa-demo-clickhouse-1","Service":"clickhouse","Networks":"vikasa-demo_vikasa","Project":"vikasa-demo"}
`

func TestParseComposePSFindsService(t *testing.T) {
	c, err := ParseComposePS([]byte(samplePS), "mardot-cab-i85-001-nats")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Name != "vikasa-demo-mardot-cab-i85-001-nats-1" {
		t.Errorf("Name = %q, want vikasa-demo-mardot-cab-i85-001-nats-1", c.Name)
	}
	if c.Project != "vikasa-demo" {
		t.Errorf("Project = %q, want vikasa-demo", c.Project)
	}
}

func TestParseComposePSServiceNotFound(t *testing.T) {
	if _, err := ParseComposePS([]byte(samplePS), "does-not-exist"); err == nil {
		t.Error("want error when service is absent from ps output")
	}
}

func TestParseComposePSIgnoresBlankLines(t *testing.T) {
	withBlanks := "\n\n" + samplePS + "\n\n"
	c, err := ParseComposePS([]byte(withBlanks), "clickhouse")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Service != "clickhouse" {
		t.Errorf("Service = %q, want clickhouse", c.Service)
	}
}

func TestParseComposePSMalformedLine(t *testing.T) {
	if _, err := ParseComposePS([]byte("not json at all"), "clickhouse"); err == nil {
		t.Error("want error on malformed JSON line")
	}
}

func TestParseComposePSAllReturnsEveryContainer(t *testing.T) {
	containers, err := ParseComposePSAll([]byte(samplePS))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(containers) != 3 {
		t.Fatalf("got %d containers, want 3", len(containers))
	}
}

func TestFindService(t *testing.T) {
	containers, err := ParseComposePSAll([]byte(samplePS))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c, err := FindService(containers, "clickhouse")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Name != "vikasa-demo-clickhouse-1" {
		t.Errorf("Name = %q, want vikasa-demo-clickhouse-1", c.Name)
	}
	if _, err := FindService(containers, "does-not-exist"); err == nil {
		t.Error("want error when service is absent")
	}
}

// TestResolveNetworkFallsBackToSibling covers the scenario `democtl
// restore` hits: after `cut`, the target container is no longer attached to
// "vikasa" specifically (though it may still be on its private cabinet-net
// — modeled here as fully empty for the simple case), so ResolveNetwork
// must learn the "vikasa" network name from a still-attached sibling in
// the same ps listing instead of failing outright.
func TestResolveNetworkFallsBackToSibling(t *testing.T) {
	containers := []ComposeContainer{
		{Name: "vikasa-demo-mardot-cab-i85-001-nats-1", Service: "mardot-cab-i85-001-nats", Networks: ""}, // disconnected
		{Name: "vikasa-demo-clickhouse-1", Service: "clickhouse", Networks: "vikasa-demo_vikasa"},
	}
	target, err := FindService(containers, "mardot-cab-i85-001-nats")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := ResolveNetwork(containers, target, WANNetworkKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "vikasa-demo_vikasa" {
		t.Errorf("got %q, want vikasa-demo_vikasa", got)
	}
}

// TestResolveNetworkFallsBackToSiblingStillOnCabinetNet is the realistic
// post-cut case: the leaf stays attached to its private cabinet-net (it was
// only disconnected from "vikasa"), so its Networks field is non-empty but
// doesn't contain "vikasa" at all. ResolveNetwork must still find
// "vikasa" from a sibling rather than mistakenly treating the leaf's
// remaining cabinet-net attachment as a match.
func TestResolveNetworkFallsBackToSiblingStillOnCabinetNet(t *testing.T) {
	containers := []ComposeContainer{
		{Name: "vikasa-demo-mardot-cab-i85-001-nats-1", Service: "mardot-cab-i85-001-nats", Networks: "vikasa-demo_cab-i85-001-net"},
		{Name: "vikasa-demo-cab-i85-001-sim-1", Service: "cab-i85-001-sim", Networks: "vikasa-demo_cab-i85-001-net"},
		{Name: "vikasa-demo-clickhouse-1", Service: "clickhouse", Networks: "vikasa-demo_vikasa"},
	}
	target, err := FindService(containers, "mardot-cab-i85-001-nats")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := ResolveNetwork(containers, target, WANNetworkKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "vikasa-demo_vikasa" {
		t.Errorf("got %q, want vikasa-demo_vikasa (not the cabinet-net)", got)
	}
}

func TestResolveNetworkPrefersTargetWhenAttached(t *testing.T) {
	containers := []ComposeContainer{
		{Name: "a", Service: "mardot-cab-i85-001-nats", Networks: "vikasa-demo_vikasa"},
		{Name: "b", Service: "clickhouse", Networks: "some-other-network"},
	}
	target, err := FindService(containers, "mardot-cab-i85-001-nats")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := ResolveNetwork(containers, target, WANNetworkKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "vikasa-demo_vikasa" {
		t.Errorf("got %q, want the target's own network, vikasa-demo_vikasa", got)
	}
}

// TestResolveNetworkPrefersTargetWhenAttachedToBoth covers the pre-cut
// steady state: the leaf is attached to BOTH its cabinet-net and "vikasa"
// simultaneously. ResolveNetwork must pick the "vikasa" one specifically,
// not just the first entry in the comma-separated list.
func TestResolveNetworkPrefersTargetWhenAttachedToBoth(t *testing.T) {
	containers := []ComposeContainer{
		{Name: "a", Service: "mardot-cab-i85-001-nats", Networks: "vikasa-demo_cab-i85-001-net,vikasa-demo_vikasa"},
	}
	target, err := FindService(containers, "mardot-cab-i85-001-nats")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, err := ResolveNetwork(containers, target, WANNetworkKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "vikasa-demo_vikasa" {
		t.Errorf("got %q, want vikasa-demo_vikasa (the specific key match, not the first entry)", got)
	}
}

func TestResolveNetworkErrorsWhenNoContainerIsAttached(t *testing.T) {
	containers := []ComposeContainer{
		{Name: "a", Service: "mardot-cab-i85-001-nats", Networks: ""},
		{Name: "b", Service: "clickhouse", Networks: ""},
	}
	target, _ := FindService(containers, "mardot-cab-i85-001-nats")
	if _, err := ResolveNetwork(containers, target, WANNetworkKey); err == nil {
		t.Error("want error when no container in the listing is attached to any matching network")
	}
}
