package agentrm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/determined-ai/determined/master/internal/config"
	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/pkg/check"
	"github.com/determined-ai/determined/master/pkg/model"
)

const dynamicResourcePoolConfigVersion = 1

const dynamicPoolPersistenceTimeout = 10 * time.Second
const dynamicPoolScanInterval = time.Second

var dynamicPoolNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

var setDynamicResourcePoolState = func(
	database *db.PgDB,
	ctx context.Context,
	poolName string,
	state db.DynamicResourcePoolState,
	errText *string,
) (db.DynamicResourcePool, error) {
	return database.SetDynamicResourcePoolState(ctx, poolName, state, errText)
}

var stopPreparedDynamicResourcePool = func(pool *resourcePool) {
	pool.stop()
}

var createDynamicPoolRuntime = func(
	manager *ResourceManager, cfg config.ResourcePoolConfig,
) (*resourcePool, error) {
	return manager.createResourcePool(manager.db, cfg, manager.cert)
}

var (
	// ErrInvalidDynamicResourcePool indicates an invalid or unsupported dynamic pool config.
	ErrInvalidDynamicResourcePool = errors.New("invalid dynamic resource pool")
	// ErrStaticResourcePoolConflict indicates that a desired dynamic name is statically configured.
	ErrStaticResourcePoolConflict = errors.New("resource pool name conflicts with static configuration")
	// ErrDynamicResourcePoolPersistence indicates a post-insert durable state write failed.
	ErrDynamicResourcePoolPersistence = errors.New("persisting dynamic resource pool state")
)

// NormalizeDynamicResourcePoolConfig validates a user-supplied config and freezes defaults that
// would otherwise change after a master YAML edit. Provider-backed pools are intentionally outside
// the first milestone.
func (a *ResourceManager) NormalizeDynamicResourcePoolConfig(
	cfg config.ResourcePoolConfig,
	masterDefaults model.TaskContainerDefaultsConfig,
) (config.ResourcePoolConfig, error) {
	if cfg.Provider != nil {
		return config.ResourcePoolConfig{}, fmt.Errorf(
			"%w: provider configuration is not supported", ErrInvalidDynamicResourcePool,
		)
	}
	if strings.TrimSpace(cfg.PoolName) == "" {
		return config.ResourcePoolConfig{}, fmt.Errorf(
			"%w: resource pool name cannot be empty", ErrInvalidDynamicResourcePool,
		)
	}
	if !dynamicPoolNamePattern.MatchString(cfg.PoolName) {
		return config.ResourcePoolConfig{}, fmt.Errorf(
			"%w: resource pool name must match %s",
			ErrInvalidDynamicResourcePool,
			dynamicPoolNamePattern.String(),
		)
	}

	if cfg.Scheduler == nil {
		cfg.Scheduler = a.config.Scheduler
	}
	if cfg.Scheduler == nil {
		return config.ResourcePoolConfig{}, fmt.Errorf(
			"%w: no scheduler configuration is available", ErrInvalidDynamicResourcePool,
		)
	}

	effectiveDefaults := masterDefaults
	var err error
	if cfg.TaskContainerDefaults != nil {
		effectiveDefaults, err = effectiveDefaults.Merge(*cfg.TaskContainerDefaults)
	} else {
		effectiveDefaults, err = effectiveDefaults.Merge(model.TaskContainerDefaultsConfig{})
	}
	if err != nil {
		return config.ResourcePoolConfig{}, fmt.Errorf(
			"%w: resolving task container defaults: %v", ErrInvalidDynamicResourcePool, err,
		)
	}
	cfg.TaskContainerDefaults = &effectiveDefaults

	// Deep-copy pointers inherited from master config before validation and persistence.
	cfg, err = cloneResourcePoolConfig(cfg)
	if err != nil {
		return config.ResourcePoolConfig{}, fmt.Errorf(
			"%w: copying effective configuration: %v", ErrInvalidDynamicResourcePool, err,
		)
	}
	if err = check.Validate(&cfg); err != nil {
		return config.ResourcePoolConfig{}, fmt.Errorf(
			"%w: %v", ErrInvalidDynamicResourcePool, err,
		)
	}
	if _, err = MakeScheduler(cfg.Scheduler); err != nil {
		return config.ResourcePoolConfig{}, fmt.Errorf(
			"%w: scheduler: %v", ErrInvalidDynamicResourcePool, err,
		)
	}
	return cfg, nil
}

