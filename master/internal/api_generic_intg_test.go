//go:build integration
// +build integration

package internal

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	apiPkg "github.com/determined-ai/determined/master/internal/api"
	authz2 "github.com/determined-ai/determined/master/internal/authz"
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
	"github.com/determined-ai/determined/proto/pkg/taskv1"
)

func addGenericTaskForAuthZTest(
	ctx context.Context,
	t *testing.T,
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

// lifecycleAllocationService models the allocation identities at the API boundary.
// A signaled allocation exits; a started allocation receives a new identity.
type lifecycleAllocationService struct {
	task.AllocationService
	mu         sync.Mutex
	running    map[model.AllocationID]bool
	starts     []model.AllocationID
	restores   []bool
	startCount int
	failAt     int
	started    chan struct{}
	release    chan struct{}
}

func (s *lifecycleAllocationService) GetAllAllocationIDs() []model.AllocationID {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]model.AllocationID, 0, len(s.running))
	for id, running := range s.running {
		if running {
			ids = append(ids, id)
		}
	}
	return ids
}

func (s *lifecycleAllocationService) Signal(
	id model.AllocationID, _ task.AllocationSignal, _ string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running[id] {
		return fmt.Errorf("allocation %s is not running", id)
	}
	s.running[id] = false
	now := time.Now().UTC()
	_, err := db.Bun().NewUpdate().Table("allocations").Set("end_time = ?", now).
		Where("allocation_id = ?", id).Exec(context.Background())
	if err != nil {
		return err
	}
	return db.SetPausedState(id.ToTaskID(), now)
}

