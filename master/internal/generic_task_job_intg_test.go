//go:build integration
// +build integration

package internal

import (
	"context"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/internal/rm"
	"github.com/determined-ai/determined/master/internal/rm/tasklist"
	"github.com/determined-ai/determined/master/internal/sproto"
	"github.com/determined-ai/determined/master/internal/task"
	"github.com/determined-ai/determined/master/internal/user"
	"github.com/determined-ai/determined/master/pkg/logger"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/master/pkg/ptrs"
	"github.com/determined-ai/determined/master/pkg/tasks"
	"github.com/determined-ai/determined/proto/pkg/apiv1"
	"github.com/determined-ai/determined/proto/pkg/taskv1"
)

// addGenericTaskJobForTest persists a running generic task with a config and registers its job.
func addGenericTaskJobForTest(
	ctx context.Context, t *testing.T, api *apiServer, owner model.User, name string,
) (model.TaskID, model.JobID, *genericTaskJob) {
	t.Helper()
	taskID := addGenericTaskForAuthZTest(ctx, t, owner, 1, nil, model.TaskStateActive)
	allocationID, spec, err := getGenericTaskSpec(ctx, taskID)
	require.NoError(t, err)
	spec.GenericTaskConfig = model.DefaultConfigGenericTaskConfig(nil)
	spec.GenericTaskConfig.Name = name
	spec.GenericTaskConfig.Resources.SetResourcePool("default")
	spec.GenericTaskConfig.Resources.RawPriority = ptrs.Ptr(42)
	spec.Base.TaskID = string(taskID)
	require.NoError(t, persistGenericTaskSpec(ctx, taskID, *spec, model.AllocationID(allocationID)))

	require.NoError(t, registerGenericTaskJob(api.m.rm, taskID, model.AllocationID(allocationID), spec.JobID, spec))
	t.Cleanup(func() { unregisterGenericTaskJob(spec.JobID, model.AllocationID(allocationID)) })
	genericTaskJobsMu.Lock()
	j := genericTaskJobs[spec.JobID]
	genericTaskJobsMu.Unlock()
	require.NotNil(t, j)
	return taskID, spec.JobID, j
}

func TestGenericTaskJobPriorityWeightAndPool(t *testing.T) {
	api, owner, ctx := setupAPITest(t, nil)
	taskID, jobID, j := addGenericTaskJobForTest(ctx, t, api, owner, "sweep-a")

	// Registration applies the task's priority before its allocation is requested.
	api.m.rm.(interface {
		AssertCalled(mock.TestingT, string, ...interface{}) bool
	}).AssertCalled(
		t, "SetGroupPriority", sproto.SetGroupPriority{Priority: 42, ResourcePool: "default", JobID: jobID})

	v1, err := j.ToV1Job()
	require.NoError(t, err)
	require.Equal(t, "sweep-a", v1.Name)
	require.Equal(t, int32(42), v1.Priority)
	require.Equal(t, defaultGenericTaskWeight, v1.Weight)
	require.Equal(t, string(taskID), v1.EntityId)

	// A job-queue priority change reaches the resource manager and is persisted.
	require.NoError(t, j.SetJobPriority(7))
	api.m.rm.(interface {
		AssertCalled(mock.TestingT, string, ...interface{}) bool
	}).AssertCalled(
		t, "SetGroupPriority", sproto.SetGroupPriority{Priority: 7, ResourcePool: "default", JobID: jobID})
	_, persisted, err := getGenericTaskSpec(ctx, taskID)
	require.NoError(t, err)
	require.Equal(t, 7, *persisted.GenericTaskConfig.Resources.RawPriority)

	require.ErrorContains(t, j.SetJobPriority(0), "between 1 and 99")

	// Invalid weights are refused before they reach the resource manager or the database.
	for _, weight := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		require.Equal(t, codes.InvalidArgument, status.Code(j.SetWeight(weight)), "weight %v", weight)
	}
	_, persisted, err = getGenericTaskSpec(ctx, taskID)
	require.NoError(t, err)
	require.Nil(t, persisted.GenericTaskConfig.Resources.RawWeight)

	// A weight change is persisted too.
	require.NoError(t, j.SetWeight(2.5))
	_, persisted, err = getGenericTaskSpec(ctx, taskID)
	require.NoError(t, err)
	require.Equal(t, 2.5, *persisted.GenericTaskConfig.Resources.RawWeight)

	// The scheduler's own priority change (e.g. from the web UI's job queue) is recorded.
	change, ok := tasklist.GroupPriorityChangeRegistry.Load(jobID)
	require.True(t, ok)
	require.NoError(t, change(9))
	_, persisted, err = getGenericTaskSpec(ctx, taskID)
	require.NoError(t, err)
	require.Equal(t, 9, *persisted.GenericTaskConfig.Resources.RawPriority)

	v1, err = j.ToV1Job()
	require.NoError(t, err)
	require.Equal(t, int32(9), v1.Priority)
	require.Equal(t, 2.5, v1.Weight)

	// Moving a generic task to another pool is refused instead of silently ignored.
	require.ErrorContains(t, j.SetResourcePool("other"), "not supported")
}

