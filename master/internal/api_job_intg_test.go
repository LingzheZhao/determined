//go:build integration
// +build integration

package internal

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/internal/job"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/proto/pkg/jobv1"
)

func TestJobQueueUpdatesPreflightEveryOwner(t *testing.T) {
	_, _, ctx := setupAPITest(t, nil)
	owner := db.RequireMockUser(t, db.SingleDB())
	other := db.RequireMockUser(t, db.SingleDB())
	ownedJob := db.RequireMockJob(t, db.SingleDB(), &owner.ID)
	foreignJob := db.RequireMockJob(t, db.SingleDB(), &other.ID)
	ownerlessJob := db.RequireMockJob(t, db.SingleDB(), nil)
	priority := func(id model.JobID) *jobv1.QueueControl {
		return &jobv1.QueueControl{JobId: id.String(), Action: &jobv1.QueueControl_Priority{Priority: 10}}
	}
	resourcePool := func(id model.JobID) *jobv1.QueueControl {
		return &jobv1.QueueControl{JobId: id.String(), Action: &jobv1.QueueControl_ResourcePool{ResourcePool: "other"}}
	}
	weight := func(id model.JobID) *jobv1.QueueControl {
		return &jobv1.QueueControl{JobId: id.String(), Action: &jobv1.QueueControl_Weight{Weight: 2}}
	}
	authorize := job.AuthZProvider.Get().CanControlJobQueueUpdate
	applied := 0
	apply := func(updates []*jobv1.QueueControl) error {
		applied++
		return nil
	}
	for _, denied := range []*jobv1.QueueControl{priority(foreignJob), resourcePool(foreignJob),
		weight(foreignJob), priority(ownerlessJob)} {
		err := updateJobQueueAuthorized(ctx, owner,
			[]*jobv1.QueueControl{priority(ownedJob), denied}, authorize, apply)
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		require.Zero(t, applied, "the authorized first job must not be mutated")
	}
	require.NoError(t, updateJobQueueAuthorized(ctx, owner,
		[]*jobv1.QueueControl{priority(ownedJob)}, authorize, apply))
	require.Equal(t, 1, applied)
	admin := model.User{ID: other.ID, Admin: true}
	require.NoError(t, updateJobQueueAuthorized(ctx, admin,
		[]*jobv1.QueueControl{priority(ownedJob), resourcePool(ownerlessJob)}, authorize, apply))
	require.Equal(t, 2, applied)
	// The RBAC provider still relies on the existing queue-level permission.
	require.NoError(t, (&job.JobAuthZRBAC{}).CanControlJobQueueUpdate(
		context.Background(), owner, foreignJob))
}
