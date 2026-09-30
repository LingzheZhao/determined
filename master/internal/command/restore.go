package command

import (
	"github.com/uptrace/bun"

	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/pkg/model"
)

// RestoreAction is how a master restart continues a task that has not ended, from the allocation
// that its command_state names.
type RestoreAction int

const (
	// RestoreEnded ends the task, because the allocation ended and the restart interrupted what
	// came after.
	RestoreEnded RestoreAction = iota
	// RestoreRequeue requests the allocation again under its ID, because it was never placed.
	RestoreRequeue
	// RestoreStopQueued ends the task instead of requesting the allocation again, because it was
	// never placed and the task was asked to stop.
	RestoreStopQueued
	// RestorePlaced restores the allocation through the resource manager, which reattaches its
	// containers, because it was placed and may have some.
	RestorePlaced
)

const (
	// RestoreFailedReason is the exit reason of an allocation that failed to restore.
	RestoreFailedReason = "the allocation failed to restore"
	// StopQueuedReason is the exit reason of a never-placed allocation whose task was asked to stop
	// before a restart.
	StopQueuedReason = "the task was asked to stop before the allocation was placed"
)

// DecideRestore decides how a restart continues a task from the persisted state of its allocation.
// Placement is the allocation's own ASSIGNED write, which comes before any launch, so any later
// state may have containers and is never requested again. The start time is no evidence either
// way: it is set only once a container pulls its image, and closing the open allocations at
// startup stamps it.
func DecideRestore(a *model.Allocation, stopRequested bool) RestoreAction {
	switch {
	case a.EndTime != nil || a.State != nil && *a.State == model.AllocationStateTerminated:
		return RestoreEnded
	case a.State != nil && *a.State == model.AllocationStatePending:
		if stopRequested {
			return RestoreStopQueued
		}
		return RestoreRequeue
	default:
		// An allocation without a recorded state may have containers too.
		return RestorePlaced
	}
}

// SelectLiveSnapshots selects the command_state of every task that has not ended, with its task,
// the task's job, and the allocation that command_state names.
func SelectLiveSnapshots(snapshots *[]CommandSnapshot) *bun.SelectQuery {
	return db.Bun().NewSelect().Model(snapshots).
		Relation("Allocation").
		Relation("Task").
		Relation("Task.Job").
		Where("task.end_time IS NULL")
}
