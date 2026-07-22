// Package verify implements the live ClickHouse assertions democtl runs to
// prove the demo's headline claim: buffered edge, zero data loss / zero
// duplicates after a WAN cut. Query builders and response parsers are pure
// functions (unit-tested); the HTTP round trip against a live ClickHouse
// server is integration-only, exercised by cmd/democtl at demo time.
package verify

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// HeartbeatFreshnessThresholdSeconds is the max age (in seconds) of the
// freshest heartbeat row for the fleet to be considered "live". During a
// cut, the gap must clearly exceed this for --expect-stale to prove the
// cabinet actually went dark from the central vantage point.
const HeartbeatFreshnessThresholdSeconds = 45

// Database maps a DOT short name ("mardot") to its ClickHouse database
// ("vikasa_mardot").
func Database(dot string) string {
	return "vikasa_" + dot
}

// --- Query builders (pure, unit-tested) ---

// RecentEventsQuery counts events_raw rows landed in the last 2 minutes —
// proof that fresh data is currently reaching ClickHouse.
func RecentEventsQuery(db string) string {
	return fmt.Sprintf(
		"SELECT count() FROM %s.events_raw WHERE ce_time > now() - INTERVAL 2 MINUTE FORMAT TSV", db)
}

// HeartbeatFreshnessQuery returns the age, in seconds, of the most recent
// gateway heartbeat ingested for db.
func HeartbeatFreshnessQuery(db string) string {
	return fmt.Sprintf(
		"SELECT dateDiff('second', max(ingested_at), now64(3)) FROM %s.heartbeats FORMAT TSV", db)
}

// DeadLetterQuery counts events_dead_letter rows received in the last hour
// — must be zero for a clean run (malformed/unroutable envelopes only).
func DeadLetterQuery(db string) string {
	return fmt.Sprintf(
		"SELECT count() FROM %s.events_dead_letter WHERE received_at > now() - INTERVAL 1 HOUR FORMAT TSV", db)
}

// OptimizeEventsRawQuery forces a synchronous merge of events_raw so the
// dedup check below sees ReplacingMergeTree's final, deduplicated state
// rather than whatever hasn't merged yet.
func OptimizeEventsRawQuery(db string) string {
	return fmt.Sprintf("OPTIMIZE TABLE %s.events_raw FINAL", db)
}

// DedupCountsQuery returns two tab-separated values: the raw row count and
// the distinct ce_id count over the last 30 minutes. The difference between
// them is the duplicate count, which must be zero.
func DedupCountsQuery(db string) string {
	return fmt.Sprintf(
		"SELECT count(), countDistinct(ce_id) FROM %s.events_raw WHERE ce_time > now() - INTERVAL 30 MINUTE FORMAT TSV", db)
}

// --- HTTP execution ---

// chQuery runs one query against ClickHouse's HTTP interface and returns
// the trimmed response body.
func chQuery(ctx context.Context, ch, query string) (string, error) {
	// POST with the query as the request body, not GET with ?query=...:
	// ClickHouse's HTTP interface treats GET as read-only and rejects
	// mutating statements (OPTIMIZE TABLE ... FINAL, needed by the dedup
	// check) with "Cannot execute query in readonly mode" (error 164).
	// POST works uniformly for both SELECTs and mutations.
	u := strings.TrimRight(ch, "/") + "/"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(query))
	if err != nil {
		return "", fmt.Errorf("verify: build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("verify: query clickhouse at %s: %w", ch, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("verify: read clickhouse response: %w", err)
	}
	text := strings.TrimSpace(string(body))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("verify: clickhouse query failed (status %d): %s\nquery: %s",
			resp.StatusCode, text, query)
	}
	return text, nil
}

// Query runs an arbitrary SQL statement against ClickHouse's HTTP interface
// and returns the trimmed response body. Exported so callers outside this
// package (internal/tour's phase assertions, which need cabinet-scoped and
// federation queries beyond the fixed baseline/dedup checks below) reuse the
// same POST round trip — request construction, status handling, body
// reading — instead of reimplementing it.
func Query(ctx context.Context, ch, query string) (string, error) {
	return chQuery(ctx, ch, query)
}

// --- Response parsers (pure, unit-tested) ---

// ParseScalarInt parses a single-integer TSV response body (e.g. from a
// `SELECT count(...) ... FORMAT TSV` query). Exported alongside Query for
// callers running ad hoc scalar queries outside the fixed checks below.
func ParseScalarInt(s string) (int64, error) {
	return parseCount(s)
}

// parseCount parses a single-integer TSV response body.
func parseCount(s string) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("verify: parse count %q: %w", s, err)
	}
	return n, nil
}

// parseDedupCounts parses ClickHouse's "count\tdistinct" TSV output.
func parseDedupCounts(s string) (count, distinct int64, err error) {
	fields := strings.Split(strings.TrimSpace(s), "\t")
	if len(fields) != 2 {
		return 0, 0, fmt.Errorf("verify: unexpected dedup response %q (want 2 tab-separated fields)", s)
	}
	count, err = strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("verify: parse dedup count %q: %w", fields[0], err)
	}
	distinct, err = strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("verify: parse dedup distinct %q: %w", fields[1], err)
	}
	return count, distinct, nil
}

// --- Checks ---

