//go:build integration

package agentrm

import (
	"context"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/determined-ai/determined/master/internal/config"
	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/internal/rm"
	"github.com/determined-ai/determined/master/pkg/model"
)

func TestDynamicPoolPersistenceRestart(t *testing.T) {
	database, cleanup := db.MustResolveNewPostgresDatabase(t)
	defer cleanup()
	db.MustMigrateTestPostgres(t, database, "file://../../../static/migrations", "up")

	rmConfig := &config.ResourceManagerWithPoolsConfig{
		ResourceManager: &config.ResourceManagerConfig{AgentRM: &config.AgentResourceManagerConfig{
			ClusterName:                "agent-cluster",
			DefaultComputeResourcePool: "default",
			DefaultAuxResourcePool:     "default",
			Scheduler:                  config.DefaultSchedulerConfig(),
		}},
		ResourcePools: []config.ResourcePoolConfig{{
			PoolName:                 "default",
			MaxAuxContainersPerAgent: 100,
		}},
	}
	first, err := New(database, echo.New(), rmConfig, nil, nil)
	require.NoError(t, err)
	defer first.stop()
	staticBefore, ok := first.registry.readyPool("default")
	require.True(t, ok)

	masterDefaults := *model.DefaultTaskContainerDefaults()
	masterDefaults.ForcePullImage = true
	record, created, err := first.CreateDynamicResourcePool(
		context.Background(),
		"restart-operation",
		config.ResourcePoolConfig{
			PoolName:                 "online-restart",
			MaxAuxContainersPerAgent: 100,
		},
		masterDefaults,
	)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, db.DynamicResourcePoolReady, record.State)
	require.True(t, first.IsDynamicResourcePoolReady(record.PoolName))
	staticAfter, ok := first.registry.readyPool("default")
	require.True(t, ok)
	require.Same(t, staticBefore, staticAfter, "online create must preserve existing runtime pools")

	changedMasterDefaults := *model.DefaultTaskContainerDefaults()
	changedMasterDefaults.ForcePullImage = false
	effective, err := first.TaskContainerDefaults(
		rm.ResourcePoolName(record.PoolName), changedMasterDefaults,
	)
	require.NoError(t, err)
	require.True(t, effective.ForcePullImage, "dynamic defaults must remain frozen")

	first.stop()
	restarted, err := New(database, echo.New(), rmConfig, nil, nil)
	require.NoError(t, err)
	defer restarted.stop()
	require.True(t, restarted.IsDynamicResourcePoolReady(record.PoolName))
	restartedDefaults, err := restarted.TaskContainerDefaults(
		rm.ResourcePoolName(record.PoolName), changedMasterDefaults,
	)
	require.NoError(t, err)
	require.True(t, restartedDefaults.ForcePullImage,
		"persisted effective defaults must survive restart and changed YAML")
	restored, err := database.DynamicResourcePoolByName(context.Background(), record.PoolName)
	require.NoError(t, err)
	require.Equal(t, db.DynamicResourcePoolReady, restored.State)
}
