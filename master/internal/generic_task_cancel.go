package internal

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/uptrace/bun"
	"golang.org/x/exp/slices"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/internal/task"
	"github.com/determined-ai/determined/master/pkg/model"
)

// errGenericTaskMutationBusy is returned while another generic task mutation holds
// genericTaskMutation. The caller retries.
var errGenericTaskMutationBusy = status.Error(codes.Unavailable, "generic task mutation is in progress")

// authorizedGenericTaskTree returns the generic task rootID and every descendant after
// authorizing the caller to control each of them; requestedID is the task the caller named, which
// an authorization failure reports. A cancel covers the whole tree, as KillGenericTask always has,
// so every member is authorized before anything changes.
func (a *apiServer) authorizedGenericTaskTree(
	ctx context.Context, requestedID, rootID model.TaskID,
) ([]model.Task, error) {
	members, err := a.GetTaskChildren(ctx, rootID, nil)
	if err != nil {
		return nil, err
	}
	if err := a.authorizeGenericTaskMutation(ctx, requestedID, members); err != nil {
		return nil, err
	}
	return members, nil
}

// cancelGenericTaskTree cancels the members of an authorized generic task tree. Each task has its
// own job, and members that have ended are left alone. One transaction records the cancel on
// every live member's job and cancels the members' unfinished resumes; after it commits, each
// member's current or intended allocation is signaled. The caller holds genericTaskMutation.
func cancelGenericTaskTree(ctx context.Context, members []model.Task) error {
	members = filterTasksByState(members, []model.TaskState{
		model.TaskStateCanceled, model.TaskStateCompleted,
	})
	signal, err := cancelGenericTaskMembers(ctx, members)
	if err != nil {
		return err
	}
	// An allocation that is not registered yet reads the cancel when it starts.
	live := task.DefaultService.GetAllAllocationIDs()
	for _, allocationID := range signal {
		if !slices.Contains(live, allocationID) {
			continue
		}
		if err := task.DefaultService.Signal(
			allocationID, task.KillAllocation, "user requested task kill",
		); err != nil {
			return err
		}
	}
	return nil
}

// genericTaskMember is a member of a canceled generic task tree as the cancel transaction reads it.
type genericTaskMember struct {
	TaskID    model.TaskID        `bun:"task_id"`
	State     *model.TaskState    `bun:"task_state"`
	EndTime   *time.Time          `bun:"end_time"`
	JobID     *model.JobID        `bun:"job_id"`
	AttemptID *model.AllocationID `bun:"attempt_id"`
	// AttemptLive is whether the allocation that command_state names exists and has not ended.
	AttemptLive bool `bun:"attempt_live"`
}

// live reports whether the member has not ended. A paused task has an end time but can resume.
func (m *genericTaskMember) live() bool {
	if m.State != nil &&
		(*m.State == model.TaskStatePaused || *m.State == model.TaskStateStoppingPaused) {
		return true
	}
	return m.EndTime == nil && (m.State == nil || !genericTaskTerminal(*m.State))
}

