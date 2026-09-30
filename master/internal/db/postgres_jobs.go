package db

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"

	"github.com/determined-ai/determined/master/pkg/model"
)

// AddJobTx persists the existence of a job with a transaction.
func AddJobTx(ctx context.Context, idb bun.IDB, j *model.Job) error {
	if idb == nil {
		idb = Bun()
	}

	if _, err := idb.NewInsert().Model(j).Exec(ctx); err != nil {
		return fmt.Errorf("adding job: %w", err)
	}

	return nil
}

// AddJob persists the existence of a job.
func AddJob(j *model.Job) error {
	return AddJobTx(context.TODO(), Bun(), j)
}

// JobByID retrieves a job by ID.
func JobByID(ctx context.Context, jobID model.JobID) (*model.Job, error) {
	var j model.Job
	err := Bun().NewSelect().Model(&j).
		Where("job_id = ?", jobID).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("querying job: %w", err)
	}
	return &j, nil
}

// LockJobTx locks the row of a job until the transaction ends. The decision that ends a task and a
// cancel lock the same row, so a cancel always sees whether the task has ended.
func LockJobTx(ctx context.Context, tx bun.IDB, jobID model.JobID) error {
	if _, err := tx.NewRaw(`SELECT job_id FROM jobs WHERE job_id = ? FOR UPDATE`, jobID).
		Exec(ctx); err != nil {
		return fmt.Errorf("locking job %s: %w", jobID, err)
	}
	return nil
}

// lockTaskJobTx locks the row of the job that a task belongs to, if the task has one.
func lockTaskJobTx(ctx context.Context, tx bun.IDB, taskID model.TaskID) error {
	if _, err := tx.NewRaw(`SELECT job_id FROM jobs
	WHERE job_id = (SELECT job_id FROM tasks WHERE task_id = ?) FOR UPDATE`, taskID).
		Exec(ctx); err != nil {
		return fmt.Errorf("locking the job of task %s: %w", taskID, err)
	}
	return nil
}

// RequestJobCancelTx records that a job was asked to stop, keeping the time it was first asked.
// The caller holds the job's lock and has checked that the job has not ended.
func RequestJobCancelTx(ctx context.Context, tx bun.IDB, jobID model.JobID) error {
	if _, err := tx.NewUpdate().Table("jobs").
		Set("cancel_requested_at = COALESCE(cancel_requested_at, now())").
		Where("job_id = ?", jobID).
		Exec(ctx); err != nil {
		return fmt.Errorf("recording the cancel of job %s: %w", jobID, err)
	}
	return nil
}

// JobCancelRequested reports whether a job was asked to stop.
func JobCancelRequested(ctx context.Context, jobID model.JobID) (bool, error) {
	requested, err := Bun().NewSelect().Table("jobs").
		Where("job_id = ?", jobID).
		Where("cancel_requested_at IS NOT NULL").
		Exists(ctx)
	if err != nil {
		return false, fmt.Errorf("reading whether job %s was asked to stop: %w", jobID, err)
	}
	return requested, nil
}
