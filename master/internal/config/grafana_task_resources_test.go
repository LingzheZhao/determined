package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/determined-ai/determined/master/pkg/check"
)

func TestGrafanaTaskResourcesConfigValidate(t *testing.T) {
	tests := []struct {
		name  string
		conf  GrafanaTaskResourcesConfig
		valid bool
	}{
		{"disabled", GrafanaTaskResourcesConfig{}, true},
		{"subpath and org", GrafanaTaskResourcesConfig{
			DashboardURL: "https://grafana.example/monitoring/d/det-task-resources/resources?orgId=1",
			DetCluster:   "lab-a",
		}, true},
		{"partial", GrafanaTaskResourcesConfig{DetCluster: "lab-a"}, false},
		{"unsafe scheme", GrafanaTaskResourcesConfig{DashboardURL: "javascript:alert(1)", DetCluster: "lab-a"}, false},
		{"embedded credentials", GrafanaTaskResourcesConfig{
			DashboardURL: "https://user:password@grafana.example/d/det-task-resources/resources",
			DetCluster:   "lab-a",
		}, false},
		{"wrong dashboard", GrafanaTaskResourcesConfig{
			DashboardURL: "https://grafana.example/d/other/resources", DetCluster: "lab-a",
		}, false},
		{"query secret", GrafanaTaskResourcesConfig{
			DashboardURL: "https://grafana.example/d/det-task-resources/resources?token=secret",
			DetCluster:   "lab-a",
		}, false},
		{"malformed query", GrafanaTaskResourcesConfig{
			DashboardURL: "https://grafana.example/d/det-task-resources/resources?token=%ZZ",
			DetCluster:   "lab-a",
		}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.valid {
				require.Empty(t, tc.conf.Validate())
			} else {
				require.NotEmpty(t, tc.conf.Validate())
			}
		})
	}
}

func TestGrafanaTaskResourcesConfigIsValidatedInIntegrations(t *testing.T) {
	conf := IntegrationsConfig{GrafanaTaskResources: GrafanaTaskResourcesConfig{
		DashboardURL: "file:///tmp/dashboard", DetCluster: "lab-a",
	}}
	require.ErrorContains(t, check.Validate(conf), "grafana_task_resources")
}
