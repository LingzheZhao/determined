package internal

import (
	"context"
	"fmt"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/determined-ai/determined/master/internal/command"
	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/internal/rm/tasklist"
	"github.com/determined-ai/determined/master/internal/sproto"
	"github.com/determined-ai/determined/master/internal/task"
	"github.com/determined-ai/determined/master/pkg/logger"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/master/pkg/ptrs"
)

// restoreGenericTasks restores every generic task that has not ended, from the persisted state of
// the allocation that its command_state names. A paused task has ended, so it stays paused.
func (m *Master) restoreGenericTasks(ctx context.Context) error {
	var snapshots []command.CommandSnapshot
	err := command.SelectLiveSnapshots(&snapshots).
		Where("task.task_type = ?", model.TaskTypeGeneric).
		Where("command_snapshot.generic_task_spec IS NOT NULL").
		// An unpause claims the task before command_state names its new allocation, so
		// recoverGenericTaskResumes restores the tasks that an unpause is resuming.
		Where(`NOT EXISTS (SELECT 1 FROM generic_task_resume AS r
			WHERE r.task_id = command_snapshot.task_id AND NOT r.completed)`).
		Scan(ctx)
	if err != nil {
		return err
	}

	for i := range snapshots {
		m.restoreGenericTask(ctx, &snapshots[i])
	}
	return nil
}

// restoreGenericTask continues one generic task that has not ended. A task that it cannot restore
// is ended with its allocation INFRASTRUCTURE_FAILED.
func (m *Master) restoreGenericTask(ctx context.Context, snapshot *command.CommandSnapshot) {
	taskID, allocationID := snapshot.TaskID, snapshot.AllocationID
	syslog := log.WithField("task-id", taskID).WithField("allocation-id", allocationID)
	fail := func(cause error) {
		syslog.WithError(cause).Error("failed to restore generic task")
		if err := db.FailTaskAllocation(ctx, taskID, allocationID,
			ptrs.Ptr(model.TaskStateError), command.RestoreFailedReason, cause,
		); err != nil {
			syslog.WithError(err).Error("ending a generic task that failed to restore")
		}
	}

	jobID := snapshot.Task.JobID
	if jobID == nil || snapshot.Task.Job == nil {
		fail(fmt.Errorf("task %s has no job", taskID))
		return
	}
	cancelRequested := snapshot.Task.Job.CancelRequestedAt != nil
	taskState := model.TaskStateActive
	if snapshot.Task.State != nil {
		taskState = *snapshot.Task.State
	}

	var restore bool
	switch action := command.DecideRestore(
		&snapshot.Allocation, cancelRequested || genericTaskStopping(taskState),
	); action {
	case command.RestoreEnded:
		endTime := time.Now().UTC()
		if snapshot.Allocation.EndTime != nil {
			endTime = *snapshot.Allocation.EndTime
		}
		state := endedGenericTaskState(taskState, cancelRequested, snapshot.Allocation)
		if err := db.EndLiveTask(ctx, taskID, endTime, &state); err != nil {
			syslog.WithError(err).Error("ending a generic task whose allocation ended")
		}
		return
	case command.RestoreStopQueued:
		state := endedGenericTaskState(taskState, cancelRequested, snapshot.Allocation)
		if err := db.EndQueuedTask(
			ctx, taskID, allocationID, &state, command.StopQueuedReason,
		); err != nil {
			syslog.WithError(err).Error("ending a queued generic task that was asked to stop")
		}
		return
	case command.RestoreRequeue:
		// The resource manager may have recorded resources for the allocation before the
		// allocation received them; they were never launched, and a new placement replaces them.
		if err := db.PurgeAllocationResources(ctx, db.Bun(), allocationID); err != nil {
			fail(err)
			return
		}
	case command.RestorePlaced:
		restore = true
	default:
		panic(fmt.Sprintf("unexpected restore action %d", action))
	}

	if err := m.startRestoredGenericTask(ctx, snapshot, restore); err != nil {
		fail(err)
	}
}

// startRestoredGenericTask starts the allocation of a generic task again: restored through the
// resource manager, or requested under its ID.
func (m *Master) startRestoredGenericTask(
	ctx context.Context, snapshot *command.CommandSnapshot, restore bool,
) error {
	taskID, jobID := snapshot.TaskID, *snapshot.Task.JobID
	resources := snapshot.GenericTaskSpec.GenericTaskConfig.Resources
	slots := resources.RawSlots
	if slots == nil {
		return fmt.Errorf("task %s has no slots in its resources", taskID)
	}
	resourcePool := resources.RawResourcePool
	if resourcePool == nil {
		return fmt.Errorf("task %s has no resource pool in its resources", taskID)
	}

	logCtx := logger.Context{
		"job-id":    jobID,
		"task-id":   taskID,
		"task-type": snapshot.Task.TaskType,
	}
	priorityChange := func(priority int) error {
		return nil
	}
	if err := tasklist.GroupPriorityChangeRegistry.Add(jobID, priorityChange); err != nil {
		return err
	}

	onAllocationExit := getGenericTaskOnAllocationExit(
		ctx, taskID, snapshot.AllocationID, jobID, logCtx)
	isSingleNode := resources.IsSingleNode() != nil && *resources.IsSingleNode()
	err := task.DefaultService.StartAllocation(logCtx,
		sproto.AllocateRequest{
			AllocationID:      snapshot.AllocationID,
			TaskID:            taskID,
			JobID:             jobID,
			JobSubmissionTime: snapshot.RegisteredTime,
			IsUserVisible:     true,
			Name:              fmt.Sprintf("Generic Task %s", taskID),
			SlotsNeeded:       *slots,
			ResourcePool:      *resourcePool,
			FittingRequirements: sproto.FittingRequirements{
				SingleAgent: isSingleNode,
			},

			Restore:   restore,
			Persisted: !restore,
		}, m.db, m.rm, snapshot.GenericTaskSpec, onAllocationExit)
	if err != nil {
		if err := tasklist.GroupPriorityChangeRegistry.Delete(jobID); err != nil {
			log.WithField("task-id", taskID).WithError(err).
				Error("deleting group priority change registry")
		}
		return err
	}
	if err := task.KillIfCancelRequested(ctx, jobID, snapshot.AllocationID); err != nil {
		log.WithField("task-id", taskID).WithError(err).
			Error("checking whether a restored generic task was asked to stop")
	}
	return nil
}

// genericTaskStopping reports whether a generic task was asked to pause or stop, so that an
// allocation that was never placed is not requested again.
func genericTaskStopping(state model.TaskState) bool {
	switch state {
	case model.TaskStateStoppingPaused, model.TaskStateStoppingCanceled,
		model.TaskStateStoppingError, model.TaskStateStoppingCompleted:
		return true
	default:
		return false
	}
}

// endedGenericTaskState is the state of a generic task whose allocation ended, or was closed
// before placement, without the allocation's exit reaching the task.
func endedGenericTaskState(
	state model.TaskState, cancelRequested bool, allocation model.Allocation,
) model.TaskState {
	switch {
	case cancelRequested || state == model.TaskStateStoppingCanceled:
		return model.TaskStateCanceled
	case allocation.ExitErr != nil:
		return model.TaskStateError
	case state == model.TaskStateStoppingPaused:
		return model.TaskStatePaused
	case state == model.TaskStateStoppingError:
		return model.TaskStateError
	default:
		return model.TaskStateCompleted
	}
}
