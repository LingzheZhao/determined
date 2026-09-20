package agentrm

import (
	"fmt"
	"sync"

	"github.com/jinzhu/copier"

	"github.com/determined-ai/determined/master/internal/config"
)

// poolRegistry owns resource-pool desired configuration and initialized runtime pools.
// Entries are append-only: a runtime pool is published only after initialization completes.
type poolRegistry struct {
	mu      sync.RWMutex
	entries map[string]poolRegistryEntry
	order   []string
}

type poolRegistryEntry struct {
	config                config.ResourcePoolConfig
	pool                  *resourcePool
	taskDefaultsEffective bool
}

func newPoolRegistry(configs []config.ResourcePoolConfig) (*poolRegistry, error) {
	r := &poolRegistry{entries: make(map[string]poolRegistryEntry, len(configs))}
	for _, cfg := range configs {
		if err := r.addDesired(cfg); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// addDesired appends a desired pool configuration. Existing entries are never replaced.
func (r *poolRegistry) addDesired(cfg config.ResourcePoolConfig) error {
	return r.addDesiredEntry(cfg, false)
}

// addDynamicDesired appends a persisted dynamic config whose task container defaults have already
// been resolved and frozen. Keeping this bit with the config avoids database work on task paths.
func (r *poolRegistry) addDynamicDesired(cfg config.ResourcePoolConfig) error {
	return r.addDesiredEntry(cfg, true)
}

func (r *poolRegistry) addDesiredEntry(
	cfg config.ResourcePoolConfig,
	taskDefaultsEffective bool,
) error {
	if cfg.PoolName == "" {
		return fmt.Errorf("resource pool name cannot be empty")
	}
	cfgCopy, err := cloneResourcePoolConfig(cfg)
	if err != nil {
		return fmt.Errorf("copying resource pool %s configuration: %w", cfg.PoolName, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.entries[cfg.PoolName]; ok {
		return fmt.Errorf("resource pool %s already exists", cfg.PoolName)
	}
	r.entries[cfg.PoolName] = poolRegistryEntry{
		config:                cfgCopy,
		taskDefaultsEffective: taskDefaultsEffective,
	}
	r.order = append(r.order, cfg.PoolName)
	return nil
}

// desiredConfig returns configuration regardless of whether its runtime pool is Ready. This is
// used while restoring agent state before runtime pools are initialized.
func (r *poolRegistry) desiredConfig(name string) (config.ResourcePoolConfig, bool) {
	r.mu.RLock()
	entry, ok := r.entries[name]
	r.mu.RUnlock()
	if !ok {
		return config.ResourcePoolConfig{}, false
	}
	return mustCloneResourcePoolConfig(entry.config), true
}

// readyConfig returns configuration only after the runtime pool has been published.
func (r *poolRegistry) readyConfig(name string) (config.ResourcePoolConfig, bool) {
	r.mu.RLock()
	entry, ok := r.entries[name]
	r.mu.RUnlock()
	if !ok || entry.pool == nil {
		return config.ResourcePoolConfig{}, false
	}
	return mustCloneResourcePoolConfig(entry.config), true
}

// desiredConfigs returns an ordered, isolated snapshot of all desired configurations.
func (r *poolRegistry) desiredConfigs() []config.ResourcePoolConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()
	configs := make([]config.ResourcePoolConfig, 0, len(r.order))
	for _, name := range r.order {
		configs = append(configs, mustCloneResourcePoolConfig(r.entries[name].config))
	}
	return configs
}

// publishReady atomically makes a completely initialized runtime pool available to readers.
func (r *poolRegistry) publishReady(name string, pool *resourcePool) error {
	if pool == nil {
		return fmt.Errorf("cannot publish nil resource pool %s", name)
	}
	if pool.config == nil || pool.config.PoolName != name {
		return fmt.Errorf("runtime resource pool does not match desired resource pool %s", name)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[name]
	if !ok {
		return fmt.Errorf("cannot publish undesired resource pool %s", name)
	}
	if entry.pool != nil {
		return fmt.Errorf("resource pool %s is already ready", name)
	}
	entry.pool = pool
	r.entries[name] = entry
	return nil
}

func (r *poolRegistry) readyPool(name string) (*resourcePool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.entries[name]
	if !ok || entry.pool == nil {
		return nil, false
	}
	return entry.pool, true
}

func (r *poolRegistry) readyEntry(name string) (poolRegistryEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.entries[name]
	if !ok || entry.pool == nil {
		return poolRegistryEntry{}, false
	}
	entry.config = mustCloneResourcePoolConfig(entry.config)
	return entry, true
}

// readyEntries returns an ordered snapshot. Runtime pointers retain their identity while configs
// are copied so callers cannot mutate registry state.
func (r *poolRegistry) readyEntries() []poolRegistryEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entries := make([]poolRegistryEntry, 0, len(r.order))
	for _, name := range r.order {
		entry := r.entries[name]
		if entry.pool == nil {
			continue
		}
		entry.config = mustCloneResourcePoolConfig(entry.config)
		entries = append(entries, entry)
	}
	return entries
}

func cloneResourcePoolConfig(cfg config.ResourcePoolConfig) (config.ResourcePoolConfig, error) {
	var result config.ResourcePoolConfig
	if err := copier.CopyWithOption(
		&result, &cfg, copier.Option{DeepCopy: true, IgnoreEmpty: true},
	); err != nil {
		return config.ResourcePoolConfig{}, err
	}
	return result, nil
}

func mustCloneResourcePoolConfig(cfg config.ResourcePoolConfig) config.ResourcePoolConfig {
	result, err := cloneResourcePoolConfig(cfg)
	if err != nil {
		// Configs are cloned successfully before insertion. A later clone of the same immutable
		// value cannot fail unless the config type's copy behavior changes incompatibly.
		panic(fmt.Sprintf("copying registered resource pool %s configuration: %v", cfg.PoolName, err))
	}
	return result
}
