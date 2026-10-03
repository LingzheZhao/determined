package job

import (
	"context"

	"github.com/determined-ai/determined/master/internal/authz"
	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/proto/pkg/jobv1"
)

// JobAuthZBasic is basic OSS controls.
type JobAuthZBasic struct{}

// FilterJobs returns a list of jobs that the user can view.
func (a *JobAuthZBasic) FilterJobs(
	ctx context.Context, curUser model.User, jobs []*jobv1.Job,
) ([]*jobv1.Job, error) {
	return jobs, nil
}

// CanControlJobQueue returns an error if the user is not authorized to manipulate the
// job queue.
func (a *JobAuthZBasic) CanControlJobQueue(
	ctx context.Context, curUser *model.User,
) (permErr error, err error) {
	return nil, nil
}

// CanControlJobQueueUpdate allows only the owner or an administrator to control a job.
func (a *JobAuthZBasic) CanControlJobQueueUpdate(
	ctx context.Context, curUser model.User, jobID model.JobID,
) error {
	j, err := db.JobByID(ctx, jobID)
	if err != nil {
		return err
	}
	if curUser.Admin || j.OwnerID != nil && *j.OwnerID == curUser.ID {
		return nil
	}
	return authz.PermissionDeniedError{}.WithPrefix(
		"non-admin users may not control other users' jobs:",
	)
}

func init() {
	AuthZProvider.Register("basic", &JobAuthZBasic{})
}
