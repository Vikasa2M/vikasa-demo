// Package ai implements the scaffolding for Task 19's "models-only" AI
// dashboard segment: a Grafana HTTP API client for `democtl ai-setup` /
// `ai-reset`, and the take-QA gate `democtl verify ai-dashboard` runs
// against whatever an external MCP-driven AI built. The AI's own build and
// Q&A session is presenter-driven and out of this package's reach entirely
// -- everything here is the scaffolding around it: the scoped
// folder/service-account/token, and a way to prove a take is real (every
// panel's ClickHouse query actually runs and at least one returns rows)
// before it's accepted.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Fixed names/uids for the demo's AI segment -- see
// deploy/clickhouse/users.d/ai-readonly.xml and cmd/democtl's ai-setup/
// ai-reset/verify ai-dashboard subcommands.
const (
	FolderUID   = "ai-built"
	FolderTitle = "AI Built"
	SAName      = "ai-dashboard-builder"
	TokenName   = "demo"

	// ReadonlyCHUser/ReadonlyCHPassword are the ClickHouse credentials the
	// AI's MCP ClickHouse server connects as (see
	// deploy/clickhouse/users.d/ai-readonly.xml) -- also the credentials
	// `democtl verify ai-dashboard` re-runs every panel query as, so the
	// take-QA gate proves the query works under the SAME restricted
	// (readonly, 30s max_execution_time) user the AI itself was scoped to,
	// not some more privileged one.
	ReadonlyCHUser     = "ai_readonly"
	ReadonlyCHPassword = "vikasa-ai"

	// folderEditPermission is Grafana's folder-permission "Edit" level (see
	// GET/POST /api/folders/{uid}/permissions -- permission is a small int
	// enum: 1=View, 2=Edit, 4=Admin; confirmed live against Grafana 13.1).
	folderEditPermission = 2
)

// DefaultTimeout bounds a single democtl ai-setup/ai-reset/verify
// ai-dashboard invocation (a handful of sequential Grafana + ClickHouse HTTP
// calls).
const DefaultTimeout = 60 * time.Second

// Client is a small Grafana HTTP API client. Every ai-setup/ai-reset/verify
// ai-dashboard call goes through the anonymous-admin dev-mode session (User
// empty) or HTTP Basic Auth (User = "user:pass") -- never the AI's own
// scoped service-account token, which this package ISSUES but never uses
// itself.
type Client struct {
	BaseURL string
	User    string // "user:pass" for HTTP Basic Auth; empty = no Authorization header (anonymous-admin dev mode)
	HTTP    *http.Client
}

// NewClient builds a Client against baseURL (e.g. http://localhost:3000).
// user is "admin:admin"-style basic-auth credentials, or empty to rely on
// Grafana's anonymous-admin dev-mode session (GF_AUTH_ANONYMOUS_ENABLED +
// GF_AUTH_ANONYMOUS_ORG_ROLE=Admin -- see deploy/compose/docker-compose.yml).
func NewClient(baseURL, user string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), User: user, HTTP: http.DefaultClient}
}

// do sends a Grafana API request and, if out is non-nil, decodes a
// successful JSON response body into it. It always returns the raw response
// body too (even on failure, and even when out is set) so callers can build
// informative errors from Grafana's {"message": "..."} / {"messageId":
// "...", "message": "..."} error shapes without a second round trip.
func (c *Client) do(ctx context.Context, method, path string, body, out any) (status int, respBody []byte, err error) {
	var reader io.Reader
	if body != nil {
		b, merr := json.Marshal(body)
		if merr != nil {
			return 0, nil, fmt.Errorf("ai: marshal request body for %s %s: %w", method, path, merr)
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return 0, nil, fmt.Errorf("ai: build request %s %s: %w", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.User != "" {
		user, pass, _ := strings.Cut(c.User, ":")
		req.SetBasicAuth(user, pass)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("ai: %s %s%s: %w", method, c.BaseURL, path, err)
	}
	defer resp.Body.Close()
	respBody, err = io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("ai: read response body for %s %s: %w", method, path, err)
	}
	if out != nil && len(bytes.TrimSpace(respBody)) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return resp.StatusCode, respBody, fmt.Errorf("ai: decode response for %s %s: %w (status %d, body: %s)",
				method, path, err, resp.StatusCode, respBody)
		}
	}
	return resp.StatusCode, respBody, nil
}

// apiError formats a Grafana API failure (unexpected status code) uniformly.
func apiError(action string, status int, body []byte) error {
	return fmt.Errorf("ai: %s: unexpected status %d: %s", action, status, bytes.TrimSpace(body))
}
