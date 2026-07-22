package events

import (
	signalcontrolv1 "github.com/openits/openits-models/pkg/proto/openits/signal_control/v1"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func TestContentAddressedID(t *testing.T) {
	at := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	msg := &signalcontrolv1.PhaseStateChange{SourceDeviceId: "asc-1", PhaseNumber: 2}
	a, err := New("mardot", "d1", "cab-i85-001", "asc-1", "vikasa.signal-control.phase-state-change.v1", at, msg)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := New("mardot", "d1", "cab-i85-001", "asc-1", "vikasa.signal-control.phase-state-change.v1", at, msg)
	if a.ID != b.ID {
		t.Fatal("same content must produce same ID")
	}
	msg2 := &signalcontrolv1.PhaseStateChange{SourceDeviceId: "asc-1", PhaseNumber: 4}
	c, _ := New("mardot", "d1", "cab-i85-001", "asc-1", "vikasa.signal-control.phase-state-change.v1", at, msg2)
	if a.ID == c.ID {
		t.Fatal("different content must produce different ID")
	}
}

func TestHeadersCarryCEAndMsgID(t *testing.T) {
	at := time.Now().UTC()
	e, _ := New("mardot", "d1", "cab-002", "dms-1", "vikasa.dms.mode-changed.v1", at, &signalcontrolv1.PhaseStateChange{})
	h := e.Headers()
	for _, k := range []string{"ce-id", "ce-type", "ce-source", "ce-time", "ce-specversion", "Nats-Msg-Id"} {
		if h.Get(k) == "" {
			t.Fatalf("missing header %s", k)
		}
	}
	if h.Get("Nats-Msg-Id") != e.ID {
		t.Fatal("Nats-Msg-Id must equal ce-id")
	}
}

func TestFromMsgRoundTrip(t *testing.T) {
	at := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	msg := &signalcontrolv1.PhaseStateChange{SourceDeviceId: "asc-1", PhaseNumber: 2}
	env, err := New("mardot", "d1", "cab-i85-001", "asc-1", "vikasa.signal-control.phase-state-change.v1", at, msg)
	if err != nil {
		t.Fatal(err)
	}
	natsMsg := &nats.Msg{
		Subject: env.Subject,
		Header:  env.Headers(),
		Data:    env.Data,
	}
	got, err := FromMsg(natsMsg)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != env.ID {
		t.Fatalf("ID mismatch: got %q, want %q", got.ID, env.ID)
	}
	if got.Type != env.Type {
		t.Fatalf("Type mismatch: got %q, want %q", got.Type, env.Type)
	}
	if got.Source != env.Source {
		t.Fatalf("Source mismatch: got %q, want %q", got.Source, env.Source)
	}
	if got.Subject != env.Subject {
		t.Fatalf("Subject mismatch: got %q, want %q", got.Subject, env.Subject)
	}
	if string(got.Data) != string(env.Data) {
		t.Fatalf("Data mismatch: got %v, want %v", got.Data, env.Data)
	}
	if !got.Time.Equal(env.Time) {
		t.Fatalf("Time mismatch: got %v, want %v", got.Time, env.Time)
	}
}

func TestFromMsgMissingHeaders(t *testing.T) {
	at := time.Now().UTC()
	msg := &signalcontrolv1.PhaseStateChange{SourceDeviceId: "asc-1", PhaseNumber: 2}
	env, err := New("mardot", "d1", "cab-i85-001", "asc-1", "vikasa.signal-control.phase-state-change.v1", at, msg)
	if err != nil {
		t.Fatal(err)
	}

	h := env.Headers()
	h.Del("ce-id")
	h.Del("ce-type")
	if _, err := FromMsg(&nats.Msg{Subject: env.Subject, Header: h, Data: env.Data}); err == nil {
		t.Fatal("expected error for missing ce-id/ce-type headers")
	}

	h2 := env.Headers()
	h2.Del("ce-time")
	if _, err := FromMsg(&nats.Msg{Subject: env.Subject, Header: h2, Data: env.Data}); err == nil {
		t.Fatal("expected error for missing ce-time header")
	}
}
