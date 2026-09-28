//go:build integration

package agentrm

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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
	first, err := New(context.Background(), database, echo.New(), rmConfig, nil, nil)
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
	require.Contains(t, []db.DynamicResourcePoolState{
		db.DynamicResourcePoolPending, db.DynamicResourcePoolReady,
	}, record.State)
	require.Eventually(t, func() bool {
		stored, readErr := database.DynamicResourcePoolByName(context.Background(), record.PoolName)
		return readErr == nil && stored.State == db.DynamicResourcePoolReady &&
			first.IsDynamicResourcePoolReady(record.PoolName)
	}, 10*time.Second, 20*time.Millisecond)
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
	restarted, err := New(context.Background(), database, echo.New(), rmConfig, nil, nil)
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

func TestDynamicPoolPendingWorkerRecoversReplayWithoutRestart(t *testing.T) {
	database, cleanup := db.MustResolveNewPostgresDatabase(t)
	defer cleanup()
	db.MustMigrateTestPostgres(t, database, "file://../../../static/migrations", "up")
	rmConfig := &config.ResourceManagerWithPoolsConfig{
		ResourceManager: &config.ResourceManagerConfig{AgentRM: &config.AgentResourceManagerConfig{
			ClusterName: "agent-cluster", DefaultComputeResourcePool: "default",
			DefaultAuxResourcePool: "default", Scheduler: config.DefaultSchedulerConfig(),
		}},
		ResourcePools: []config.ResourcePoolConfig{{
			PoolName: "default", MaxAuxContainersPerAgent: 100,
		}},
	}
	manager, err := New(context.Background(), database, echo.New(), rmConfig, nil, nil)
	require.NoError(t, err)
	defer manager.stop()
	staticPool, ok := manager.registry.readyPool("default")
	require.True(t, ok)
	originalCreate := createDynamicPoolRuntime
	t.Cleanup(func() { createDynamicPoolRuntime = originalCreate })
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var runtimeCreates atomic.Int32
	createDynamicPoolRuntime = func(
		manager *ResourceManager, cfg config.ResourcePoolConfig,
	) (*resourcePool, error) {
		if cfg.PoolName == "recover-pending" {
			runtimeCreates.Add(1)
			started <- struct{}{}
			<-release
		}
		return originalCreate(manager, cfg)
	}

	cfg := config.ResourcePoolConfig{PoolName: "recover-pending", MaxAuxContainersPerAgent: 100}
	normalized, err := manager.NormalizeDynamicResourcePoolConfig(
		cfg, *model.DefaultTaskContainerDefaults(),
	)
	require.NoError(t, err)
	raw, hash, err := marshalDynamicResourcePoolConfig(normalized)
	require.NoError(t, err)
	// This is the durable state left by an insert whose following read failed or was canceled.
	_, created, err := database.CreateDynamicResourcePool(context.Background(), db.DynamicResourcePool{
		ClusterName: "agent-cluster", PoolName: cfg.PoolName,
		ConfigVersion: dynamicResourcePoolConfigVersion, IdempotencyKey: "recover-key",
		Config: raw, ConfigHash: hash,
	})
	require.NoError(t, err)
	require.True(t, created)
	require.Eventually(t, func() bool {
		select {
		case <-started:
			return true
		default:
			return false
		}
	}, 10*time.Second, 20*time.Millisecond)

	const replays = 16
	var group sync.WaitGroup
	for i := 0; i < replays; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			replayed, wasCreated, replayErr := manager.CreateDynamicResourcePool(
				context.Background(), "recover-key", cfg, *model.DefaultTaskContainerDefaults(),
			)
			if replayErr != nil || wasCreated || replayed.PoolName != cfg.PoolName {
				t.Errorf("replay: record=%+v created=%v err=%v", replayed, wasCreated, replayErr)
			}
		}()
	}
	group.Wait()
	releaseOnce.Do(func() { close(release) })
	require.Eventually(t, func() bool {
		stored, readErr := database.DynamicResourcePoolByName(context.Background(), cfg.PoolName)
		return readErr == nil && stored.State == db.DynamicResourcePoolReady &&
			manager.IsDynamicResourcePoolReady(cfg.PoolName)
	}, 10*time.Second, 20*time.Millisecond)
	runtime, ok := manager.registry.readyPool(cfg.PoolName)
	require.True(t, ok)
	_, _, err = manager.CreateDynamicResourcePool(
		context.Background(), "recover-key", cfg, *model.DefaultTaskContainerDefaults(),
	)
	require.NoError(t, err)
	afterReplay, ok := manager.registry.readyPool(cfg.PoolName)
	require.True(t, ok)
	require.Same(t, runtime, afterReplay)
	require.EqualValues(t, 1, runtimeCreates.Load())
	staticAfter, ok := manager.registry.readyPool("default")
	require.True(t, ok)
	require.Same(t, staticPool, staticAfter)
}

