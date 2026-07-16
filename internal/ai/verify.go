package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// --- Pure logic (unit-tested): macro substitution, panel-query extraction,
// newest-dashboard selection. Everything below this section does the live
// HTTP round trips. ---

// SubstituteMacros rewrites the Grafana ClickHouse-datasource macros a
// panel's rawSql relies on into plain SQL ClickHouse's HTTP interface can
// execute directly (Grafana itself only expands these inside its own
// datasource proxy, which `democtl verify ai-dashboard` deliberately
// bypasses -- it re-runs the query as ai_readonly directly against
// ClickHouse, the same restricted user the AI itself is scoped to):
//
//   - $__timeFilter(<expr>) -> "<expr> > now() - INTERVAL 6 HOUR"
//   - $__fromTime            -> "(now() - INTERVAL 6 HOUR)"
//   - $__toTime              -> "now()"
//
// 6 hours is an arbitrary but generous fixed window -- wide enough that a
// panel scoped to "recent" data (the AI's dashboard has no time-range
// picker of its own at verify time) has something to return without
// depending on exactly when the take was recorded.
//
// Only these two macro forms are handled, matching the models-only system
// prompt's own instruction (demo/ai/prompts/system-models-only.md, method
// step 4): every time-bounded query MUST use $__timeFilter(ce_time) (or
// $__timeFilter(bucket) on rollups). If a panel uses some other macro this
// doesn't know about, its query will fail against ClickHouse and
// `verify ai-dashboard` will report that panel FAIL -- which is itself
// useful signal that the take strayed from the prompt's method.
func SubstituteMacros(sql string) string {
	sql = substituteTimeFilter(sql)
	sql = strings.ReplaceAll(sql, "$__fromTime", "(now() - INTERVAL 6 HOUR)")
	sql = strings.ReplaceAll(sql, "$__toTime", "now()")
	return sql
}

const timeFilterMacro = "$__timeFilter("

// substituteTimeFilter replaces every $__timeFilter(<expr>) with "<expr> >
// now() - INTERVAL 6 HOUR", scanning for the matching close paren by depth
// rather than a naive non-greedy regex, so it isn't fooled by a nested
// paren in <expr> (e.g. a qualified/expression column).
func substituteTimeFilter(sql string) string {
	var b strings.Builder
	for {
		idx := strings.Index(sql, timeFilterMacro)
		if idx == -1 {
			b.WriteString(sql)
			break
		}
		b.WriteString(sql[:idx])
		rest := sql[idx+len(timeFilterMacro):]

		depth := 1
		end := -1
		for i, r := range rest {
			switch r {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					end = i
				}
			}
			if end != -1 {
				break
			}
		}
		if end == -1 {
			// Unbalanced parens -- shouldn't happen with valid SQL. Bail
			// out rather than loop forever; leave the rest untouched so the
			// eventual ClickHouse error is at least informative.
			b.WriteString(timeFilterMacro)
			b.WriteString(rest)
			break
		}
		arg := strings.TrimSpace(rest[:end])
		b.WriteString(arg)
		b.WriteString(" > now() - INTERVAL 6 HOUR")
		sql = rest[end+1:]
	}
	return b.String()
}

// PanelQuery is one panel's ClickHouse target, extracted from a dashboard's
// JSON.
type PanelQuery struct {
	PanelID    int
	PanelTitle string
	RawSQL     string
}

// rawPanel/rawTarget/rawDashboard are minimal decodes of Grafana's
// dashboard JSON -- only the fields ExtractPanelQueries needs, tolerant of
// every other field Grafana (or the AI) put there.
type rawTarget struct {
	RawSQL string `json:"rawSql"`
}

type rawPanel struct {
	ID      int         `json:"id"`
	Title   string      `json:"title"`
	Targets []rawTarget `json:"targets"`
	Panels  []rawPanel  `json:"panels"` // nested panels under a row-type panel
}

type rawDashboard struct {
	Panels []rawPanel `json:"panels"`
}

// ExtractPanelQueries walks dashboardJSON (the "dashboard" object from GET
// /api/dashboards/uid/{uid}, not the whole response envelope) and returns
// every panel target with a non-empty ClickHouse rawSql, recursing into
// row-type panels' nested panels.
func ExtractPanelQueries(dashboardJSON []byte) ([]PanelQuery, error) {
	var d rawDashboard
	if err := json.Unmarshal(dashboardJSON, &d); err != nil {
		return nil, fmt.Errorf("ai: parse dashboard JSON: %w", err)
	}
	var out []PanelQuery
	var walk func([]rawPanel)
	walk = func(panels []rawPanel) {
		for _, p := range panels {
			for _, t := range p.Targets {
				if strings.TrimSpace(t.RawSQL) == "" {
					continue
				}
				out = append(out, PanelQuery{PanelID: p.ID, PanelTitle: p.Title, RawSQL: t.RawSQL})
			}
			if len(p.Panels) > 0 {
				walk(p.Panels)
			}
		}
	}
	walk(d.Panels)
	return out, nil
}

// NewestDashboard returns the dashboard with the highest id among results
// ("newest" -- Grafana dashboard ids are assigned monotonically increasing
// on creation) and true, or the zero value and false if results is empty.
func NewestDashboard(results []dashSearchResult) (dashSearchResult, bool) {
	if len(results) == 0 {
		return dashSearchResult{}, false
	}
	newest := results[0]
	for _, r := range results[1:] {
		if r.ID > newest.ID {
			newest = r
		}
	}
	return newest, true
}

