package ai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSubstituteMacrosTimeFilter(t *testing.T) {
	cases := []struct {
		name, sql, want string
	}{
		{
			"plain column",
			"SELECT count() FROM t WHERE $__timeFilter(ce_time)",
			"SELECT count() FROM t WHERE ce_time > now() - INTERVAL 6 HOUR",
		},
		{
			"qualified column",
			"SELECT count() FROM t p WHERE $__timeFilter(p.ce_time)",
			"SELECT count() FROM t p WHERE p.ce_time > now() - INTERVAL 6 HOUR",
		},
		{
			"rollup bucket column",
			"SELECT sum(n) FROM events_1m WHERE $__timeFilter(bucket)",
			"SELECT sum(n) FROM events_1m WHERE bucket > now() - INTERVAL 6 HOUR",
		},
		{
			"nested parens in the argument",
			"SELECT 1 WHERE $__timeFilter(toStartOfInterval(ce_time, INTERVAL 1 MINUTE))",
			"SELECT 1 WHERE toStartOfInterval(ce_time, INTERVAL 1 MINUTE) > now() - INTERVAL 6 HOUR",
		},
		{
			"two occurrences in one query",
			"SELECT * FROM t WHERE $__timeFilter(ce_time) AND $__timeFilter(other_time)",
			"SELECT * FROM t WHERE ce_time > now() - INTERVAL 6 HOUR AND other_time > now() - INTERVAL 6 HOUR",
		},
		{
			"no macro present",
			"SELECT count() FROM t",
			"SELECT count() FROM t",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SubstituteMacros(tc.sql); got != tc.want {
				t.Errorf("SubstituteMacros(%q) = %q, want %q", tc.sql, got, tc.want)
			}
		})
	}
}

