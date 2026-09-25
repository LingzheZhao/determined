package internal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
	"golang.org/x/exp/slices"

	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/internal/rm/tasklist"
	"github.com/determined-ai/determined/master/internal/sproto"
	"github.com/determined-ai/determined/master/internal/task"
	"github.com/determined-ai/determined/master/pkg/logger"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/master/pkg/tasks"
)

// Generic task mutations share one lock because a pause or kill of a parent can
// overlap an unpause of any descendant. A second request fails while a start is
// in progress; after a partial failure it can resume the persisted plan.
var genericTaskMutation sync.Mutex

type genericTaskResume struct {
	bun.BaseModel   `bun:"table:generic_task_resume"`
	RootTaskID      model.TaskID       `bun:"root_task_id"`
	OperationID     string             `bun:"operation_id"`
	TaskID          model.TaskID       `bun:"task_id,pk"`
	OldAllocationID model.AllocationID `bun:"old_allocation_id"`
	NewAllocationID model.AllocationID `bun:"new_allocation_id"`
	Ordinal         int                `bun:"ordinal"`
	Completed       bool               `bun:"completed"`
	Canceled        bool               `bun:"canceled"`
}

func pendingGenericTaskResume(ctx context.Context, rootID model.TaskID) ([]genericTaskResume, error) {
	var rows []genericTaskResume
	err := db.Bun().NewSelect().Model(&rows).Where("root_task_id = ?", rootID).
		OrderExpr("ordinal").Scan(ctx)
	return rows, err
}

func genericTaskResumeConflicts(ctx context.Context, tasks []model.Task) error {
	ids := make([]model.TaskID, 0, len(tasks))
	for _, t := range tasks {
		ids = append(ids, t.TaskID)
	}
	if len(ids) == 0 {
		return nil
	}
	n, err := db.Bun().NewSelect().Table("generic_task_resume").
		Where("task_id IN (?) OR root_task_id IN (?)", bun.In(ids), bun.In(ids)).Count(ctx)
	if err != nil {
		return err
	}
	if n != 0 {
		return fmt.Errorf("generic task resume is in progress")
	}
	return nil
}