// --- Live orchestration ---

// PanelResult is one panel query's take-QA outcome.
type PanelResult struct {
	PanelID    int
	PanelTitle string
	Query      string // the query actually run, after macro substitution
	Rows       int
	Pass       bool
	Detail     string
}

// VerifyAIDashboard is `democtl verify ai-dashboard`'s take-QA gate: find
// the newest dashboard in the ai-built folder, extract every panel's
// ClickHouse query, substitute Grafana's time macros, and re-run each
// against ClickHouse as ai_readonly (the same restricted user the AI itself
// used). It returns every panel's PASS/FAIL for reporting even when it also
// returns a non-nil error; the error is non-nil iff the take should be
// rejected: any panel query errored, or every panel returned zero rows
// (nothing would actually render on screen).
func VerifyAIDashboard(ctx context.Context, grafana *Client, chURL string) ([]PanelResult, error) {
	results, err := grafana.searchFolderDashboards(ctx)
	if err != nil {
		return nil, fmt.Errorf("verify ai-dashboard: %w", err)
	}
	newest, ok := NewestDashboard(results)
	if !ok {
		return nil, fmt.Errorf(
			"verify ai-dashboard: no dashboards found in the %q folder -- has the AI built one yet? (see demo/ai/prompts)", FolderTitle)
	}

	var envelope struct {
		Dashboard json.RawMessage `json:"dashboard"`
	}
	status, body, err := grafana.do(ctx, http.MethodGet, "/api/dashboards/uid/"+newest.UID, nil, &envelope)
	if err != nil {
		return nil, fmt.Errorf("verify ai-dashboard: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("verify ai-dashboard: %w", apiError(fmt.Sprintf("fetch dashboard %s", newest.UID), status, body))
	}

	queries, err := ExtractPanelQueries(envelope.Dashboard)
	if err != nil {
		return nil, fmt.Errorf("verify ai-dashboard: %w", err)
	}
	if len(queries) == 0 {
		return nil, fmt.Errorf(
			"verify ai-dashboard: dashboard %q (%s) has no ClickHouse panel queries", newest.Title, newest.UID)
	}

	panelResults := make([]PanelResult, 0, len(queries))
	anyRows := false
	anyErr := false
	for _, q := range queries {
		substituted := SubstituteMacros(q.RawSQL)
		rows, err := runCHRowCount(ctx, chURL, substituted)
		pr := PanelResult{PanelID: q.PanelID, PanelTitle: q.PanelTitle, Query: substituted}
		if err != nil {
			pr.Pass = false
			pr.Detail = err.Error()
			anyErr = true
		} else {
			pr.Rows = rows
			pr.Pass = true
			pr.Detail = fmt.Sprintf("%d row(s)", rows)
			if rows > 0 {
				anyRows = true
			}
		}
		panelResults = append(panelResults, pr)
	}

	switch {
	case anyErr:
		return panelResults, fmt.Errorf("verify ai-dashboard: %d/%d panel quer%s errored -- see per-panel detail above",
			countFailed(panelResults), len(panelResults), plural(countFailed(panelResults)))
	case !anyRows:
		return panelResults, fmt.Errorf(
			"verify ai-dashboard: every panel query ran without error but returned 0 rows across all %d panel(s) -- nothing would render on screen",
			len(panelResults))
	default:
		return panelResults, nil
	}
}

func countFailed(results []PanelResult) int {
	n := 0
	for _, r := range results {
		if !r.Pass {
			n++
		}
	}
	return n
}

func plural(n int) string {
	if n == 1 {
		return "y failed"
	}
	return "ies failed"
}

// formatClauseRe matches a trailing FORMAT clause so runCHRowCount doesn't
// double up if a panel's rawSql already specifies one.
var formatClauseRe = regexp.MustCompile(`(?i)\bFORMAT\s+\w+\s*$`)

// runCHRowCount runs sql against ClickHouse's HTTP interface as
// ReadonlyCHUser/ReadonlyCHPassword and returns the number of result rows
// (TSV output, one line per row -- correct regardless of column count or
// the values in them, which is what "the query returns rows" means here:
// e.g. `SELECT count() FROM t` always returns exactly one row, even when
// that count is 0).
func runCHRowCount(ctx context.Context, chURL, sql string) (int, error) {
	trimmed := strings.TrimRight(strings.TrimSpace(sql), "; \t\n")
	if !formatClauseRe.MatchString(trimmed) {
		trimmed += "\nFORMAT TSV"
	}

	u := strings.TrimRight(chURL, "/") + "/?user=" + url.QueryEscape(ReadonlyCHUser) +
		"&password=" + url.QueryEscape(ReadonlyCHPassword)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(trimmed))
	if err != nil {
		return 0, fmt.Errorf("build clickhouse request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("query clickhouse: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("read clickhouse response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("clickhouse query failed (status %d): %s\nquery: %s",
			resp.StatusCode, strings.TrimSpace(string(respBody)), trimmed)
	}
	return countTSVRows(respBody), nil
}

func countTSVRows(body []byte) int {
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	n := 0
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			n++
		}
	}
	return n
}