// CreateDynamicResourcePool persists a normalized desired config. The RM-owned worker initializes
// Pending records and reconciles Ready records missing a runtime after ambiguous state writes.
func (a *ResourceManager) CreateDynamicResourcePool(
	ctx context.Context,
	idempotencyKey string,
	cfg config.ResourcePoolConfig,
	masterDefaults model.TaskContainerDefaultsConfig,
) (record db.DynamicResourcePool, created bool, err error) {
	if err = validateDynamicPoolIdempotencyKey(idempotencyKey); err != nil {
		return record, false, err
	}

	cfg, err = a.NormalizeDynamicResourcePoolConfig(cfg, masterDefaults)
	if err != nil {
		return record, false, err
	}
	configJSON, configHash, err := marshalDynamicResourcePoolConfig(cfg)
	if err != nil {
		return record, false, err
	}

	// A desired name without a durable dynamic record is a static pool in this RM.
	if _, ok := a.registry.desiredConfig(cfg.PoolName); ok {
		if _, getErr := a.db.DynamicResourcePoolByName(ctx, cfg.PoolName); errors.Is(
			getErr, db.ErrDynamicResourcePoolNotFound,
		) {
			return record, false, fmt.Errorf(
				"%w: %q", ErrStaticResourcePoolConflict, cfg.PoolName,
			)
		} else if getErr != nil {
			return record, false, getErr
		}
	}

	record, created, err = a.db.CreateDynamicResourcePool(ctx, db.DynamicResourcePool{
		ClusterName:    a.config.ClusterName,
		PoolName:       cfg.PoolName,
		ConfigVersion:  dynamicResourcePoolConfigVersion,
		IdempotencyKey: idempotencyKey,
		Config:         configJSON,
		ConfigHash:     configHash,
	})
	if err == nil && (record.State == db.DynamicResourcePoolPending ||
		record.State == db.DynamicResourcePoolReady && !a.IsDynamicResourcePoolReady(record.PoolName)) {
		a.wakeDynamicPoolWorker()
	}
	return record, created, err
}

func validateDynamicPoolIdempotencyKey(idempotencyKey string) error {
	if strings.TrimSpace(idempotencyKey) == "" {
		return fmt.Errorf("%w: idempotency_key is required", ErrInvalidDynamicResourcePool)
	}
	if len(idempotencyKey) > 512 {
		return fmt.Errorf(
			"%w: idempotency_key must be at most 512 bytes", ErrInvalidDynamicResourcePool,
		)
	}
	return nil
}

// RetryDynamicResourcePool explicitly retries a Failed dynamic pool operation.
func (a *ResourceManager) RetryDynamicResourcePool(
	ctx context.Context, poolName string,
) (db.DynamicResourcePool, error) {
	record, err := a.db.BeginDynamicResourcePoolRetry(ctx, a.config.ClusterName, poolName)
	if err != nil {
		return db.DynamicResourcePool{}, err
	}
	a.wakeDynamicPoolWorker()
	return record, nil
}

func (a *ResourceManager) startDynamicPoolWorker(parentCtx context.Context) {
	ctx, cancel := context.WithCancel(parentCtx)
	a.dynamicPoolCancel = cancel
	a.dynamicPoolWake = make(chan struct{}, 1)
	a.dynamicPoolDone = make(chan struct{})
	go func() {
		defer close(a.dynamicPoolDone)
		ticker := time.NewTicker(dynamicPoolScanInterval)
		defer ticker.Stop()
		for {
			a.advancePendingDynamicPools(ctx)
			select {
			case <-ctx.Done():
				return
			case <-a.dynamicPoolWake:
			case <-ticker.C:
			}
		}
	}()
}

func (a *ResourceManager) wakeDynamicPoolWorker() {
	if a.dynamicPoolWake != nil {
		select {
		case a.dynamicPoolWake <- struct{}{}:
		default:
		}
	}
}

// One worker per agent RM serializes initialization, replay, and periodic recovery.
func (a *ResourceManager) advancePendingDynamicPools(ctx context.Context) {
	scanCtx, cancel := context.WithTimeout(ctx, dynamicPoolPersistenceTimeout)
	records, err := a.db.ListDynamicResourcePools(scanCtx, a.config.ClusterName)
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			a.syslog.WithError(err).Warn("scanning dynamic resource pools")
		}
		return
	}
	for _, record := range records {
		if ctx.Err() != nil {
			return
		}
		if record.State != db.DynamicResourcePoolPending &&
			(record.State != db.DynamicResourcePoolReady || a.IsDynamicResourcePoolReady(record.PoolName)) {
			continue
		}
		cfg, decodeErr := decodeStoredDynamicResourcePool(record)
		if decodeErr != nil {
			if record.State == db.DynamicResourcePoolPending {
				_, err = a.failDynamicResourcePool(ctx, record, decodeErr.Error())
			} else {
				err = decodeErr
			}
		} else {
			_, err = a.initializeDynamicResourcePool(ctx, record, cfg)
		}
		if err != nil && ctx.Err() == nil {
			a.syslog.WithError(err).WithField("pool", record.PoolName).
				Warn("advancing dynamic resource pool")
		}
	}
}

