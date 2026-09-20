package rm

import "github.com/determined-ai/determined/master/internal/config"

// ResourcePoolSchedulerConfigProvider exposes the effective scheduler of a Ready pool.
// Implementations must not acquire job locks when looking up pool configuration.
type ResourcePoolSchedulerConfigProvider interface {
	ResourcePoolSchedulerConfig(poolName string) (*config.SchedulerConfig, bool)
}

// DefaultPriorityForPool resolves defaults from live pools, including dynamically created pools.
func DefaultPriorityForPool(manager ResourceManager, poolName string) int {
	if provider, ok := manager.(ResourcePoolSchedulerConfigProvider); ok {
		if scheduler, exists := provider.ResourcePoolSchedulerConfig(poolName); exists {
			if scheduler != nil && scheduler.Priority != nil && scheduler.Priority.DefaultPriority != nil {
				return *scheduler.Priority.DefaultPriority
			}
			// Preserve YAML pools' historical fallback to their manager's priority.
			// Dynamic pools without a priority scheduler are absent from YAML and use 42.
			return config.DefaultPriorityForPool(poolName)
		}
	}
	return config.DefaultPriorityForPool(poolName)
}

// ReadRMPreemptionStatus resolves preemption from the live pool's effective scheduler.
func ReadRMPreemptionStatus(manager ResourceManager, poolName string) bool {
	if provider, ok := manager.(ResourcePoolSchedulerConfigProvider); ok {
		if scheduler, exists := provider.ResourcePoolSchedulerConfig(poolName); exists {
			return scheduler != nil && scheduler.GetPreemption()
		}
	}
	return config.ReadRMPreemptionStatus(poolName)
}
