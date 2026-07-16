// Command federation-sink drains all 3 DOTs' DMZ JetStream streams (the
// corridor-only subject share each DOT's dmz cluster republishes) into the
// shared vikasa_federation ClickHouse database. One process, one durable
// pull consumer per DOT (3 goroutines), each reusing
// internal/sink.RunWithConnAndMetrics exactly like cmd/central-sink does —
// the only difference is the subject shape on the wire: DMZ-transformed
// messages carry the 8-token "share" subject form
// (vikasa.<dot>.share.<corridor>.<cabinet>.<service>.<controller>.<event>),
// which internal/sink.Rows parses via events.ParseShareSubject after
// events.ParseSubject's 7-token attempt fails.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	clickhouse "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Vikasa2M/vikasa-demo/internal/sink"
)

const (
	metricsAddr = ":9091"

	// See cmd/central-sink/main.go's defaultBatchSize/defaultMaxWait comment:
	// same fix (Flush does one insert per table, so the fetch needs to be
	// big enough that each table's grouped insert is worth doing), same
	// tuning knobs, just federation-sink's own env var names since it's a
	// separate process/container (FEDERATION_ prefix matching its existing
	// FEDERATION_CH_DSN/FEDERATION_DOTS).
	defaultBatchSize        = 8000
	defaultMaxWait          = 1 * time.Second
	maxAckPendingMultiplier = 4

	ackWait        = 30 * time.Second
	connectRetries = 60
	connectDelay   = 2 * time.Second
	chDatabase     = "vikasa_federation"
	durable        = "federation-sink"
)

