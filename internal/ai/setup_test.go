package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// scriptedGrafana replays a fixed sequence of (status, body) responses, one
// per request received, and records every request's method+path+body for
// assertions -- used to exercise EnsureFolder/EnsureServiceAccount/
// EnsureServiceAccountToken's conflict/idempotency handling without a live
// Grafana.
type scriptedGrafana struct {
	responses []scriptedResponse
	requests  []recordedRequest
}

type scriptedResponse struct {
	status int
	body   string
}

type recordedRequest struct {
	method string
	path   string
	body   string
}

func (s *scriptedGrafana) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	i := 0
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.requests = append(s.requests, recordedRequest{method: r.Method, path: r.URL.Path + "?" + r.URL.RawQuery, body: string(body)})
		if i >= len(s.responses) {
			t.Fatalf("scriptedGrafana: received unexpected request #%d: %s %s", i, r.Method, r.URL.Path)
		}
		resp := s.responses[i]
		i++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.status)
		_, _ = w.Write([]byte(resp.body))
	}
}

func TestEnsureFolderCreatesOnFirstCall(t *testing.T) {
	script := &scriptedGrafana{responses: []scriptedResponse{
		{http.StatusOK, `{"id":1,"uid":"ai-built","title":"AI Built"}`},
	}}
	srv := httptest.NewServer(script.handler(t))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	if err := c.EnsureFolder(context.Background()); err != nil {
		t.Fatalf("EnsureFolder: %v", err)
	}
	if len(script.requests) != 1 || script.requests[0].method != http.MethodPost || script.requests[0].path != "/api/folders?" {
		t.Errorf("unexpected requests: %+v", script.requests)
	}
}

func TestEnsureFolderIgnoresConflict(t *testing.T) {
	for _, status := range []int{http.StatusConflict, http.StatusPreconditionFailed} {
		script := &scriptedGrafana{responses: []scriptedResponse{
			{status, `{"message":"conflict"}`},
		}}
		srv := httptest.NewServer(script.handler(t))
		err := NewClient(srv.URL, "").EnsureFolder(context.Background())
		srv.Close()
		if err != nil {
			t.Errorf("EnsureFolder: status %d should be treated as already-exists, got error: %v", status, err)
		}
	}
}

func TestEnsureFolderErrorsOnUnexpectedStatus(t *testing.T) {
	script := &scriptedGrafana{responses: []scriptedResponse{
		{http.StatusInternalServerError, `{"message":"boom"}`},
	}}
	srv := httptest.NewServer(script.handler(t))
	defer srv.Close()
	if err := NewClient(srv.URL, "").EnsureFolder(context.Background()); err == nil {
		t.Error("want error for an unrecognized status, got nil")
	}
}

func TestEnsureServiceAccountCreatesOnFirstCall(t *testing.T) {
	script := &scriptedGrafana{responses: []scriptedResponse{
		{http.StatusCreated, `{"id":2,"name":"ai-dashboard-builder"}`},
	}}
	srv := httptest.NewServer(script.handler(t))
	defer srv.Close()

	id, err := NewClient(srv.URL, "").EnsureServiceAccount(context.Background())
	if err != nil {
		t.Fatalf("EnsureServiceAccount: %v", err)
	}
	if id != 2 {
		t.Errorf("got id %d, want 2", id)
	}
}

func TestEnsureServiceAccountFallsBackToSearchOnConflict(t *testing.T) {
	script := &scriptedGrafana{responses: []scriptedResponse{
		{http.StatusBadRequest, `{"statusCode":400,"messageId":"serviceaccounts.ErrAlreadyExists","message":"service account already exists"}`},
		{http.StatusOK, `{"totalCount":1,"serviceAccounts":[{"id":2,"name":"ai-dashboard-builder"}]}`},
	}}
	srv := httptest.NewServer(script.handler(t))
	defer srv.Close()

	id, err := NewClient(srv.URL, "").EnsureServiceAccount(context.Background())
	if err != nil {
		t.Fatalf("EnsureServiceAccount: %v", err)
	}
	if id != 2 {
		t.Errorf("got id %d, want 2 (found via search fallback)", id)
	}
}

func TestEnsureServiceAccountErrorsWhenSearchFallbackFindsNothing(t *testing.T) {
	script := &scriptedGrafana{responses: []scriptedResponse{
		{http.StatusBadRequest, `{"message":"boom"}`},
		{http.StatusOK, `{"totalCount":0,"serviceAccounts":[]}`},
	}}
	srv := httptest.NewServer(script.handler(t))
	defer srv.Close()

	if _, err := NewClient(srv.URL, "").EnsureServiceAccount(context.Background()); err == nil {
		t.Error("want error when create fails and search finds no existing account, got nil")
	}
}

func TestEnsureServiceAccountTokenRotatesExistingByName(t *testing.T) {
	script := &scriptedGrafana{responses: []scriptedResponse{
		// GET tokens -- one already named "demo".
		{http.StatusOK, `[{"id":1,"name":"demo"}]`},
		// DELETE that token.
		{http.StatusOK, `{"message":"deleted"}`},
		// POST a fresh one.
		{http.StatusOK, `{"id":2,"name":"demo","key":"glsa_fresh"}`},
	}}
	srv := httptest.NewServer(script.handler(t))
	defer srv.Close()

	key, err := NewClient(srv.URL, "").EnsureServiceAccountToken(context.Background(), 2)
	if err != nil {
		t.Fatalf("EnsureServiceAccountToken: %v", err)
	}
	if key != "glsa_fresh" {
		t.Errorf("got key %q, want glsa_fresh", key)
	}
	if script.requests[1].method != http.MethodDelete {
		t.Errorf("expected the stale token to be deleted before recreating; requests: %+v", script.requests)
	}
}

