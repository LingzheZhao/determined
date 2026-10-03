package model

import (
	"strings"
	"testing"

	"github.com/ghodss/yaml"
	"github.com/stretchr/testify/require"

	"github.com/determined-ai/determined/master/pkg/check"
)

func TestGenericTaskConfigNameAndDescription(t *testing.T) {
	config := DefaultConfigGenericTaskConfig(nil)
	require.NoError(t, yaml.UnmarshalStrict([]byte(`
name: eval-sweep
description: evaluates the checkpoints of run 12
entrypoint: ["python", "eval.py"]
`), &config, yaml.DisallowUnknownFields))
	require.Equal(t, "eval-sweep", config.Name)
	require.Equal(t, "evaluates the checkpoints of run 12", config.Description)
	require.NoError(t, check.Validate(config))

	config.Name = strings.Repeat("x", 256)
	require.ErrorContains(t, check.Validate(config), "name must be at most 255 characters")
}
