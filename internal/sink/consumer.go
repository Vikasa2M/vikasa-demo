package sink

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/Vikasa2M/vikasa-demo/internal/events"
)

// ConsumerConfig configures the central-sink pull consumer: which stream and
// durable to bind, how big/long to fetch, and which database the flushed
// rows target.
type ConsumerConfig struct {
	NATSURL       string        // e.g. nats://mardot-central:4222
	StreamName    string        // e.g. VIKASA_MARDOT_CENTRAL_D1_D1_0
	Durable       string        // e.g. central-sink
	FilterSubject string        // e.g. vikasa.mardot.>
	Database      string        // e.g. vikasa_mardot
	BatchSize     int           // 256
	MaxWait       time.Duration // 2s
	AckWait       time.Duration // 30s; keep short (<=1s) in tests so redelivery is observable
	// MaxAckPending caps how many delivered-but-unacked messages JetStream
	// lets this durable have outstanding before it stops delivering more
	// (see nats.MaxAckPending). It must be comfortably >= BatchSize or the
	// server throttles delivery below what a single Fetch(BatchSize) could
	// otherwise return in one round trip, silently capping throughput no
	// matter how large BatchSize/MaxWait are tuned. 0 leaves the JetStream
	// server default in place (1000 as of nats-server 2.12, applied when
	// MaxAckPending is omitted from the consumer config) — what every
	// existing test relies on by leaving this field unset.
	MaxAckPending int
}

// Metrics is the small callback surface the consumer loop reports through.
// main.go wires these to Prometheus collectors; tests can leave it nil
// (the loop treats a nil func as a no-op).
type Metrics struct {
	EventInserted func(table string, n int)
	FlushDuration func(seconds float64)
	DeadLetter    func(n int)
	FlushError    func()
}

func (m *Metrics) eventInserted(table string, n int) {
	if m != nil && m.EventInserted != nil {
		m.EventInserted(table, n)
	}
}

func (m *Metrics) flushDuration(seconds float64) {
	if m != nil && m.FlushDuration != nil {
		m.FlushDuration(seconds)
	}
}

func (m *Metrics) deadLetter(n int) {
	if m != nil && m.DeadLetter != nil {
		m.DeadLetter(n)
	}
}

func (m *Metrics) flushError() {
	if m != nil && m.FlushError != nil {
		m.FlushError()
	}
}

const (
	backoffMin = 100 * time.Millisecond
	backoffMax = 5 * time.Second

	deadLetterTable = "events_dead_letter"
)

// bindRetryAttempts/bindRetryDelay bound how long runWithConn waits for its
// target stream to exist before giving up. On a fresh `make demo` bring-up,
// central-sink/federation-sink containers start at the same time as the NATS
// tiers but BEFORE stream-init has created the JetStream streams they bind
// to; without a retry here, the initial js.PullSubscribe bind fails
// immediately ("stream not found") and the caller (cmd/central-sink,
// cmd/federation-sink) treats that as fatal. 60 attempts x 2s (~2 minutes)
// mirrors the connectNATS retry budget already used by both sink binaries,
// which is comfortably longer than the few seconds stream-init actually
// takes to run after the tiers come up. Package-level vars (not consts) so
// tests can shrink the delay instead of waiting out the full budget.
var (
	bindRetryAttempts = 60
	bindRetryDelay    = 2 * time.Second
)

var deadLetterColumns = []string{"subject", "ce_id", "ce_type", "error", "payload"}

// Run dials cfg.NATSURL and delegates to RunWithConn.
func Run(ctx context.Context, cfg ConsumerConfig, ins Inserter) error {
	return RunWithMetrics(ctx, cfg, ins, nil)
}

// RunWithMetrics is Run with an injected metrics callback set (main.go's
// entry point once Prometheus is wired up).
func RunWithMetrics(ctx context.Context, cfg ConsumerConfig, ins Inserter, m *Metrics) error {
	nc, err := nats.Connect(cfg.NATSURL)
	if err != nil {
		return err
	}
	defer nc.Close()
	return runWithConn(ctx, cfg, nc, ins, m)
}

