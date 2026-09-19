//go:build integration
// +build integration

package internal

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	apiPkg "github.com/determined-ai/determined/master/internal/api"
	authz2 "github.com/determined-ai/determined/master/internal/authz"
	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/master/pkg/ptrs"
	"github.com/determined-ai/determined/master/pkg/tasks"
	"github.com/determined-ai/determined/proto/pkg/apiv1"
	"github.com/determined-ai/determined/proto/pkg/taskv1"
)

func addGenericTaskForAuthZTest(
	t *testing.T,
	ctx context.Context,
	owner model.User,
	workspaceID int,
	parentID *model.TaskID,
	state model.TaskState,
) model.TaskID {
	t.Helper()
	jobID := model.NewJobID()
	require.NoError(t, db.AddJob(&model.Job{
		JobID: jobID, JobType: model.JobTypeGeneric, OwnerID: &owner.ID,
	}))

	taskID := model.NewTaskID()
	require.NoError(t, db.AddTask(ctx, &model.Task{
		TaskID: taskID, TaskType: model.TaskTypeGeneric, JobID: &jobID,
		ParentID: parentID, State: ptrs.Ptr(state),
	}))
	allocationID := model.AllocationID(taskID.String() + ".0")
	now := time.Now().UTC()
	require.NoError(t, db.AddAllocation(ctx, &model.Allocation{
		AllocationID: allocationID,
		TaskID:       taskID,
		Slots:        1,
		ResourcePool: "default",
		StartTime:    &now,
		Ports:        map[string]int{},
	}))
	require.NoError(t, persistGenericTaskSpec(ctx, taskID, tasks.GenericTaskSpec{
		Base: tasks.TaskSpec{Owner: &owner}, WorkspaceID: workspaceID, JobID: jobID,
	}, allocationID))
	return taskID
}

func TestPropagateTaskState(t *testing.T) {
	api, _, ctx := setupAPITest(t, nil)

	parentID := model.NewTaskID()
	child1ID := model.NewTaskID()
	child2ID := model.NewTaskID()

	parentModel := &model.Task{TaskType: model.TaskTypeGeneric, TaskID: parentID}
	child1Model := &model.Task{TaskType: model.TaskTypeGeneric, TaskID: child1ID, ParentID: &parentID}
	child2Model := &model.Task{TaskType: model.TaskTypeGeneric, TaskID: child2ID, ParentID: &parentID}
	require.NoError(t, db.AddTask(ctx, parentModel))
	require.NoError(t, db.AddTask(ctx, child1Model))
	require.NoError(t, db.AddTask(ctx, child2Model))

	overrideTasks := []model.TaskState{}
	require.NoError(t, api.PropagateTaskState(ctx, parentID, model.TaskStateStoppingCanceled, overrideTasks))

	parent, err := api.GetTask(ctx, &apiv1.GetTaskRequest{TaskId: parentID.String()})
	require.NoError(t, err)
	child1, err := api.GetTask(ctx, &apiv1.GetTaskRequest{TaskId: child1ID.String()})
	require.NoError(t, err)
	child2, err := api.GetTask(ctx, &apiv1.GetTaskRequest{TaskId: child2ID.String()})
	require.NoError(t, err)
	require.Equal(t, taskv1.GenericTaskState_GENERIC_TASK_STATE_STOPPING_CANCELED, *parent.Task.TaskState)
	require.Equal(t, taskv1.GenericTaskState_GENERIC_TASK_STATE_STOPPING_CANCELED, *child1.Task.TaskState)
	require.Equal(t, taskv1.GenericTaskState_GENERIC_TASK_STATE_STOPPING_CANCELED, *child2.Task.TaskState)
}

func TestFindRoot(t *testing.T) {
	api, _, ctx := setupAPITest(t, nil)

	parentID := model.NewTaskID()
	child1ID := model.NewTaskID()
	child2ID := model.NewTaskID()

	parent := &model.Task{TaskType: model.TaskTypeGeneric, TaskID: parentID}
	child1 := &model.Task{TaskType: model.TaskTypeGeneric, TaskID: child1ID, ParentID: &parentID}
	child2 := &model.Task{TaskType: model.TaskTypeGeneric, TaskID: child2ID, ParentID: &parentID}
	require.NoError(t, db.AddTask(ctx, parent))
	require.NoError(t, db.AddTask(ctx, child1))
	require.NoError(t, db.AddTask(ctx, child2))

	taskID, err := api.FindRoot(ctx, child1ID)
	require.NoError(t, err)
	require.Equal(t, parentID, taskID)
}