func TestSubstituteMacrosFromToTime(t *testing.T) {
	sql := "SELECT * FROM t WHERE ce_time BETWEEN $__fromTime AND $__toTime"
	want := "SELECT * FROM t WHERE ce_time BETWEEN (now() - INTERVAL 6 HOUR) AND now()"
	if got := SubstituteMacros(sql); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSubstituteMacrosUnbalancedParensDoesNotHang(t *testing.T) {
	// Malformed input (missing close paren) must not loop forever -- it
	// should just come back with the macro left in place so the eventual
	// ClickHouse syntax error is informative.
	sql := "SELECT * FROM t WHERE $__timeFilter(ce_time"
	got := SubstituteMacros(sql)
	if !strings.Contains(got, "$__timeFilter(") {
		t.Errorf("expected the unbalanced macro to be left untouched, got %q", got)
	}
}

func TestExtractPanelQueriesTopLevel(t *testing.T) {
	dashboardJSON := []byte(`{
		"panels": [
			{"id": 1, "title": "Speed", "targets": [{"rawSql": "SELECT 1 FROM t"}]},
			{"id": 2, "title": "Text panel (no query)", "targets": []},
			{"id": 3, "title": "Map", "targets": [{"rawSql": "SELECT 2 FROM t"}, {"rawSql": ""}]}
		]
	}`)
	got, err := ExtractPanelQueries(dashboardJSON)
	if err != nil {
		t.Fatalf("ExtractPanelQueries: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d panel queries, want 2 (empty-rawSql targets and query-less panels must be skipped): %+v", len(got), got)
	}
	if got[0].PanelID != 1 || got[0].RawSQL != "SELECT 1 FROM t" {
		t.Errorf("got[0] = %+v, want panel 1's query", got[0])
	}
	if got[1].PanelID != 3 || got[1].RawSQL != "SELECT 2 FROM t" {
		t.Errorf("got[1] = %+v, want panel 3's query", got[1])
	}
}

func TestExtractPanelQueriesNestedRowPanels(t *testing.T) {
	dashboardJSON := []byte(`{
		"panels": [
			{"id": 1, "type": "row", "title": "Row", "panels": [
				{"id": 2, "title": "Nested", "targets": [{"rawSql": "SELECT 3 FROM t"}]}
			]}
		]
	}`)
	got, err := ExtractPanelQueries(dashboardJSON)
	if err != nil {
		t.Fatalf("ExtractPanelQueries: %v", err)
	}
	if len(got) != 1 || got[0].PanelID != 2 || got[0].RawSQL != "SELECT 3 FROM t" {
		t.Fatalf("got %+v, want the nested panel's single query", got)
	}
}

func TestExtractPanelQueriesNoPanels(t *testing.T) {
	got, err := ExtractPanelQueries([]byte(`{"panels": []}`))
	if err != nil {
		t.Fatalf("ExtractPanelQueries: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d queries, want 0", len(got))
	}
}

func TestExtractPanelQueriesInvalidJSON(t *testing.T) {
	if _, err := ExtractPanelQueries([]byte("not json")); err == nil {
		t.Error("want error for invalid dashboard JSON, got nil")
	}
}

func TestNewestDashboardPicksHighestID(t *testing.T) {
	results := []dashSearchResult{
		{ID: 10, UID: "old", Title: "Old"},
		{ID: 30, UID: "newest", Title: "Newest"},
		{ID: 20, UID: "mid", Title: "Mid"},
	}
	got, ok := NewestDashboard(results)
	if !ok {
		t.Fatal("NewestDashboard: want ok=true")
	}
	if got.UID != "newest" {
		t.Errorf("got %+v, want the id=30 entry", got)
	}
}

func TestNewestDashboardEmpty(t *testing.T) {
	_, ok := NewestDashboard(nil)
	if ok {
		t.Error("NewestDashboard(nil): want ok=false")
	}
}

// --- VerifyAIDashboard (integration-shaped, but driven by httptest stubs
// so no live Grafana/ClickHouse is needed) ---

// stubGrafana serves search/dashboard-fetch responses from a fixed table
// keyed by "METHOD path".
func stubGrafana(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.RequestURI()
		if body, ok := routes[key]; ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(body))
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"message":"not found: ` + key + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func stubClickHouse(t *testing.T, rowsPerQuery map[string]int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		q := string(body)
		for substr, rows := range rowsPerQuery {
			if strings.Contains(q, substr) {
				w.WriteHeader(http.StatusOK)
				for i := 0; i < rows; i++ {
					_, _ = w.Write([]byte("1\n"))
				}
				return
			}
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("Code: 60. DB::Exception: Unknown table"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestVerifyAIDashboardPass(t *testing.T) {
	grafana := stubGrafana(t, map[string]string{
		"GET /api/search?folderUIDs=ai-built&type=dash-db": `[{"id":5,"uid":"abc123","title":"Corridor Operations"}]`,
		"GET /api/dashboards/uid/abc123": `{"dashboard":{"panels":[
			{"id":1,"title":"Speed","targets":[{"rawSql":"SELECT speed FROM t WHERE $__timeFilter(ce_time)"}]}
		]}}`,
	})
	ch := stubClickHouse(t, map[string]int{"speed FROM t": 3})

	client := NewClient(grafana.URL, "")
	results, err := VerifyAIDashboard(context.Background(), client, ch.URL)
	if err != nil {
		t.Fatalf("VerifyAIDashboard: unexpected error: %v", err)
	}
	if len(results) != 1 || !results[0].Pass || results[0].Rows != 3 {
		t.Fatalf("got %+v, want one passing panel with 3 rows", results)
	}
	if strings.Contains(results[0].Query, "$__timeFilter") {
		t.Errorf("query sent to clickhouse still contains an unexpanded macro: %q", results[0].Query)
	}
}

func TestVerifyAIDashboardFailsWhenNoDashboards(t *testing.T) {
	grafana := stubGrafana(t, map[string]string{
		"GET /api/search?folderUIDs=ai-built&type=dash-db": `[]`,
	})
	client := NewClient(grafana.URL, "")
	_, err := VerifyAIDashboard(context.Background(), client, "http://unused")
	if err == nil {
		t.Fatal("want error when the ai-built folder has no dashboards, got nil")
	}
}

func TestVerifyAIDashboardFailsWhenAllPanelsEmpty(t *testing.T) {
	grafana := stubGrafana(t, map[string]string{
		"GET /api/search?folderUIDs=ai-built&type=dash-db": `[{"id":5,"uid":"abc123","title":"Corridor Operations"}]`,
		"GET /api/dashboards/uid/abc123": `{"dashboard":{"panels":[
			{"id":1,"title":"Speed","targets":[{"rawSql":"SELECT speed FROM t WHERE $__timeFilter(ce_time)"}]}
		]}}`,
	})
	ch := stubClickHouse(t, map[string]int{"speed FROM t": 0})

	client := NewClient(grafana.URL, "")
	results, err := VerifyAIDashboard(context.Background(), client, ch.URL)
	if err == nil {
		t.Fatal("want error when every panel returns 0 rows, got nil")
	}
	if len(results) != 1 || !results[0].Pass {
		t.Errorf("panel query itself should still PASS (it ran without error, just returned 0 rows): %+v", results)
	}
}

func TestVerifyAIDashboardFailsOnPanelQueryError(t *testing.T) {
	grafana := stubGrafana(t, map[string]string{
		"GET /api/search?folderUIDs=ai-built&type=dash-db": `[{"id":5,"uid":"abc123","title":"Corridor Operations"}]`,
		"GET /api/dashboards/uid/abc123": `{"dashboard":{"panels":[
			{"id":1,"title":"Broken","targets":[{"rawSql":"SELECT * FROM does_not_exist"}]},
			{"id":2,"title":"OK","targets":[{"rawSql":"SELECT speed FROM t"}]}
		]}}`,
	})
	ch := stubClickHouse(t, map[string]int{"speed FROM t": 2})

	client := NewClient(grafana.URL, "")
	results, err := VerifyAIDashboard(context.Background(), client, ch.URL)
	if err == nil {
		t.Fatal("want error when a panel query errors, got nil")
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2 (both panels reported even though one failed)", len(results))
	}
	if results[0].Pass {
		t.Errorf("panel 1 (does_not_exist) should FAIL: %+v", results[0])
	}
	if !results[1].Pass {
		t.Errorf("panel 2 (valid query) should still PASS: %+v", results[1])
	}
}

func TestVerifyAIDashboardPicksNewestAmongMultiple(t *testing.T) {
	grafana := stubGrafana(t, map[string]string{
		"GET /api/search?folderUIDs=ai-built&type=dash-db": `[
			{"id":1,"uid":"old","title":"Old take"},
			{"id":9,"uid":"new","title":"Newest take"}
		]`,
		"GET /api/dashboards/uid/new": `{"dashboard":{"panels":[
			{"id":1,"title":"OK","targets":[{"rawSql":"SELECT speed FROM t"}]}
		]}}`,
	})
	ch := stubClickHouse(t, map[string]int{"speed FROM t": 1})

	client := NewClient(grafana.URL, "")
	if _, err := VerifyAIDashboard(context.Background(), client, ch.URL); err != nil {
		t.Fatalf("VerifyAIDashboard: %v (expected it to fetch the newest uid, not the old one)", err)
	}
}
