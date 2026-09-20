package agentrm

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/determined-ai/determined/master/internal/config"
	"github.com/determined-ai/determined/master/internal/config/provconfig"
	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/pkg/model"
)

func testDynamicPoolRM() *ResourceManager {
	return &ResourceManager{config: &config.AgentResourceManagerConfig{
		ClusterName: "agent-cluster",
		Scheduler:   config.DefaultSchedulerConfig(),
	}}
}

func TestNormalizeDynamicResourcePoolConfig(t *testing.T) {
	rm := testDynamicPoolRM()
	defaults := *model.DefaultTaskContainerDefaults()
	defaults.ForcePullImage = true

	normalized, err := rm.NormalizeDynamicResourcePoolConfig(config.ResourcePoolConfig{
		PoolName:                 "online-pool_1",
		MaxAuxContainersPerAgent: 100,
	}, defaults)
	require.NoError(t, err)
	require.NotNil(t, normalized.Scheduler)
	require.NotSame(t, rm.config.Scheduler, normalized.Scheduler)
	require.NotNil(t, normalized.TaskContainerDefaults)
	require.True(t, normalized.TaskContainerDefaults.ForcePullImage)

	// Effective values and pointer graphs are frozen independently of later YAML changes.
	*rm.config.Scheduler.Priority.DefaultPriority = 99
	defaults.ForcePullImage = false
	require.Equal(t, config.DefaultSchedulingPriority,
		*normalized.Scheduler.Priority.DefaultPriority)
	require.True(t, normalized.TaskContainerDefaults.ForcePullImage)
}

func TestNormalizeDynamicResourcePoolConfigRejectsUnsupported(t *testing.T) {
	rm := testDynamicPoolRM()
	defaults := *model.DefaultTaskContainerDefaults()

	for _, name := range []string{"", " leading", "path/name", "control\nname"} {
		_, err := rm.NormalizeDynamicResourcePoolConfig(config.ResourcePoolConfig{
			PoolName: name,
		}, defaults)
		require.ErrorIs(t, err, ErrInvalidDynamicResourcePool)
	}

	_, err := rm.NormalizeDynamicResourcePoolConfig(config.ResourcePoolConfig{
		PoolName: "provider-pool",
		Provider: &provconfig.Config{},
	}, defaults)
	require.ErrorIs(t, err, ErrInvalidDynamicResourcePool)
	require.Contains(t, err.Error(), "provider")

	_, err = rm.NormalizeDynamicResourcePoolConfig(config.ResourcePoolConfig{
		PoolName: "removed-scheduler",
		Scheduler: &config.SchedulerConfig{
			RoundRobin:    &config.RoundRobinSchedulerConfig{},
			FittingPolicy: "best",
		},
	}, defaults)
	require.ErrorIs(t, err, ErrInvalidDynamicResourcePool)
	require.Contains(t, err.Error(), "round robin")
}

func TestValidateDynamicPoolIdempotencyKey(t *testing.T) {
	require.NoError(t, validateDynamicPoolIdempotencyKey("operation-123"))
	for _, key := range []string{"", " \t", strings.Repeat("x", 513)} {
		require.ErrorIs(t, validateDynamicPoolIdempotencyKey(key), ErrInvalidDynamicResourcePool)
	}
}

func TestDecodeStoredDynamicResourcePool(t *testing.T) {
	rm := testDynamicPoolRM()
	cfg, err := rm.NormalizeDynamicResourcePoolConfig(config.ResourcePoolConfig{
		PoolName:                 "persisted-pool",
		MaxAuxContainersPerAgent: 100,
	}, *model.DefaultTaskContainerDefaults())
	require.NoError(t, err)
	raw, hash, err := marshalDynamicResourcePoolConfig(cfg)
	require.NoError(t, err)
	record := db.DynamicResourcePool{
		PoolName:      cfg.PoolName,
		ConfigVersion: dynamicResourcePoolConfigVersion,
		Config:        raw,
		ConfigHash:    hash,
	}
	decoded, err := decodeStoredDynamicResourcePool(record)
	require.NoError(t, err)
	require.Equal(t, cfg.PoolName, decoded.PoolName)

	record.ConfigVersion++
	_, err = decodeStoredDynamicResourcePool(record)
	require.ErrorContains(t, err, "unsupported config version")
	record.ConfigVersion = dynamicResourcePoolConfigVersion
	record.ConfigHash = "corrupt"
	_, err = decodeStoredDynamicResourcePool(record)
	require.ErrorContains(t, err, "hash mismatch")

	var object map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &object))
	object["unexpected"] = true
	record.Config, err = json.Marshal(object)
	require.NoError(t, err)
	_, err = decodeStoredDynamicResourcePool(record)
	require.ErrorContains(t, err, "unknown field")
}
