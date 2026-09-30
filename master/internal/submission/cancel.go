package submission

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pkg/errors"
	"github.com/uptrace/bun"

	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/pkg/model"
)

// A cancel is durable first: it records that the job was asked to stop, under the lock of the
// job's row, and only then signals what runs. The decision that ends a task takes the same lock,
// so a cancel either commits before the end, which then ends the job CANCELED, or sees the end
// and changes nothing. Every start reads the request after it registers its allocation, so a job
// that a cancel cannot signal yet is stopped as it starts.

// CancelTask records that the command or shell of a job was asked to stop, unless its task has
// ended. It returns whether the task is live and, if so, the attempt that command_state names,
// which the caller signals after the commit.
func CancelTask(
	ctx context.Context, jobID model.JobID,
) (attempt model.AllocationID, live bool, err error) {
	err = db.Bun().RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := db.LockJobTx(ctx, tx, jobID); err != nil {
			return err
		}
		var task struct {
			Live      bool                `bun:"live"`
			AttemptID *model.AllocationID `bun:"attempt_id"`
		}
		err := tx.NewSelect().Table("tasks").
			ColumnExpr("tasks.end_time IS NULL AS live").
			ColumnExpr("cs.allocation_id AS attempt_id").
			Join("LEFT JOIN command_state cs ON cs.task_id = tasks.task_id").
			Where("tasks.job_id = ?", jobID).
			Scan(ctx, &task)
		if errors.Is(err, sql.ErrNoRows) {
			return db.ErrNotFound
		} else if err != nil {
			return fmt.Errorf("reading the task of job %s: %w", jobID, err)
		}
		if !task.Live {
			return nil
		}
		live = true
		if task.AttemptID != nil {
			attempt = *task.AttemptID
		}
		return db.RequestJobCancelTx(ctx, tx, jobID)
	})
	return attempt, live, err
}

// CancelExperiment records that the experiment of a job was asked to stop, unless it has ended or
// was deleted. It returns whether the experiment is live and, if so, its ID, which the caller
// kills after the commit.
func CancelExperiment(
	ctx context.Context, jobID model.JobID,
) (experimentID int, live bool, err error) {
	err = db.Bun().RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := db.LockJobTx(ctx, tx, jobID); err != nil {
			return err
		}
		var exp struct {
			ID    int         `bun:"id"`
			State model.State `bun:"state"`
		}
		err := tx.NewSelect().Table("experiments").Column("id", "state").
			Where("job_id = ?", jobID).
			Scan(ctx, &exp)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		} else if err != nil {
			return fmt.Errorf("reading the experiment of job %s: %w", jobID, err)
		}
		if model.TerminalStates[exp.State] || model.DeletingStates[exp.State] {
			return nil
		}
		experimentID, live = exp.ID, true
		return db.RequestJobCancelTx(ctx, tx, jobID)
	})
	return experimentID, live, err
}