func (a *ResourceManager) initializeDynamicResourcePool(
	ctx context.Context,
	record db.DynamicResourcePool,
	cfg config.ResourcePoolConfig,
) (db.DynamicResourcePool, error) {
	if existing, ok := a.registry.desiredConfig(cfg.PoolName); ok {
		_, existingHash, err := marshalDynamicResourcePoolConfig(existing)
		if err != nil || existingHash != record.ConfigHash {
			if err == nil {
				err = fmt.Errorf("desired runtime config differs from durable config")
			}
			if record.State == db.DynamicResourcePoolReady {
				return record, err
			}
			return a.failDynamicResourcePool(ctx, record, err.Error())
		}
		if _, ready := a.registry.readyPool(cfg.PoolName); ready {
			if record.State == db.DynamicResourcePoolReady {
				return record, nil
			}
			return setDynamicResourcePoolState(
				a.db, ctx, cfg.PoolName, db.DynamicResourcePoolReady, nil,
			)
		}
	} else if err := a.registry.addDynamicDesired(cfg); err != nil {
		if record.State == db.DynamicResourcePoolReady {
			return record, err
		}
		return a.failDynamicResourcePool(ctx, record, err.Error())
	}

	pool, err := createDynamicPoolRuntime(a, cfg)
	if err != nil {
		if record.State == db.DynamicResourcePoolReady {
			return record, err
		}
		return a.failDynamicResourcePool(ctx, record, err.Error())
	}
	// Ready may have committed even though its original write returned an error. Rebuild its
	// runtime from the frozen config without changing durable state; transient failures retry.
	if record.State == db.DynamicResourcePoolReady {
		if err = a.registry.publishReady(cfg.PoolName, pool); err != nil {
			stopPreparedDynamicResourcePool(pool)
			return record, err
		}
		return record, nil
	}
	persistenceCtx, cancel := dynamicPoolPersistenceContext(ctx)
	ready, err := setDynamicResourcePoolState(
		a.db, persistenceCtx, cfg.PoolName, db.DynamicResourcePoolReady, nil,
	)
	cancel()
	if err != nil {
		stopPreparedDynamicResourcePool(pool)
		failed, failErr := a.failDynamicResourcePool(ctx, record, err.Error())
		if failErr != nil && failed.State != db.DynamicResourcePoolFailed {
			return failed, fmt.Errorf("%w: Ready write failed: %v; Failed write failed: %v",
				ErrDynamicResourcePoolPersistence, err, failErr)
		}
		return failed, fmt.Errorf("%w: Ready write failed: %v",
			ErrDynamicResourcePoolPersistence, err)
	}
	if err = a.registry.publishReady(cfg.PoolName, pool); err != nil {
		stopPreparedDynamicResourcePool(pool)
		return a.failDynamicResourcePool(ctx, ready, err.Error())
	}
	return ready, nil
}

func (a *ResourceManager) failDynamicResourcePool(
	ctx context.Context, record db.DynamicResourcePool, failureMessage string,
) (db.DynamicResourcePool, error) {
	persistenceCtx, cancel := dynamicPoolPersistenceContext(ctx)
	defer cancel()
	failed, err := setDynamicResourcePoolState(
		a.db, persistenceCtx, record.PoolName, db.DynamicResourcePoolFailed, &failureMessage,
	)
	if err != nil {
		return record, fmt.Errorf(
			"%w: recording initialization failure (%v): %v",
			ErrDynamicResourcePoolPersistence, failureMessage, err,
		)
	}
	// Runtime initialization failures are durable operation results, not transport failures.
	return failed, nil
}

// IsDynamicResourcePoolReady reports whether a runtime pool has been atomically published. It is
// used to avoid advertising a transient durable Ready written immediately before publication.
func (a *ResourceManager) IsDynamicResourcePoolReady(poolName string) bool {
	_, ready := a.registry.readyPool(poolName)
	return ready
}

func dynamicPoolPersistenceContext(requestCtx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(requestCtx), dynamicPoolPersistenceTimeout)
}

