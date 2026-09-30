//go:build integration
// +build integration

package internal

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/determined-ai/determined/master/internal/command"
	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/internal/mocks"
	"github.com/determined-ai/determined/master/internal/rm"
	"github.com/determined-ai/determined/master/internal/rm/rmevents"
	"github.com/determined-ai/determined/master/internal/rm/tasklist"
	"github.com/determined-ai/determined/master/internal/sproto"
	"github.com/determined-ai/determined/master/internal/task"
	"github.com/determined-ai/determined/master/pkg/aproto"
	"github.com/determined-ai/determined/master/pkg/device"
	"github.com/determined-ai/determined/master/pkg/logger"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/master/pkg/ptrs"
	"github.com/determined-ai/determined/master/pkg/tasks"
	"github.com/determined-ai/determined/proto/pkg/apiv1"
)

// allocateRequest is an allocation request that a restarted master sent the resource manager, with
// the number of resources recorded for the allocation when it did.
type allocateRequest struct {
	sproto.AllocateRequest
	resourceRows int
}

// recordingRM records the allocation requests of a restarted master, and fails the requests of the
// allocations in fail.
type recordingRM struct {
	mu       sync.Mutex
	requests map[model.AllocationID][]allocateRequest
	fail     map[model.AllocationID]bool
}

func (r *recordingRM) allocate(msg sproto.AllocateRequest) (*sproto.ResourcesSubscription, error) {
	rows, err := db.Bun().NewSelect().Table("allocation_resources").
		Where("allocation_id = ?", msg.AllocationID).Count(context.Background())
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests[msg.AllocationID] = append(r.requests[msg.AllocationID],
		allocateRequest{AllocateRequest: msg, resourceRows: rows})
	if r.fail[msg.AllocationID] {
		return nil, fmt.Errorf("the resource manager lost pool %s", msg.ResourcePool)
	}
	return rmevents.Subscribe(msg.AllocationID), nil
}

func (r *recordingRM) requested(id model.AllocationID) []allocateRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requests[id]
}

// restartCommands restores the commands as a restarted master does, with a new command service
// over a resource manager that records what the restore asks of it.
func restartCommands(
	ctx context.Context, t *testing.T, api *apiServer, fail ...model.AllocationID,
) *recordingRM {
	recorder := &recordingRM{
		requests: map[model.AllocationID][]allocateRequest{},
		fail:     map[model.AllocationID]bool{},
	}
	for _, id := range fail {
		recorder.fail[id] = true
	}
	mockRM := submissionMockRM(recorder.allocate)
	mockRM.On("Release", mock.Anything).Return().Run(func(args mock.Arguments) {
		if msg := args[0].(sproto.ResourcesReleased); msg.ResourcesID == nil {
			rmevents.Publish(msg.AllocationID, sproto.ResourcesReleasedEvent{})
		}
	})
	api.m.rm = mockRM
	cs, err := command.NewService(api.m.db, mockRM)
	require.NoError(t, err)
	command.SetDefaultService(cs)
	require.NoError(t, cs.RestoreAllCommands(ctx))
	require.NoError(t, api.m.closeOpenAllocations(ctx))
	return recorder
}

// crashCommand lets go of a command's allocation as a master crash would, leaving its rows for a
// restarted master to restore.
func crashCommand(ctx context.Context, t *testing.T, taskID model.TaskID) {
	var snapshot command.CommandSnapshot
	require.NoError(t, db.Bun().NewSelect().Model(&snapshot).Relation("Task").
		Where("command_snapshot.task_id = ?", taskID).Scan(ctx))
	require.NoError(t, task.DefaultService.Detach(snapshot.AllocationID))
	require.Eventually(t, func() bool {
		completed, err := db.TaskCompleted(ctx, taskID)
		require.NoError(t, err)
		return completed
	}, 10*time.Second, 10*time.Millisecond)
	// A detached allocation still runs its exit callback, which ends the task; a crash does not.
	_, err := db.Bun().NewUpdate().Table("tasks").Set("end_time = NULL").
		Where("task_id = ?", taskID).Exec(ctx)
	require.NoError(t, err)
	// A new master starts with an empty registry.
	_ = tasklist.GroupPriorityChangeRegistry.Delete(*snapshot.Task.JobID)
}

