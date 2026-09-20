package agentrm

import (
	"fmt"
	"sync"
	"testing"

	"github.com/docker/docker/api/types/registry"
	"github.com/stretchr/testify/require"

	"github.com/determined-ai/determined/master/internal/config"
	"github.com/determined-ai/determined/master/internal/rm"
	"github.com/determined-ai/determined/master/pkg/model"
)

const poolRegistryTestPoolName = "test-pool"

func TestPoolRegistryRejectsDuplicatesWithoutReplacing(t *testing.T) {
	registry, err := newPoolRegistry([]config.ResourcePoolConfig{
		{PoolName: poolRegistryTestPoolName, Description: "original"},
	})
	require.NoError(t, err)

	err = registry.addDesired(config.ResourcePoolConfig{
		PoolName: poolRegistryTestPoolName, Description: "replacement",
	})
	require.ErrorContains(t, err, "resource pool test-pool already exists")

	cfg, ok := registry.desiredConfig(poolRegistryTestPoolName)
	require.True(t, ok)
	require.Equal(t, "original", cfg.Description)

	_, err = newPoolRegistry([]config.ResourcePoolConfig{
		{PoolName: poolRegistryTestPoolName}, {PoolName: poolRegistryTestPoolName},
	})
	require.ErrorContains(t, err, "resource pool test-pool already exists")
}

func TestPoolRegistrySnapshotsAreOrderedAndIsolated(t *testing.T) {
	priority := 7
	maxSlots := 3
	workDir := "/original"
	configs := []config.ResourcePoolConfig{
		{
			PoolName: "first",
			Scheduler: &config.SchedulerConfig{
				Priority:      &config.PrioritySchedulerConfig{DefaultPriority: &priority},
				FittingPolicy: best,
			},
			TaskContainerDefaults: &model.TaskContainerDefaultsConfig{
				AddCapabilities: []string{"CAP_SYS_PTRACE"},
				WorkDir:         &workDir,
				Kubernetes:      &model.KubernetesTaskContainerDefaults{MaxSlotsPerPod: &maxSlots},
			},
		},
		{PoolName: "second"},
	}
	registry, err := newPoolRegistry(configs)
	require.NoError(t, err)

	// Mutating the caller's config after insertion must not change registry state.
	*configs[0].Scheduler.Priority.DefaultPriority = 99
	configs[0].TaskContainerDefaults.AddCapabilities[0] = "CHANGED"
	*configs[0].TaskContainerDefaults.WorkDir = "/changed"
	*configs[0].TaskContainerDefaults.Kubernetes.MaxSlotsPerPod = 99

	snapshot := registry.desiredConfigs()
	require.Equal(t, []string{"first", "second"}, []string{snapshot[0].PoolName, snapshot[1].PoolName})
	require.Equal(t, 7, *snapshot[0].Scheduler.Priority.DefaultPriority)
	require.Equal(t, []string{"CAP_SYS_PTRACE"}, snapshot[0].TaskContainerDefaults.AddCapabilities)
	require.Equal(t, "/original", *snapshot[0].TaskContainerDefaults.WorkDir)
	require.Equal(t, 3, *snapshot[0].TaskContainerDefaults.Kubernetes.MaxSlotsPerPod)

	// Mutating any returned config must likewise be isolated from future readers.
	*snapshot[0].Scheduler.Priority.DefaultPriority = 101
	snapshot[0].TaskContainerDefaults.AddCapabilities[0] = "RETURNED_COPY_CHANGED"
	*snapshot[0].TaskContainerDefaults.WorkDir = "/returned-copy-changed"

	cfg, ok := registry.desiredConfig("first")
	require.True(t, ok)
	require.Equal(t, 7, *cfg.Scheduler.Priority.DefaultPriority)
	require.Equal(t, []string{"CAP_SYS_PTRACE"}, cfg.TaskContainerDefaults.AddCapabilities)
	require.Equal(t, "/original", *cfg.TaskContainerDefaults.WorkDir)

	firstPool := &resourcePool{config: &config.ResourcePoolConfig{PoolName: "first"}}
	require.NoError(t, registry.publishReady("first", firstPool))
	ready := registry.readyEntries()
	require.Len(t, ready, 1)
	require.Same(t, firstPool, ready[0].pool)
	ready[0].config.Description = "changed"
	require.NotEqual(t, "changed", registry.readyEntries()[0].config.Description)
}

