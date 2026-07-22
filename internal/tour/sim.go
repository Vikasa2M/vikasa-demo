package tour

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// injectScenario POSTs to a cabinet sim's /inject/{scenario} endpoint (see
// cmd/cabinet-sim's newMux and internal/sim/scenario.go's Scenario dispatch)
// on its host-mapped port. A 202 Accepted means the scenario was queued for
// the sim's next 250ms Tick.
func injectScenario(ctx context.Context, port int, scenario string) error {
	url := fmt.Sprintf("http://localhost:%d/inject/%s", port, scenario)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return fmt.Errorf("tour: build inject request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("tour: POST %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("tour: POST %s: want 202 Accepted, got %s", url, resp.Status)
	}
	return nil
}

// simHealth is cabinet-sim's GET /healthz response body.
type simHealth struct {
	Status  string `json:"status"`
	Cabinet string `json:"cabinet"`
	Dropped int64  `json:"dropped"`
}

// simDropped returns the cabinet sim's cumulative dropped-publish counter
// (cabinet-sim's Publisher.Dropped(), surfaced via /healthz). Zero across a
// WAN cut is the demo's "the edge buffers, nothing is lost" proof at the
// sim's own vantage point — a cheap alternative to querying the cabinet's
// JetStream storage bytes via the Prometheus NATS exporter.
func simDropped(ctx context.Context, port int) (int64, error) {
	url := fmt.Sprintf("http://localhost:%d/healthz", port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("tour: build healthz request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("tour: GET %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("tour: GET %s: want 200 OK, got %s", url, resp.Status)
	}
	var h simHealth
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return 0, fmt.Errorf("tour: decode %s response: %w", url, err)
	}
	return h.Dropped, nil
}
