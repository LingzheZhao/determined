package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/determined-ai/determined/master/pkg/check"
)

func TestTaskResourcesConfigValidate(t *testing.T) {
	tests := []struct {
		name  string
		conf  TaskResourcesConfig
		valid bool
	}{
		{"disabled", TaskResourcesConfig{}, true},
		{"internal origin", TaskResourcesConfig{PrometheusURL: "http://prometheus:9090", DetCluster: "cvgl"}, true},
		{"partial", TaskResourcesConfig{DetCluster: "cvgl"}, false},
		{"credentials", TaskResourcesConfig{PrometheusURL: "http://user:pass@prometheus:9090", DetCluster: "cvgl"}, false},
		{"query", TaskResourcesConfig{PrometheusURL: "http://prometheus:9090?query=up", DetCluster: "cvgl"}, false},
		{"path", TaskResourcesConfig{PrometheusURL: "http://prometheus:9090/redirect", DetCluster: "cvgl"}, false},
		{"fragment", TaskResourcesConfig{PrometheusURL: "http://prometheus:9090/#x", DetCluster: "cvgl"}, false},
		{"scheme", TaskResourcesConfig{PrometheusURL: "file:///tmp/prometheus", DetCluster: "cvgl"}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.valid {
				require.Empty(t, test.conf.Validate())
			} else {
				require.NotEmpty(t, test.conf.Validate())
			}
		})
	}
	require.ErrorContains(t, check.Validate(IntegrationsConfig{TaskResources: TaskResourcesConfig{
		PrometheusURL: "file:///tmp/prometheus", DetCluster: "cvgl",
	}}), "task_resources")
}