// Deps are the external inputs a Check needs to run.
type Deps struct {
	CH string // ClickHouse HTTP base URL, e.g. http://localhost:8123
	DB string // ClickHouse database, e.g. vikasa_mardot

	// ExpectStale inverts the heartbeat-freshness check: instead of
	// asserting the fleet is live, it asserts the freshness gap is
	// visible (age > HeartbeatFreshnessThresholdSeconds). Used during the
	// cut phase of the demo to prove the outage is observable from
	// ClickHouse's vantage point.
	ExpectStale bool
}

// CheckResult is one named assertion's outcome.
type CheckResult struct {
	Name   string
	Pass   bool
	Detail string
}

// Check is one named assertion against a live system. The returned error is
// reserved for infrastructure failures (ClickHouse unreachable, unparsable
// response) that prevent the assertion from being evaluated at all — a
// failed assertion is reported via CheckResult.Pass, not an error.
type Check func(ctx context.Context, deps Deps) (CheckResult, error)

// CheckRecentEvents asserts events_raw has received rows in the last 2
// minutes: fresh data is currently reaching ClickHouse.
func CheckRecentEvents(ctx context.Context, deps Deps) (CheckResult, error) {
	out, err := chQuery(ctx, deps.CH, RecentEventsQuery(deps.DB))
	if err != nil {
		return CheckResult{}, err
	}
	n, err := parseCount(out)
	if err != nil {
		return CheckResult{}, err
	}
	return CheckResult{
		Name:   "recent-events",
		Pass:   n > 0,
		Detail: fmt.Sprintf("events_raw rows with ce_time in last 2m = %d (want > 0)", n),
	}, nil
}

// CheckHeartbeatFreshness asserts the freshest heartbeat is within
// HeartbeatFreshnessThresholdSeconds — or, with deps.ExpectStale, asserts
// the opposite: the gap has grown past the threshold, proving a cut is
// visible.
func CheckHeartbeatFreshness(ctx context.Context, deps Deps) (CheckResult, error) {
	out, err := chQuery(ctx, deps.CH, HeartbeatFreshnessQuery(deps.DB))
	if err != nil {
		return CheckResult{}, err
	}
	age, err := parseCount(out)
	if err != nil {
		return CheckResult{}, err
	}
	fresh := age <= HeartbeatFreshnessThresholdSeconds
	if deps.ExpectStale {
		return CheckResult{
			Name: "heartbeat-freshness[expect-stale]",
			Pass: !fresh,
			Detail: fmt.Sprintf("max(ingested_at) age = %ds (want > %ds: gap must be visible)",
				age, HeartbeatFreshnessThresholdSeconds),
		}, nil
	}
	return CheckResult{
		Name: "heartbeat-freshness",
		Pass: fresh,
		Detail: fmt.Sprintf("max(ingested_at) age = %ds (want <= %ds)",
			age, HeartbeatFreshnessThresholdSeconds),
	}, nil
}

// CheckDeadLetters asserts no envelopes were dead-lettered in the last hour.
func CheckDeadLetters(ctx context.Context, deps Deps) (CheckResult, error) {
	out, err := chQuery(ctx, deps.CH, DeadLetterQuery(deps.DB))
	if err != nil {
		return CheckResult{}, err
	}
	n, err := parseCount(out)
	if err != nil {
		return CheckResult{}, err
	}
	return CheckResult{
		Name:   "dead-letters",
		Pass:   n == 0,
		Detail: fmt.Sprintf("events_dead_letter rows received in last 1h = %d (want 0)", n),
	}, nil
}

// baselineChecks is the fixed set `verify baseline` runs, in report order.
var baselineChecks = []Check{CheckRecentEvents, CheckHeartbeatFreshness, CheckDeadLetters}

// RunBaseline runs every baseline Check against deps and returns all
// results (even if an earlier one failed) so the caller can print a full
// PASS/FAIL report. It returns an error only on infrastructure failure.
func RunBaseline(ctx context.Context, deps Deps) ([]CheckResult, error) {
	results := make([]CheckResult, 0, len(baselineChecks))
	for _, check := range baselineChecks {
		r, err := check(ctx, deps)
		if err != nil {
			return results, err
		}
		results = append(results, r)
	}
	return results, nil
}

// DedupResult is the zero-duplicates proof: raw row count, distinct ce_id
// count, and their difference (must be 0) over the last 30 minutes.
type DedupResult struct {
	Count    int64
	Distinct int64
	Diff     int64
}

// RunDedup runs `OPTIMIZE TABLE events_raw FINAL` (so ReplacingMergeTree's
// dedup state is current) and then compares count() against
// countDistinct(ce_id). Diff == 0 is the demo's zero-duplicates proof.
func RunDedup(ctx context.Context, deps Deps) (DedupResult, error) {
	if _, err := chQuery(ctx, deps.CH, OptimizeEventsRawQuery(deps.DB)); err != nil {
		return DedupResult{}, fmt.Errorf("verify: optimize events_raw final: %w", err)
	}
	out, err := chQuery(ctx, deps.CH, DedupCountsQuery(deps.DB))
	if err != nil {
		return DedupResult{}, err
	}
	count, distinct, err := parseDedupCounts(out)
	if err != nil {
		return DedupResult{}, err
	}
	return DedupResult{Count: count, Distinct: distinct, Diff: count - distinct}, nil
}

// DefaultTimeout bounds a single democtl verify invocation (a handful of
// sequential ClickHouse HTTP queries).
const DefaultTimeout = 30 * time.Second
