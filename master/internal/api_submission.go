package internal

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/determined-ai/determined/master/internal/authz"
	"github.com/determined-ai/determined/master/internal/command"
	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/internal/experiment"
	"github.com/determined-ai/determined/master/pkg/model"
)

// A replay returns a job the caller submitted under the same idempotency key, so the caller owns
// it. It still needs read access to the job now: access revoked since the submit is honored. A
// job whose task or experiment row is gone, such as a deleted experiment, is left to its owner.

// authorizeTaskReplay checks that the caller may read the task of a job it submitted earlier.
func authorizeTaskReplay(ctx context.Context, curUser model.User, job *model.Job) error {
	var taskID model.TaskID
	err := db.Bun().NewSelect().Table("tasks").Column("task_id").
		Where("job_id = ?", job.JobID).
		Limit(1).
		Scan(ctx, &taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return fmt.Errorf("finding the task of job %s: %w", job.JobID, err)
	}

	metadata, err := command.IdentifyTask(ctx, taskID)
	if errors.Is(err, db.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	return replayAuthzError(job, command.AuthZProvider.Get().CanGetNSC(ctx, curUser, metadata.WorkspaceID))
}

// authorizeExperimentReplay checks that the caller may read the experiment of a job it submitted
// earlier.
func authorizeExperimentReplay(ctx context.Context, curUser model.User, job *model.Job) error {
	var expID int
	err := db.Bun().NewSelect().Table("experiments").Column("id").
		Where("job_id = ?", job.JobID).
		Scan(ctx, &expID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return fmt.Errorf("finding the experiment of job %s: %w", job.JobID, err)
	}

	exp, err := db.ExperimentByID(ctx, expID)
	if errors.Is(err, db.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	return replayAuthzError(job, experiment.AuthZProvider.Get().CanGetExperiment(ctx, curUser, exp))
}

func replayAuthzError(job *model.Job, err error) error {
	if authz.IsPermissionDenied(err) {
		return status.Errorf(codes.PermissionDenied,
			"the idempotency key names job %s, which the caller may no longer read: %s", job.JobID, err)
	}
	return err
}