// addTickResources records resources for an allocation as the agent resource manager's tick does
// before the allocation receives them.
func addTickResources(ctx context.Context, t *testing.T, allocationID model.AllocationID) string {
	containerID := uuid.NewString()
	_, err := db.Bun().NewRaw(`INSERT INTO allocation_resources (resource_id, allocation_id, rank)
		VALUES (?, ?, -1)`, containerID, allocationID).Exec(ctx)
	require.NoError(t, err)
	_, err = db.Bun().NewRaw(`INSERT INTO resourcemanagers_agent_containers
		(container_id, resource_id, agent_id) VALUES (?, ?, 'agent')`, containerID, containerID).Exec(ctx)
	require.NoError(t, err)
	return containerID
}

func countRows(ctx context.Context, t *testing.T, table, column string, value any) int {
	n, err := db.Bun().NewSelect().Table(table).Where("? = ?", bun.Ident(column), value).Count(ctx)
	require.NoError(t, err)
	return n
}

func allocationOf(ctx context.Context, t *testing.T, id model.AllocationID) *model.Allocation {
	a, err := db.AllocationByID(ctx, id)
	require.NoError(t, err)
	return a
}

// requireOpenQueued checks that an allocation is still the open, never-placed attempt of its live
// task, which a restart requested again without adding to the task's allocations.
func requireOpenQueued(ctx context.Context, t *testing.T, id model.AllocationID, allocations int) {
	a := allocationOf(ctx, t, id)
	require.Equal(t, model.AllocationStatePending, *a.State)
	require.Nil(t, a.StartTime)
	require.Nil(t, a.EndTime)
	require.Nil(t, a.ExitClass)
	completed, err := db.TaskCompleted(ctx, a.TaskID)
	require.NoError(t, err)
	require.False(t, completed)
	require.Equal(t, allocations, countRows(ctx, t, "allocations", "task_id", a.TaskID),
		"a restart must not create another allocation")
}

// requireFailedRestore checks that an allocation was closed as a failed restore together with the
// end of its task.
func requireFailedRestore(ctx context.Context, t *testing.T, id model.AllocationID) *model.Task {
	a := allocationOf(ctx, t, id)
	require.Equal(t, model.AllocationStateTerminated, *a.State)
	require.NotNil(t, a.EndTime)
	require.Equal(t, model.ExitClassInfrastructureFailed, *a.ExitClass)
	require.Equal(t, command.RestoreFailedReason, *a.ExitReason)
	taskModel, err := db.TaskByID(ctx, a.TaskID)
	require.NoError(t, err)
	require.NotNil(t, taskModel.EndTime)
	require.Equal(t, *a.EndTime, *taskModel.EndTime, "the allocation and task end together")
	return taskModel
}

// countingResources are resources that count their starts.
func countingResources(
	allocationID model.AllocationID, rID sproto.ResourcesID, starts *atomic.Int32,
) *mocks.Resources {
	var r mocks.Resources
	r.On("Start", mock.Anything, mock.Anything, mock.Anything).Return(nil).
		Run(func(mock.Arguments) { starts.Add(1) })
	r.On("Summary").Return(sproto.ResourcesSummary{
		AllocationID:  allocationID,
		ResourcesID:   rID,
		ResourcesType: sproto.ResourcesTypeDockerContainer,
		AgentDevices:  map[aproto.ID][]device.Device{"agent": nil},
	})
	r.On("Kill", mock.Anything).Return().Run(func(mock.Arguments) {
		rmevents.Publish(allocationID, &sproto.ResourcesStateChanged{
			ResourcesID:      rID,
			ResourcesState:   sproto.Terminated,
			ResourcesStopped: &sproto.ResourcesStopped{},
		})
	})
	return &r
}

