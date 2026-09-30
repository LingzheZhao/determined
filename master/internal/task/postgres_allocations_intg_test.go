//go:build integration
// +build integration

package task

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/oauth2.v3/utils/uuid"

	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/pkg/etc"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/master/pkg/ptrs"
	"github.com/determined-ai/determined/proto/pkg/taskv1"
)

func TestPersistAllocationWorkspaceInfo(t *testing.T) {
	ctx := context.Background()
	require.NoError(t, etc.SetRootPath(db.RootFromDB))
	pgDB, cleanup := db.MustResolveTestPostgres(t)
	defer cleanup()
	db.MustMigrateTestPostgres(t, pgDB, db.MigrationsFromDB)

	type TestCase struct {
		description string
		isTrial     bool
	}
	testCases := []TestCase{
		{description: "NTSC Task", isTrial: false},
		{description: "Trial", isTrial: true},
	}

	for _, tc := range testCases {
		t.Run(tc.description, func(t *testing.T) {
			workspaceID := model.DefaultWorkspaceID
			workspaceName := model.DefaultWorkspaceName

			// Create dummy allocation & experiment ids.
			allocID := model.AllocationID(uuid.Must(uuid.NewRandom()).String())
			user := db.RequireMockUser(t, pgDB)
			exp := db.RequireMockExperiment(t, pgDB, user)

			var err error
			switch tc.isTrial {
			case true:
				err = InsertTrialAllocationWorkspaceRecord(
					ctx,
					exp.ID,
					allocID,
				)
			case false:
				err = InsertNTSCAllocationWorkspaceRecord(
					ctx,
					allocID,
					workspaceID,
					workspaceName,
				)
			}
			require.NoError(t, err)

			// Check if entry in allocation_workspace_info table is correct.
			var persistedInfo model.AllocationWorkspaceRecord
			err = db.Bun().NewSelect().
				Model(&persistedInfo).
				Where("allocation_id = ?", allocID).
				Scan(ctx)

			require.NoError(t, err)
			require.Equal(t, allocID, persistedInfo.AllocationID)
			require.Equal(t, workspaceID, persistedInfo.WorkspaceID)
			require.Equal(t, workspaceName, persistedInfo.WorkspaceName)
			if tc.isTrial {
				require.Equal(t, exp.ID, persistedInfo.ExperimentID)
			}
		})
	}
}

func TestGetAllocationExitFields(t *testing.T) {
	ctx := context.Background()
	require.NoError(t, etc.SetRootPath(db.RootFromDB))
	pgDB, cleanup := db.MustResolveTestPostgres(t)
	defer cleanup()
	db.MustMigrateTestPostgres(t, pgDB, db.MigrationsFromDB)

	taskModel := db.RequireMockTask(t, pgDB, nil)
	add := func(i int) *model.Allocation {
		a := &model.Allocation{
			AllocationID: model.AllocationID(fmt.Sprintf("%s.%d", taskModel.TaskID, i)),
			TaskID:       taskModel.TaskID,
			Slots:        1,
			ResourcePool: "default",
			State:        ptrs.Ptr(model.AllocationStateRunning),
		}
		require.NoError(t, db.AddAllocation(ctx, a))
		return a
	}

	failed := add(0)
	failed.State = ptrs.Ptr(model.AllocationStateTerminated)
	failed.ExitReason = ptrs.Ptr("allocation failed: boom")
	failed.StatusCode = ptrs.Ptr(int32(2))
	failed.ExitClass = ptrs.Ptr(model.ExitClassWorkloadFailed)
	failed.ExitDetail = model.NewExitDetail(
		taskv1.FailureType_FAILURE_TYPE_RESOURCES_FAILED.String(), ptrs.Ptr(int32(2)), "boom")
	require.NoError(t, db.AddAllocationExitStatus(ctx, failed))

	res, err := DefaultService.GetAllocation(ctx, string(failed.AllocationID))
	require.NoError(t, err)
	proto := res.Proto()
	require.Equal(t, "allocation failed: boom", proto.GetExitReason())
	require.Equal(t, taskv1.ExitClass_EXIT_CLASS_WORKLOAD_FAILED, proto.ExitClass)
	require.NotNil(t, proto.ExitDetail)
	require.Equal(t, map[string]any{
		"failure_type": "FAILURE_TYPE_RESOURCES_FAILED",
		"exit_code":    float64(2),
		"message":      "boom",
	}, proto.ExitDetail.AsMap())

	// An allocation that has not recorded an exit reads as unspecified, with no detail.
	running := add(1)
	res, err = DefaultService.GetAllocation(ctx, string(running.AllocationID))
	require.NoError(t, err)
	proto = res.Proto()
	require.Equal(t, taskv1.ExitClass_EXIT_CLASS_UNSPECIFIED, proto.ExitClass)
	require.Nil(t, proto.ExitDetail)
}