// cancelGenericTaskMembers records the cancel of the live members of a generic task tree in one
// transaction and returns the allocations to signal after it commits. A member whose allocation
// has ended and that no resume is starting, such as a paused task, ends CANCELED here.
func cancelGenericTaskMembers(ctx context.Context, members []model.Task) ([]model.AllocationID, error) {
	ids := make([]model.TaskID, 0, len(members))
	var jobIDs []model.JobID
	for _, m := range members {
		ids = append(ids, m.TaskID)
		if m.JobID != nil {
			jobIDs = append(jobIDs, *m.JobID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	sort.Slice(jobIDs, func(i, j int) bool { return jobIDs[i] < jobIDs[j] })

	var rows []genericTaskResume
	if err := db.Bun().NewSelect().Model(&rows).
		Where("task_id IN (?) OR root_task_id IN (?)", bun.In(ids), bun.In(ids)).Scan(ctx); err != nil {
		return nil, err
	}
	live := task.DefaultService.GetAllAllocationIDs()
	started := make(map[model.TaskID]bool, len(rows))
	for _, row := range rows {
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

	var signal []model.AllocationID
	err := db.Bun().RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		// The jobs are locked before any task, in one order, as the exit decision locks them.
		if len(jobIDs) > 0 {
			if _, err := tx.NewRaw(`SELECT job_id FROM jobs WHERE job_id IN (?)
			ORDER BY job_id FOR UPDATE`, bun.In(jobIDs)).Exec(ctx); err != nil {
				return fmt.Errorf("locking the jobs of generic tasks: %w", err)
			}
		}
		read := append(slices.Clone(ids), resumeTaskIDs(rows)...)
		var current []genericTaskMember
		if err := tx.NewRaw(`SELECT t.task_id, t.task_state, t.end_time, t.job_id,
			cs.allocation_id AS attempt_id,
			a.allocation_id IS NOT NULL AND a.end_time IS NULL
				AND a.state IS DISTINCT FROM ? AS attempt_live
		FROM tasks t
		LEFT JOIN command_state cs ON cs.task_id = t.task_id
		LEFT JOIN allocations a ON a.allocation_id = cs.allocation_id
		WHERE t.task_id IN (?)
		FOR UPDATE OF t`, model.AllocationStateTerminated, bun.In(read)).
			Scan(ctx, &current); err != nil {
			return fmt.Errorf("reading generic tasks: %w", err)
		}
		byID := make(map[model.TaskID]*genericTaskMember, len(current))
		for i := range current {
			byID[current[i].TaskID] = &current[i]
		}

		var liveIDs []model.TaskID
		var liveJobs []model.JobID
		for _, id := range ids {
			m, ok := byID[id]
			if !ok || !m.live() {
				continue
			}
			liveIDs = append(liveIDs, id)
			if m.JobID != nil {
				liveJobs = append(liveJobs, *m.JobID)
			}
		}
		if len(liveJobs) > 0 {
			if _, err := tx.NewUpdate().Table("jobs").
				Set("cancel_requested_at = COALESCE(cancel_requested_at, now())").
				Where("job_id IN (?)", bun.In(liveJobs)).Exec(ctx); err != nil {
				return fmt.Errorf("recording the cancel of generic tasks: %w", err)
			}
		}
		if len(liveIDs) > 0 {
			if _, err := tx.NewUpdate().Table("tasks").
				Set("task_state = ?", model.TaskStateStoppingCanceled).
				Where("task_id IN (?)", bun.In(liveIDs)).
				Where("task_state NOT IN (?)", bun.In([]model.TaskState{
					model.TaskStateCanceled, model.TaskStateCompleted, model.TaskStateError,
				})).Exec(ctx); err != nil {
				return err
			}
		}

		now := time.Now().UTC()
		resuming := make(map[model.TaskID]bool, len(rows))
		for _, row := range rows {
			if m, ok := byID[row.TaskID]; !ok || m.State == nil || genericTaskTerminal(*m.State) {
				if _, err := tx.NewUpdate().Table("generic_task_resume").Set("completed = TRUE").
					Where("task_id = ?", row.TaskID).Exec(ctx); err != nil {
					return err
				}
				continue
			}
			resuming[row.TaskID] = true
			if _, err := tx.NewUpdate().Table("generic_task_resume").
				Set("canceled = TRUE").Set("completed = ?", !started[row.TaskID]).
				Where("task_id = ?", row.TaskID).Exec(ctx); err != nil {
				return err
			}
			if !started[row.TaskID] {
				if _, err := tx.NewUpdate().Table("tasks").
					Set("task_state = ?", model.TaskStateCanceled).Set("end_time = ?", now).
					Where("task_id = ?", row.TaskID).Exec(ctx); err != nil {
					return err
				}
				continue
			}
			signal = append(signal, row.NewAllocationID)
		}

		for _, id := range liveIDs {
			m := byID[id]
			switch {
			case resuming[id]:
			case m.AttemptLive:
				signal = append(signal, *m.AttemptID)
			default:
				// Nothing runs for the member, such as a paused task whose no_pause child
				// still runs, so no exit will end it.
				if _, err := tx.NewUpdate().Table("tasks").
					Set("task_state = ?", model.TaskStateCanceled).Set("end_time = ?", now).
					Where("task_id = ?", id).Exec(ctx); err != nil {
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
	return signal, nil
}

func resumeTaskIDs(rows []genericTaskResume) []model.TaskID {
	ids := make([]model.TaskID, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.TaskID)
	}
	return ids
}
