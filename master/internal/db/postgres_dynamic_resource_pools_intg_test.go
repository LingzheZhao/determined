//go:build integration

package db

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDynamicResourcePoolPersistenceAndIdempotency(t *testing.T) {
	database, cleanup := MustResolveNewPostgresDatabase(t)
	defer cleanup()
	MustMigrateTestPostgres(t, database, "file://../../static/migrations", "up")

	ctx := context.Background()
	desired := DynamicResourcePool{
		ClusterName:    "agents-a",
		PoolName:       "online-a",
		ConfigVersion:  1,
		IdempotencyKey: "operation-a",
		Config:         json.RawMessage(`{"pool_name":"online-a"}`),
		ConfigHash:     "hash-a",
	}
	createdRecord, created, err := database.CreateDynamicResourcePool(ctx, desired)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, DynamicResourcePoolPending, createdRecord.State)

	replayed, created, err := database.CreateDynamicResourcePool(ctx, desired)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, createdRecord.PoolName, replayed.PoolName)
	require.Equal(t, createdRecord.CreatedAt, replayed.CreatedAt)

	conflictingKey := desired
	conflictingKey.PoolName = "online-b"
	conflictingKey.ConfigHash = "hash-b"
	_, _, err = database.CreateDynamicResourcePool(ctx, conflictingKey)
	require.ErrorIs(t, err, ErrDynamicResourcePoolConflict)

	conflictingName := desired
	conflictingName.IdempotencyKey = "operation-b"
	_, _, err = database.CreateDynamicResourcePool(ctx, conflictingName)
	require.ErrorIs(t, err, ErrDynamicResourcePoolConflict)

	message := "initialization failed"
	failed, err := database.SetDynamicResourcePoolState(
		ctx, desired.PoolName, DynamicResourcePoolFailed, &message,
	)
	require.NoError(t, err)
	require.Equal(t, DynamicResourcePoolFailed, failed.State)
	require.Equal(t, message, *failed.Error)
	pending, err := database.BeginDynamicResourcePoolRetry(
		ctx, desired.ClusterName, desired.PoolName,
	)
	require.NoError(t, err)
	require.Equal(t, DynamicResourcePoolPending, pending.State)
	_, err = database.BeginDynamicResourcePoolRetry(ctx, desired.ClusterName, desired.PoolName)
	require.ErrorIs(t, err, ErrDynamicResourcePoolNotFailed)

	// Reconnect to the same database to exercise the restart read path rather than an in-memory
	// object retained by the writer.
	restarted, err := ConnectPostgres(database.URL)
	require.NoError(t, err)
	defer func() { require.NoError(t, restarted.Close()) }()
	restored, err := restarted.ListDynamicResourcePools(ctx, desired.ClusterName)
	require.NoError(t, err)
	require.Len(t, restored, 1)
	require.Equal(t, desired.ConfigHash, restored[0].ConfigHash)
	require.Equal(t, DynamicResourcePoolPending, restored[0].State)
}

func TestDynamicResourcePoolConcurrentCreate(t *testing.T) {
	database, cleanup := MustResolveNewPostgresDatabase(t)
	defer cleanup()
	MustMigrateTestPostgres(t, database, "file://../../static/migrations", "up")

	desired := DynamicResourcePool{
		ClusterName:    "agents-a",
		PoolName:       "concurrent-online",
		ConfigVersion:  1,
		IdempotencyKey: "concurrent-operation",
		Config:         json.RawMessage(`{"pool_name":"concurrent-online"}`),
		ConfigHash:     "concurrent-hash",
	}
	const creators = 8
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(creators)
	created := make(chan bool, creators)
	errs := make(chan error, creators)
	for i := 0; i < creators; i++ {
		go func() {
			defer wait.Done()
			<-start
			_, wasCreated, err := database.CreateDynamicResourcePool(context.Background(), desired)
			created <- wasCreated
			errs <- err
		}()
	}
	close(start)
	wait.Wait()
	close(created)
	close(errs)
	createdCount := 0
	for wasCreated := range created {
		if wasCreated {
			createdCount++
		}
	}
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, 1, createdCount)
	records, err := database.ListDynamicResourcePools(context.Background(), desired.ClusterName)
	require.NoError(t, err)
	require.Len(t, records, 1)
}
