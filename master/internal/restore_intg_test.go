//go:build integration
// +build integration

package internal

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/internal/experiment"
	"github.com/determined-ai/determined/master/pkg/model"
)

func countUserSessions(ctx context.Context, t *testing.T, userID model.UserID) int {
	count, err := db.Bun().NewSelect().Table("user_sessions").
		Where("user_id = ?", userID).
		Count(ctx)
	require.NoError(t, err)
	return count
}

// TestRestoreExperimentSession checks that a failed experiment restore deletes the user session it
// created, and that a successful restore keeps it.
func TestRestoreExperimentSession(t *testing.T) {
	api, curUser, ctx := setupAPITest(t, nil)

	t.Run("failed restore", func(t *testing.T) {
		exp := createTestExp(t, api, curUser)
		// A snapshot newer than this master cannot be shimmed, so the restore fails after it
		// created the experiment's session.
		require.NoError(t, api.m.db.SaveSnapshot(exp.ID, experimentSnapshotVersion+1, []byte(`{}`)))

		before := countUserSessions(ctx, t, curUser.ID)
		err := api.m.restoreExperiment(exp)
		require.ErrorContains(t, err, "cannot shim")
		require.Equal(t, before, countUserSessions(ctx, t, curUser.ID))
	})

	t.Run("successful restore", func(t *testing.T) {
		exp := createTestExp(t, api, curUser)

		before := countUserSessions(ctx, t, curUser.ID)
		require.NoError(t, api.m.restoreExperiment(exp))
		t.Cleanup(func() {
			if e, ok := experiment.ExperimentRegistry.Load(exp.ID); ok {
				require.NoError(t, e.KillExperiment())
			}
		})
		require.Equal(t, before+1, countUserSessions(ctx, t, curUser.ID))
	})
}