func cancelGenericTaskResumeMembers(ctx context.Context, tasks []model.Task) (map[model.TaskID]model.AllocationID, error) {
	ids := make([]model.TaskID, 0, len(tasks))
	for _, t := range tasks {
		ids = append(ids, t.TaskID)
	}
	result := make(map[model.TaskID]model.AllocationID)
	if len(ids) == 0 {
		return result, nil
	}
	var rows []genericTaskResume
	if err := db.Bun().NewSelect().Model(&rows).
		Where("task_id IN (?) OR root_task_id IN (?)", bun.In(ids), bun.In(ids)).Scan(ctx); err != nil {
		return nil, err
	}
	live := task.DefaultService.GetAllAllocationIDs()
	started := make(map[model.TaskID]bool, len(rows))
	for _, row := range rows {
		result[row.TaskID] = row.NewAllocationID
		if slices.Contains(live, row.NewAllocationID) {
			started[row.TaskID] = true
			continue
		}
		exists, err := db.Bun().NewSelect().Table("allocations").
			Where("allocation_id = ?", row.NewAllocationID).Exists(ctx)
		if err != nil {
			return nil, err
		}
		started[row.TaskID] = exists
	}
	err := db.Bun().RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		final := make(map[model.TaskID]bool, len(rows))
		for _, row := range rows {
			var state model.TaskState
			if err := tx.NewSelect().Table("tasks").Column("task_state").
				Where("task_id = ?", row.TaskID).For("UPDATE").Scan(ctx, &state); err != nil {
				return err
			}
			final[row.TaskID] = genericTaskTerminal(state)
		}
		if _, err := tx.NewUpdate().Table("tasks").Set("task_state = ?", model.TaskStateStoppingCanceled).
			Where("task_id IN (?)", bun.In(ids)).
			Where("task_state NOT IN (?)", bun.In([]model.TaskState{
				model.TaskStateCanceled, model.TaskStateCompleted, model.TaskStateError,
			})).Exec(ctx); err != nil {
			return err
		}
		for _, row := range rows {
			if final[row.TaskID] {
				if _, err := tx.NewUpdate().Table("generic_task_resume").Set("completed = TRUE").
					Where("task_id = ?", row.TaskID).Exec(ctx); err != nil {
					return err
				}
				continue
			}
			if _, err := tx.NewUpdate().Table("generic_task_resume").
				Set("canceled = TRUE").Set("completed = ?", !started[row.TaskID]).
				Where("task_id = ?", row.TaskID).Exec(ctx); err != nil {
				return err
			}
			if !started[row.TaskID] {
				if _, err := tx.NewUpdate().Table("tasks").
					Set("task_state = ?", model.TaskStateCanceled).Set("end_time = ?", time.Now().UTC()).
					Where("task_id = ?", row.TaskID).Exec(ctx); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if err := cleanupGenericTaskResume(ctx, row.RootTaskID, row.OperationID); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func cleanupGenericTaskResume(ctx context.Context, rootID model.TaskID, operationID string) error {
	_, err := db.Bun().NewRaw(`DELETE FROM generic_task_resume AS r
		WHERE r.root_task_id = ? AND r.operation_id = ?
		AND NOT EXISTS (SELECT 1 FROM generic_task_resume AS pending
			WHERE pending.root_task_id = r.root_task_id AND pending.operation_id = r.operation_id
			AND NOT pending.completed)`, rootID, operationID).Exec(ctx)
	return err
}

func finishCanceledGenericTaskResume(taskID model.TaskID, allocationID model.AllocationID) error {
	ctx := context.Background()
	var row genericTaskResume
	err := db.Bun().NewSelect().Model(&row).
		Where("task_id = ? AND new_allocation_id = ? AND canceled", taskID, allocationID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err := db.Bun().NewUpdate().Table("tasks").Set("task_state = ?", model.TaskStateCanceled).
		Set("end_time = ?", time.Now().UTC()).Where("task_id = ?", taskID).Exec(ctx); err != nil {
		return err
	}
	if err := completeGenericTaskResumeMember(ctx, row); err != nil {
		return err
	}
	return cleanupGenericTaskResume(ctx, row.RootTaskID, row.OperationID)
}

func makeGenericTaskResumePlan(ctx context.Context, rootID model.TaskID, members []model.Task) ([]genericTaskResume, error) {
	plan := make([]genericTaskResume, 0, len(members))
	operationID := uuid.NewString()
	for _, member := range members {
		if member.State == nil || *member.State != model.TaskStatePaused {
			continue
		}
		oldID, spec, err := getGenericTaskSpec(ctx, member.TaskID)
		if err != nil {
			return nil, fmt.Errorf("retrieving task %s spec: %w", member.TaskID, err)
		}
		if spec == nil || member.JobID == nil {
			return nil, fmt.Errorf("missing task %s spec or job", member.TaskID)
		}
		if spec.GenericTaskConfig.Resources.Slots() == nil {
			return nil, fmt.Errorf("task %s has no slots", member.TaskID)
		}
		oldAllocationID := model.AllocationID(oldID)
		suffix, err := oldAllocationID.GetAllocationSpecifier()
		if err != nil {
			return nil, err
		}
		var ended bool
		err = db.Bun().NewSelect().Table("allocations").ColumnExpr("end_time IS NOT NULL").
			Where("allocation_id = ? AND task_id = ?", oldID, member.TaskID).Scan(ctx, &ended)
		if err != nil {
			return nil, err
		}
		if !ended {
			return nil, fmt.Errorf("task %s allocation has not stopped", member.TaskID)
		}
		plan = append(plan, genericTaskResume{
			RootTaskID: rootID, OperationID: operationID, TaskID: member.TaskID, OldAllocationID: oldAllocationID,
			NewAllocationID: model.AllocationID(fmt.Sprintf("%s.%d", member.TaskID, suffix+1)),
			Ordinal:         len(plan),
		})
	}
	if len(plan) == 0 {
		return nil, fmt.Errorf("task %s has no paused members", rootID)
	}
	sort.SliceStable(plan, func(i, j int) bool { return plan[i].TaskID == rootID })
	for i := range plan {
		plan[i].Ordinal = i
	}
	// The plan is committed before any member changes state or starts an allocation.
	err := db.Bun().RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var state model.TaskState
		if err := tx.NewSelect().Table("tasks").Column("task_state").
			Where("task_id = ?", rootID).For("UPDATE").Scan(ctx, &state); err != nil {
			return err
		}
		if state != model.TaskStatePaused {
			return fmt.Errorf("task %s is no longer paused", rootID)
		}
		_, err := tx.NewInsert().Model(&plan).Exec(ctx)
		return err
	})
	return plan, err
}

func (a *apiServer) runGenericTaskResume(ctx context.Context, plan []genericTaskResume) (runErr error) {
	var current genericTaskResume
	defer func() {
		if runErr != nil {
			runErr = fmt.Errorf("resuming operation %s root %s member %s allocation %s: %w",
				current.OperationID, current.RootTaskID, current.TaskID, current.NewAllocationID, runErr)
		}
	}()
	for _, member := range plan {
		current = member
		if member.Completed {
			continue
		}
		if member.Canceled {
			if slices.Contains(task.DefaultService.GetAllAllocationIDs(), member.NewAllocationID) {
				if err := task.DefaultService.Signal(member.NewAllocationID, task.KillAllocation, "resume canceled by user"); err != nil {
					return err
				}
				continue
			}
			var allocation model.Allocation
			err := db.Bun().NewSelect().Model(&allocation).Where("allocation_id = ?", member.NewAllocationID).Scan(ctx)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil && allocation.EndTime == nil {
				var t model.Task
				if err := db.Bun().NewSelect().Model(&t).Where("task_id = ?", member.TaskID).Scan(ctx); err != nil {
					return err
				}
				_, spec, err := getGenericTaskSpec(ctx, member.TaskID)
				if err != nil {
					return err
				}
				if spec == nil || t.JobID == nil {
					return fmt.Errorf("missing task %s spec or job", member.TaskID)
				}
				if err := a.startGenericTaskResumeAllocation(ctx, member, t, spec, true); err != nil {
					return err
				}
				if err := task.DefaultService.Signal(member.NewAllocationID, task.KillAllocation, "resume canceled by user"); err != nil {
					return err
				}
				continue
			}
			if _, err := db.Bun().NewUpdate().Table("tasks").Set("task_state = ?", model.TaskStateCanceled).
				Set("end_time = ?", time.Now().UTC()).Where("task_id = ?", member.TaskID).Exec(ctx); err != nil {
				return err
			}
			if err := completeGenericTaskResumeMember(ctx, member); err != nil {
				return err
			}
			continue
		}
		var t model.Task
		if err := db.Bun().NewSelect().Model(&t).Where("task_id = ?", member.TaskID).Scan(ctx); err != nil {
			return err
		}
		var allocation model.Allocation
		allocationErr := db.Bun().NewSelect().Model(&allocation).
			Where("allocation_id = ?", member.NewAllocationID).Scan(ctx)
		if allocationErr != nil && !errors.Is(allocationErr, sql.ErrNoRows) {
			return allocationErr
		}
		oldID, spec, err := getGenericTaskSpec(ctx, member.TaskID)
		if err != nil {
			return err
		}
		if spec == nil || t.JobID == nil {
			return fmt.Errorf("missing task %s spec or job", member.TaskID)
		}
		if oldID != member.OldAllocationID.String() && oldID != member.NewAllocationID.String() {
			return fmt.Errorf("task %s allocation changed while resuming", member.TaskID)
		}
		if allocationErr == nil && allocation.EndTime != nil {
			if err := persistGenericTaskSpec(ctx, member.TaskID, *spec, member.NewAllocationID); err != nil {
				return err
			}
			if err := reconcileEndedGenericTaskResume(ctx, t, allocation); err != nil {
				return err
			}
			if err := completeGenericTaskResumeMember(ctx, member); err != nil {
				return err
			}
			continue
		}
		if t.State == nil || (*t.State != model.TaskStatePaused && *t.State != model.TaskStateActive) {
			if err := completeGenericTaskResumeMember(ctx, member); err != nil {
				return err
			}
			continue
		}
		if *t.State == model.TaskStatePaused {
			claimed, err := claimPausedGenericTask(ctx, member.TaskID, member.OldAllocationID)
			if err != nil {
				return err
			}
			if !claimed {
				return fmt.Errorf("cannot claim paused task %s", member.TaskID)
			}
		}
		if _, found := tasklist.GroupPriorityChangeRegistry.Load(*t.JobID); !found {
			priorityChange := func(priority int) error { spec.GenericTaskConfig.Resources.SetPriority(&priority); return nil }
			if err := tasklist.GroupPriorityChangeRegistry.Add(*t.JobID, priorityChange); err != nil {
				return err
			}
		}
		live := slices.Contains(task.DefaultService.GetAllAllocationIDs(), member.NewAllocationID)
		if !live {
			if err := a.startGenericTaskResumeAllocation(ctx, member, t, spec, allocationErr == nil); err != nil {
				return err
			}
		}
		// The plan retains the identity across a crash or a lost snapshot commit response.
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		err = persistGenericTaskSpec(persistCtx, member.TaskID, *spec, member.NewAllocationID)
		if err == nil {
			err = completeGenericTaskResumeMember(persistCtx, member)
		}
		cancel()
		if err != nil {
			return err
		}
	}
	return cleanupGenericTaskResume(ctx, plan[0].RootTaskID, plan[0].OperationID)
}

func (a *apiServer) startGenericTaskResumeAllocation(
	ctx context.Context, member genericTaskResume, t model.Task, spec *tasks.GenericTaskSpec, restore bool,
) error {
	logCtx := logger.Context{"job-id": t.JobID, "task-id": t.TaskID, "task-type": model.TaskTypeGeneric}
	singleNode := spec.GenericTaskConfig.Resources.IsSingleNode() != nil && *spec.GenericTaskConfig.Resources.IsSingleNode()
	now := time.Now().UTC()
	return task.DefaultService.StartAllocation(logCtx, sproto.AllocateRequest{
		AllocationID: member.NewAllocationID, TaskID: member.TaskID, JobID: *t.JobID,
		JobSubmissionTime: now, RequestTime: now, IsUserVisible: true,
		Name:                fmt.Sprintf("Generic Task %s", member.TaskID),
		SlotsNeeded:         *spec.GenericTaskConfig.Resources.Slots(),
		ResourcePool:        spec.GenericTaskConfig.Resources.ResourcePool(),
		FittingRequirements: sproto.FittingRequirements{SingleAgent: singleNode},
		Preemption: sproto.PreemptionConfig{Preemptible: true,
			TimeoutDuration: time.Duration(spec.GenericTaskConfig.PreemptionTimeout) * time.Second},
		Restore: restore,
	}, a.m.db, a.m.rm, spec,
		getGenericTaskOnAllocationExit(context.WithoutCancel(ctx), member.TaskID, member.NewAllocationID, *t.JobID, logCtx))
}

func reconcileEndedGenericTaskResume(ctx context.Context, t model.Task, allocation model.Allocation) error {
	if t.State == nil || genericTaskTerminal(*t.State) {
		return nil
	}
	state := model.TaskStateCompleted
	switch {
	case *t.State == model.TaskStateStoppingCanceled:
		state = model.TaskStateCanceled
	case allocation.ExitErr != nil:
		state = model.TaskStateError
	case *t.State == model.TaskStateStoppingPaused:
		state = model.TaskStatePaused
	case *t.State == model.TaskStateStoppingError:
		state = model.TaskStateError
	}
	_, err := db.Bun().NewUpdate().Table("tasks").Set("task_state = ?", state).
		Set("end_time = ?", allocation.EndTime).Where("task_id = ?", t.TaskID).
		Where("task_state = ?", *t.State).Exec(ctx)
	return err
}

func genericTaskTerminal(state model.TaskState) bool {
	return slices.Contains([]model.TaskState{
		model.TaskStateCanceled, model.TaskStateCompleted, model.TaskStateError,
	}, state)
}

func completeGenericTaskResumeMember(ctx context.Context, member genericTaskResume) error {
	_, err := db.Bun().NewUpdate().Table("generic_task_resume").Set("completed = TRUE").
		Where("task_id = ? AND new_allocation_id = ?", member.TaskID, member.NewAllocationID).Exec(ctx)
	return err
}

func (m *Master) recoverGenericTaskResumes(ctx context.Context) error {
	var roots []model.TaskID
	if err := db.Bun().NewSelect().Table("generic_task_resume").
		ColumnExpr("DISTINCT root_task_id").Scan(ctx, &roots); err != nil {
		return err
	}
	a := &apiServer{m: m}
	for _, root := range roots {
		plan, err := pendingGenericTaskResume(ctx, root)
		if err != nil {
			return err
		}
		if len(plan) > 0 {
			if err := a.runGenericTaskResume(ctx, plan); err != nil {
				return fmt.Errorf("recovering task %s resume: %w", root, err)
			}
		}
	}
	return nil
}
