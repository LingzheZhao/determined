package job

import (
	"context"

	"github.com/determined-ai/determined/master/internal/authz"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/proto/pkg/jobv1"
)

// JobAuthZ describes authz methods for jobs.
type JobAuthZ interface {
	// FilterJobs returns a list of jobs that the user is authorized to view.
	FilterJobs(
		ctx context.Context, curUser model.User, jobs []*jobv1.Job,
	) ([]*jobv1.Job, error)

	// CanControlJobQueue returns an error if the user is not authorized to manipulate the
	// job queue.
	CanControlJobQueue(
		ctx context.Context, curUser *model.User,
	) (permErr error, err error)

	// CanControlJobQueueUpdate checks a specific target before any batch update is applied.
	CanControlJobQueueUpdate(ctx context.Context, curUser model.User, jobID model.JobID) error
}

// AuthZProvider is the authz registry for Notebooks, Shells, and Commands.
var AuthZProvider authz.AuthZProviderType[JobAuthZ]
