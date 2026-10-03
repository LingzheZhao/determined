//go:build integration
// +build integration

package internal

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/internal/rm"
	"github.com/determined-ai/determined/master/internal/rm/tasklist"
	"github.com/determined-ai/determined/master/internal/sproto"
	"github.com/determined-ai/determined/master/internal/task"
	"github.com/determined-ai/determined/master/pkg/logger"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/master/pkg/ptrs"
	"github.com/determined-ai/determined/master/pkg/tasks"
	"github.com/determined-ai/determined/proto/pkg/apiv1"
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
	require.True(t, req.Preemption.Preemptible, "a paused task must get its preemption timeout")
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
