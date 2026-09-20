package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// DynamicResourcePoolState is the durable initialization state of a dynamic resource pool.
type DynamicResourcePoolState string

const (
	// DynamicResourcePoolPending means the desired config is durable but runtime initialization has
	// not completed.
	DynamicResourcePoolPending DynamicResourcePoolState = "Pending"
	// DynamicResourcePoolReady means the runtime resource pool is available for admission.
	DynamicResourcePoolReady DynamicResourcePoolState = "Ready"
	// DynamicResourcePoolFailed means runtime initialization failed and requires an explicit retry.
	DynamicResourcePoolFailed DynamicResourcePoolState = "Failed"
)

var (
	// ErrDynamicResourcePoolConflict indicates a pool name or idempotency key conflict.
	ErrDynamicResourcePoolConflict = errors.New("dynamic resource pool conflict")
	// ErrDynamicResourcePoolNotFound indicates that a dynamic resource pool does not exist.
	ErrDynamicResourcePoolNotFound = errors.New("dynamic resource pool not found")
	// ErrDynamicResourcePoolNotFailed indicates that a retry targeted a non-failed operation.
	ErrDynamicResourcePoolNotFailed = errors.New("dynamic resource pool is not failed")
)

// DynamicResourcePool is the durable desired configuration and operation status for a dynamic
// resource pool. Config is the normalized, effective ResourcePoolConfig JSON.
type DynamicResourcePool struct {
	ClusterName    string                   `db:"cluster_name" json:"cluster_name"`
	PoolName       string                   `db:"pool_name" json:"pool_name"`
	ConfigVersion  int                      `db:"config_version" json:"config_version"`
	IdempotencyKey string                   `db:"idempotency_key" json:"-"`
	Config         json.RawMessage          `db:"config" json:"config"`
	ConfigHash     string                   `db:"config_hash" json:"-"`
	State          DynamicResourcePoolState `db:"state" json:"state"`
	Error          *string                  `db:"error" json:"error,omitempty"`
	CreatedAt      time.Time                `db:"created_at" json:"created_at"`
	UpdatedAt      time.Time                `db:"updated_at" json:"updated_at"`
}

// CreateDynamicResourcePool durably inserts a Pending desired config. An exact replay using the
// same resource-manager-scoped idempotency key returns the original operation. Pool names are
// globally unique because regular multi-RM admission routes by pool name alone.
func (db *PgDB) CreateDynamicResourcePool(
	ctx context.Context,
	record DynamicResourcePool,
) (stored DynamicResourcePool, created bool, err error) {
	result, err := db.sql.ExecContext(ctx, `
INSERT INTO dynamic_resource_pools
    (cluster_name, pool_name, config_version, idempotency_key, config, config_hash, state)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT DO NOTHING`,
		record.ClusterName,
		record.PoolName,
		record.ConfigVersion,
		record.IdempotencyKey,
		[]byte(record.Config),
		record.ConfigHash,
		DynamicResourcePoolPending,
	)
	if err != nil {
		return DynamicResourcePool{}, false, fmt.Errorf("inserting dynamic resource pool: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return DynamicResourcePool{}, false, fmt.Errorf("checking dynamic resource pool insert: %w", err)
	}
	if rows == 1 {
		stored, err = db.DynamicResourcePoolByName(ctx, record.PoolName)
		return stored, true, err
	}

	stored, err = db.dynamicResourcePoolByIdempotencyKey(
		ctx, record.ClusterName, record.IdempotencyKey,
	)
	if err == nil {
		if stored.PoolName == record.PoolName &&
			stored.ConfigVersion == record.ConfigVersion &&
			stored.ConfigHash == record.ConfigHash {
			return stored, false, nil
		}
		return DynamicResourcePool{}, false, fmt.Errorf(
			"%w: idempotency key already names a different desired config",
			ErrDynamicResourcePoolConflict,
		)
	}
	if !errors.Is(err, ErrDynamicResourcePoolNotFound) {
		return DynamicResourcePool{}, false, err
	}

	if _, err = db.DynamicResourcePoolByName(ctx, record.PoolName); err == nil {
		return DynamicResourcePool{}, false, fmt.Errorf(
			"%w: resource pool name %q already exists", ErrDynamicResourcePoolConflict, record.PoolName,
		)
	} else if !errors.Is(err, ErrDynamicResourcePoolNotFound) {
		return DynamicResourcePool{}, false, err
	}
	return DynamicResourcePool{}, false, fmt.Errorf(
		"%w: desired config conflicted with an existing operation", ErrDynamicResourcePoolConflict,
	)
}

// DynamicResourcePoolByName returns a dynamic resource pool by its globally unique name.
func (db *PgDB) DynamicResourcePoolByName(
	ctx context.Context, poolName string,
) (DynamicResourcePool, error) {
	var record DynamicResourcePool
	err := db.sql.GetContext(ctx, &record, `
SELECT cluster_name, pool_name, config_version, idempotency_key, config, config_hash,
       state, error, created_at, updated_at
FROM dynamic_resource_pools
WHERE pool_name = $1`, poolName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DynamicResourcePool{}, ErrDynamicResourcePoolNotFound
		}
		return DynamicResourcePool{}, fmt.Errorf("reading dynamic resource pool: %w", err)
	}
	return record, nil
}