// RunWithConn runs the consumer loop against an already-connected *nats.Conn
// (used by tests, and by Run after it dials). It does not close nc.
func RunWithConn(ctx context.Context, cfg ConsumerConfig, nc *nats.Conn, ins Inserter) error {
	return runWithConn(ctx, cfg, nc, ins, nil)
}

// RunWithConnAndMetrics is RunWithConn plus an injected metrics callback set.
func RunWithConnAndMetrics(ctx context.Context, cfg ConsumerConfig, nc *nats.Conn, ins Inserter, m *Metrics) error {
	return runWithConn(ctx, cfg, nc, ins, m)
}

func runWithConn(ctx context.Context, cfg ConsumerConfig, nc *nats.Conn, ins Inserter, m *Metrics) error {
	ackWait := cfg.AckWait
	if ackWait <= 0 {
		ackWait = 30 * time.Second
	}

	js, err := nc.JetStream()
	if err != nil {
		return err
	}
	sub, err := bindStream(ctx, js, cfg, ackWait)
	if err != nil {
		return err
	}

	backoff := backoffMin
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		msgs, err := sub.Fetch(cfg.BatchSize, nats.MaxWait(cfg.MaxWait))
		if err != nil {
			if errors.Is(err, nats.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) {
				continue
			}
			// Any other fetch error (dead NATS, etc.): back off exponentially
			// instead of spinning the CPU / hammering the server.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > backoffMax {
				backoff = backoffMax
			}
			continue
		}

		if len(msgs) == 0 {
			continue
		}
		// Fetched count is the direct signal that BatchSize/MaxWait/
		// MaxAckPending tuning is actually taking effect: if the server's
		// MaxAckPending were too low for BatchSize, this would plateau well
		// below cfg.BatchSize even with a large backlog waiting.
		log.Printf("sink: fetched %d/%d messages (durable=%s)", len(msgs), cfg.BatchSize, cfg.Durable)

		if err := processBatch(ctx, cfg, ins, msgs, m); err != nil {
			// Flush failed: ack nothing, let redelivery happen after AckWait.
			// Back off exponentially (instead of resetting on the next
			// successful Fetch) so a persistently broken sink doesn't
			// hot-loop retrying inserts at the backoff floor forever.
			m.flushError()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > backoffMax {
				backoff = backoffMax
			}
			continue
		}

		// Batch actually handled (flush succeeded, or there was nothing to
		// flush and any bad messages were dead-lettered and acked): the
		// sink is healthy, so the backoff can reset.
		backoff = backoffMin
	}
}

// bindStream binds the durable pull consumer to cfg.StreamName, retrying on
// any bind error (most commonly "stream not found" because stream-init
// hasn't run yet) up to bindRetryAttempts times, bindRetryDelay apart. This
// is the fix for the sink-vs-stream-init startup race on a fresh `make demo`
// bring-up: instead of the caller treating a too-early bind as fatal, the
// consumer just waits for the stream to show up. Returns ctx.Err() if ctx is
// cancelled while waiting, and the last bind error if the budget is
// exhausted without ctx being cancelled.
//
// It also self-heals one specific persistent (non-transient) bind failure:
// the durable already exists on the stream but with a different
// MaxAckPending than cfg requests (e.g. a stale durable left over from a
// config change in an earlier deploy — 65536 from an old build vs 32000 in
// the current one). Unlike "stream not found", that error is NOT transient:
// every retry hits the exact same mismatch and the sink never binds. This
// actually happened under full-fleet load and wedged all 4 sinks until the durables
// were deleted by hand. See healConfigMismatch for the detection/repair.
func bindStream(ctx context.Context, js nats.JetStreamContext, cfg ConsumerConfig, ackWait time.Duration) (*nats.Subscription, error) {
	var lastErr error
	healAttempts := 0
	for attempt := 1; attempt <= bindRetryAttempts; attempt++ {
		sub, err := js.PullSubscribe(cfg.FilterSubject, cfg.Durable,
			nats.BindStream(cfg.StreamName), nats.AckWait(ackWait), nats.MaxAckPending(cfg.MaxAckPending))
		if err == nil {
			return sub, nil
		}
		lastErr = err

		// Bounded (maxConfigHealAttempts): don't heal-loop forever if
		// something else is wrong that healConfigMismatch can't actually
		// fix (e.g. it keeps reporting a mismatch after we just fixed it).
		// bindRetryAttempts is a second, coarser backstop on top of this.
		if healAttempts < maxConfigHealAttempts && healConfigMismatch(js, cfg) {
			healAttempts++
			continue // retry immediately; the durable was just fixed
		}

		log.Printf("sink: bind to stream=%s durable=%s failed (attempt %d/%d), retrying in %s: %v",
			cfg.StreamName, cfg.Durable, attempt, bindRetryAttempts, bindRetryDelay, err)

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(bindRetryDelay):
		}
	}
	return nil, fmt.Errorf("bind to stream=%s durable=%s after %d attempts: %w",
		cfg.StreamName, cfg.Durable, bindRetryAttempts, lastErr)
}