func TestGenericTaskJobUnregisterKeepsNewerAllocation(t *testing.T) {
	api, owner, ctx := setupAPITest(t, nil)
	taskID, jobID, j := addGenericTaskJobForTest(ctx, t, api, owner, "")

	// After an unpause, the task's new allocation is registered ...
	newAllocation := model.AllocationID(taskID.String() + ".1")
	require.NoError(t, registerGenericTaskJob(api.m.rm, taskID, newAllocation, jobID, j.spec))
	t.Cleanup(func() { unregisterGenericTaskJob(jobID, newAllocation) })

	// ... and the old allocation's late exit must not unregister it.
	unregisterGenericTaskJob(jobID, j.allocationID)
	_, ok := tasklist.GroupPriorityChangeRegistry.Load(jobID)
	require.True(t, ok)
	genericTaskJobsMu.Lock()
	current := genericTaskJobs[jobID]
	genericTaskJobsMu.Unlock()
	require.Equal(t, newAllocation, current.allocationID)

	unregisterGenericTaskJob(jobID, newAllocation)
	_, ok = tasklist.GroupPriorityChangeRegistry.Load(jobID)
	require.False(t, ok)
}

// captureAllocationService records the allocation requests of created tasks.
type captureAllocationService struct {
	task.AllocationService
	mu   sync.Mutex
	reqs []sproto.AllocateRequest
}

func (s *captureAllocationService) StartAllocation(
	_ logger.Context, req sproto.AllocateRequest, _ db.DB, _ rm.ResourceManager,
	_ tasks.TaskSpecifier, _ func(*task.AllocationExited),
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, req)
	now := time.Now().UTC()
	return db.AddAllocation(context.Background(), &model.Allocation{
		AllocationID: req.AllocationID, TaskID: req.TaskID, Slots: req.SlotsNeeded,
		ResourcePool: req.ResourcePool, StartTime: &now, Ports: map[string]int{},
	})
}

func TestCreateGenericTaskNameAndProxyPorts(t *testing.T) {
	api, _, ctx := setupAPITest(t, nil)
	service := &captureAllocationService{}
	oldService := task.DefaultService
	task.DefaultService = service
	t.Cleanup(func() { task.DefaultService = oldService })

	resp, err := api.CreateGenericTask(ctx, &apiv1.CreateGenericTaskRequest{
		Config: `
name: notebook-server
description: serves a notebook on port 8888
entrypoint: ["sleep", "infinity"]
resources:
  slots: 0
environment:
  proxy_ports:
    - proxy_port: 8888
      proxy_tcp: false
`,
	})
	require.NoError(t, err)
	taskID := model.TaskID(resp.TaskId)

	require.Len(t, service.reqs, 1)
	req := service.reqs[0]
	require.Equal(t, "notebook-server", req.Name)
	require.False(t, req.Preemption.Preemptible, "the scheduler must not preempt a generic task")
	require.True(t, req.Preemption.GracefulStop, "a paused task must get its preemption timeout")
	ports := map[int]bool{}
	for _, p := range req.ProxyPorts {
		ports[p.Port] = true
	}
	require.True(t, ports[8888], "allocation request lacks the configured proxy port: %v", req.ProxyPorts)

	allocationID, spec, err := getGenericTaskSpec(ctx, taskID)
	require.NoError(t, err)
	require.Equal(t, 8888, spec.GenericTaskConfig.Environment.Ports["8888"])
	require.Equal(t, "serves a notebook on port 8888", spec.GenericTaskConfig.Description)
	require.Equal(t, string(taskID), spec.Base.TaskID)

	// The created task is in the job queue under its name.
	genericTaskJobsMu.Lock()
	j := genericTaskJobs[spec.JobID]
	genericTaskJobsMu.Unlock()
	require.NotNil(t, j)
	t.Cleanup(func() { unregisterGenericTaskJob(spec.JobID, model.AllocationID(allocationID)) })
	v1, err := j.ToV1Job()
	require.NoError(t, err)
	require.Equal(t, "notebook-server", v1.Name)
}