func main() {
	dotsFlag := flag.String("dots", envOr("FEDERATION_DOTS", "gdot,ncdot,scdot"), "comma-separated list of DOTs to federate")
	chDSN := flag.String("ch-dsn", envOr("FEDERATION_CH_DSN", "clickhouse://clickhouse:9000"), "ClickHouse DSN")
	batchSize := flag.Int("batch-size", envIntOr("FEDERATION_BATCH_SIZE", defaultBatchSize),
		"consumer fetch batch size (messages per Fetch, across all tables), per DOT")
	maxWait := flag.Duration("max-wait", envDurationOr("FEDERATION_MAX_WAIT", defaultMaxWait),
		"consumer fetch max wait before flushing a partial batch, per DOT")
	flag.Parse()

	dots := splitCSV(*dotsFlag)
	if len(dots) == 0 {
		log.Fatalf("federation-sink: no DOTs configured (-dots/FEDERATION_DOTS)")
	}

	conn, err := connectClickHouse(*chDSN, chDatabase)
	if err != nil {
		log.Fatalf("federation-sink: connect ClickHouse at %s: %v", *chDSN, err)
	}
	// One shared inserter: all 3 DOTs land in the same vikasa_federation
	// database, and ClickHouseInserter.Insert is safe for concurrent use
	// (each call prepares and sends its own batch).
	ins := &sink.ClickHouseInserter{Conn: conn, DB: chDatabase}

	m := newMetrics()
	go func() {
		log.Printf("federation-sink: metrics listening on %s/metrics", metricsAddr)
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		if err := http.ListenAndServe(metricsAddr, mux); err != nil && err != http.ErrServerClosed {
			log.Printf("federation-sink: metrics server error: %v", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	var wg sync.WaitGroup
	for _, dot := range dots {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// The NATS dial happens inside the goroutine (not sequentially
			// before spawn) so one slow/unreachable DOT's dmz server can't
			// delay the other DOTs' consumers from starting — connectNATS
			// retries for up to connectRetries*connectDelay (~2 minutes) on
			// its own.
			natsURL := fmt.Sprintf("nats://%s-dmz:4222", dot)
			nc, err := connectNATS(natsURL)
			if err != nil {
				log.Printf("federation-sink: connect NATS at %s failed, dot=%s will not be federated: %v", natsURL, dot, err)
				return
			}
			defer nc.Close()

			cfg := sink.ConsumerConfig{
				NATSURL:       natsURL,
				StreamName:    fmt.Sprintf("VIKASA_%s_DMZ", strings.ToUpper(dot)),
				Durable:       durable,
				FilterSubject: fmt.Sprintf("vikasa.%s.share.>", dot),
				Database:      chDatabase,
				BatchSize:     *batchSize,
				MaxWait:       *maxWait,
				AckWait:       ackWait,
				MaxAckPending: *batchSize * maxAckPendingMultiplier,
			}

			log.Printf("federation-sink: consuming dot=%s stream=%s durable=%s filter=%s database=%s batch_size=%d max_wait=%s max_ack_pending=%d",
				dot, cfg.StreamName, cfg.Durable, cfg.FilterSubject, cfg.Database, cfg.BatchSize, cfg.MaxWait, cfg.MaxAckPending)
			if err := sink.RunWithConnAndMetrics(ctx, cfg, nc, ins, m.forDot(dot)); err != nil && ctx.Err() == nil {
				// A single DOT's unrecoverable consumer error must not take
				// down the other DOTs' federation pipelines sharing this
				// process: log and let this goroutine exit, leaving the
				// sibling goroutines (and the process) running. The
				// startup-race case this used to guard against (this DOT's
				// dmz stream not existing yet) no longer reaches here —
				// internal/sink.bindStream now retries the bind until
				// stream-init creates the stream. gen-compose's `restart:
				// on-failure` on federation-sink is the belt-and-suspenders
				// recovery for a whole-process failure (e.g. ClickHouse
				// itself going away).
				log.Printf("federation-sink: consumer loop for %s exited: %v", dot, err)
			}
		}()
	}

	// SIGTERM/SIGINT cancels ctx, which every one of the 3 consumer loops
	// above observes (RunWithConnAndMetrics returns ctx.Err()); wait for all
	// of them to actually stop before the deferred nc.Close() calls run.
	wg.Wait()
	log.Printf("federation-sink: shutting down")
}

// connectNATS retries the initial connection since federation-sink can start
// before a DOT's dmz server is ready (mirrors cmd/central-sink's connectNATS).
func connectNATS(url string) (*nats.Conn, error) {
	var err error
	for i := 1; i <= connectRetries; i++ {
		var nc *nats.Conn
		if nc, err = nats.Connect(url); err == nil {
			return nc, nil
		}
		log.Printf("federation-sink: NATS connect attempt %d/%d to %s failed: %v", i, connectRetries, url, err)
		time.Sleep(connectDelay)
	}
	return nil, err
}

// connectClickHouse parses the DSN, pins the database, and opens a native
// clickhouse-go/v2 connection.
func connectClickHouse(dsn, database string) (clickhouse.Conn, error) {
	opts, err := clickhouse.ParseDSN(dsn)
	if err != nil {
		return nil, err
	}
	opts.Auth.Database = database
	return clickhouse.Open(opts)
}

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// envIntOr parses key as a positive int, falling back to def (and logging)
// if it's unset or invalid.
func envIntOr(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		log.Printf("federation-sink: invalid %s=%q, using default %d", key, v, def)
		return def
	}
	return n
}

// envDurationOr parses key as a positive time.Duration (e.g. "1s"), falling
// back to def (and logging) if it's unset or invalid.
func envDurationOr(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		log.Printf("federation-sink: invalid %s=%q, using default %s", key, v, def)
		return def
	}
	return d
}

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// metrics holds the Prometheus collectors federation-sink exposes on
// :9091/metrics, all labeled by "dot" (unlike cmd/central-sink's per-DOT
// process, this one process drains all 3 DOTs, so the dot label is what
// makes per-pipeline health/lag distinguishable).
type metrics struct {
	eventsTotal      *prometheus.CounterVec
	flushSeconds     *prometheus.HistogramVec
	deadLettersTotal *prometheus.CounterVec
	flushErrorsTotal *prometheus.CounterVec
}

func newMetrics() *metrics {
	return &metrics{
		eventsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "vikasa_federation_sink_events_total",
			Help: "Rows successfully flushed to ClickHouse, by dot and table.",
		}, []string{"dot", "table"}),
		flushSeconds: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "vikasa_federation_sink_flush_seconds",
			Help:    "Time taken by each sink.Flush call, by dot.",
			Buckets: prometheus.DefBuckets,
		}, []string{"dot"}),
		deadLettersTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "vikasa_federation_sink_dead_letters_total",
			Help: "Envelopes that failed to parse/convert and were dead-lettered, by dot.",
		}, []string{"dot"}),
		flushErrorsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "vikasa_federation_sink_flush_errors_total",
			Help: "Flush calls that failed (batch redelivered, not acked), by dot.",
		}, []string{"dot"}),
	}
}

func (m *metrics) forDot(dot string) *sink.Metrics {
	return &sink.Metrics{
		EventInserted: func(table string, n int) { m.eventsTotal.WithLabelValues(dot, table).Add(float64(n)) },
		FlushDuration: func(seconds float64) { m.flushSeconds.WithLabelValues(dot).Observe(seconds) },
		DeadLetter:    func(n int) { m.deadLettersTotal.WithLabelValues(dot).Add(float64(n)) },
		FlushError:    func() { m.flushErrorsTotal.WithLabelValues(dot).Inc() },
	}
}