func TestEnsureServiceAccountTokenSkipsDeleteWhenNoneExists(t *testing.T) {
	script := &scriptedGrafana{responses: []scriptedResponse{
		{http.StatusOK, `[]`},
		{http.StatusOK, `{"id":1,"name":"demo","key":"glsa_first"}`},
	}}
	srv := httptest.NewServer(script.handler(t))
	defer srv.Close()

	key, err := NewClient(srv.URL, "").EnsureServiceAccountToken(context.Background(), 2)
	if err != nil {
		t.Fatalf("EnsureServiceAccountToken: %v", err)
	}
	if key != "glsa_first" {
		t.Errorf("got key %q, want glsa_first", key)
	}
	if len(script.requests) != 2 {
		t.Errorf("expected exactly list+create (no delete), got %d requests: %+v", len(script.requests), script.requests)
	}
}

func TestGrantFolderEditorSendsScopedPermission(t *testing.T) {
	script := &scriptedGrafana{responses: []scriptedResponse{
		{http.StatusOK, `{"message":"Folder permissions updated"}`},
	}}
	srv := httptest.NewServer(script.handler(t))
	defer srv.Close()

	if err := NewClient(srv.URL, "").GrantFolderEditor(context.Background(), 2); err != nil {
		t.Fatalf("GrantFolderEditor: %v", err)
	}
	var sent struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal([]byte(script.requests[0].body), &sent); err != nil {
		t.Fatalf("decode sent body: %v", err)
	}
	if len(sent.Items) != 1 {
		t.Fatalf("want exactly one permission item (the folder must be scoped to ONLY this SA), got %+v", sent.Items)
	}
	if id, _ := sent.Items[0]["userId"].(float64); int64(id) != 2 {
		t.Errorf("userId = %v, want 2", sent.Items[0]["userId"])
	}
	if perm, _ := sent.Items[0]["permission"].(float64); int(perm) != folderEditPermission {
		t.Errorf("permission = %v, want %d (Editor)", sent.Items[0]["permission"], folderEditPermission)
	}
}

func TestSetupEndToEndAgainstScriptedGrafana(t *testing.T) {
	script := &scriptedGrafana{responses: []scriptedResponse{
		{http.StatusOK, `{"id":1,"uid":"ai-built","title":"AI Built"}`}, // EnsureFolder
		{http.StatusCreated, `{"id":2,"name":"ai-dashboard-builder"}`},  // EnsureServiceAccount
		{http.StatusOK, `{"message":"ok"}`},                             // GrantFolderEditor
		{http.StatusOK, `[]`},                                           // token list (none yet)
		{http.StatusOK, `{"id":1,"name":"demo","key":"glsa_abc"}`},      // token create
	}}
	srv := httptest.NewServer(script.handler(t))
	defer srv.Close()

	result, err := Setup(context.Background(), NewClient(srv.URL, ""), "http://localhost:3000", "localhost", "8123")
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if result.GrafanaAPIKey != "glsa_abc" {
		t.Errorf("GrafanaAPIKey = %q, want glsa_abc", result.GrafanaAPIKey)
	}
	env := result.EnvBlock()
	for _, want := range []string{
		"GRAFANA_URL=http://localhost:3000",
		"GRAFANA_SERVICE_ACCOUNT_TOKEN=glsa_abc",
		"GRAFANA_API_KEY=glsa_abc",
		"CLICKHOUSE_HOST=localhost",
		"CLICKHOUSE_PORT=8123",
		"CLICKHOUSE_USER=ai_readonly",
		"CLICKHOUSE_PASSWORD=vikasa-ai",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("EnvBlock() missing %q; got:\n%s", want, env)
		}
	}
}

func TestResetDeletesEveryDashboardInFolder(t *testing.T) {
	script := &scriptedGrafana{responses: []scriptedResponse{
		{http.StatusOK, `[{"id":1,"uid":"aaa","title":"Take 1"},{"id":2,"uid":"bbb","title":"Take 2"}]`},
		{http.StatusOK, `{"message":"deleted"}`},
		{http.StatusOK, `{"message":"deleted"}`},
	}}
	srv := httptest.NewServer(script.handler(t))
	defer srv.Close()

	n, err := Reset(context.Background(), NewClient(srv.URL, ""))
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if n != 2 {
		t.Errorf("got %d deleted, want 2", n)
	}
	if script.requests[1].path != "/api/dashboards/uid/aaa?" || script.requests[2].path != "/api/dashboards/uid/bbb?" {
		t.Errorf("unexpected delete requests: %+v", script.requests[1:])
	}
}

func TestResetEmptyFolderDeletesNothing(t *testing.T) {
	script := &scriptedGrafana{responses: []scriptedResponse{
		{http.StatusOK, `[]`},
	}}
	srv := httptest.NewServer(script.handler(t))
	defer srv.Close()

	n, err := Reset(context.Background(), NewClient(srv.URL, ""))
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if n != 0 {
		t.Errorf("got %d deleted, want 0", n)
	}
}