func requireAllocationState(
	ctx context.Context, t *testing.T, id model.AllocationID, state model.AllocationState,
) {
	require.Eventually(t, func() bool {
		a := allocationOf(ctx, t, id)
		return a.State != nil && *a.State == state
	}, 10*time.Second, 10*time.Millisecond)
}

func TestRestoreCommandsByAllocationState(t *testing.T) {
	api, _, ctx := setupSubmissionTest(t, nil)
	launch := func() (model.TaskID, model.AllocationID, model.JobID) {
		resp, err := api.LaunchCommand(ctx, &apiv1.LaunchCommandRequest{
			Config: commandConfig(t, "true"),
		})
		require.NoError(t, err)
		return model.TaskID(resp.Command.Id), model.AllocationID(resp.Command.Id + ".1"),
			model.JobID(resp.Command.JobId)
	}

	// Queued: the tick recorded resources that the allocation never received.
	queuedTask, queued, _ := launch()
	addTickResources(ctx, t, queued)
	crashCommand(ctx, t, queuedTask)

	// Placed: the allocation recorded ASSIGNED and launched its container, and the master crashed
	// before the container pulled its image.
	placedTask, placed, _ := launch()
	rID := sproto.ResourcesID(uuid.NewString())
	var firstStarts, restoredStarts atomic.Int32
	rmevents.Publish(placed, &sproto.ResourcesAllocated{
		ID: placed, ResourcePool: "default",
		Resources: sproto.ResourceList{rID: countingResources(placed, rID, &firstStarts)},
	})
	requireAllocationState(ctx, t, placed, model.AllocationStateAssigned)
	require.Nil(t, allocationOf(ctx, t, placed).StartTime)
	require.Equal(t, int32(1), firstStarts.Load())
	crashCommand(ctx, t, placedTask)

	// Queued and asked to stop before the crash.
	canceledTask, canceled, canceledJob := launch()
	addTickResources(ctx, t, canceled)
	crashCommand(ctx, t, canceledTask)
	_, err := db.Bun().NewUpdate().Table("jobs").Set("cancel_requested_at = ?", time.Now().UTC()).
		Where("job_id = ?", canceledJob).Exec(ctx)
	require.NoError(t, err)

	// Ended: the allocation closed, and the crash came before its task ended.
	endedTask, ended, _ := launch()
	crashCommand(ctx, t, endedTask)
	endTime := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	_, err = db.Bun().NewUpdate().Table("allocations").
		Set("state = ?", model.AllocationStateTerminated).
		Set("start_time = ?", endTime).Set("end_time = ?", endTime).
		Where("allocation_id = ?", ended).Exec(ctx)
	require.NoError(t, err)

	// Failing: the restarted master cannot request the allocation again.
	failingTask, failing, failingJob := launch()
	crashCommand(ctx, t, failingTask)

	recorder := restartCommands(ctx, t, api, failing)

	requests := recorder.requested(queued)
	require.Len(t, requests, 1)
	require.False(t, requests[0].Restore)
	require.True(t, requests[0].Persisted)
	require.Zero(t, requests[0].resourceRows, "the tick's resources are purged before the request")
	requireOpenQueued(ctx, t, queued, 1)
	require.Contains(t, task.DefaultService.GetAllAllocationIDs(), queued)

	requests = recorder.requested(placed)
	require.Len(t, requests, 1)
	require.True(t, requests[0].Restore)
	rmevents.Publish(placed, &sproto.ResourcesAllocated{
		ID: placed, ResourcePool: "default", Recovered: true,
		Resources: sproto.ResourceList{rID: countingResources(placed, rID, &restoredStarts)},
	})
	require.NoError(t, task.DefaultService.WaitForRestore(ctx, placed))
	require.Zero(t, restoredStarts.Load(), "a restored allocation starts no container")
	placedAllocation := allocationOf(ctx, t, placed)
	require.Equal(t, model.AllocationStateAssigned, *placedAllocation.State)
	require.Nil(t, placedAllocation.EndTime)
	require.Equal(t, 1, countRows(ctx, t, "allocations", "task_id", placedTask))

	require.Empty(t, recorder.requested(canceled))
	canceledAllocation := allocationOf(ctx, t, canceled)
	require.Equal(t, model.AllocationStateTerminated, *canceledAllocation.State)
	require.Equal(t, model.ExitClassNone, *canceledAllocation.ExitClass)
	require.Equal(t, command.StopQueuedReason, *canceledAllocation.ExitReason)
	require.Zero(t, countRows(ctx, t, "allocation_resources", "allocation_id", canceled))
	canceledTaskModel, err := db.TaskByID(ctx, canceledTask)
	require.NoError(t, err)
	require.Equal(t, *canceledAllocation.EndTime, *canceledTaskModel.EndTime)

	require.Empty(t, recorder.requested(ended))
	endedTaskModel, err := db.TaskByID(ctx, endedTask)
	require.NoError(t, err)
	require.NotNil(t, endedTaskModel.EndTime)
	require.True(t, endTime.Equal(*endedTaskModel.EndTime))

	require.Len(t, recorder.requested(failing), 1)
	requireFailedRestore(ctx, t, failing)
	_, registered := tasklist.GroupPriorityChangeRegistry.Load(failingJob)
	require.False(t, registered)
	require.NotContains(t, task.DefaultService.GetAllAllocationIDs(), failing)

	require.NoError(t, task.DefaultService.Signal(placed, task.KillAllocation, "test cleanup"))
	require.Eventually(t, func() bool {
		return !slices.Contains(task.DefaultService.GetAllAllocationIDs(), placed)
	}, 10*time.Second, 10*time.Millisecond)

	// The queued command is queued again across a second restart, under the same ID and still
	// without a start time.
	crashCommand(ctx, t, queuedTask)
	recorder = restartCommands(ctx, t, api)
	requests = recorder.requested(queued)
	require.Len(t, requests, 1)
	require.False(t, requests[0].Restore)
	requireOpenQueued(ctx, t, queued, 1)
	require.NoError(t, task.DefaultService.Signal(queued, task.KillAllocation, "test cleanup"))
}

