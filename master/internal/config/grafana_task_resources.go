package config

import (
	"fmt"
	"net/url"
	"strings"
)

// GrafanaTaskResourcesConfig enables links to an externally managed task resources dashboard.
// Its URL is sent to the browser, so it must never contain credentials.
type GrafanaTaskResourcesConfig struct {
	DashboardURL string `json:"dashboard_url"`
	DetCluster   string `json:"det_cluster"`
}

// Validate checks the opt-in dashboard URL and its cluster selector.
func (c GrafanaTaskResourcesConfig) Validate() []error {
	if c.DashboardURL == "" && c.DetCluster == "" {
		return nil
	}
	if c.DashboardURL == "" || strings.TrimSpace(c.DetCluster) == "" {
		return []error{fmt.Errorf("grafana_task_resources requires dashboard_url and det_cluster")}
	}
	u, err := url.Parse(c.DashboardURL)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") ||
		u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return []error{fmt.Errorf("grafana_task_resources dashboard_url must be an http(s) URL without credentials or fragment")}
	}
	if !strings.Contains(u.EscapedPath(), "/d/det-task-resources/") &&
		!strings.HasSuffix(u.EscapedPath(), "/d/det-task-resources") {
		return []error{fmt.Errorf("grafana_task_resources dashboard_url must use dashboard UID det-task-resources")}
	}
	// The URL is delivered to every signed-in browser. Keep only Grafana's ordinary org selector.
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return []error{fmt.Errorf("grafana_task_resources dashboard_url has invalid query parameters: %w", err)}
	}
	for key := range query {
		if key != "orgId" {
			return []error{fmt.Errorf("grafana_task_resources dashboard_url only supports the orgId query parameter")}
		}
	}
	return nil
}
