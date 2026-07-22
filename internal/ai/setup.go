package ai

import (
	"context"
	"fmt"
	"net/http"
)

// EnsureFolder creates the "AI Built" folder (uid ai-built) if it doesn't
// already exist. Idempotent: a uid collision (observed live against
// Grafana 13.1 as 412 "the folder has been changed by someone else") or a
// title collision (409, per the brief -- not observed live, since Grafana
// allows duplicate titles under different uids, but harmless to also treat
// as "already there") are both treated as success, not an error.
func (c *Client) EnsureFolder(ctx context.Context) error {
	status, body, err := c.do(ctx, http.MethodPost, "/api/folders",
		map[string]string{"uid": FolderUID, "title": FolderTitle}, nil)
	if err != nil {
		return err
	}
	switch status {
	case http.StatusOK, http.StatusCreated, http.StatusConflict, http.StatusPreconditionFailed:
		return nil
	default:
		return apiError(fmt.Sprintf("create folder %q", FolderUID), status, body)
	}
}

// serviceAccountSearchResult is the subset of GET
// /api/serviceaccounts/search's per-account fields EnsureServiceAccount
// needs to find an existing account by exact name.
type serviceAccountSearchResult struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// EnsureServiceAccount creates the ai-dashboard-builder service account
// (role "None" -- it gets its only permission from the ai-built folder
// grant, nothing org-wide) if it doesn't already exist, and returns its id
// either way. Idempotent: Grafana 13.1 rejects a duplicate name with 400
// (messageId serviceaccounts.ErrAlreadyExists, not a 409) -- on ANY
// non-success create response this falls back to searching by name rather
// than pattern-matching that specific error shape, so it's also robust to
// the account having been created out-of-band (e.g. by hand in the UI).
func (c *Client) EnsureServiceAccount(ctx context.Context) (id int64, err error) {
	var created struct {
		ID int64 `json:"id"`
	}
	status, body, err := c.do(ctx, http.MethodPost, "/api/serviceaccounts",
		map[string]string{"name": SAName, "role": "None"}, &created)
	if err != nil {
		return 0, err
	}
	if status == http.StatusOK || status == http.StatusCreated {
		return created.ID, nil
	}

	existingID, ok, ferr := c.findServiceAccountByName(ctx, SAName)
	if ferr != nil {
		return 0, ferr
	}
	if !ok {
		return 0, fmt.Errorf("ai: %w (and no existing service account named %q was found)",
			apiError("create service account", status, body), SAName)
	}
	return existingID, nil
}

func (c *Client) findServiceAccountByName(ctx context.Context, name string) (id int64, found bool, err error) {
	var result struct {
		ServiceAccounts []serviceAccountSearchResult `json:"serviceAccounts"`
	}
	status, body, err := c.do(ctx, http.MethodGet, "/api/serviceaccounts/search?query="+name, nil, &result)
	if err != nil {
		return 0, false, err
	}
	if status != http.StatusOK {
		return 0, false, apiError("search service accounts", status, body)
	}
	for _, sa := range result.ServiceAccounts {
		if sa.Name == name {
			return sa.ID, true, nil
		}
	}
	return 0, false, nil
}

// GrantFolderEditor grants service account saID Editor permission on the
// ai-built folder -- and ONLY that folder, since the folder-permissions API
// replaces the folder's entire permission list with exactly what's sent
// (confirmed live: this also drops the default org-role-based Viewer/Editor
// entries every fresh folder starts with, which is the point -- the SA's
// "None" org role plus this one folder-scoped grant is what makes "the
// token can create a dashboard in AI Built and NOT elsewhere" true).
func (c *Client) GrantFolderEditor(ctx context.Context, saID int64) error {
	status, body, err := c.do(ctx, http.MethodPost, "/api/folders/"+FolderUID+"/permissions",
		map[string]any{
			"items": []map[string]any{
				{"userId": saID, "permission": folderEditPermission},
			},
		}, nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return apiError(fmt.Sprintf("grant folder %q editor to service account %d", FolderUID, saID), status, body)
	}
	return nil
}

// saToken is one entry from GET /api/serviceaccounts/{id}/tokens.
type saToken struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// EnsureServiceAccountToken returns a working API token for saID named
// TokenName. Grafana never re-exposes a token's secret after creation, so
// the only way to make this idempotent (every run of `democtl ai-setup`
// prints a token that actually works right now) is to ROTATE: delete any
// existing token by that name first, then create a fresh one. A stale
// token from a previous take is harmless to invalidate -- ai-setup is meant
// to be re-run before every take per the RUNBOOK.
func (c *Client) EnsureServiceAccountToken(ctx context.Context, saID int64) (key string, err error) {
	listPath := fmt.Sprintf("/api/serviceaccounts/%d/tokens", saID)
	var tokens []saToken
	status, body, err := c.do(ctx, http.MethodGet, listPath, nil, &tokens)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", apiError(fmt.Sprintf("list tokens for service account %d", saID), status, body)
	}
	for _, t := range tokens {
		if t.Name != TokenName {
			continue
		}
		delStatus, delBody, err := c.do(ctx, http.MethodDelete, fmt.Sprintf("%s/%d", listPath, t.ID), nil, nil)
		if err != nil {
			return "", err
		}
		if delStatus != http.StatusOK {
			return "", apiError(fmt.Sprintf("delete stale token %d for service account %d", t.ID, saID), delStatus, delBody)
		}
	}

	var created struct {
		Key string `json:"key"`
	}
	status, body, err = c.do(ctx, http.MethodPost, listPath, map[string]string{"name": TokenName}, &created)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", apiError(fmt.Sprintf("create token for service account %d", saID), status, body)
	}
	return created.Key, nil
}

