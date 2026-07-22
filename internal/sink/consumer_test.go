package sink

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"

	"github.com/Vikasa2M/vikasa-demo/internal/events"
	signalcontrolv1 "github.com/openits/openits-models/pkg/proto/openits/signal_control/v1"
)

// runJSPlain starts an in-process JetStream-enabled NATS server with no
// JetStream domain configured, mirroring internal/sim/publisher_test.go's
// runJS but without the cabinet-scoped domain — the central sink binds a
// single shared stream, not a per-cabinet leaf.
func runJSPlain(t *testing.T) (*server.Server, *nats.Conn) {
	t.Helper()
	opts := natstest.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true
	opts.StoreDir = t.TempDir()
	s := natstest.RunServer(&opts)
	nc, err := nats.Connect(s.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { nc.Close(); s.Shutdown() })
	return s, nc
}

type countIns struct {
	rows atomic.Int64
	fail atomic.Bool
}

func (c *countIns) Insert(_ context.Context, _ string, _ []string, rows [][]any) error {
	if c.fail.Load() {
		return context.DeadlineExceeded
	}
	c.rows.Add(int64(len(rows)))
	return nil
}

func TestConsumerAckAfterFlushAndRedelivery(t *testing.T) {
	_, nc := runJSPlain(t) // like Task 6's runJS but no domain; add helper here
	js, _ := nc.JetStream()
	_, err := js.AddStream(&nats.StreamConfig{Name: "T", Subjects: []string{"vikasa.>"}})
	if err != nil {
		t.Fatal(err)
	}
	env, _ := events.New("mardot", "d1", "cab-002", "asc-1",
		"vikasa.signal-control.phase-state-change.v1", time.Now().UTC(),
		&signalcontrolv1.PhaseStateChange{SourceDeviceId: "asc-1", PhaseNumber: 2})
	m := &nats.Msg{Subject: env.Subject, Header: env.Headers(), Data: env.Data}
	if _, err := js.PublishMsg(m); err != nil {
		t.Fatal(err)
	}

	ins := &countIns{}
	ins.fail.Store(true) // first pass: flush fails → no ack
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	cfg := ConsumerConfig{NATSURL: nc.ConnectedUrl(), StreamName: "T", Durable: "sinktest",
		FilterSubject: "vikasa.>", Database: "vikasa_mardot", BatchSize: 8, MaxWait: 500 * time.Millisecond,
		AckWait: time.Second}
	go func() {
		time.Sleep(2 * time.Second)
		ins.fail.Store(false) // recover → redelivery must land the rows
	}()
	_ = RunWithConn(ctx, cfg, nc, ins) // Run variant taking an existing conn, for tests
	if ins.rows.Load() < 2 {           // typed row + events_raw row
		t.Fatalf("expected rows after recovery, got %d", ins.rows.Load())
	}
}

// TestConsumerBindRetriesUntilStreamAppears is a regression test for the
// `make demo` fresh-bring-up crash: central-sink/federation-sink containers
// start at the same time as the NATS tiers but BEFORE stream-init has
// created the JetStream stream they bind to. Before the bindStream retry
// loop, that initial js.PullSubscribe bind failed immediately ("stream not
// found") and Run/RunWithConn returned that error straight away, which the
// sink binaries treated as fatal (log.Fatalf) — crashing the container
// before stream-init ever got a chance to run.
//
// This starts an in-process JetStream server with NO stream yet, launches
// the consumer against that not-yet-existing stream, and only creates the
// stream ~1s later (mirroring stream-init's few-seconds-after-bring-up
// timing) and publishes a message. It asserts the consumer's Run call blocks
// past that point instead of returning immediately, then binds and processes
// the message once the stream exists.
func TestConsumerBindRetriesUntilStreamAppears(t *testing.T) {
	// Shrink the retry delay so the test doesn't have to wait out the real
	// ~2-minute production budget; still exercises the same retry-loop code
	// path (multiple failed bind attempts before one that succeeds).
	origDelay := bindRetryDelay
	bindRetryDelay = 100 * time.Millisecond
	t.Cleanup(func() { bindRetryDelay = origDelay })

	_, nc := runJSPlain(t)
	js, _ := nc.JetStream()

	env, _ := events.New("mardot", "d1", "cab-004", "asc-1",
		"vikasa.signal-control.phase-state-change.v1", time.Now().UTC(),
		&signalcontrolv1.PhaseStateChange{SourceDeviceId: "asc-1", PhaseNumber: 2})

	ins := &countIns{}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	cfg := ConsumerConfig{NATSURL: nc.ConnectedUrl(), StreamName: "LATEBOUND", Durable: "latebound-test",
		FilterSubject: "vikasa.>", Database: "vikasa_mardot", BatchSize: 8, MaxWait: 200 * time.Millisecond,
		AckWait: time.Second}

	runErr := make(chan error, 1)
	go func() { runErr <- RunWithConn(ctx, cfg, nc, ins) }()

	// The stream genuinely does not exist yet: give bindStream a couple of
	// failed attempts (with the shrunk delay above) before creating it, so
	// this actually exercises retry rather than a lucky first-attempt race.
	select {
	case err := <-runErr:
		t.Fatalf("consumer returned before the stream ever existed (bind did not retry): %v", err)
	case <-time.After(1 * time.Second):
	}

	if _, err := js.AddStream(&nats.StreamConfig{Name: "LATEBOUND", Subjects: []string{"vikasa.>"}}); err != nil {
		t.Fatal(err)
	}
	m := &nats.Msg{Subject: env.Subject, Header: env.Headers(), Data: env.Data}
	if _, err := js.PublishMsg(m); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for ins.rows.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-runErr

	if got := ins.rows.Load(); got == 0 {
		t.Fatalf("expected the consumer to bind once the stream appeared and process the message, got 0 rows inserted")
	}
}