// recordingAllocationService stands in for the allocation service of a restarted master: it
// records the allocations that are started, and fails the starts of the allocations in fail.
type recordingAllocationService struct {
	task.AllocationService
	mu       sync.Mutex
	requests map[model.AllocationID][]sproto.AllocateRequest
	fail     map[model.AllocationID]bool
}

func newRecordingAllocationService(fail ...model.AllocationID) *recordingAllocationService {
	s := &recordingAllocationService{
		requests: map[model.AllocationID][]sproto.AllocateRequest{},
		fail:     map[model.AllocationID]bool{},
	}
	for _, id := range fail {
		s.fail[id] = true
	}
	return s
}

func (s *recordingAllocationService) StartAllocation(
	_ logger.Context, req sproto.AllocateRequest, _ db.DB, _ rm.ResourceManager,
	_ tasks.TaskSpecifier, _ func(*task.AllocationExited),
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail[req.AllocationID] {
		return fmt.Errorf("the resource manager lost pool %s", req.ResourcePool)
	}
	if _, ok := s.requests[req.AllocationID]; !ok && !req.Restore && !req.Persisted {
		// A new allocation inserts its row, as the allocation service does.
		if err := db.AddAllocation(context.Background(), &model.Allocation{
			AllocationID: req.AllocationID, TaskID: req.TaskID, Slots: req.SlotsNeeded,
			ResourcePool: req.ResourcePool, State: ptrs.Ptr(model.AllocationStatePending),
			Ports: map[string]int{},
		}); err != nil {
			return err
		}
	}
	s.requests[req.AllocationID] = append(s.requests[req.AllocationID], req)
	return nil
}

func (s *recordingAllocationService) GetAllAllocationIDs() []model.AllocationID {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]model.AllocationID, 0, len(s.requests))
	for id := range s.requests {
		ids = append(ids, id)
	}
	return ids
}

func (s *recordingAllocationService) requested(id model.AllocationID) []sproto.AllocateRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[id]
}

