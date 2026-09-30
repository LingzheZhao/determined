package command

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pkg/errors"
	"github.com/uptrace/bun"

	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/pkg/model"
)

// GetCommandOwnerID gets a command's ownerID from a taskID. Uses persisted command state.
// Returns db.ErrNotFound if a command with given taskID does not exist.
func GetCommandOwnerID(ctx context.Context, taskID model.TaskID) (model.UserID, error) {
	ownerIDBun := &struct {
		bun.BaseModel `bun:"table:command_state"`
		OwnerID       model.UserID `bun:"owner_id"`
	}{}

	if err := db.Bun().NewSelect().Model(ownerIDBun).
		ColumnExpr("generic_command_spec->'Base'->'Owner'->'id' AS owner_id").
		Where("task_id = ?", taskID).
		Scan(ctx); err != nil {
		if errors.Cause(err) == sql.ErrNoRows {
			return 0, db.ErrNotFound
		}
		return 0, err
	}

	return ownerIDBun.OwnerID, nil
}

// TaskMetadata captures minimal metadata about a task.
type TaskMetadata struct {
	bun.BaseModel `bun:"table:command_state"`
	WorkspaceID   model.AccessScopeID `bun:"workspace_id"`
	TaskType      model.TaskType      `bun:"task_type"`
	ExperimentIDs []int32             `bun:"experiment_ids"`
	TrialIDs      []int32             `bun:"trial_ids"`
}

// IdentifyTask returns the task metadata for a given task ID.
// Returns db.ErrNotFound if a command with given taskID does not exist.
func IdentifyTask(ctx context.Context, taskID model.TaskID) (TaskMetadata, error) {
	metadata := TaskMetadata{}
	// A generic task stores an empty generic_command_spec, whose workspace is 0, beside its
	// generic_task_spec, so the generic task spec is read first.
	if err := db.Bun().NewSelect().Model(&metadata).
		ColumnExpr(`COALESCE(generic_task_spec->'WorkspaceID',
			generic_command_spec->'Metadata'->'workspace_id') AS workspace_id`).
		ColumnExpr("generic_command_spec->>'TaskType' as task_type").
		ColumnExpr("generic_command_spec->'Metadata'->'experiment_ids' as experiment_ids").
		ColumnExpr("generic_command_spec->'Metadata'->'trial_ids' as trial_ids").
		Where("task_id = ?", taskID).
		Scan(ctx); err != nil {
		if errors.Cause(err) == sql.ErrNoRows {
			return metadata, db.ErrNotFound
		}
		return metadata, err
	}
	return metadata, nil
}

// NewTaskRecords are the rows that the commit transaction of a new command, shell, notebook,
// TensorBoard, or generic task writes besides its job row. Committing command_state and the first
// allocation with the task leaves nothing for a crash before the start to lose.
type NewTaskRecords struct {
	Task             *model.Task
	ContextDirectory []byte
	WorkspaceID      int
	WorkspaceName    string
	// Allocation is the first allocation, which is written as PENDING.
	Allocation *model.Allocation
	Snapshot   *CommandSnapshot
}

// InsertNewTaskTx writes the records of a new task in a transaction.
func InsertNewTaskTx(ctx context.Context, tx bun.IDB, r NewTaskRecords) error {
	if err := db.AddTaskTx(ctx, tx, r.Task); err != nil {
		return fmt.Errorf("persisting task %v: %w", r.Task.TaskID, err)
	}

	contextDirectory := r.ContextDirectory
	if contextDirectory == nil {
		contextDirectory = []byte{}
	}
	if _, err := tx.NewInsert().Model(&model.TaskContextDirectory{
		TaskID:           r.Task.TaskID,
		ContextDirectory: contextDirectory,
	}).Exec(ctx); err != nil {
		return fmt.Errorf("persisting context directory files for task %s: %w", r.Task.TaskID, err)
	}

	if _, err := tx.NewInsert().Model(&model.AllocationWorkspaceRecord{
		AllocationID:  r.Allocation.AllocationID,
		WorkspaceID:   r.WorkspaceID,
		WorkspaceName: r.WorkspaceName,
	}).Exec(ctx); err != nil {
		return fmt.Errorf("persisting workspace information for allocation %s: %w",
			r.Allocation.AllocationID, err)
	}

	pending := model.AllocationStatePending
	r.Allocation.State = &pending
	if r.Allocation.Ports == nil {
		r.Allocation.Ports = map[string]int{}
	}
	if _, err := tx.NewInsert().Model(r.Allocation).Exec(ctx); err != nil {
		return fmt.Errorf("persisting allocation %s: %w", r.Allocation.AllocationID, err)
	}

	if _, err := tx.NewInsert().Model(r.Snapshot).Exec(ctx); err != nil {
		return fmt.Errorf("persisting command state of task %s: %w", r.Task.TaskID, err)
	}
	return nil
}