func TestSetTaskStatesOnlyAffectsAuthorizedSnapshot(t *testing.T) {
	_, _, ctx := setupAPITest(t, nil)
	parentID := model.NewTaskID()
	childID := model.NewTaskID()
	active := model.TaskStateActive
	parent := model.Task{
		TaskType: model.TaskTypeGeneric, TaskID: parentID, State: &active,
	}
	child := model.Task{
		TaskType: model.TaskTypeGeneric, TaskID: childID, ParentID: &parentID, State: &active,
	}
	require.NoError(t, db.AddTask(ctx, &parent))
	require.NoError(t, db.AddTask(ctx, &child))

	require.NoError(t, setTaskStates(
		ctx, []model.Task{parent}, model.TaskStateStoppingCanceled, nil,
	))
	updatedParent, err := db.TaskByID(ctx, parentID)
	require.NoError(t, err)
	updatedChild, err := db.TaskByID(ctx, childID)
	require.NoError(t, err)
	require.Equal(t, model.TaskStateStoppingCanceled, *updatedParent.State)
	require.Equal(t, model.TaskStateActive, *updatedChild.State)
}

func TestGetTaskChildren(t *testing.T) {
	api, _, ctx := setupAPITest(t, nil)

	parentID := model.NewTaskID()
	child1ID := model.NewTaskID()
	child2ID := model.NewTaskID()

	parent := &model.Task{TaskType: model.TaskTypeGeneric, TaskID: parentID}
	child1 := &model.Task{TaskType: model.TaskTypeGeneric, TaskID: child1ID, ParentID: &parentID}
	child2 := &model.Task{TaskType: model.TaskTypeGeneric, TaskID: child2ID, ParentID: &parentID}
	require.NoError(t, db.AddTask(ctx, parent))
	require.NoError(t, db.AddTask(ctx, child1))
	require.NoError(t, db.AddTask(ctx, child2))

	taskSet := map[model.TaskID]bool{parentID: true, child1ID: true, child2ID: true}

	overrideTasks := []model.TaskState{}
	tasks, err := api.GetTaskChildren(ctx, parentID, overrideTasks)
	require.NoError(t, err)
	for _, e := range tasks {
		_, ok := taskSet[e.TaskID]
		require.True(t, ok)
	}
}

func TestGenericTaskMutationsRequireControlAuthorization(t *testing.T) {
	tests := []struct {
		name  string
		state model.TaskState
		call  func(*apiServer, context.Context, string) error
	}{
		{
			name:  "kill",
			state: model.TaskStateActive,
			call: func(api *apiServer, ctx context.Context, taskID string) error {
				_, err := api.KillGenericTask(ctx, &apiv1.KillGenericTaskRequest{TaskId: taskID})
				return err
			},
		},
		{
			name:  "pause",
			state: model.TaskStateActive,
			call: func(api *apiServer, ctx context.Context, taskID string) error {
				_, err := api.PauseGenericTask(ctx, &apiv1.PauseGenericTaskRequest{TaskId: taskID})
				return err
			},
		},
		{
			name:  "unpause",
			state: model.TaskStatePaused,
			call: func(api *apiServer, ctx context.Context, taskID string) error {
				_, err := api.UnpauseGenericTask(ctx, &apiv1.UnpauseGenericTaskRequest{TaskId: taskID})
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			api, authZ, curUser, ctx := setupNTSCAuthzTest(t)
			taskID := addGenericTaskForAuthZTest(t, ctx, curUser, 11, nil, test.state)

			authZ.On("CanGetNSC", mock.Anything, curUser, model.AccessScopeID(11)).
				Return(nil).Once()
			authZ.On("CanControlGenericTask", mock.Anything, curUser,
				model.AccessScopeID(11), &curUser.ID).
				Return(authz2.PermissionDeniedError{}).Once()

			err := test.call(api, ctx, taskID.String())
			require.Equal(t, codes.PermissionDenied, status.Code(err))
			persistedTask, err := db.TaskByID(ctx, taskID)
			require.NoError(t, err)
			require.Equal(t, test.state, *persistedTask.State)
		})
	}
}