// TestConsumerRebindsAfterConfigMismatch is a regression test for the
// production incident where a durable already existed with a stale
// MaxAckPending (65536 from an earlier deploy) that didn't match what the
// current build requests (32000): every js.PullSubscribe attempt hit the
// exact same "configuration requests max ack pending to be 32000, but
// consumer's value is 65536" error, so bindStream's retry loop looped
// forever without ever binding — the sink never ingested a message. This
// wedged all 4 sinks under load until someone manually deleted the
// stale durables.
//
// It pre-creates the durable directly via js.AddConsumer with a DIFFERENT
// MaxAckPending than the consumer under test will request (mirroring the
// stale durable), then starts the consumer and asserts it self-heals (via
// healConfigMismatch), binds, and processes a published message — instead
// of retrying the same mismatch forever. It also confirms the durable's
// server-side MaxAckPending actually ends up matching what the consumer
// requested, so the fix isn't just "eventually succeeded by luck".
//
// Load-bearing check performed manually during development: commenting out
// the `healConfigMismatch(js, cfg)` call in bindStream (falling back to the
// old retry-on-any-error behavior, which just re-hits the same mismatch
// every attempt) made this test hang until its context deadline and fail
// with 0 rows inserted; restoring the call fixed it.
func TestConsumerRebindsAfterConfigMismatch(t *testing.T) {
	_, nc := runJSPlain(t)
	js, _ := nc.JetStream()
	if _, err := js.AddStream(&nats.StreamConfig{Name: "MISMATCH", Subjects: []string{"vikasa.>"}}); err != nil {
		t.Fatal(err)
	}

	// Pre-create the durable directly with a MaxAckPending the consumer
	// config below will NOT request, mirroring a stale durable left over
	// from an earlier deploy that used a different MaxAckPending setting.
	const staleMaxAckPending = 100
	const wantMaxAckPending = 250
	if _, err := js.AddConsumer("MISMATCH", &nats.ConsumerConfig{
		Durable:       "mismatch-test",
		AckPolicy:     nats.AckExplicitPolicy,
		FilterSubject: "vikasa.>",
		AckWait:       time.Second,
		MaxAckPending: staleMaxAckPending,
	}); err != nil {
		t.Fatal(err)
	}

	env, _ := events.New("mardot", "d1", "cab-005", "asc-1",
		"vikasa.signal-control.phase-state-change.v1", time.Now().UTC(),
		&signalcontrolv1.PhaseStateChange{SourceDeviceId: "asc-1", PhaseNumber: 2})
	m := &nats.Msg{Subject: env.Subject, Header: env.Headers(), Data: env.Data}
	if _, err := js.PublishMsg(m); err != nil {
		t.Fatal(err)
	}

	ins := &countIns{}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	cfg := ConsumerConfig{NATSURL: nc.ConnectedUrl(), StreamName: "MISMATCH", Durable: "mismatch-test",
		FilterSubject: "vikasa.>", Database: "vikasa_mardot", BatchSize: 8, MaxWait: 200 * time.Millisecond,
		AckWait: time.Second, MaxAckPending: wantMaxAckPending}

	runErr := make(chan error, 1)
	go func() { runErr <- RunWithConn(ctx, cfg, nc, ins) }()

	deadline := time.Now().Add(6 * time.Second)
	for ins.rows.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-runErr

	if got := ins.rows.Load(); got == 0 {
		t.Fatalf("expected the consumer to self-heal the stale-config durable, bind, and process the message, got 0 rows inserted")
	}

	info, err := js.ConsumerInfo("MISMATCH", "mismatch-test")
	if err != nil {
		t.Fatalf("ConsumerInfo after self-heal: %v", err)
	}
	if info.Config.MaxAckPending != wantMaxAckPending {
		t.Fatalf("expected durable's MaxAckPending to be healed to %d, got %d", wantMaxAckPending, info.Config.MaxAckPending)
	}
}