func TestDynamicPoolReadyWorkerRecoversCommittedWriteError(t *testing.T) {
	database, cleanup := db.MustResolveNewPostgresDatabase(t)
	defer cleanup()
	db.MustMigrateTestPostgres(t, database, "file://../../../static/migrations", "up")
	rmConfig := &config.ResourceManagerWithPoolsConfig{
		ResourceManager: &config.ResourceManagerConfig{AgentRM: &config.AgentResourceManagerConfig{
			ClusterName: "agent-cluster", DefaultComputeResourcePool: "default",
			DefaultAuxResourcePool: "default", Scheduler: config.DefaultSchedulerConfig(),
		}},
		ResourcePools: []config.ResourcePoolConfig{{
			PoolName: "default", MaxAuxContainersPerAgent: 100,
		}},
	}
	manager, err := New(context.Background(), database, echo.New(), rmConfig, nil, nil)
	require.NoError(t, err)
	defer manager.stop()
	manager.StopDynamicPoolWorker()
	staticPool, ok := manager.registry.readyPool("default")
	require.True(t, ok)

	originalSetState := setDynamicResourcePoolState
	originalStop := stopPreparedDynamicResourcePool
	originalCreate := createDynamicPoolRuntime
	t.Cleanup(func() {
		setDynamicResourcePoolState = originalSetState
		stopPreparedDynamicResourcePool = originalStop
		createDynamicPoolRuntime = originalCreate
	})
	type createdRuntime struct {
		pool *resourcePool
		cfg  config.ResourcePoolConfig
	}
	created := make(chan createdRuntime, 3)
	var stopped atomic.Int32
	var failRecovery atomic.Bool
	createDynamicPoolRuntime = func(
		manager *ResourceManager, cfg config.ResourcePoolConfig,
	) (*resourcePool, error) {
		if cfg.PoolName == "recover-ready" && failRecovery.CompareAndSwap(true, false) {
			return nil, errors.New("temporary runtime initialization failure")
		}
		pool, createErr := originalCreate(manager, cfg)
		if createErr == nil && cfg.PoolName == "recover-ready" {
			created <- createdRuntime{pool: pool, cfg: cfg}
		}
		return pool, createErr
	}
	stopPreparedDynamicResourcePool = func(pool *resourcePool) {
		stopped.Add(1)
		originalStop(pool)
	}
	setDynamicResourcePoolState = func(
		database *db.PgDB, ctx context.Context, poolName string,
		state db.DynamicResourcePoolState, errText *string,
	) (db.DynamicResourcePool, error) {
		if poolName == "recover-ready" {
			if state == db.DynamicResourcePoolReady {
				_, setErr := originalSetState(database, ctx, poolName, state, errText)
				if setErr != nil {
					return db.DynamicResourcePool{}, setErr
				}
				return db.DynamicResourcePool{}, errors.New("response lost after Ready commit")
			}
			if state == db.DynamicResourcePoolFailed {
				return db.DynamicResourcePool{}, errors.New("database unavailable for Failed write")
			}
		}
		return originalSetState(database, ctx, poolName, state, errText)
	}

	cfg := config.ResourcePoolConfig{PoolName: "recover-ready", MaxAuxContainersPerAgent: 100}
	defaults := *model.DefaultTaskContainerDefaults()
	defaults.ForcePullImage = true
	normalized, err := manager.NormalizeDynamicResourcePoolConfig(cfg, defaults)
	require.NoError(t, err)
	raw, hash, err := marshalDynamicResourcePoolConfig(normalized)
	require.NoError(t, err)
	record, wasCreated, err := database.CreateDynamicResourcePool(
		context.Background(), db.DynamicResourcePool{
			ClusterName: "agent-cluster", PoolName: cfg.PoolName,
			ConfigVersion: dynamicResourcePoolConfigVersion, IdempotencyKey: "recover-ready-key",
			Config: raw, ConfigHash: hash,
		},
	)
	require.NoError(t, err)
	require.True(t, wasCreated)
	_, err = manager.initializeDynamicResourcePool(context.Background(), record, normalized)
	require.ErrorIs(t, err, ErrDynamicResourcePoolPersistence)
	first := <-created
	require.EqualValues(t, 1, stopped.Load(), "the unpublishable prepared runtime must stop")
	require.False(t, manager.IsDynamicResourcePoolReady(cfg.PoolName))
	stored, err := database.DynamicResourcePoolByName(context.Background(), cfg.PoolName)
	require.NoError(t, err)
	require.Equal(t, db.DynamicResourcePoolReady, stored.State)

	// Restore database writes and change the master scheduler. The worker must use the saved config.
	setDynamicResourcePoolState = originalSetState
	failedCfg := normalized
	failedCfg.PoolName = "remain-failed"
	failedRaw, failedHash, err := marshalDynamicResourcePoolConfig(failedCfg)
	require.NoError(t, err)
	_, wasCreated, err = database.CreateDynamicResourcePool(
		context.Background(), db.DynamicResourcePool{
			ClusterName: "agent-cluster", PoolName: failedCfg.PoolName,
			ConfigVersion: dynamicResourcePoolConfigVersion, IdempotencyKey: "remain-failed-key",
			Config: failedRaw, ConfigHash: failedHash,
		},
	)
	require.NoError(t, err)
	require.True(t, wasCreated)
	failure := "requires an explicit retry"
	_, err = database.SetDynamicResourcePoolState(
		context.Background(), failedCfg.PoolName, db.DynamicResourcePoolFailed, &failure,
	)
	require.NoError(t, err)
	*manager.config.Scheduler.Priority.DefaultPriority = 99
	failRecovery.Store(true)
	manager.startDynamicPoolWorker(context.Background())
	require.Eventually(t, func() bool {
		return manager.IsDynamicResourcePoolReady(cfg.PoolName)
	}, 10*time.Second, 20*time.Millisecond)
	second := <-created
	require.NotSame(t, first.pool, second.pool)
	require.Equal(t, normalized, first.cfg)
	require.Equal(t, normalized, second.cfg)
	require.EqualValues(t, 1, stopped.Load())
	runtime, ok := manager.registry.readyPool(cfg.PoolName)
	require.True(t, ok)
	require.Same(t, second.pool, runtime)
	staticAfter, ok := manager.registry.readyPool("default")
	require.True(t, ok)
	require.Same(t, staticPool, staticAfter)
	stored, err = database.DynamicResourcePoolByName(context.Background(), cfg.PoolName)
	require.NoError(t, err)
	require.Equal(t, db.DynamicResourcePoolReady, stored.State)
	manager.StopDynamicPoolWorker()
	manager.advancePendingDynamicPools(context.Background())
	require.Len(t, created, 0, "reconciliation must not create another runtime")
	failed, err := database.DynamicResourcePoolByName(context.Background(), failedCfg.PoolName)
	require.NoError(t, err)
	require.Equal(t, db.DynamicResourcePoolFailed, failed.State)
	require.False(t, manager.IsDynamicResourcePoolReady(failedCfg.PoolName))
}

