// internal/sim/publisher_test.go
package sim

import (
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	signalcontrolv1 "github.com/openits/openits-models/pkg/proto/openits/signal_control/v1"
)

func runJS(t *testing.T) (*server.Server, *nats.Conn) {
	t.Helper()
	opts := natstest.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true
	opts.StoreDir = t.TempDir()
	opts.JetStreamDomain = "mardot-d1-cab-test"
	s := natstest.RunServer(&opts)
	nc, err := nats.Connect(s.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nc.Close(); s.Shutdown() })
	return s, nc
}

func TestPublisherWritesBufferWithDedup(t *testing.T) {
	_, nc := runJS(t)
	cfg := Config{Dot: "mardot", District: "d1", Cabinet: "cab-test", Seed: 1, BaseVPH: 600}
	pub, err := NewPublisher(nc, cfg)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
	msg := &signalcontrolv1.PhaseStateChange{SourceDeviceId: "asc-1", PhaseNumber: 2}
	pub.Emit("asc-1", "vikasa.signal-control.phase-state-change.v1", at, msg)
	pub.Emit("asc-1", "vikasa.signal-control.phase-state-change.v1", at, msg) // identical → deduped
	// Emit is async now — wait for both publishes to be acked before reading
	// the stream state.
	if err := pub.Flush(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	js, _ := nc.JetStream()
	si, err := js.StreamInfo("VIKASA_BUFFER")
	if err != nil {
		t.Fatal(err)
	}
	if si.State.Msgs != 1 {
		t.Fatalf("dedup failed: want 1 msg, got %d", si.State.Msgs)
	}
}