// alwaysFailIns is an Inserter whose Insert always fails, simulating a
// ClickHouse outage: every processBatch/Flush attempt fails, so the consumer
// loop should back off exponentially rather than retrying at backoffMin
// forever.
type alwaysFailIns struct {
	inserts atomic.Int64
}

func (a *alwaysFailIns) Insert(_ context.Context, _ string, _ []string, rows [][]any) error {
	a.inserts.Add(1)
	return context.DeadlineExceeded
}

// TestConsumerFlushFailureBacksOff is a regression test for the bug where
// the consumer loop reset backoff to backoffMin after every successful
// sub.Fetch, BEFORE processBatch ran. That meant a persistent flush failure
// (e.g. ClickHouse down) with live traffic still arriving never actually
// grew the backoff: each new Fetch succeeded and reset it back to the
// floor, so the loop hammered Flush roughly every 100ms indefinitely.
//
// With the fix, backoff only resets to backoffMin when processBatch returns
// nil (a batch was actually handled); a processBatch error grows the
// backoff (capped at backoffMax) and sleeps for it before the next
// iteration. This test pre-publishes a large pool of messages (so the
// consumer is never blocked waiting on Fetch) and points it at an Inserter
// that always fails Flush, then counts flush attempts (via the FlushError
// metric callback, which fires exactly once per failed processBatch) over a
// fixed 2s window.
//
// With correct exponential backoff (100ms, 200ms, 400ms, 800ms, 1600ms, ...
// capped at 5s) that yields roughly 5 attempts in 2s. Pinned at the 100ms
// floor (the bug), it's closer to 15-20. 8 is a robust threshold that
// separates the two comfortably without being flaky.
//
// Load-bearing check performed manually during development: reverting the
// fix (moving `backoff = backoffMin` back to right after a successful
// Fetch, before processBatch) made this test fail with attempts in the
// high teens; restoring the fix brought it back under the threshold.
func TestConsumerFlushFailureBacksOff(t *testing.T) {
	_, nc := runJSPlain(t)
	js, _ := nc.JetStream()
	_, err := js.AddStream(&nats.StreamConfig{Name: "FLUSHFAIL", Subjects: []string{"vikasa.>"}})
	if err != nil {
		t.Fatal(err)
	}

	env, _ := events.New("mardot", "d1", "cab-003", "asc-1",
		"vikasa.signal-control.phase-state-change.v1", time.Now().UTC(),
		&signalcontrolv1.PhaseStateChange{SourceDeviceId: "asc-1", PhaseNumber: 2})

	// Pre-publish a large pool of messages so the consumer is never blocked
	// waiting on Fetch for new data during the measurement window — that
	// would mask the difference between backed-off and pinned-at-the-floor
	// retry behavior. Even the buggy ~100ms-floor rate needs well under this
	// many messages (BatchSize=4) over 2s. Each publish gets its own
	// Nats-Msg-Id: env.Headers() otherwise sets the same deterministic
	// Nats-Msg-Id on every copy, and JetStream's dedup window would silently
	// drop all but the first as duplicates.
	for i := 0; i < 200; i++ {
		h := env.Headers()
		h.Set("Nats-Msg-Id", fmt.Sprintf("%s-%d", env.ID, i))
		msg := &nats.Msg{Subject: env.Subject, Header: h, Data: env.Data}
		if _, err := js.PublishMsg(msg); err != nil {
			t.Fatal(err)
		}
	}

	ins := &alwaysFailIns{}
	var attempts atomic.Int64
	m := &Metrics{FlushError: func() { attempts.Add(1) }}

	cfg := ConsumerConfig{NATSURL: nc.ConnectedUrl(), StreamName: "FLUSHFAIL", Durable: "flushfailtest",
		FilterSubject: "vikasa.>", Database: "vikasa_mardot", BatchSize: 4, MaxWait: 200 * time.Millisecond,
		AckWait: 10 * time.Second}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = RunWithConnAndMetrics(ctx, cfg, nc, ins, m)

	got := attempts.Load()
	if got == 0 {
		t.Fatalf("expected at least one flush attempt, got 0")
	}
	if got > 8 {
		t.Fatalf("expected backoff to suppress repeated flush attempts, got %d attempts in 2s (want <= 8); "+
			"backoff may be resetting on bare Fetch success instead of on processBatch success", got)
	}
	if ins.inserts.Load() == 0 {
		t.Fatalf("expected Inserter.Insert to be attempted at least once")
	}
}