// SetupResult is the ready-to-paste MCP env block `democtl ai-setup`
// prints.
type SetupResult struct {
	GrafanaURL     string
	GrafanaAPIKey  string
	ClickHouseHost string
	ClickHousePort string
}

// EnvBlock formats r as the KEY=value block demo/ai/mcp/README.md's example
// config expects, ready to paste into an MCP client's env. Both
// GRAFANA_SERVICE_ACCOUNT_TOKEN (grafana/mcp-grafana's current, documented
// env var -- confirmed against its upstream README) and the older
// GRAFANA_API_KEY (deprecated there but still honored, for MCP clients
// pinned to an older mcp-grafana build) are printed with the same token
// value, so pasting this block works regardless of which one the
// presenter's installed MCP client version expects.
func (r SetupResult) EnvBlock() string {
	return fmt.Sprintf(
		"GRAFANA_URL=%s\n"+
			"GRAFANA_SERVICE_ACCOUNT_TOKEN=%s\n"+
			"GRAFANA_API_KEY=%s\n"+
			"CLICKHOUSE_HOST=%s\n"+
			"CLICKHOUSE_PORT=%s\n"+
			"CLICKHOUSE_USER=%s\n"+
			"CLICKHOUSE_PASSWORD=%s\n",
		r.GrafanaURL, r.GrafanaAPIKey, r.GrafanaAPIKey, r.ClickHouseHost, r.ClickHousePort, ReadonlyCHUser, ReadonlyCHPassword)
}

// Setup runs the full ai-setup sequence against c: ensure the AI Built
// folder, ensure the ai-dashboard-builder service account, grant it Editor
// on that folder only, and issue it a fresh token. Every step is
// idempotent, so re-running ai-setup before each take (per the RUNBOOK) is
// the intended, safe usage.
func Setup(ctx context.Context, c *Client, grafanaURL, chHost, chPort string) (SetupResult, error) {
	if err := c.EnsureFolder(ctx); err != nil {
		return SetupResult{}, fmt.Errorf("ai-setup: %w", err)
	}
	saID, err := c.EnsureServiceAccount(ctx)
	if err != nil {
		return SetupResult{}, fmt.Errorf("ai-setup: %w", err)
	}
	if err := c.GrantFolderEditor(ctx, saID); err != nil {
		return SetupResult{}, fmt.Errorf("ai-setup: %w", err)
	}
	token, err := c.EnsureServiceAccountToken(ctx, saID)
	if err != nil {
		return SetupResult{}, fmt.Errorf("ai-setup: %w", err)
	}
	return SetupResult{
		GrafanaURL:     grafanaURL,
		GrafanaAPIKey:  token,
		ClickHouseHost: chHost,
		ClickHousePort: chPort,
	}, nil
}

// dashSearchResult is the subset of GET /api/search's per-dashboard fields
// Reset and VerifyAIDashboard need.
type dashSearchResult struct {
	ID    int64  `json:"id"`
	UID   string `json:"uid"`
	Title string `json:"title"`
}

// searchFolderDashboards lists every dash-db in the ai-built folder.
func (c *Client) searchFolderDashboards(ctx context.Context) ([]dashSearchResult, error) {
	var results []dashSearchResult
	status, body, err := c.do(ctx, http.MethodGet,
		"/api/search?folderUIDs="+FolderUID+"&type=dash-db", nil, &results)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, apiError(fmt.Sprintf("search folder %q", FolderUID), status, body)
	}
	return results, nil
}

// Reset deletes every dashboard in the ai-built folder -- a clean slate
// between takes, so a bad take's half-built dashboard never gets mistaken
// for the next take's, and `verify ai-dashboard` (which always inspects
// "the newest" dashboard in the folder) never accidentally grades stale
// work. Returns the number of dashboards deleted.
func Reset(ctx context.Context, c *Client) (int, error) {
	results, err := c.searchFolderDashboards(ctx)
	if err != nil {
		return 0, fmt.Errorf("ai-reset: %w", err)
	}
	deleted := 0
	for _, d := range results {
		status, body, err := c.do(ctx, http.MethodDelete, "/api/dashboards/uid/"+d.UID, nil, nil)
		if err != nil {
			return deleted, fmt.Errorf("ai-reset: %w", err)
		}
		if status != http.StatusOK {
			return deleted, fmt.Errorf("ai-reset: %w", apiError(fmt.Sprintf("delete dashboard %s (%q)", d.UID, d.Title), status, body))
		}
		deleted++
	}
	return deleted, nil
}