// ValidatePersistedDynamicPoolConfigs validates every durable desired config against all static
// resource pools and resource-manager types. It is called before startup mutates agent state.
func ValidatePersistedDynamicPoolConfigs(
	ctx context.Context,
	database *db.PgDB,
	rmConfigs []*config.ResourceManagerWithPoolsConfig,
) error {
	records, err := database.ListDynamicResourcePools(ctx, "")
	if err != nil {
		return err
	}
	staticNames := make(map[string]string)
	agentClusters := make(map[string]bool)
	for _, rmConfig := range rmConfigs {
		clusterName := rmConfig.ResourceManager.ClusterName()
		agentClusters[clusterName] = rmConfig.ResourceManager.AgentRM != nil
		for _, pool := range rmConfig.ResourcePools {
			staticNames[pool.PoolName] = clusterName
		}
	}
	for _, record := range records {
		isAgent, exists := agentClusters[record.ClusterName]
		if !exists {
			return fmt.Errorf(
				"dynamic resource pool %q references unknown resource manager %q",
				record.PoolName, record.ClusterName,
			)
		}
		if !isAgent {
			return fmt.Errorf(
				"dynamic resource pool %q references unsupported non-agent resource manager %q",
				record.PoolName, record.ClusterName,
			)
		}
		if staticCluster, ok := staticNames[record.PoolName]; ok {
			return fmt.Errorf(
				"dynamic resource pool %q conflicts with static pool in resource manager %q",
				record.PoolName, staticCluster,
			)
		}
		if _, err = decodeStoredDynamicResourcePool(record); err != nil {
			return fmt.Errorf("dynamic resource pool %q: %w", record.PoolName, err)
		}
	}
	return nil
}

// loadDynamicPoolConfigs returns durable desired configs for one agent RM. The global startup
// validator is responsible for cross-RM/static collisions.
func loadDynamicPoolConfigs(
	database *db.PgDB,
	clusterName string,
	static []config.ResourcePoolConfig,
) ([]config.ResourcePoolConfig, error) {
	records, err := database.ListDynamicResourcePools(context.Background(), clusterName)
	if err != nil {
		return nil, err
	}
	staticNames := make(map[string]bool, len(static))
	for _, cfg := range static {
		staticNames[cfg.PoolName] = true
	}
	configs := make([]config.ResourcePoolConfig, 0, len(records))
	for _, record := range records {
		if staticNames[record.PoolName] {
			return nil, fmt.Errorf(
				"dynamic resource pool %q conflicts with static resource pool", record.PoolName,
			)
		}
		cfg, err := decodeStoredDynamicResourcePool(record)
		if err != nil {
			return nil, fmt.Errorf("dynamic resource pool %q: %w", record.PoolName, err)
		}
		configs = append(configs, cfg)
	}
	return configs, nil
}

// markDynamicPoolsReady records successful startup initialization of persisted pools.
func markDynamicPoolsReady(
	ctx context.Context,
	database *db.PgDB,
	configs []config.ResourcePoolConfig,
) error {
	for _, cfg := range configs {
		if _, err := database.SetDynamicResourcePoolState(
			ctx, cfg.PoolName, db.DynamicResourcePoolReady, nil,
		); err != nil {
			return err
		}
	}
	return nil
}

