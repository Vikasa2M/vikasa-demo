// internal/sim/publisher.go
package sim

import (
	"errors"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"

	"github.com/Vikasa2M/vikasa-demo/internal/events"
)

// bufferStreamName is the cabinet's local JetStream buffer stream name,
// mirroring the vikasa-infra cabinet contract.
const bufferStreamName = "VIKASA_BUFFER"

// asyncMaxPending bounds how many un-acked async publishes a cabinet may have
// in flight. Emit publishes ASYNCHRONOUSLY (see Emit) so the 250ms device
// tick loop never blocks waiting for the leaf's JetStream ack — critical at
// ~100-cabinet scale, where a busy box slows acks and a synchronous publish
// would make each device's scheduled clock fall behind real time (events then
// carry stale ce-times). This window absorbs seconds of ack latency; only
// sustained overload past it applies backpressure (PublishMsgAsync blocks
// until a slot frees), which is the correct behavior.
const asyncMaxPending = 4096

// Publisher implements sim.Emitter by wrapping device messages in a
// CloudEvents envelope (internal/events) and asynchronously JetStream-
// publishing them to the cabinet's local VIKASA_BUFFER stream, with
// Nats-Msg-Id set for dedup.
type Publisher struct {
	cfg     Config
	js      nats.JetStreamContext
	dropped atomic.Uint64
}

// NewPublisher builds a Publisher over nc, ensuring the cabinet's local
// VIKASA_BUFFER stream exists (bootstrap is idempotent — safe even if the
// caller already ran EnsureBuffer). The JetStream context is configured for
// bounded async publishing: publishes never block the tick loop on acks, and
// an async publish that ultimately fails its ack is counted as a drop.
func NewPublisher(nc *nats.Conn, cfg Config) (*Publisher, error) {
	p := &Publisher{cfg: cfg}
	js, err := nc.JetStream(
		nats.PublishAsyncMaxPending(asyncMaxPending),
		nats.PublishAsyncErrHandler(func(_ nats.JetStream, _ *nats.Msg, err error) {
			p.dropped.Add(1)
			log.Printf("cabinet-sim: drop event (async ack failed): err=%v", err)
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("jetstream context: %w", err)
	}
	p.js = js
	if err := EnsureBuffer(js, cfg.Dot, cfg.District, cfg.Cabinet); err != nil {
		return nil, fmt.Errorf("ensure buffer: %w", err)
	}
	return p, nil
}

// Flush waits up to timeout for every in-flight async publish to be acked by
// the leaf. Call it on shutdown BEFORE draining the connection so buffered
// events aren't dropped mid-flight. Returns an error if the timeout elapses
// with publishes still pending.
func (p *Publisher) Flush(timeout time.Duration) error {
	select {
	case <-p.js.PublishAsyncComplete():
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("cabinet-sim: %d async publish(es) still pending after %s", p.js.PublishAsyncPending(), timeout)
	}
}

// EnsureBuffer creates the cabinet's VIKASA_BUFFER stream if missing. It is
// idempotent: if the stream already exists, it is left as-is.
func EnsureBuffer(js nats.JetStreamContext, dot, district, cabinet string) error {
	cfg := &nats.StreamConfig{
		Name:        bufferStreamName,
		Subjects:    []string{fmt.Sprintf("vikasa.%s.%s.%s.>", dot, district, cabinet)},
		Retention:   nats.LimitsPolicy,
		Storage:     nats.FileStorage,
		MaxBytes:    1 << 30, // 1 GiB, DiscardOld default
		MaxAge:      30 * 24 * time.Hour,
		Duplicates:  2 * time.Minute,
		Compression: nats.S2Compression,
	}
	if _, err := js.StreamInfo(cfg.Name); err == nil {
		return nil // already bootstrapped
	} else if !errors.Is(err, nats.ErrStreamNotFound) {
		return fmt.Errorf("check buffer stream %s: %w", cfg.Name, err)
	}
	if _, err := js.AddStream(cfg); err != nil {
		return fmt.Errorf("create buffer stream %s: %w", cfg.Name, err)
	}
	return nil
}

// Emit builds a CloudEvents envelope for msg via events.New and
// asynchronously JetStream-publishes it to the cabinet's local buffer, with
// Nats-Msg-Id (the envelope's ce-id) driving JetStream's server-side dedup
// window. The publish does NOT wait for the leaf's ack (see asyncMaxPending),
// so a slow ack never stalls the device tick loop; the ack lands later and a
// failed one is counted via the PublishAsyncErrHandler set in NewPublisher.
//
// The buffer is the durability boundary, not the sim: a synchronous publish
// failure (backpressure or rejection) or a malformed envelope is logged and
// counted as a drop rather than crashing the simulator.
func (p *Publisher) Emit(controller, ceType string, occurredAt time.Time, msg proto.Message) {
	env, err := events.New(p.cfg.Dot, p.cfg.District, p.cfg.Cabinet, controller, ceType, occurredAt, msg)
	if err != nil {
		p.drop("build envelope", controller, ceType, err)
		return
	}
	if _, err := p.js.PublishMsgAsync(&nats.Msg{Subject: env.Subject, Header: env.Headers(), Data: env.Data}); err != nil {
		p.drop("publish", controller, ceType, err)
		return
	}
}

func (p *Publisher) drop(stage, controller, ceType string, err error) {
	p.dropped.Add(1)
	log.Printf("cabinet-sim: drop event (%s failed): controller=%s ceType=%s err=%v", stage, controller, ceType, err)
}

// Dropped returns the count of events dropped since startup (envelope build
// or publish failures), for /healthz reporting.
func (p *Publisher) Dropped() uint64 { return p.dropped.Load() }
