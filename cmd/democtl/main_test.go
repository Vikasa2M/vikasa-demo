package main

import (
	"reflect"
	"testing"
)

func TestSplitOnlyNamesEmptyReturnsNil(t *testing.T) {
	std, ai, err := splitOnlyNames("", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if std != nil || ai != nil {
		t.Errorf("got std=%v ai=%v, want both nil for an empty --only", std, ai)
	}
}

func TestSplitOnlyNamesStandardPhasesOnly(t *testing.T) {
	std, ai, err := splitOnlyNames("wan-cut,restore", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(std, []string{"wan-cut", "restore"}) {
		t.Errorf("std = %v, want [wan-cut restore]", std)
	}
	if len(ai) != 0 {
		t.Errorf("ai = %v, want empty", ai)
	}
}

func TestSplitOnlyNamesAIPhasesRequireAIFlag(t *testing.T) {
	if _, _, err := splitOnlyNames("ai-build", false); err == nil {
		t.Error("want error naming an AI-segment phase without --ai, got nil")
	}
	if _, _, err := splitOnlyNames("ai-qa", false); err == nil {
		t.Error("want error naming an AI-segment phase without --ai, got nil")
	}
}

func TestSplitOnlyNamesAIPhasesWithAIFlag(t *testing.T) {
	std, ai, err := splitOnlyNames("wan-cut,ai-build,ai-qa", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(std, []string{"wan-cut"}) {
		t.Errorf("std = %v, want [wan-cut]", std)
	}
	if !ai["ai-build"] || !ai["ai-qa"] {
		t.Errorf("ai = %v, want both ai-build and ai-qa set", ai)
	}
}

func TestSplitOnlyNamesSkipsBlanksFromStrayCommas(t *testing.T) {
	std, _, err := splitOnlyNames("wan-cut,, ,restore", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(std, []string{"wan-cut", "restore"}) {
		t.Errorf("std = %v, want [wan-cut restore] with blanks skipped", std)
	}
}

func TestSplitOnlyNamesOnlyAINamesLeavesStdEmpty(t *testing.T) {
	std, ai, err := splitOnlyNames("ai-build", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(std) != 0 {
		t.Errorf("std = %v, want empty", std)
	}
	if !ai["ai-build"] || ai["ai-qa"] {
		t.Errorf("ai = %v, want only ai-build set", ai)
	}
}

func TestNetworkConnectArgsConnectIncludesAlias(t *testing.T) {
	got := networkConnectArgs("connect", "vikasa-demo_vikasa", "vikasa-demo-gdot-cab-i85-001-nats-1", "gdot-cab-i85-001-nats")
	want := []string{"network", "connect", "vikasa-demo_vikasa", "--alias", "gdot-cab-i85-001-nats", "vikasa-demo-gdot-cab-i85-001-nats-1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestNetworkConnectArgsDisconnectHasNoAlias(t *testing.T) {
	got := networkConnectArgs("disconnect", "vikasa-demo_vikasa", "vikasa-demo-gdot-cab-i85-001-nats-1", "gdot-cab-i85-001-nats")
	want := []string{"network", "disconnect", "vikasa-demo_vikasa", "vikasa-demo-gdot-cab-i85-001-nats-1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	for _, a := range got {
		if a == "--alias" {
			t.Fatal("disconnect must not pass --alias (docker network disconnect has no such flag)")
		}
	}
}
