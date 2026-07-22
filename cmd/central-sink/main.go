// Command central-sink runs the durable pull consumer that drains the
// central JetStream stream and flushes converted rows into ClickHouse,
// acking only after a batch has landed (see internal/sink.RunWithConnAndMetrics).
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
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

	// defaultBatchSize/defaultMaxWait replace the old 256/2s pair, which
	// throttled the sink to ~7 events/s: Flush (internal/sink/registry.go)
	// issues one PrepareBatch+Send per TABLE, so a 256-message fetch spread
	// across ~14 event tables meant ~18-row inserts, ~14 tiny ClickHouse
	// inserts every 2s. Many small inserts create many small parts, which
	// ClickHouse then has to merge under pressure — the actual bottleneck.
	// Fetching thousands of messages per cycle instead means each table's
	// grouped insert is now hundreds-to-thousands of rows, so the same
	// per-table-insert-per-flush shape becomes efficient rather than
	// pathological. Both are overridable via SINK_BATCH_SIZE/SINK_MAX_WAIT
	// (or -batch-size/-max-wait) for empirical tuning without a rebuild.
	defaultBatchSize = 8000
	defaultMaxWait   = 1 * time.Second

	// maxAckPendingMultiplier sizes the pull consumer's MaxAckPending off
	// BatchSize: JetStream won't deliver more than MaxAckPending unacked
	// messages to a durable at once, so if it were left at the server
	// default (1000) a BatchSize above ~1000 could never actually be
	// filled in one Fetch — the larger BatchSize would silently do
	// nothing. 4x gives headroom for the batch in flight plus the next one
	// arriving while the current flush is still being acked.
	maxAckPendingMultiplier = 4

	ackWait        = 30 * time.Second
	connectRetries = 60
	connectDelay   = 2 * time.Second
)

func main() {
	natsURL := flag.String("nats-url", envOr("SINK_NATS_URL", nats.DefaultURL), "NATS URL")
	streamName := flag.String("stream", envOr("SINK_STREAM", ""), "JetStream stream name to bind to")
	durable := flag.String("durable", envOr("SINK_DURABLE", "central-sink"), "durable pull consumer name")
	filter := flag.String("filter", envOr("SINK_FILTER", "vikasa.>"), "subject filter for the pull consumer")
	chDSN := flag.String("ch-dsn", envOr("SINK_CH_DSN", "clickhouse://clickhouse:9000"), "ClickHouse DSN")
	chDatabase := flag.String("ch-database", envOr("SINK_CH_DATABASE", "vikasa_mardot"), "ClickHouse database")
	batchSize := flag.Int("batch-size", envIntOr("SINK_BATCH_SIZE", defaultBatchSize),
		"consumer fetch batch size (messages per Fetch, across all tables)")
	maxWait := flag.Duration("max-wait", envDurationOr("SINK_MAX_WAIT", defaultMaxWait),
		"consumer fetch max wait before flushing a partial batch")
	flag.Parse()

	if *streamName == "" {
		log.Fatalf("central-sink: SINK_STREAM (or -stream) is required")
	}

	conn, err := connectClickHouse(*chDSN, *chDatabase)
	if err != nil {
		log.Fatalf("central-sink: connect ClickHouse at %s: %v", *chDSN, err)
	}
	ins := &sink.ClickHouseInserter{Conn: conn, DB: *chDatabase}

	nc, err := connectNATS(*natsURL)
	if err != nil {
		log.Fatalf("central-sink: connect NATS at %s: %v", *natsURL, err)
	}
	defer nc.Close()

	m := newMetrics()
	go func() {
		log.Printf("central-sink: metrics listening on %s/metrics", metricsAddr)
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		if err := http.ListenAndServe(metricsAddr, mux); err != nil && err != http.ErrServerClosed {
			log.Printf("central-sink: metrics server error: %v", err)
		}
	}()

	cfg := sink.ConsumerConfig{
		NATSURL:       *natsURL,
		StreamName:    *streamName,
		Durable:       *durable,
		FilterSubject: *filter,
		Database:      *chDatabase,
		BatchSize:     *batchSize,
		MaxWait:       *maxWait,
		AckWait:       ackWait,
		MaxAckPending: *batchSize * maxAckPendingMultiplier,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	log.Printf("central-sink: consuming stream=%s durable=%s filter=%s database=%s batch_size=%d max_wait=%s max_ack_pending=%d",
		cfg.StreamName, cfg.Durable, cfg.FilterSubject, cfg.Database, cfg.BatchSize, cfg.MaxWait, cfg.MaxAckPending)
	if err := sink.RunWithConnAndMetrics(ctx, cfg, nc, ins, m.toSinkMetrics()); err != nil && ctx.Err() == nil {
		log.Fatalf("central-sink: consumer loop exited: %v", err)
	}
	log.Printf("central-sink: shutting down")
}

// connectNATS retries the initial connection since central-sink can start
// before NATS is ready (mirrors cmd/cabinet-sim's connectNATS).
func connectNATS(url string) (*nats.Conn, error) {
	var err error
	for i := 1; i <= connectRetries; i++ {
		var nc *nats.Conn
		if nc, err = nats.Connect(url); err == nil {
			return nc, nil
		}
		log.Printf("central-sink: NATS connect attempt %d/%d to %s failed: %v", i, connectRetries, url, err)
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
		log.Printf("central-sink: invalid %s=%q, using default %d", key, v, def)
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
		log.Printf("central-sink: invalid %s=%q, using default %s", key, v, def)
		return def
	}
	return d
}

// metrics holds the Prometheus collectors central-sink exposes on
// :9091/metrics; sink.RunWithConnAndMetrics reports through the callback
// adapter returned by toSinkMetrics so internal/sink stays free of a direct
// Prometheus dependency.
type metrics struct {
	eventsTotal      *prometheus.CounterVec
	flushSeconds     prometheus.Histogram
	deadLettersTotal prometheus.Counter
	flushErrorsTotal prometheus.Counter
}

func newMetrics() *metrics {
	return &metrics{
		eventsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "vikasa_sink_events_total",
			Help: "Rows successfully flushed to ClickHouse, by table.",
		}, []string{"table"}),
		flushSeconds: promauto.NewHistogram(prometheus.HistogramOpts{
			Name:    "vikasa_sink_flush_seconds",
			Help:    "Time taken by each sink.Flush call.",
			Buckets: prometheus.DefBuckets,
		}),
		deadLettersTotal: promauto.NewCounter(prometheus.CounterOpts{
			Name: "vikasa_sink_dead_letters_total",
			Help: "Envelopes that failed to parse/convert and were dead-lettered.",
		}),
		flushErrorsTotal: promauto.NewCounter(prometheus.CounterOpts{
			Name: "vikasa_sink_flush_errors_total",
			Help: "Flush calls that failed (batch redelivered, not acked).",
		}),
	}
}

func (m *metrics) toSinkMetrics() *sink.Metrics {
	return &sink.Metrics{
		EventInserted: func(table string, n int) { m.eventsTotal.WithLabelValues(table).Add(float64(n)) },
		FlushDuration: func(seconds float64) { m.flushSeconds.Observe(seconds) },
		DeadLetter:    func(n int) { m.deadLettersTotal.Add(float64(n)) },
		FlushError:    func() { m.flushErrorsTotal.Inc() },
	}
}