func decodeStoredDynamicResourcePool(
	record db.DynamicResourcePool,
) (config.ResourcePoolConfig, error) {
	if record.ConfigVersion != dynamicResourcePoolConfigVersion {
		return config.ResourcePoolConfig{}, fmt.Errorf(
			"unsupported config version %d", record.ConfigVersion,
		)
	}
	if err := ValidateDynamicResourcePoolConfigJSON(record.Config); err != nil {
		return config.ResourcePoolConfig{}, fmt.Errorf("validating stored config schema: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(record.Config))
	decoder.DisallowUnknownFields()
	var cfg config.ResourcePoolConfig
	if err := decoder.Decode(&cfg); err != nil {
		return config.ResourcePoolConfig{}, fmt.Errorf("decoding effective config: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return config.ResourcePoolConfig{}, err
	}
	if cfg.PoolName != record.PoolName {
		return config.ResourcePoolConfig{}, fmt.Errorf(
			"stored pool name %q does not match record name %q", cfg.PoolName, record.PoolName,
		)
	}
	if cfg.Provider != nil {
		return config.ResourcePoolConfig{}, fmt.Errorf("provider configuration is unsupported")
	}
	if cfg.Scheduler == nil || cfg.TaskContainerDefaults == nil {
		return config.ResourcePoolConfig{}, fmt.Errorf(
			"effective scheduler and task_container_defaults must be stored",
		)
	}
	if err := check.Validate(&cfg); err != nil {
		return config.ResourcePoolConfig{}, fmt.Errorf("validating effective config: %w", err)
	}
	if _, err := MakeScheduler(cfg.Scheduler); err != nil {
		return config.ResourcePoolConfig{}, fmt.Errorf("validating scheduler: %w", err)
	}
	_, hash, err := marshalDynamicResourcePoolConfig(cfg)
	if err != nil {
		return config.ResourcePoolConfig{}, err
	}
	if hash != record.ConfigHash {
		return config.ResourcePoolConfig{}, fmt.Errorf("effective config hash mismatch")
	}
	return cfg, nil
}

// ValidateDynamicResourcePoolConfigJSON strictly validates object fields before custom config JSON
// unmarshallers can silently ignore unknown fields. Kubernetes pod specs remain ordinary
// Kubernetes JSON objects and are validated by their own decoder.
func ValidateDynamicResourcePoolConfigJSON(raw json.RawMessage) error {
	var rawConfig map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawConfig); err != nil {
		return fmt.Errorf("config must be a JSON object: %w", err)
	}
	if err := rejectUnknownDynamicPoolJSONFields(
		rawConfig, dynamicPoolJSONFieldsForType(reflect.TypeOf(config.ResourcePoolConfig{})), "config",
	); err != nil {
		return err
	}
	if provider, ok := rawConfig["provider"]; ok &&
		!bytes.Equal(bytes.TrimSpace(provider), []byte("null")) {
		return fmt.Errorf("config.provider is not supported")
	}
	if schedulerRaw, ok := rawConfig["scheduler"]; ok &&
		!bytes.Equal(bytes.TrimSpace(schedulerRaw), []byte("null")) {
		var scheduler map[string]json.RawMessage
		if err := json.Unmarshal(schedulerRaw, &scheduler); err != nil {
			return fmt.Errorf("config.scheduler must be a JSON object: %w", err)
		}
		allowed := dynamicPoolJSONFieldsForType(reflect.TypeOf(config.SchedulerConfig{}))
		allowed["type"] = true
		allowed["preemption"] = true
		allowed["default_priority"] = true
		if err := rejectUnknownDynamicPoolJSONFields(
			scheduler, allowed, "config.scheduler",
		); err != nil {
			return err
		}
	}
	if defaultsRaw, ok := rawConfig["task_container_defaults"]; ok &&
		!bytes.Equal(bytes.TrimSpace(defaultsRaw), []byte("null")) {
		var defaults map[string]json.RawMessage
		if err := json.Unmarshal(defaultsRaw, &defaults); err != nil {
			return fmt.Errorf("config.task_container_defaults must be a JSON object: %w", err)
		}
		defaultsType := reflect.TypeOf(model.TaskContainerDefaultsConfig{})
		if err := rejectUnknownDynamicPoolJSONFields(
			defaults, dynamicPoolJSONFieldsForType(defaultsType), "config.task_container_defaults",
		); err != nil {
			return err
		}
		for _, field := range []string{"registry_auth", "kubernetes"} {
			nestedRaw, exists := defaults[field]
			if !exists || bytes.Equal(bytes.TrimSpace(nestedRaw), []byte("null")) {
				continue
			}
			nestedType, exists := dynamicPoolJSONFieldType(defaultsType, field)
			if !exists {
				continue
			}
			var nested map[string]json.RawMessage
			if err := json.Unmarshal(nestedRaw, &nested); err != nil {
				return fmt.Errorf(
					"config.task_container_defaults.%s must be a JSON object: %w", field, err,
				)
			}
			if err := rejectUnknownDynamicPoolJSONFields(
				nested,
				dynamicPoolJSONFieldsForType(nestedType),
				"config.task_container_defaults."+field,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func rejectUnknownDynamicPoolJSONFields(
	object map[string]json.RawMessage, allowed map[string]bool, path string,
) error {
	for field := range object {
		if !allowed[field] {
			return fmt.Errorf("unknown field %q in %s", field, path)
		}
	}
	return nil
}

func dynamicPoolJSONFieldsForType(typ reflect.Type) map[string]bool {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	result := make(map[string]bool)
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name != "" && name != "-" {
			result[name] = true
		}
	}
	return result
}

func dynamicPoolJSONFieldType(typ reflect.Type, jsonName string) (reflect.Type, bool) {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == jsonName {
			return field.Type, true
		}
	}
	return nil, false
}

func marshalDynamicResourcePoolConfig(
	cfg config.ResourcePoolConfig,
) (json.RawMessage, string, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, "", fmt.Errorf("marshaling effective dynamic pool config: %w", err)
	}
	hash := sha256.Sum256(raw)
	return raw, hex.EncodeToString(hash[:]), nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("JSON body must contain exactly one value")
		}
		return fmt.Errorf("reading JSON body: %w", err)
	}
	return nil
}