func TestDynamicPoolRetryWorkerStopsWithMasterContext(t *testing.T) {
	database, cleanup := db.MustResolveNewPostgresDatabase(t)
	defer cleanup()
	db.MustMigrateTestPostgres(t, database, "file://../../../static/migrations", "up")
	masterCtx, cancelMaster := context.WithCancel(context.Background())
	defer cancelMaster()
	rmConfig := &config.ResourceManagerWithPoolsConfig{
		ResourceManager: &config.ResourceManagerConfig{AgentRM: &config.AgentResourceManagerConfig{
			ClusterName: "agent-cluster", DefaultComputeResourcePool: "default",
			DefaultAuxResourcePool: "default", Scheduler: config.DefaultSchedulerConfig(),
		}},
		ResourcePools: []config.ResourcePoolConfig{{
			PoolName: "default", MaxAuxContainersPerAgent: 100,
		}},
	}
	manager, err := New(masterCtx, database, echo.New(), rmConfig, nil, nil)
	require.NoError(t, err)
	defer manager.stop()
	// Keep the initial scanner out of the setup window while arranging a durable failure.
	manager.StopDynamicPoolWorker()
	cfg := config.ResourcePoolConfig{PoolName: "retry-failed", MaxAuxContainersPerAgent: 100}
	normalized, err := manager.NormalizeDynamicResourcePoolConfig(
		cfg, *model.DefaultTaskContainerDefaults(),
	)
	require.NoError(t, err)
	raw, hash, err := marshalDynamicResourcePoolConfig(normalized)
	require.NoError(t, err)
	_, created, err := database.CreateDynamicResourcePool(context.Background(), db.DynamicResourcePool{
		ClusterName: "agent-cluster", PoolName: cfg.PoolName,
		ConfigVersion: dynamicResourcePoolConfigVersion, IdempotencyKey: "retry-key",
		Config: raw, ConfigHash: hash,
	})
	require.NoError(t, err)
	require.True(t, created)
	failure := "previous runtime initialization failed"
	_, err = database.SetDynamicResourcePoolState(
		context.Background(), cfg.PoolName, db.DynamicResourcePoolFailed, &failure,
	)
	require.NoError(t, err)

	manager.startDynamicPoolWorker(masterCtx)
	retried, err := manager.RetryDynamicResourcePool(context.Background(), cfg.PoolName)
	require.NoError(t, err)
	require.Equal(t, db.DynamicResourcePoolPending, retried.State)
	require.Eventually(t, func() bool {
		stored, readErr := database.DynamicResourcePoolByName(context.Background(), cfg.PoolName)
		return readErr == nil && stored.State == db.DynamicResourcePoolReady &&
			manager.IsDynamicResourcePoolReady(cfg.PoolName)
	}, 10*time.Second, 20*time.Millisecond)

	cancelMaster()
	select {
	case <-manager.dynamicPoolDone:
	case <-time.After(2 * time.Second):
		t.Fatal("dynamic pool worker did not exit after master context cancellation")
	}
}