// maxConfigHealAttempts bounds how many times bindStream will try to
// self-heal a stale-durable-config mismatch (healConfigMismatch) before
// giving up on healing and falling through to the plain retry/backoff loop
// for the rest of the bindRetryAttempts budget. Without this bound, a
// mismatch that healing genuinely can't fix would retry-heal forever
// instead of eventually surfacing the bind error like any other persistent
// failure.
const maxConfigHealAttempts = 3

// healConfigMismatch checks whether bindStream's last PullSubscribe attempt
// failed because a durable named cfg.Durable already exists on
// cfg.StreamName with a MaxAckPending different from what cfg requests, and
// if so repairs it so the next PullSubscribe attempt succeeds. It returns
// true when it made a change worth retrying for, false otherwise (including
// "not a mismatch" and "couldn't fix it").
//
// Detection reads the durable's actual server-side config via
// js.ConsumerInfo and compares MaxAckPending directly, rather than string-
// matching the bind error text (nats.go's checkConfig in js.go builds
// something like "nats: configuration requests max ack pending to be X, but
// consumer's value is Y" with a bare fmt.Errorf — no typed/wrapped sentinel
// error exists for this). ConsumerInfo is the more robust signal: it reads
// the actual state that caused the failure instead of depending on exact
// wording that could change across nats.go versions.
//
// Repair prefers js.UpdateConsumer (mutates the existing durable's
// MaxAckPending in place; nats-server's consumer update path explicitly
// supports changing MaxAckPending on a live consumer, so this causes no
// message redelivery). If that fails for any reason, it falls back to
// js.DeleteConsumer so the next PullSubscribe recreates the durable from
// scratch with the current config — safe even though it forces redelivery
// of any in-flight messages, because every event's ce-id is content-
// addressed and ClickHouse inserts use ReplacingMergeTree, so redelivered
// duplicates are idempotent.
func healConfigMismatch(js nats.JetStreamContext, cfg ConsumerConfig) bool {
	if cfg.MaxAckPending <= 0 {
		// 0 means "leave the server default alone" — this process has no
		// opinion about MaxAckPending, so there's nothing to disagree with
		// the existing durable about. Mirrors nats.go checkConfig's own
		// guard (it only compares/complains when the user's MaxAckPending >
		// 0), so this can't spuriously fire on the many existing configs
		// (tests, and any future caller) that leave MaxAckPending unset.
		return false
	}

	info, err := js.ConsumerInfo(cfg.StreamName, cfg.Durable)
	if err != nil {
		// Consumer (or stream) genuinely doesn't exist yet, or the lookup
		// itself failed some other way: not a config mismatch we can fix by
		// updating/deleting a durable that isn't there. Leave it to the
		// ordinary retry loop — this is the stream-not-found startup-race
		// path bindStream already handles, and it must stay untouched.
		return false
	}
	if info.Config.MaxAckPending == cfg.MaxAckPending {
		// The existing durable already matches what we're asking for;
		// whatever failed the bind, it wasn't a MaxAckPending mismatch, so
		// there's nothing for this function to fix.
		return false
	}

	log.Printf("sink: durable %s on stream %s exists with stale config (max_ack_pending %d != %d); updating to recreate",
		cfg.Durable, cfg.StreamName, info.Config.MaxAckPending, cfg.MaxAckPending)

	newCfg := info.Config
	newCfg.MaxAckPending = cfg.MaxAckPending
	if _, err := js.UpdateConsumer(cfg.StreamName, &newCfg); err == nil {
		return true
	} else {
		log.Printf("sink: UpdateConsumer for durable %s failed (%v); deleting to recreate instead", cfg.Durable, err)
	}

	if err := js.DeleteConsumer(cfg.StreamName, cfg.Durable); err != nil {
		log.Printf("sink: DeleteConsumer for durable %s also failed: %v", cfg.Durable, err)
		return false
	}
	return true
}