func TestCreateGenericTaskInvalidConfig(t *testing.T) {
	api, _, ctx := setupAPITest(t, nil)
	for name, config := range map[string]string{
		"unknown key":    "entrypoint: [\"true\"]\nnot_a_key: 1\n",
		"mistyped key":   "entrypoint: true\n",
		"negative slots": "entrypoint: [\"true\"]\nresources:\n  slots: -1\n",
		"no entrypoint":  "resources:\n  slots: 0\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := api.CreateGenericTask(ctx, &apiv1.CreateGenericTaskRequest{Config: config})
			require.Equal(t, codes.InvalidArgument, status.Code(err), "%v", err)
		})
	}
}

func TestGenericTaskMutationRefusalsAreClientErrors(t *testing.T) {
	api, owner, ctx := setupAPITest(t, nil)
	active := addGenericTaskForAuthZTest(ctx, t, owner, 1, nil, model.TaskStateActive)
	paused := addGenericTaskForAuthZTest(ctx, t, owner, 1, nil, model.TaskStatePaused)
	completed := addGenericTaskForAuthZTest(ctx, t, owner, 1, nil, model.TaskStateCompleted)
	noPause := addGenericTaskForAuthZTest(ctx, t, owner, 1, nil, model.TaskStateActive)
	_, err := db.Bun().NewUpdate().Table("tasks").Set("no_pause = true").
		Where("task_id = ?", noPause).Exec(ctx)
	require.NoError(t, err)

	missing := model.NewTaskID().String()
	for name, c := range map[string]struct {
		call func() error
		code codes.Code
	}{
		"kill missing task": {func() error {
			_, err := api.KillGenericTask(ctx, &apiv1.KillGenericTaskRequest{TaskId: missing})
			return err
		}, codes.NotFound},
		"pause missing task": {func() error {
			_, err := api.PauseGenericTask(ctx, &apiv1.PauseGenericTaskRequest{TaskId: missing})
			return err
		}, codes.NotFound},
		"unpause missing task": {func() error {
			_, err := api.UnpauseGenericTask(ctx, &apiv1.UnpauseGenericTaskRequest{TaskId: missing})
			return err
		}, codes.NotFound},
		"kill completed task": {func() error {
			_, err := api.KillGenericTask(ctx, &apiv1.KillGenericTaskRequest{TaskId: completed.String()})
			return err
		}, codes.FailedPrecondition},
		"pause paused task": {func() error {
			_, err := api.PauseGenericTask(ctx, &apiv1.PauseGenericTaskRequest{TaskId: paused.String()})
			return err
		}, codes.FailedPrecondition},
		"pause no_pause task": {func() error {
			_, err := api.PauseGenericTask(ctx, &apiv1.PauseGenericTaskRequest{TaskId: noPause.String()})
			return err
		}, codes.FailedPrecondition},
		"unpause active task": {func() error {
			_, err := api.UnpauseGenericTask(ctx, &apiv1.UnpauseGenericTaskRequest{TaskId: active.String()})
			return err
		}, codes.FailedPrecondition},
	} {
		t.Run(name, func(t *testing.T) {
			err := c.call()
			require.Equal(t, c.code, status.Code(err), "%v", err)
		})
	}
}

