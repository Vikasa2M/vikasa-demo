package verify

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDatabase(t *testing.T) {
	if got, want := Database("mardot"), "vikasa_mardot"; got != want {
		t.Errorf("Database(mardot) = %q, want %q", got, want)
	}
	if got, want := Database("veldot"), "vikasa_veldot"; got != want {
		t.Errorf("Database(veldot) = %q, want %q", got, want)
	}
}

func TestQueryBuilders(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{
			"RecentEventsQuery",
			RecentEventsQuery("vikasa_mardot"),
			"SELECT count() FROM vikasa_mardot.events_raw WHERE ce_time > now() - INTERVAL 2 MINUTE FORMAT TSV",
		},
		{
			"HeartbeatFreshnessQuery",
			HeartbeatFreshnessQuery("vikasa_mardot"),
			"SELECT dateDiff('second', max(ingested_at), now64(3)) FROM vikasa_mardot.heartbeats FORMAT TSV",
		},
		{
			"DeadLetterQuery",
			DeadLetterQuery("vikasa_mardot"),
			"SELECT count() FROM vikasa_mardot.events_dead_letter WHERE received_at > now() - INTERVAL 1 HOUR FORMAT TSV",
		},
		{
			"OptimizeEventsRawQuery",
			OptimizeEventsRawQuery("vikasa_mardot"),
			"OPTIMIZE TABLE vikasa_mardot.events_raw FINAL",
		},
		{
			"DedupCountsQuery",
			DedupCountsQuery("vikasa_mardot"),
			"SELECT count(), countDistinct(ce_id) FROM vikasa_mardot.events_raw WHERE ce_time > now() - INTERVAL 30 MINUTE FORMAT TSV",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("got %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func TestParseCount(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"42\n", 42, false},
		{"0", 0, false},
		{"  7  ", 7, false},
		{"not-a-number", 0, true},
		{"", 0, true},
	}
	for _, tc := range cases {
		got, err := parseCount(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseCount(%q): want error, got nil", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseCount(%q): unexpected error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("parseCount(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseDedupCounts(t *testing.T) {
	count, distinct, err := parseDedupCounts("100\t100\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 100 || distinct != 100 {
		t.Errorf("got count=%d distinct=%d, want 100/100", count, distinct)
	}

	count, distinct, err = parseDedupCounts("105\t100")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if diff := count - distinct; diff != 5 {
		t.Errorf("diff = %d, want 5", diff)
	}

	if _, _, err := parseDedupCounts("only-one-field"); err == nil {
		t.Error("want error for malformed single-field response")
	}
	if _, _, err := parseDedupCounts("abc\t100"); err == nil {
		t.Error("want error for non-numeric count field")
	}
	if _, _, err := parseDedupCounts("100\tabc"); err == nil {
		t.Error("want error for non-numeric distinct field")
	}
}

// stubCH returns an httptest server that serves a fixed response body for
// every query, and records the last query string it received (URL-decoded)
// so tests can assert the right SQL was sent.
func stubCH(t *testing.T, body string) (*httptest.Server, *string) {
	t.Helper()
	var lastQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		lastQuery = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &lastQuery
}

func TestCheckRecentEventsPassFail(t *testing.T) {
	srv, lastQuery := stubCH(t, "3\n")
	r, err := CheckRecentEvents(context.Background(), Deps{CH: srv.URL, DB: "vikasa_mardot"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !r.Pass {
		t.Errorf("want Pass=true for count=3, got Detail=%q", r.Detail)
	}
	if !strings.Contains(*lastQuery, "events_raw") {
		t.Errorf("query sent did not target events_raw: %q", *lastQuery)
	}

	srv2, _ := stubCH(t, "0\n")
	r2, err := CheckRecentEvents(context.Background(), Deps{CH: srv2.URL, DB: "vikasa_mardot"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r2.Pass {
		t.Errorf("want Pass=false for count=0")
	}
}

func TestCheckHeartbeatFreshnessNormalAndExpectStale(t *testing.T) {
	// Fresh (10s old): normal mode passes, expect-stale mode fails.
	srvFresh, _ := stubCH(t, "10\n")
	r, err := CheckHeartbeatFreshness(context.Background(), Deps{CH: srvFresh.URL, DB: "vikasa_mardot"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !r.Pass {
		t.Errorf("normal mode: want Pass=true for age=10s, got Detail=%q", r.Detail)
	}

	rStale, err := CheckHeartbeatFreshness(context.Background(), Deps{CH: srvFresh.URL, DB: "vikasa_mardot", ExpectStale: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rStale.Pass {
		t.Errorf("expect-stale mode: want Pass=false for age=10s (fresh, not stale), got Detail=%q", rStale.Detail)
	}

	// Stale (90s old): normal mode fails, expect-stale mode passes.
	srvStale, _ := stubCH(t, "90\n")
	r2, err := CheckHeartbeatFreshness(context.Background(), Deps{CH: srvStale.URL, DB: "vikasa_mardot"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r2.Pass {
		t.Errorf("normal mode: want Pass=false for age=90s, got Detail=%q", r2.Detail)
	}

	r2Stale, err := CheckHeartbeatFreshness(context.Background(), Deps{CH: srvStale.URL, DB: "vikasa_mardot", ExpectStale: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !r2Stale.Pass {
		t.Errorf("expect-stale mode: want Pass=true for age=90s (stale, as expected), got Detail=%q", r2Stale.Detail)
	}
}

func TestCheckDeadLetters(t *testing.T) {
	srvClean, _ := stubCH(t, "0\n")
	r, err := CheckDeadLetters(context.Background(), Deps{CH: srvClean.URL, DB: "vikasa_mardot"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !r.Pass {
		t.Errorf("want Pass=true for 0 dead letters, got Detail=%q", r.Detail)
	}

	srvDirty, _ := stubCH(t, "2\n")
	r2, err := CheckDeadLetters(context.Background(), Deps{CH: srvDirty.URL, DB: "vikasa_mardot"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r2.Pass {
		t.Errorf("want Pass=false for 2 dead letters")
	}
}

func TestCheckReturnsErrorOnHTTPFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Code: 60. DB::Exception: Table doesn't exist"))
	}))
	t.Cleanup(srv.Close)

	if _, err := CheckRecentEvents(context.Background(), Deps{CH: srv.URL, DB: "vikasa_mardot"}); err == nil {
		t.Error("want error when ClickHouse returns 500")
	}
}

func TestRunBaselineReportsAllChecksInOrder(t *testing.T) {
	// A server that answers each of the three baseline queries differently
	// based on which table it targets, so RunBaseline's ordering and
	// aggregation can be verified in one pass.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		q := string(b)
		w.WriteHeader(http.StatusOK)
		switch {
		case strings.Contains(q, "events_raw"):
			_, _ = w.Write([]byte("5\n"))
		case strings.Contains(q, "heartbeats"):
			_, _ = w.Write([]byte("5\n"))
		case strings.Contains(q, "events_dead_letter"):
			_, _ = w.Write([]byte("0\n"))
		}
	}))
	t.Cleanup(srv.Close)

	results, err := RunBaseline(context.Background(), Deps{CH: srv.URL, DB: "vikasa_mardot"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("want 3 results, got %d", len(results))
	}
	wantNames := []string{"recent-events", "heartbeat-freshness", "dead-letters"}
	for i, name := range wantNames {
		if results[i].Name != name {
			t.Errorf("results[%d].Name = %q, want %q", i, results[i].Name, name)
		}
		if !results[i].Pass {
			t.Errorf("results[%d] (%s) unexpectedly failed: %s", i, name, results[i].Detail)
		}
	}
}

func TestRunDedupOptimizesThenComparesCounts(t *testing.T) {
	var sawOptimize bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		q := string(b)
		w.WriteHeader(http.StatusOK)
		if strings.HasPrefix(q, "OPTIMIZE") {
			sawOptimize = true
			return // OPTIMIZE has no result body
		}
		_, _ = w.Write([]byte("100\t100\n"))
	}))
	t.Cleanup(srv.Close)

	res, err := RunDedup(context.Background(), Deps{CH: srv.URL, DB: "vikasa_mardot"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !sawOptimize {
		t.Error("RunDedup did not send the OPTIMIZE TABLE ... FINAL query before counting")
	}
	if res.Count != 100 || res.Distinct != 100 || res.Diff != 0 {
		t.Errorf("got %+v, want Count=100 Distinct=100 Diff=0", res)
	}
}