func TestKillGenericTaskAuthorizesRootTreeBeforeMutation(t *testing.T) {
	api, authZ, curUser, ctx := setupNTSCAuthzTest(t)
	rootID := addGenericTaskForAuthZTest(t, ctx, curUser, 11, nil, model.TaskStateActive)
	requestedID := addGenericTaskForAuthZTest(
		t, ctx, curUser, 11, &rootID, model.TaskStateActive,
	)
	otherWorkspaceChildID := addGenericTaskForAuthZTest(
		t, ctx, curUser, 12, &rootID, model.TaskStateActive,
	)

	authZ.On("CanGetNSC", mock.Anything, curUser, mock.Anything).Return(nil)
	authZ.On("CanControlGenericTask", mock.Anything, curUser,
		model.AccessScopeID(11), mock.Anything).Return(nil)
	authZ.On("CanControlGenericTask", mock.Anything, curUser,
		model.AccessScopeID(12), mock.Anything).Return(authz2.PermissionDeniedError{})

	_, err := api.KillGenericTask(ctx, &apiv1.KillGenericTaskRequest{
		TaskId: requestedID.String(), KillFromRoot: true,
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	for _, taskID := range []model.TaskID{rootID, requestedID, otherWorkspaceChildID} {
		persistedTask, err := db.TaskByID(ctx, taskID)
		require.NoError(t, err)
		require.Equal(t, model.TaskStateActive, *persistedTask.State)
	}
}

func TestPauseAndUnpauseAuthorizeDescendantsBeforeMutation(t *testing.T) {
	tests := []struct {
		name  string
		state model.TaskState
		call  func(*apiServer, context.Context, string) error
	}{
		{
			name:  "pause",
			state: model.TaskStateActive,
			call: func(api *apiServer, ctx context.Context, taskID string) error {
				_, err := api.PauseGenericTask(ctx, &apiv1.PauseGenericTaskRequest{TaskId: taskID})
				return err
			},
		},
		{
			name:  "unpause",
			state: model.TaskStatePaused,
			call: func(api *apiServer, ctx context.Context, taskID string) error {
				_, err := api.UnpauseGenericTask(ctx, &apiv1.UnpauseGenericTaskRequest{TaskId: taskID})
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			api, authZ, curUser, ctx := setupNTSCAuthzTest(t)
			rootID := addGenericTaskForAuthZTest(t, ctx, curUser, 11, nil, test.state)
			childID := addGenericTaskForAuthZTest(
				t, ctx, curUser, 12, &rootID, test.state,
			)

			authZ.On("CanGetNSC", mock.Anything, curUser, mock.Anything).Return(nil)
			authZ.On("CanControlGenericTask", mock.Anything, curUser,
				model.AccessScopeID(11), mock.Anything).Return(nil)
			authZ.On("CanControlGenericTask", mock.Anything, curUser,
				model.AccessScopeID(12), mock.Anything).Return(authz2.PermissionDeniedError{})

			err := test.call(api, ctx, rootID.String())
			require.Equal(t, codes.PermissionDenied, status.Code(err))
			for _, taskID := range []model.TaskID{rootID, childID} {
				persistedTask, err := db.TaskByID(ctx, taskID)
				require.NoError(t, err)
				require.Equal(t, test.state, *persistedTask.State)
			}
		})
	}
}

func TestGenericTaskMutationHidesTaskWithoutViewAuthorization(t *testing.T) {
	api, authZ, curUser, ctx := setupNTSCAuthzTest(t)
	taskID := addGenericTaskForAuthZTest(t, ctx, curUser, 11, nil, model.TaskStateActive)
	authZ.On("CanGetNSC", mock.Anything, curUser, model.AccessScopeID(11)).
		Return(authz2.PermissionDeniedError{}).Once()

	_, err := api.PauseGenericTask(ctx, &apiv1.PauseGenericTaskRequest{TaskId: taskID.String()})
	require.ErrorIs(t, err, apiPkg.NotFoundErrs("task", taskID.String(), true))
	authZ.AssertNotCalled(t, "CanControlGenericTask", mock.Anything, mock.Anything,
		mock.Anything, mock.Anything)
}