func TestPoolRegistryConcurrentLookupAndPublication(t *testing.T) {
	registry, err := newPoolRegistry([]config.ResourcePoolConfig{{PoolName: poolRegistryTestPoolName}})
	require.NoError(t, err)
	runtimePool := &resourcePool{
		config: &config.ResourcePoolConfig{PoolName: poolRegistryTestPoolName},
	}

	const readers = 16
	const iterations = 1000
	start := make(chan struct{})
	errs := make(chan error, readers)
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < iterations; j++ {
				cfg, ok := registry.desiredConfig(poolRegistryTestPoolName)
				if !ok || cfg.PoolName != poolRegistryTestPoolName {
					errs <- fmt.Errorf("desired lookup failed")
					return
				}
				if pool, ready := registry.readyPool(poolRegistryTestPoolName); ready && pool != runtimePool {
					errs <- fmt.Errorf("ready lookup changed runtime identity")
					return
				}
				if cfg, ready := registry.readyConfig(poolRegistryTestPoolName); ready &&
					cfg.PoolName != poolRegistryTestPoolName {
					errs <- fmt.Errorf("ready config lookup returned wrong pool")
					return
				}
				_ = registry.desiredConfigs()
				for _, entry := range registry.readyEntries() {
					if entry.pool != runtimePool {
						errs <- fmt.Errorf("ready snapshot changed runtime identity")
						return
					}
				}
			}
		}()
	}

	close(start)
	require.NoError(t, registry.publishReady(poolRegistryTestPoolName, runtimePool))
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	pool, ok := registry.readyPool("pool")
	require.True(t, ok)
	require.Same(t, runtimePool, pool)
}

func TestResourcePoolSchedulerConfigUsesReadyEffectiveConfig(t *testing.T) {
	globalPriority := 11
	localPriority := 22
	configs := []config.ResourcePoolConfig{
		{PoolName: "inherited"},
		{
			PoolName: "local",
			Scheduler: &config.SchedulerConfig{
				Priority:      &config.PrioritySchedulerConfig{DefaultPriority: &localPriority},
				FittingPolicy: best,
			},
		},
	}
	registry, err := newPoolRegistry(configs)
	require.NoError(t, err)
	for _, cfg := range configs {
		require.NoError(t, registry.publishReady(cfg.PoolName, &resourcePool{config: &cfg}))
	}
	rm := &ResourceManager{
		config: &config.AgentResourceManagerConfig{Scheduler: &config.SchedulerConfig{
			Priority:      &config.PrioritySchedulerConfig{DefaultPriority: &globalPriority},
			FittingPolicy: best,
		}},
		registry: registry,
	}

	inherited, ok := rm.ResourcePoolSchedulerConfig("inherited")
	require.True(t, ok)
	require.Equal(t, 11, *inherited.Priority.DefaultPriority)
	*inherited.Priority.DefaultPriority = 99
	inheritedAgain, ok := rm.ResourcePoolSchedulerConfig("inherited")
	require.True(t, ok)
	require.Equal(t, 11, *inheritedAgain.Priority.DefaultPriority)

	local, ok := rm.ResourcePoolSchedulerConfig("local")
	require.True(t, ok)
	require.Equal(t, 22, *local.Priority.DefaultPriority)
	*local.Priority.DefaultPriority = 99
	localAgain, ok := rm.ResourcePoolSchedulerConfig("local")
	require.True(t, ok)
	require.Equal(t, 22, *localAgain.Priority.DefaultPriority)

	_, ok = rm.ResourcePoolSchedulerConfig("missing")
	require.False(t, ok)
}

func TestTaskContainerDefaultsKeepsDynamicEffectiveValues(t *testing.T) {
	staticConfig := config.ResourcePoolConfig{
		PoolName:              "static",
		TaskContainerDefaults: &model.TaskContainerDefaultsConfig{},
	}
	dynamicConfig := config.ResourcePoolConfig{
		PoolName:              "dynamic",
		TaskContainerDefaults: &model.TaskContainerDefaultsConfig{},
	}
	registryState, err := newPoolRegistry([]config.ResourcePoolConfig{staticConfig})
	require.NoError(t, err)
	require.NoError(t, registryState.addDynamicDesired(dynamicConfig))
	require.NoError(t, registryState.publishReady(
		"static", &resourcePool{config: &staticConfig},
	))
	require.NoError(t, registryState.publishReady(
		"dynamic", &resourcePool{config: &dynamicConfig},
	))
	manager := &ResourceManager{registry: registryState}

	workDir := "/new-master-default"
	masterDefaults := model.TaskContainerDefaultsConfig{
		ForcePullImage:  true,
		AddCapabilities: []string{"MASTER_CAPABILITY"},
		WorkDir:         &workDir,
		RegistryAuth:    &registry.AuthConfig{Username: "new-master-default"},
	}

	dynamicDefaults, err := manager.TaskContainerDefaults(
		rm.ResourcePoolName("dynamic"), masterDefaults,
	)
	require.NoError(t, err)
	require.False(t, dynamicDefaults.ForcePullImage)
	require.Empty(t, dynamicDefaults.AddCapabilities)
	require.Nil(t, dynamicDefaults.WorkDir)
	require.Nil(t, dynamicDefaults.RegistryAuth)

	staticDefaults, err := manager.TaskContainerDefaults(
		rm.ResourcePoolName("static"), masterDefaults,
	)
	require.NoError(t, err)
	require.True(t, staticDefaults.ForcePullImage)
	require.Equal(t, []string{"MASTER_CAPABILITY"}, staticDefaults.AddCapabilities)
	require.Equal(t, "/new-master-default", *staticDefaults.WorkDir)
	require.Equal(t, "new-master-default", staticDefaults.RegistryAuth.Username)
}