func TestDynamicPoolStartupRejectsUnsupportedVersion(t *testing.T) {
	database, cleanup := db.MustResolveNewPostgresDatabase(t)
	defer cleanup()
	db.MustMigrateTestPostgres(t, database, "file://../../../static/migrations", "up")

	_, created, err := database.CreateDynamicResourcePool(context.Background(), db.DynamicResourcePool{
		ClusterName:    "agent-cluster",
		PoolName:       "future-version",
		ConfigVersion:  999,
		IdempotencyKey: "future-operation",
		Config:         []byte(`{"pool_name":"future-version"}`),
		ConfigHash:     "future-hash",
	})
	require.NoError(t, err)
	require.True(t, created)
	err = ValidatePersistedDynamicPoolConfigs(
		context.Background(), database, []*config.ResourceManagerWithPoolsConfig{{
			ResourceManager: &config.ResourceManagerConfig{AgentRM: &config.AgentResourceManagerConfig{
				ClusterName: "agent-cluster",
			}},
			ResourcePools: []config.ResourcePoolConfig{{PoolName: "default"}},
		}},
	)
	require.ErrorContains(t, err, "unsupported config version 999")
}

func TestDynamicPoolStartupRejectsStaticCollision(t *testing.T) {
	database, cleanup := db.MustResolveNewPostgresDatabase(t)
	defer cleanup()
	db.MustMigrateTestPostgres(t, database, "file://../../../static/migrations", "up")

	rmForNormalization := testDynamicPoolRM()
	cfg, err := rmForNormalization.NormalizeDynamicResourcePoolConfig(
		config.ResourcePoolConfig{PoolName: "collision", MaxAuxContainersPerAgent: 100},
		*model.DefaultTaskContainerDefaults(),
	)
	require.NoError(t, err)
	raw, hash, err := marshalDynamicResourcePoolConfig(cfg)
	require.NoError(t, err)
	_, created, err := database.CreateDynamicResourcePool(context.Background(), db.DynamicResourcePool{
		ClusterName:    "agent-cluster",
		PoolName:       cfg.PoolName,
		ConfigVersion:  dynamicResourcePoolConfigVersion,
		IdempotencyKey: "collision-operation",
		Config:         raw,
		ConfigHash:     hash,
	})
	require.NoError(t, err)
	require.True(t, created)
	err = ValidatePersistedDynamicPoolConfigs(
		context.Background(), database, []*config.ResourceManagerWithPoolsConfig{{
			ResourceManager: &config.ResourceManagerConfig{AgentRM: &config.AgentResourceManagerConfig{
				ClusterName: "agent-cluster",
			}},
			ResourcePools: []config.ResourcePoolConfig{{PoolName: cfg.PoolName}},
		}},
	)
	require.ErrorContains(t, err, "conflicts with static pool")
}