// badEnvelope records one message from the batch that failed to parse into
// an Envelope (events.FromMsg) or convert into Rows (sink.Rows), for a
// deferred events_dead_letter insert.
type badEnvelope struct {
	subject, ceID, ceType string
	cause                 error
	payload               []byte
}

// processBatch converts every message in the batch to rows, then flushes
// the good rows exactly once. Dead-letter inserts for malformed messages are
// deferred until AFTER that flush succeeds (or is skipped because there were
// no good rows) — if the flush fails, the whole batch (including the bad
// messages) is redelivered and re-processed from scratch, so writing their
// dead-letter rows early would duplicate them on every retry. Only once the
// batch is known-good does it dead-letter the bad messages and ack every
// message in the batch. Returns a non-nil error if Flush failed, in which
// case the caller must NOT ack anything so JetStream redelivers the batch.
func processBatch(ctx context.Context, cfg ConsumerConfig, ins Inserter, msgs []*nats.Msg, m *Metrics) error {
	var rows []Row
	var bad []badEnvelope

	for _, msg := range msgs {
		env, err := events.FromMsg(msg)
		if err != nil {
			bad = append(bad, badEnvelope{subject: msg.Subject, cause: err, payload: msg.Data})
			continue
		}
		rs, err := Rows(env)
		if err != nil {
			bad = append(bad, badEnvelope{subject: env.Subject, ceID: env.ID, ceType: env.Type, cause: err, payload: env.Data})
			continue
		}
		rows = append(rows, rs...)
	}

	if len(rows) > 0 {
		start := time.Now()
		flushErr := Flush(ctx, ins, cfg.Database, rows)
		m.flushDuration(time.Since(start).Seconds())
		if flushErr != nil {
			return flushErr
		}
		for _, tc := range tableCounts(rows) {
			m.eventInserted(tc.table, tc.n)
		}
	}

	// Good rows (if any) are now durably flushed. Record dead letters and
	// ack everything, including the bad messages — a single malformed
	// envelope must not wedge the stream, and dead-letter insert failures
	// (logged, counted) don't block acking either.
	if len(bad) > 0 {
		for _, b := range bad {
			deadLetterRow(ctx, ins, b)
		}
		m.deadLetter(len(bad))
	}

	for _, msg := range msgs {
		if err := msg.Ack(); err != nil {
			log.Printf("central-sink: ack failed for subject %s: %v", msg.Subject, err)
		}
	}
	return nil
}

// deadLetterRow inserts one row into events_dead_letter for a message that
// failed to parse into an Envelope or convert into sink Rows. A failure here
// is logged and counted but must not crash the consumer or block the rest
// of the batch — the offending message is still acked by the caller so a
// single bad message can't wedge the stream forever.
func deadLetterRow(ctx context.Context, ins Inserter, b badEnvelope) {
	row := [][]any{{b.subject, b.ceID, b.ceType, b.cause.Error(), string(b.payload)}}
	if err := ins.Insert(ctx, deadLetterTable, deadLetterColumns, row); err != nil {
		log.Printf("central-sink: dead-letter insert failed for subject %s: %v", b.subject, err)
	}
}

type tableCount struct {
	table string
	n     int
}

// tableCounts summarizes how many rows landed per table, in first-seen
// order, for the events-inserted-total metric.
func tableCounts(rows []Row) []tableCount {
	counts := make(map[string]int)
	var order []string
	for _, r := range rows {
		if _, seen := counts[r.Table]; !seen {
			order = append(order, r.Table)
		}
		counts[r.Table]++
	}
	out := make([]tableCount, 0, len(order))
	for _, t := range order {
		out = append(out, tableCount{table: t, n: counts[t]})
	}
	return out
}