func TestGenericTasksAreNotPausableByDefault(t *testing.T) {
	api, _, ctx := setupAPITest(t, nil)
	service := &captureAllocationService{}
	oldService := task.DefaultService
	task.DefaultService = service
	t.Cleanup(func() { task.DefaultService = oldService })

	create := func(noPause *bool) model.TaskID {
		resp, err := api.CreateGenericTask(ctx, &apiv1.CreateGenericTaskRequest{
			Config: "entrypoint: [\"true\"]\nresources:\n  slots: 0\n", NoPause: noPause,
		})
		require.NoError(t, err)
		return model.TaskID(resp.TaskId)
	}
	byDefault := create(nil)
	pausable := create(ptrs.Ptr(false))

	got, err := db.TaskByID(ctx, byDefault)
	require.NoError(t, err)
	require.True(t, *got.NoPause, "an unset no_pause must be stored as true")
	_, err = api.PauseGenericTask(ctx, &apiv1.PauseGenericTaskRequest{TaskId: byDefault.String()})
	require.Equal(t, codes.FailedPrecondition, status.Code(err), "%v", err)

	got, err = db.TaskByID(ctx, pausable)
	require.NoError(t, err)
	require.False(t, *got.NoPause)
}

func TestGetGenericTasksFiltersByOwnerStateAndParent(t *testing.T) {
	api, owner, ctx := setupAPITest(t, nil)
	other, err := db.HackAddUser(ctx, &model.User{Username: uuid.NewString()})
	require.NoError(t, err)
	otherFull, err := user.ByID(ctx, other)
	require.NoError(t, err)
	otherUser := ptrs.Ptr(otherFull.ToUser())

	root := addGenericTaskForAuthZTest(ctx, t, owner, 1, nil, model.TaskStateActive)
	child := addGenericTaskForAuthZTest(ctx, t, owner, 1, &root, model.TaskStatePaused)
	foreign := addGenericTaskForAuthZTest(ctx, t, *otherUser, 1, nil, model.TaskStateActive)

	ids := func(resp *apiv1.GetGenericTasksResponse) []string {
		var out []string
		for _, task := range resp.Tasks {
			out = append(out, task.TaskId)
		}
		return out
	}

	resp, err := api.GetGenericTasks(ctx, &apiv1.GetGenericTasksRequest{Users: []string{otherUser.Username}})
	require.NoError(t, err)
	require.Equal(t, []string{foreign.String()}, ids(resp))
	require.Equal(t, otherUser.Username, resp.Tasks[0].Username)
	require.Equal(t, int32(other), resp.Tasks[0].UserId)
	require.Equal(t, "Generic Task "+foreign.String(), resp.Tasks[0].Name)
	require.Equal(t, taskv1.GenericTaskState_GENERIC_TASK_STATE_ACTIVE, resp.Tasks[0].State)

	resp, err = api.GetGenericTasks(ctx, &apiv1.GetGenericTasksRequest{UserIds: []int32{int32(owner.ID)}})
	require.NoError(t, err)
	require.Subset(t, ids(resp), []string{root.String(), child.String()})
	require.NotContains(t, ids(resp), foreign.String())

	resp, err = api.GetGenericTasks(ctx, &apiv1.GetGenericTasksRequest{TaskIds: []string{foreign.String()}})
	require.NoError(t, err)
	require.Equal(t, []string{foreign.String()}, ids(resp))

	resp, err = api.GetGenericTasks(ctx, &apiv1.GetGenericTasksRequest{ParentId: ptrs.Ptr(root.String())})
	require.NoError(t, err)
	require.Equal(t, []string{child.String()}, ids(resp))
	require.Equal(t, root.String(), *resp.Tasks[0].ParentId)

	resp, err = api.GetGenericTasks(ctx, &apiv1.GetGenericTasksRequest{
		UserIds: []int32{int32(owner.ID)},
		States:  []taskv1.GenericTaskState{taskv1.GenericTaskState_GENERIC_TASK_STATE_PAUSED},
	})
	require.NoError(t, err)
	require.Contains(t, ids(resp), child.String())
	require.NotContains(t, ids(resp), root.String())

	resp, err = api.GetGenericTasks(ctx, &apiv1.GetGenericTasksRequest{
		Users: []string{otherUser.Username}, Limit: 1,
	})
	require.NoError(t, err)
	require.Equal(t, int32(1), resp.Pagination.Total)

	_, err = api.GetGenericTasks(ctx, &apiv1.GetGenericTasksRequest{
		States: []taskv1.GenericTaskState{taskv1.GenericTaskState_GENERIC_TASK_STATE_UNSPECIFIED},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