func TestRestoreGenericTasksByAllocationState(t *testing.T) {
	api, _, ctx := setupSubmissionTest(t, nil)
	oldService := task.DefaultService
	t.Cleanup(func() { task.DefaultService = oldService })
	task.DefaultService = newRecordingAllocationService()

	create := func() (model.TaskID, model.AllocationID, model.JobID) {
		resp, err := api.CreateGenericTask(ctx, &apiv1.CreateGenericTaskRequest{
			Config:    "entrypoint: [sh, -c, 'true']\nresources: {slots: 0}\n",
			ProjectId: ptrs.Ptr(int32(1)),
		})
		require.NoError(t, err)
		taskID := model.TaskID(resp.TaskId)
		taskModel, err := db.TaskByID(ctx, taskID)
		require.NoError(t, err)
		// A new master starts with an empty registry.
		_ = tasklist.GroupPriorityChangeRegistry.Delete(*taskModel.JobID)
		return taskID, model.AllocationID(resp.TaskId + ".1"), *taskModel.JobID
	}
	setTask := func(taskID model.TaskID, state model.TaskState, ended bool) {
		q := db.Bun().NewUpdate().Table("tasks").Set("task_state = ?", state).
			Where("task_id = ?", taskID)
		if ended {
			q = q.Set("end_time = ?", time.Now().UTC())
		}
		_, err := q.Exec(ctx)
		require.NoError(t, err)
	}
	setAllocation := func(id model.AllocationID, state model.AllocationState, ended bool) {
		q := db.Bun().NewUpdate().Table("allocations").Set("state = ?", state).
			Where("allocation_id = ?", id)
		if ended {
			now := time.Now().UTC()
			q = q.Set("start_time = ?", now).Set("end_time = ?", now)
		}
		_, err := q.Exec(ctx)
		require.NoError(t, err)
	}

	queuedTask, queued, _ := create()
	addTickResources(ctx, t, queued)

	placedTask, placed, _ := create()
	setAllocation(placed, model.AllocationStateAssigned, false)

	pausedTask, paused, _ := create()
	setAllocation(paused, model.AllocationStateTerminated, true)
	setTask(pausedTask, model.TaskStatePaused, true)

	pausingQueuedTask, pausingQueued, _ := create()
	addTickResources(ctx, t, pausingQueued)
	setTask(pausingQueuedTask, model.TaskStateStoppingPaused, false)

	pausingPlacedTask, pausingPlaced, _ := create()
	setAllocation(pausingPlaced, model.AllocationStateRunning, false)
	setTask(pausingPlacedTask, model.TaskStateStoppingPaused, false)

	// Unpausing: the unpause claimed the paused task and started its next allocation, which was
	// still queued, before command_state named it.
	unpausingTask, unpausingOld, _ := create()
	setAllocation(unpausingOld, model.AllocationStateTerminated, true)
	setTask(unpausingTask, model.TaskStatePaused, true)
	members, err := api.GetTaskChildren(ctx, unpausingTask, nil)
	require.NoError(t, err)
	plan, err := makeGenericTaskResumePlan(ctx, unpausingTask, members)
	require.NoError(t, err)
	claimed, err := claimPausedGenericTask(ctx, unpausingTask, unpausingOld)
	require.NoError(t, err)
	require.True(t, claimed)
	unpausing := plan[0].NewAllocationID
	require.NoError(t, db.AddAllocation(ctx, &model.Allocation{
		AllocationID: unpausing, TaskID: unpausingTask, Slots: 0, ResourcePool: "default",
		State: ptrs.Ptr(model.AllocationStatePending), Ports: map[string]int{},
	}))
	addTickResources(ctx, t, unpausing)

	canceledTask, canceled, canceledJob := create()
	_, err = db.Bun().NewUpdate().Table("jobs").Set("cancel_requested_at = ?", time.Now().UTC()).
		Where("job_id = ?", canceledJob).Exec(ctx)
	require.NoError(t, err)

	endedTask, ended, _ := create()
	setAllocation(ended, model.AllocationStateTerminated, true)
	_, err = db.Bun().NewUpdate().Table("allocations").Set("exit_error = ?", "exit code 1").
		Where("allocation_id = ?", ended).Exec(ctx)
	require.NoError(t, err)

	failingTask, failing, failingJob := create()

	// The master restarts.
	service := newRecordingAllocationService(failing)
	task.DefaultService = service
	require.NoError(t, api.m.restoreGenericTasks(ctx))
	require.NoError(t, api.m.recoverGenericTaskResumes(ctx))
	require.NoError(t, api.m.closeOpenAllocations(ctx))

	requireTask := func(taskID model.TaskID, state model.TaskState, ended bool) *model.Task {
		taskModel, err := db.TaskByID(ctx, taskID)
		require.NoError(t, err)
		require.Equal(t, state, *taskModel.State)
		require.Equal(t, ended, taskModel.EndTime != nil)
		return taskModel
	}
	requireRequest := func(id model.AllocationID, restore bool) {
		requests := service.requested(id)
		require.Len(t, requests, 1, "allocation %s", id)
		require.Equal(t, restore, requests[0].Restore)
		require.Equal(t, !restore, requests[0].Persisted)
	}

	requireRequest(queued, false)
	requireOpenQueued(ctx, t, queued, 1)
	require.Zero(t, countRows(ctx, t, "allocation_resources", "allocation_id", queued))
	requireTask(queuedTask, model.TaskStateActive, false)

	requireRequest(placed, true)
	requireTask(placedTask, model.TaskStateActive, false)

	require.Empty(t, service.requested(paused))
	requireTask(pausedTask, model.TaskStatePaused, true)

	// A pause that reached a queued allocation pauses the task instead of queuing it again, and the
	// task can be unpaused.
	require.Empty(t, service.requested(pausingQueued))
	requireTask(pausingQueuedTask, model.TaskStatePaused, true)
	pausingQueuedAllocation := allocationOf(ctx, t, pausingQueued)
	require.Equal(t, model.ExitClassNone, *pausingQueuedAllocation.ExitClass)
	require.NotNil(t, pausingQueuedAllocation.EndTime)
	require.Zero(t, countRows(ctx, t, "allocation_resources", "allocation_id", pausingQueued))
	_, err = api.UnpauseGenericTask(ctx, &apiv1.UnpauseGenericTaskRequest{
		TaskId: pausingQueuedTask.String(),
	})
	require.NoError(t, err)
	require.Len(t, service.requested(model.AllocationID(pausingQueuedTask.String()+".2")), 1)

	requireRequest(pausingPlaced, true)
	requireTask(pausingPlacedTask, model.TaskStateStoppingPaused, false)

	// The unpause resumes on the restarted master: its queued allocation is requested again, not
	// restored, and the task is not ended for the allocation that the pause ended.
	require.Empty(t, service.requested(unpausingOld))
	requireRequest(unpausing, false)
	requireOpenQueued(ctx, t, unpausing, 2)
	require.Zero(t, countRows(ctx, t, "allocation_resources", "allocation_id", unpausing))
	requireTask(unpausingTask, model.TaskStateActive, false)
	allocationID, _, err := getGenericTaskSpec(ctx, unpausingTask)
	require.NoError(t, err)
	require.Equal(t, unpausing.String(), allocationID)

	require.Empty(t, service.requested(canceled))
	requireTask(canceledTask, model.TaskStateCanceled, true)
	require.Equal(t, model.ExitClassNone, *allocationOf(ctx, t, canceled).ExitClass)

	require.Empty(t, service.requested(ended))
	endedTaskModel := requireTask(endedTask, model.TaskStateError, true)
	require.Equal(t, *allocationOf(ctx, t, ended).EndTime, *endedTaskModel.EndTime)

	require.Empty(t, service.requested(failing))
	failedTask := requireFailedRestore(ctx, t, failing)
	require.Equal(t, model.TaskStateError, *failedTask.State)
	require.Equal(t, failingTask, failedTask.TaskID)
	_, registered := tasklist.GroupPriorityChangeRegistry.Load(failingJob)
	require.False(t, registered)
}
