package events

import "testing"

func TestSubject(t *testing.T) {
	s, err := Subject("gdot", "d1", "cab-i85-001", "signal-control", "asc-1", "phase-state-change")
	if err != nil {
		t.Fatal(err)
	}
	want := "vikasa.gdot.d1.cab-i85-001.signal-control.asc-1.phase-state-change"
	if s != want {
		t.Fatalf("got %q want %q", s, want)
	}
}

func TestSubjectRejectsBadTokens(t *testing.T) {
	for _, bad := range []string{"cab_001", "cab.001", "CAB-001", ""} {
		if _, err := Subject("gdot", "d1", bad, "dms", "dms-1", "mode-changed"); err == nil {
			t.Fatalf("token %q should be rejected", bad)
		}
	}
}

func TestServiceEvent(t *testing.T) {
	svc, ev, err := ServiceEvent("vikasa.signal-control.phase-state-change.v1")
	if err != nil || svc != "signal-control" || ev != "phase-state-change" {
		t.Fatalf("got %q %q %v", svc, ev, err)
	}
}

func TestParseSubjectRoundTrip(t *testing.T) {
	p, err := ParseSubject("vikasa.gdot.d1.cab-i85-001.signal-control.asc-1.phase-state-change")
	if err != nil {
		t.Fatal(err)
	}
	if p.Cabinet != "cab-i85-001" || p.Service != "signal-control" || p.Controller != "asc-1" {
		t.Fatalf("bad parse: %+v", p)
	}
}

func TestParseShareSubject(t *testing.T) {
	p, err := ParseShareSubject("vikasa.gdot.share.i85.cab-i85-001.signal-control.asc-1.phase-state-change")
	if err != nil {
		t.Fatal(err)
	}
	want := SubjectParts{
		Dot:        "gdot",
		District:   "share-i85",
		Cabinet:    "cab-i85-001",
		Service:    "signal-control",
		Controller: "asc-1",
		Event:      "phase-state-change",
	}
	if p != want {
		t.Fatalf("got %+v want %+v", p, want)
	}
}

func TestParseShareSubjectRejectsBadForm(t *testing.T) {
	for _, bad := range []string{
		"vikasa.gdot.d1.cab-i85-001.signal-control.asc-1.phase-state-change", // 7-token internal form
		"vikasa.gdot.share.i85.cab-i85-001.signal-control.asc-1",             // too short
		"nope.gdot.share.i85.cab-i85-001.signal-control.asc-1.phase-state-change",
	} {
		if _, err := ParseShareSubject(bad); err == nil {
			t.Fatalf("subject %q should be rejected", bad)
		}
	}
}