func (db *PgDB) dynamicResourcePoolByIdempotencyKey(
	ctx context.Context, clusterName, idempotencyKey string,
) (DynamicResourcePool, error) {
	var record DynamicResourcePool
	err := db.sql.GetContext(ctx, &record, `
SELECT cluster_name, pool_name, config_version, idempotency_key, config, config_hash,
       state, error, created_at, updated_at
FROM dynamic_resource_pools
WHERE cluster_name = $1 AND idempotency_key = $2`, clusterName, idempotencyKey)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DynamicResourcePool{}, ErrDynamicResourcePoolNotFound
		}
		return DynamicResourcePool{}, fmt.Errorf("reading dynamic resource pool by idempotency key: %w", err)
	}
	return record, nil
}

// ListDynamicResourcePools lists all dynamic resource pools, optionally scoped to a resource
// manager cluster name, in creation order.
func (db *PgDB) ListDynamicResourcePools(
	ctx context.Context, clusterName string,
) ([]DynamicResourcePool, error) {
	records := []DynamicResourcePool{}
	query := `
SELECT cluster_name, pool_name, config_version, idempotency_key, config, config_hash,
       state, error, created_at, updated_at
FROM dynamic_resource_pools`
	args := []interface{}{}
	if clusterName != "" {
		query += " WHERE cluster_name = $1"
		args = append(args, clusterName)
	}
	query += " ORDER BY created_at, pool_name"
	if err := db.sql.SelectContext(ctx, &records, query, args...); err != nil {
		return nil, fmt.Errorf("listing dynamic resource pools: %w", err)
	}
	return records, nil
}

// SetDynamicResourcePoolState records an initialization result.
func (db *PgDB) SetDynamicResourcePoolState(
	ctx context.Context,
	poolName string,
	state DynamicResourcePoolState,
	errText *string,
) (DynamicResourcePool, error) {
	var record DynamicResourcePool
	err := db.sql.GetContext(ctx, &record, `
UPDATE dynamic_resource_pools
SET state = $2, error = $3, updated_at = NOW()
WHERE pool_name = $1
RETURNING cluster_name, pool_name, config_version, idempotency_key, config, config_hash,
          state, error, created_at, updated_at`, poolName, state, errText)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DynamicResourcePool{}, ErrDynamicResourcePoolNotFound
		}
		return DynamicResourcePool{}, fmt.Errorf("updating dynamic resource pool state: %w", err)
	}
	return record, nil
}

// BeginDynamicResourcePoolRetry atomically changes a Failed operation back to Pending.
func (db *PgDB) BeginDynamicResourcePoolRetry(
	ctx context.Context, clusterName, poolName string,
) (DynamicResourcePool, error) {
	var record DynamicResourcePool
	err := db.sql.GetContext(ctx, &record, `
UPDATE dynamic_resource_pools
SET state = $3, error = NULL, updated_at = NOW()
WHERE cluster_name = $1 AND pool_name = $2 AND state = $4
RETURNING cluster_name, pool_name, config_version, idempotency_key, config, config_hash,
          state, error, created_at, updated_at`,
		clusterName, poolName, DynamicResourcePoolPending, DynamicResourcePoolFailed)
	if err == nil {
		return record, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return DynamicResourcePool{}, fmt.Errorf("beginning dynamic resource pool retry: %w", err)
	}
	record, getErr := db.DynamicResourcePoolByName(ctx, poolName)
	if getErr != nil {
		return DynamicResourcePool{}, getErr
	}
	if record.ClusterName != clusterName {
		return DynamicResourcePool{}, ErrDynamicResourcePoolNotFound
	}
	return DynamicResourcePool{}, fmt.Errorf(
		"%w: current state is %s", ErrDynamicResourcePoolNotFailed, record.State,
	)
}
