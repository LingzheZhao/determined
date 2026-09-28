package config

import (
	"fmt"
	"net/url"
	"strings"
)

// TaskResourcesConfig configures the server-side Prometheus integration for task resource charts.
// PrometheusURL is never sent to the browser. Its configured host is the sole allowed upstream.
type TaskResourcesConfig struct {
	PrometheusURL string `json:"prometheus_url"`
	DetCluster    string `json:"det_cluster"`
}

func (c TaskResourcesConfig) Enabled() bool { return c.PrometheusURL != "" }

// Validate restricts the upstream to one administrator-selected HTTP endpoint.
func (c TaskResourcesConfig) Validate() []error {
	if c.PrometheusURL == "" && c.DetCluster == "" {
		return nil
	}
	if c.PrometheusURL == "" || strings.TrimSpace(c.DetCluster) == "" {
		return []error{fmt.Errorf("task_resources requires prometheus_url and det_cluster")}
	}
	u, err := url.Parse(c.PrometheusURL)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") ||
		u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || u.Opaque != "" || u.Path != "" && u.Path != "/" {
		return []error{fmt.Errorf("task_resources prometheus_url must be an http(s) origin without credentials, path, query, or fragment")}
	}
	if strings.ContainsAny(c.DetCluster, "\x00\r\n") {
		return []error{fmt.Errorf("task_resources det_cluster contains invalid characters")}
	}
	return nil
}
