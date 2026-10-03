package tasks

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/determined-ai/determined/master/pkg/etc"
	"github.com/determined-ai/determined/master/pkg/model"
)

func TestGenericTaskSpecDisplayName(t *testing.T) {
	spec := GenericTaskSpec{}
	require.Equal(t, "Generic Task", spec.DisplayName())
	spec.Base.TaskID = "abc"
	require.Equal(t, "Generic Task abc", spec.DisplayName())
	spec.GenericTaskConfig.Name = "eval-sweep"
	require.Equal(t, "eval-sweep", spec.DisplayName())

	require.NoError(t, etc.SetRootPath("../../static/srv"))
	spec.Base.AgentUserGroup = &model.AgentUserGroup{}
	require.Equal(t, "eval-sweep", spec.ToTaskSpec().Description)
}

func TestGenericTaskSpecProxyPortsAndEnvPorts(t *testing.T) {
	spec := GenericTaskSpec{}
	spec.GenericTaskConfig.Environment.ProxyPorts = model.ProxyPortsConfig{
		{ProxyPort: 8888, ProxyTCP: false},
		{ProxyPort: 6006, ProxyTCP: true, Unauthenticated: true},
	}
	// A port the config already exposes is kept.
	spec.GenericTaskConfig.Environment.Ports = map[string]int{"9000": 9000}

	ports := map[int]bool{}
	for _, pp := range spec.ProxyPorts() {
		ports[pp.ProxyPort()] = true
	}
	require.True(t, ports[8888] && ports[6006])

	spec.MakeEnvPorts()
	require.Equal(t, map[string]int{"9000": 9000, "8888": 8888, "6006": 6006},
		spec.GenericTaskConfig.Environment.Ports)
	require.True(t, strings.HasPrefix(spec.DisplayName(), "Generic Task"))
}