func (s *lifecycleAllocationService) StartAllocation(
	_ logger.Context, req sproto.AllocateRequest, _ db.DB, _ rm.ResourceManager,
	_ tasks.TaskSpecifier, _ func(*task.AllocationExited),
) error {
	s.mu.Lock()
	s.startCount++
	firstStart := s.startCount == 1
	fail := s.startCount == s.failAt
	s.mu.Unlock()
	if fail {
		return fmt.Errorf("injected allocation start failure")
	}
	if firstStart && s.started != nil {
		close(s.started)
		<-s.release
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[req.AllocationID] {
		return fmt.Errorf("allocation %s already running", req.AllocationID)
	}
	now := time.Now().UTC()
	if err := db.AddAllocation(context.Background(), &model.Allocation{
		AllocationID: req.AllocationID, TaskID: req.TaskID, Slots: req.SlotsNeeded,
		ResourcePool: req.ResourcePool, StartTime: &now, Ports: map[string]int{},
	}); err != nil {
		return err
	}
	s.running[req.AllocationID] = true
	s.starts = append(s.starts, req.AllocationID)
	s.restores = append(s.restores, req.Restore)
	return nil
}

func TestGenericTaskTreePauseUnpauseKeepsNoPauseAllocations(t *testing.T) {
	api, owner, ctx := setupAPITest(t, nil)
	rootID := addGenericTaskForAuthZTest(ctx, t, owner, 11, nil, model.TaskStateActive)
	pausableID := addGenericTaskForAuthZTest(ctx, t, owner, 11, &rootID, model.TaskStateActive)
	noPauseID := addGenericTaskForAuthZTest(ctx, t, owner, 11, &rootID, model.TaskStateActive)
	defaultNoPauseID := addGenericTaskForAuthZTest(ctx, t, owner, 11, &rootID, model.TaskStateActive)

	for _, entry := range []struct {
		id      model.TaskID
		noPause *bool
	}{
		{pausableID, ptrs.Ptr(false)}, {noPauseID, ptrs.Ptr(true)},
		{defaultNoPauseID, nil},
	} {
		_, err := db.Bun().NewUpdate().Table("tasks").Set("no_pause = ?", entry.noPause).
			Where("task_id = ?", entry.id).Exec(ctx)
		require.NoError(t, err)
	}

	service := &lifecycleAllocationService{running: map[model.AllocationID]bool{}}
	for _, id := range []model.TaskID{rootID, pausableID, noPauseID, defaultNoPauseID} {
		allocationID := model.AllocationID(id.String() + ".0")
		service.running[allocationID] = true
		allocationString, spec, err := getGenericTaskSpec(ctx, id)
		require.NoError(t, err)
		require.Equal(t, allocationID.String(), allocationString)
		spec.GenericTaskConfig = model.DefaultConfigGenericTaskConfig(nil)
		spec.GenericTaskConfig.Resources.SetResourcePool("default")
		require.NoError(t, persistGenericTaskSpec(ctx, id, *spec, allocationID))
	}
	oldService := task.DefaultService
	task.DefaultService = service
	t.Cleanup(func() { task.DefaultService = oldService })

	_, err := api.PauseGenericTask(ctx, &apiv1.PauseGenericTaskRequest{TaskId: rootID.String()})
	require.NoError(t, err)
	for _, id := range []model.TaskID{rootID, pausableID} {
		persisted, err := db.TaskByID(ctx, id)
		require.NoError(t, err)
		require.Equal(t, model.TaskStatePaused, *persisted.State)
		require.False(t, service.running[model.AllocationID(id.String()+".0")])
	}
	for _, id := range []model.TaskID{noPauseID, defaultNoPauseID} {
		persisted, err := db.TaskByID(ctx, id)
		require.NoError(t, err)
		require.Equal(t, model.TaskStateActive, *persisted.State)
		require.True(t, service.running[model.AllocationID(id.String()+".0")])
	}

	_, err = api.UnpauseGenericTask(ctx, &apiv1.UnpauseGenericTaskRequest{TaskId: rootID.String()})
	require.NoError(t, err)
	require.ElementsMatch(t, []model.AllocationID{
		model.AllocationID(rootID.String() + ".1"),
		model.AllocationID(pausableID.String() + ".1"),
	}, service.starts)
	for _, id := range []model.TaskID{rootID, pausableID} {
		allocationID, _, err := getGenericTaskSpec(ctx, id)
		require.NoError(t, err)
		require.Equal(t, id.String()+".1", allocationID)
	}
	for _, id := range []model.TaskID{noPauseID, defaultNoPauseID} {
		persisted, err := db.TaskByID(ctx, id)
		require.NoError(t, err)
		require.Equal(t, model.TaskStateActive, *persisted.State)
		allocationID, err := getAllocationFromTaskID(ctx, id)
		require.NoError(t, err)
		require.Equal(t, id.String()+".0", allocationID)
		require.True(t, service.running[model.AllocationID(allocationID)])
	}
	_, err = api.UnpauseGenericTask(ctx, &apiv1.UnpauseGenericTaskRequest{TaskId: rootID.String()})
	require.Error(t, err)
	require.Len(t, service.starts, 2)
}

func TestClaimPausedGenericTaskWaitsForAllocationAndClaimsOnce(t *testing.T) {
	_, owner, ctx := setupAPITest(t, nil)
	id := addGenericTaskForAuthZTest(ctx, t, owner, 11, nil, model.TaskStatePaused)
	allocationID := model.AllocationID(id.String() + ".0")

	claimed, err := claimPausedGenericTask(ctx, id, allocationID)
	require.NoError(t, err)
	require.False(t, claimed)

	_, err = db.Bun().NewUpdate().Table("allocations").Set("end_time = ?", time.Now().UTC()).
		Where("allocation_id = ?", allocationID).Exec(ctx)
	require.NoError(t, err)
	claimed, err = claimPausedGenericTask(ctx, id, allocationID)
	require.NoError(t, err)
	require.True(t, claimed)
	claimed, err = claimPausedGenericTask(ctx, id, allocationID)
	require.NoError(t, err)
	require.False(t, claimed)

	require.NoError(t, rollbackUnpauseState(ctx, id, allocationID))
	newAllocationID := model.AllocationID(id.String() + ".1")
	now := time.Now().UTC()
	require.NoError(t, db.AddAllocation(ctx, &model.Allocation{
		AllocationID: newAllocationID, TaskID: id, Slots: 1,
		ResourcePool: "default", StartTime: &now, EndTime: &now, Ports: map[string]int{},
	}))
	_, spec, err := getGenericTaskSpec(ctx, id)
	require.NoError(t, err)
	require.NoError(t, persistGenericTaskSpec(ctx, id, *spec, newAllocationID))
	claimed, err = claimPausedGenericTask(ctx, id, allocationID)
	require.NoError(t, err)
	require.False(t, claimed, "a stale allocation must not reserve a newer paused task")
}

func TestConcurrentUnpauseGenericTaskStartsOnce(t *testing.T) {
	api, owner, ctx := setupAPITest(t, nil)
	id := addGenericTaskForAuthZTest(ctx, t, owner, 11, nil, model.TaskStatePaused)
	oldAllocationID := model.AllocationID(id.String() + ".0")
	_, spec, err := getGenericTaskSpec(ctx, id)
	require.NoError(t, err)
	spec.GenericTaskConfig = model.DefaultConfigGenericTaskConfig(nil)
	spec.GenericTaskConfig.Resources.SetResourcePool("default")
	require.NoError(t, persistGenericTaskSpec(ctx, id, *spec, oldAllocationID))
	_, err = db.Bun().NewUpdate().Table("allocations").Set("end_time = ?", time.Now().UTC()).
		Where("allocation_id = ?", oldAllocationID).Exec(ctx)
	require.NoError(t, err)

	service := &lifecycleAllocationService{
		running: map[model.AllocationID]bool{},
		started: make(chan struct{}), release: make(chan struct{}),
	}
	releaseStart := sync.OnceFunc(func() { close(service.release) })
	t.Cleanup(releaseStart)
	oldService := task.DefaultService
	task.DefaultService = service
	t.Cleanup(func() { task.DefaultService = oldService })
	jobID := spec.JobID
	require.NoError(t, tasklist.GroupPriorityChangeRegistry.Add(jobID, func(int) error { return nil }))
	t.Cleanup(func() { _ = tasklist.GroupPriorityChangeRegistry.Delete(jobID) })

	errors := make(chan error, 2)
	request := &apiv1.UnpauseGenericTaskRequest{TaskId: id.String()}
	go func() { _, err := api.UnpauseGenericTask(ctx, request); errors <- err }()
	select {
	case <-service.started:
	case err := <-errors:
		require.NoError(t, err)
		t.Fatal("unpause did not start an allocation")
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for allocation start")
	}
	go func() { _, err := api.UnpauseGenericTask(ctx, request); errors <- err }()
	select {
	case err := <-errors:
		require.Error(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("second unpause did not return while the first start was pending")
	}
	releaseStart()
	select {
	case err := <-errors:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("first unpause did not complete")
	}
	require.Equal(t, []model.AllocationID{model.AllocationID(id.String() + ".1")}, service.starts)
}

func preparePausedGenericTaskForResume(t *testing.T, ctx context.Context, owner model.User, parent *model.TaskID) model.TaskID {
	t.Helper()
	id := addGenericTaskForAuthZTest(ctx, t, owner, 11, parent, model.TaskStatePaused)
	oldID := model.AllocationID(id.String() + ".0")
	_, spec, err := getGenericTaskSpec(ctx, id)
	require.NoError(t, err)
	spec.GenericTaskConfig = model.DefaultConfigGenericTaskConfig(nil)
	spec.GenericTaskConfig.Resources.SetResourcePool("default")
	require.NoError(t, persistGenericTaskSpec(ctx, id, *spec, oldID))
	_, err = db.Bun().NewUpdate().Table("allocations").Set("end_time = ?", time.Now().UTC()).
		Where("allocation_id = ?", oldID).Exec(ctx)
	require.NoError(t, err)
	return id
}

func TestGenericTaskResumeRetriesPartialTreeWithSameAllocations(t *testing.T) {
	api, owner, ctx := setupAPITest(t, nil)
	root := preparePausedGenericTaskForResume(t, ctx, owner, nil)
	child := preparePausedGenericTaskForResume(t, ctx, owner, &root)
	service := &lifecycleAllocationService{running: map[model.AllocationID]bool{}, failAt: 2}
	oldService := task.DefaultService
	task.DefaultService = service
	t.Cleanup(func() { task.DefaultService = oldService })

	request := &apiv1.UnpauseGenericTaskRequest{TaskId: root.String()}
	_, err := api.UnpauseGenericTask(ctx, request)
	require.ErrorContains(t, err, "injected allocation start failure")
	plan, err := pendingGenericTaskResume(ctx, root)
	require.NoError(t, err)
	require.Len(t, plan, 2)
	_, err = api.UnpauseGenericTask(ctx, request)
	require.NoError(t, err)
	require.ElementsMatch(t, []model.AllocationID{
		model.AllocationID(root.String() + ".1"), model.AllocationID(child.String() + ".1"),
	}, service.starts)
	plan, err = pendingGenericTaskResume(ctx, root)
	require.NoError(t, err)
	require.Empty(t, plan)
}

func TestGenericTaskResumeRecoversClaimAndStartWindows(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(fmt.Sprintf("started=%t", started), func(t *testing.T) {
			api, owner, ctx := setupAPITest(t, nil)
			id := preparePausedGenericTaskForResume(t, ctx, owner, nil)
			service := &lifecycleAllocationService{running: map[model.AllocationID]bool{}}
			oldService := task.DefaultService
			task.DefaultService = service
			t.Cleanup(func() { task.DefaultService = oldService })
			members, err := api.GetTaskChildren(ctx, id, nil)
			require.NoError(t, err)
			plan, err := makeGenericTaskResumePlan(ctx, id, members)
			require.NoError(t, err)
			claimed, err := claimPausedGenericTask(ctx, id, plan[0].OldAllocationID)
			require.NoError(t, err)
			require.True(t, claimed)
			if started {
				_, spec, err := getGenericTaskSpec(ctx, id)
				require.NoError(t, err)
				var taskModel model.Task
				require.NoError(t, db.Bun().NewSelect().Model(&taskModel).Where("task_id = ?", id).Scan(ctx))
				require.NoError(t, service.StartAllocation(logger.Context{}, sproto.AllocateRequest{
					AllocationID: plan[0].NewAllocationID, TaskID: id, JobID: *taskModel.JobID,
					SlotsNeeded: 1, ResourcePool: "default",
				}, api.m.db, api.m.rm, spec, nil))
				service.running[plan[0].NewAllocationID] = false // a new master has an empty runtime registry
			}
			require.NoError(t, api.m.recoverGenericTaskResumes(ctx))
			if started {
				require.Equal(t, []model.AllocationID{plan[0].NewAllocationID, plan[0].NewAllocationID}, service.starts)
				require.Equal(t, []bool{false, true}, service.restores)
			} else {
				require.Equal(t, []model.AllocationID{plan[0].NewAllocationID}, service.starts)
				require.Equal(t, []bool{false}, service.restores)
			}
			allocationID, _, err := getGenericTaskSpec(ctx, id)
			require.NoError(t, err)
			require.Equal(t, plan[0].NewAllocationID.String(), allocationID)
		})
	}
}

func TestGenericTaskResumeKillCancelsPendingTree(t *testing.T) {
	api, owner, ctx := setupAPITest(t, nil)
	root := preparePausedGenericTaskForResume(t, ctx, owner, nil)
	_ = preparePausedGenericTaskForResume(t, ctx, owner, &root)
	service := &lifecycleAllocationService{running: map[model.AllocationID]bool{}, failAt: 2}
	oldService := task.DefaultService
	task.DefaultService = service
	t.Cleanup(func() { task.DefaultService = oldService })

	_, err := api.UnpauseGenericTask(ctx, &apiv1.UnpauseGenericTaskRequest{TaskId: root.String()})
	require.ErrorContains(t, err, "injected allocation start failure")
	_, err = api.PauseGenericTask(ctx, &apiv1.PauseGenericTaskRequest{TaskId: root.String()})
	require.ErrorContains(t, err, "generic task resume is in progress")
	_, err = api.KillGenericTask(ctx, &apiv1.KillGenericTaskRequest{TaskId: root.String()})
	require.NoError(t, err)
	plan, err := pendingGenericTaskResume(ctx, root)
	require.NoError(t, err)
	require.False(t, service.running[model.AllocationID(root.String()+".1")])
	require.Len(t, plan, 2)
	require.True(t, plan[0].Canceled)
	require.NoError(t, finishCanceledGenericTaskResume(root, model.AllocationID(root.String()+".1")))
	plan, err = pendingGenericTaskResume(ctx, root)
	require.NoError(t, err)
	require.Empty(t, plan)
}

func TestGenericTaskResumeKillPreservesEndedErrorMember(t *testing.T) {
	api, owner, ctx := setupAPITest(t, nil)
	root := preparePausedGenericTaskForResume(t, ctx, owner, nil)
	child := preparePausedGenericTaskForResume(t, ctx, owner, &root)
	service := &lifecycleAllocationService{running: map[model.AllocationID]bool{}}
	oldService := task.DefaultService
	task.DefaultService = service
	t.Cleanup(func() { task.DefaultService = oldService })
	members, err := api.GetTaskChildren(ctx, root, nil)
	require.NoError(t, err)
	_, err = makeGenericTaskResumePlan(ctx, root, members)
	require.NoError(t, err)
	_, err = db.Bun().NewUpdate().Table("tasks").Set("task_state = ?", model.TaskStateError).
		Set("end_time = ?", time.Now().UTC()).Where("task_id = ?", root).Exec(ctx)
	require.NoError(t, err)
	_, err = api.KillGenericTask(ctx, &apiv1.KillGenericTaskRequest{TaskId: root.String()})
	require.NoError(t, err)
	rootTask, err := db.TaskByID(ctx, root)
	require.NoError(t, err)
	require.Equal(t, model.TaskStateError, *rootTask.State)
	childTask, err := db.TaskByID(ctx, child)
	require.NoError(t, err)
	require.Equal(t, model.TaskStateCanceled, *childTask.State)
	plan, err := pendingGenericTaskResume(ctx, root)
	require.NoError(t, err)
	require.Empty(t, plan)
}

func TestGenericTaskResumeRestoresCanceledStartBeforeSnapshot(t *testing.T) {
	api, owner, ctx := setupAPITest(t, nil)
	id := preparePausedGenericTaskForResume(t, ctx, owner, nil)
	service := &lifecycleAllocationService{running: map[model.AllocationID]bool{}}
	oldService := task.DefaultService
	task.DefaultService = service
	t.Cleanup(func() { task.DefaultService = oldService })
	members, err := api.GetTaskChildren(ctx, id, nil)
	require.NoError(t, err)
	plan, err := makeGenericTaskResumePlan(ctx, id, members)
	require.NoError(t, err)
	claimed, err := claimPausedGenericTask(ctx, id, plan[0].OldAllocationID)
	require.NoError(t, err)
	require.True(t, claimed)
	now := time.Now().UTC()
	require.NoError(t, db.AddAllocation(ctx, &model.Allocation{
		AllocationID: plan[0].NewAllocationID, TaskID: id, Slots: 1,
		ResourcePool: "default", StartTime: &now, Ports: map[string]int{},
	}))
	_, err = api.KillGenericTask(ctx, &apiv1.KillGenericTaskRequest{TaskId: id.String()})
	require.NoError(t, err)
	require.NoError(t, api.m.recoverGenericTaskResumes(ctx))
	require.Equal(t, []model.AllocationID{plan[0].NewAllocationID}, service.starts)
	require.Equal(t, []bool{true}, service.restores)
	require.NoError(t, finishCanceledGenericTaskResume(id, plan[0].NewAllocationID))
	remaining, err := pendingGenericTaskResume(ctx, id)
	require.NoError(t, err)
	require.Empty(t, remaining)
}

func TestGenericTaskResumeReconcilesEndedAllocationBeforeCallback(t *testing.T) {
	api, owner, ctx := setupAPITest(t, nil)
	id := preparePausedGenericTaskForResume(t, ctx, owner, nil)
	service := &lifecycleAllocationService{running: map[model.AllocationID]bool{}}
	oldService := task.DefaultService
	task.DefaultService = service
	t.Cleanup(func() { task.DefaultService = oldService })
	members, err := api.GetTaskChildren(ctx, id, nil)
	require.NoError(t, err)
	plan, err := makeGenericTaskResumePlan(ctx, id, members)
	require.NoError(t, err)
	claimed, err := claimPausedGenericTask(ctx, id, plan[0].OldAllocationID)
	require.NoError(t, err)
	require.True(t, claimed)
	now := time.Now().UTC()
	require.NoError(t, db.AddAllocation(ctx, &model.Allocation{
		AllocationID: plan[0].NewAllocationID, TaskID: id, Slots: 1,
		ResourcePool: "default", StartTime: &now, EndTime: &now, Ports: map[string]int{},
	}))
	require.NoError(t, api.m.recoverGenericTaskResumes(ctx))
	got, err := db.TaskByID(ctx, id)
	require.NoError(t, err)
	require.Equal(t, model.TaskStateCompleted, *got.State)
	require.Empty(t, service.starts)
	allocationID, _, err := getGenericTaskSpec(ctx, id)
	require.NoError(t, err)
	require.Equal(t, plan[0].NewAllocationID.String(), allocationID)
}

func TestGenericTaskResumeEndedAllocationErrorBeatsStoppingPause(t *testing.T) {
	api, owner, ctx := setupAPITest(t, nil)
	id := preparePausedGenericTaskForResume(t, ctx, owner, nil)
	service := &lifecycleAllocationService{running: map[model.AllocationID]bool{}}
	oldService := task.DefaultService
	task.DefaultService = service
	t.Cleanup(func() { task.DefaultService = oldService })
	members, err := api.GetTaskChildren(ctx, id, nil)
	require.NoError(t, err)
	plan, err := makeGenericTaskResumePlan(ctx, id, members)
	require.NoError(t, err)
	claimed, err := claimPausedGenericTask(ctx, id, plan[0].OldAllocationID)
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = db.Bun().NewUpdate().Table("tasks").Set("task_state = ?", model.TaskStateStoppingPaused).
		Where("task_id = ?", id).Exec(ctx)
	require.NoError(t, err)
	now := time.Now().UTC()
	exitErr := "container failed"
	require.NoError(t, db.AddAllocation(ctx, &model.Allocation{
		AllocationID: plan[0].NewAllocationID, TaskID: id, Slots: 1,
		ResourcePool: "default", StartTime: &now, EndTime: &now, ExitErr: &exitErr,
		Ports: map[string]int{},
	}))
	require.NoError(t, api.m.recoverGenericTaskResumes(ctx))
	got, err := db.TaskByID(ctx, id)
	require.NoError(t, err)
	require.Equal(t, model.TaskStateError, *got.State)
}

func TestGenericTaskResumeRejectsPauseAndKillDuringStart(t *testing.T) {
	api, owner, ctx := setupAPITest(t, nil)
	id := preparePausedGenericTaskForResume(t, ctx, owner, nil)
	service := &lifecycleAllocationService{
		running: map[model.AllocationID]bool{}, started: make(chan struct{}), release: make(chan struct{}),
	}
	release := sync.OnceFunc(func() { close(service.release) })
	t.Cleanup(release)
	oldService := task.DefaultService
	task.DefaultService = service
	t.Cleanup(func() { task.DefaultService = oldService })
	done := make(chan error, 1)
	go func() {
		_, err := api.UnpauseGenericTask(ctx, &apiv1.UnpauseGenericTaskRequest{TaskId: id.String()})
		done <- err
	}()
	select {
	case <-service.started:
	case <-time.After(10 * time.Second):
		t.Fatal("start did not reach allocation service")
	}
	_, err := api.PauseGenericTask(ctx, &apiv1.PauseGenericTaskRequest{TaskId: id.String()})
	require.ErrorContains(t, err, "generic task mutation is in progress")
	_, err = api.KillGenericTask(ctx, &apiv1.KillGenericTaskRequest{TaskId: id.String()})
	require.ErrorContains(t, err, "generic task mutation is in progress")
	release()
	require.NoError(t, <-done)
}

func TestGenericTaskResumeRetryAuthorizesPrunedGrandchild(t *testing.T) {
	api, authZ, curUser, ctx := setupNTSCAuthzTest(t)
	root := preparePausedGenericTaskForResume(t, ctx, curUser, nil)
	child := preparePausedGenericTaskForResume(t, ctx, curUser, &root)
	grandchild := preparePausedGenericTaskForResume(t, ctx, curUser, &child)
	oldID, spec, err := getGenericTaskSpec(ctx, grandchild)
	require.NoError(t, err)
	spec.WorkspaceID = 12
	require.NoError(t, persistGenericTaskSpec(ctx, grandchild, *spec, model.AllocationID(oldID)))
	members, err := api.GetTaskChildren(ctx, root, nil)
	require.NoError(t, err)
	plan, err := makeGenericTaskResumePlan(ctx, root, members)
	require.NoError(t, err)
	require.Len(t, plan, 3)
	claimed, err := claimPausedGenericTask(ctx, root, model.AllocationID(root.String()+".0"))
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = db.Bun().NewUpdate().Table("tasks").Set("task_state = ?", model.TaskStateCompleted).
		Set("end_time = ?", time.Now().UTC()).Where("task_id = ?", child).Exec(ctx)
	require.NoError(t, err)
	visible, err := api.GetTaskChildren(ctx, root, []model.TaskState{model.TaskStateCompleted})
	require.NoError(t, err)
	require.Len(t, visible, 1, "completed intermediate task prunes its paused descendant")
	authZ.On("CanGetNSC", mock.Anything, curUser, mock.Anything).Return(nil)
	authZ.On("CanControlGenericTask", mock.Anything, curUser,
		model.AccessScopeID(11), mock.Anything).Return(nil)
	authZ.On("CanControlGenericTask", mock.Anything, curUser,
		model.AccessScopeID(12), mock.Anything).Return(authz2.PermissionDeniedError{})
	_, err = api.UnpauseGenericTask(ctx, &apiv1.UnpauseGenericTaskRequest{TaskId: root.String()})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	rootTask, err := db.TaskByID(ctx, root)
	require.NoError(t, err)
	require.Equal(t, model.TaskStateActive, *rootTask.State)
	grandchildTask, err := db.TaskByID(ctx, grandchild)
	require.NoError(t, err)
	require.Equal(t, model.TaskStatePaused, *grandchildTask.State)
	remaining, err := pendingGenericTaskResume(ctx, root)
	require.NoError(t, err)
	require.Len(t, remaining, 3)
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
			taskID := addGenericTaskForAuthZTest(ctx, t, curUser, 11, nil, test.state)

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
	rootID := addGenericTaskForAuthZTest(ctx, t, curUser, 11, nil, model.TaskStateActive)
	requestedID := addGenericTaskForAuthZTest(
		ctx, t, curUser, 11, &rootID, model.TaskStateActive,
	)
	otherWorkspaceChildID := addGenericTaskForAuthZTest(
		ctx, t, curUser, 12, &rootID, model.TaskStateActive,
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
			rootID := addGenericTaskForAuthZTest(ctx, t, curUser, 11, nil, test.state)
			childID := addGenericTaskForAuthZTest(
				ctx, t, curUser, 12, &rootID, test.state,
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
	taskID := addGenericTaskForAuthZTest(ctx, t, curUser, 11, nil, model.TaskStateActive)
	authZ.On("CanGetNSC", mock.Anything, curUser, model.AccessScopeID(11)).
		Return(authz2.PermissionDeniedError{}).Once()

	_, err := api.PauseGenericTask(ctx, &apiv1.PauseGenericTaskRequest{TaskId: taskID.String()})
	require.ErrorIs(t, err, apiPkg.NotFoundErrs("task", taskID.String(), true))
	authZ.AssertNotCalled(t, "CanControlGenericTask", mock.Anything, mock.Anything,
		mock.Anything, mock.Anything)
}
