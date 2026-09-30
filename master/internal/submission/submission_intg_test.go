//go:build integration
// +build integration

package submission

import (
	"context"
	"log"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/proto/pkg/apiv1"
)

func TestMain(m *testing.M) {
	pgDB, _, err := db.ResolveTestPostgres()
	if err != nil {
		log.Panicln(err)
	}
	if err := db.MigrateTestPostgres(pgDB, "file://../../static/migrations", "up"); err != nil {
		log.Panicln(err)
	}
	os.Exit(m.Run())
}

func commandJob(owner model.UserID) *model.Job {
	return &model.Job{JobID: model.NewJobID(), JobType: model.JobTypeCommand, OwnerID: &owner}
}

// TestRunKeyRaceReplaysTheWinner pins the race that the unique index decides: two submits of one
// key both find it free, and the one that commits second rolls back and replays the first.
func TestRunKeyRaceReplaysTheWinner(t *testing.T) {
	ctx := context.Background()
	owner := db.RequireMockUser(t, db.SingleDB()).ID
	req := &apiv1.LaunchCommandRequest{
		Submit: &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
	}
	winner, err := NewCommand(owner, req, nil)
	require.NoError(t, err)
	loser, err := NewCommand(owner, req, nil)
	require.NoError(t, err)

	replayed := func(result *apiv1.SubmitResult) *testResponse { return &testResponse{result: result} }
	authorize := func(context.Context, *model.Job) error { return nil }

	inserted := make(chan struct{})
	release := make(chan struct{})
	var winnerResp *testResponse
	var winnerErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		winnerResp, winnerErr = Run(ctx, winner, Handler[*testResponse]{
			AuthorizeReplay: authorize,
			Replayed:        replayed,
			Prepare:         func(context.Context) error { return nil },
			Commit: func(ctx context.Context, tx bun.Tx) error {
				if err := winner.InsertJobTx(ctx, tx, commandJob(owner)); err != nil {
					return err
				}
				close(inserted)
				<-release
				return nil
			},
			Dispatch: func(context.Context, model.JobID) error { return nil },
			Respond: func(_ context.Context, result *apiv1.SubmitResult) (*testResponse, error) {
				return &testResponse{result: result}, nil
			},
		})
	}()
	<-inserted

	loserWroteMore := false
	loserResp, err := Run(ctx, loser, Handler[*testResponse]{
		AuthorizeReplay: authorize,
		Replayed:        replayed,
		Prepare: func(context.Context) error {
			// The loser found the key free; let the winner commit.
			close(release)
			return nil
		},
		Commit: func(ctx context.Context, tx bun.Tx) error {
			if err := loser.InsertJobTx(ctx, tx, commandJob(owner)); err != nil {
				return err
			}
			loserWroteMore = true
			return nil
		},
		Dispatch: func(context.Context, model.JobID) error { return nil },
		Respond:  unexpected[*testResponse](t, "respond"),
	})
	<-done
	require.NoError(t, winnerErr)
	require.NoError(t, err)

	require.False(t, winnerResp.result.Replayed)
	require.True(t, loserResp.result.Replayed)
	require.Equal(t, winnerResp.result.JobId, loserResp.result.JobId)
	require.False(t, loserWroteMore, "the loser stops at its job row")

	n, err := db.Bun().NewSelect().Table("jobs").
		Where("owner_id = ? AND idempotency_key = ?", owner, req.Submit.IdempotencyKey).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

func TestInsertJobTxKeyInUseIsOnlyTheKeyIndex(t *testing.T) {
	ctx := context.Background()
	owner := db.RequireMockUser(t, db.SingleDB()).ID
	s, err := NewCommand(owner, &apiv1.LaunchCommandRequest{
		Submit: &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
	}, nil)
	require.NoError(t, err)

	job := commandJob(owner)
	require.NoError(t, s.InsertJobTx(ctx, db.Bun(), job))

	err = s.InsertJobTx(ctx, db.Bun(), commandJob(owner))
	require.ErrorIs(t, err, errKeyInUse)

	// A duplicate job ID is an error, not a key in use.
	other, err := NewCommand(owner, &apiv1.LaunchCommandRequest{
		Submit: &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
	}, nil)
	require.NoError(t, err)
	err = other.InsertJobTx(ctx, db.Bun(), &model.Job{
		JobID: job.JobID, JobType: model.JobTypeCommand, OwnerID: &owner,
	})
	require.Error(t, err)
	require.False(t, errors.Is(err, errKeyInUse))

	// Without submit options, the job row has no key or digest and the default admission.
	legacy, err := NewCommand(owner, &apiv1.LaunchCommandRequest{}, nil)
	require.NoError(t, err)
	legacyJob := commandJob(owner)
	require.NoError(t, legacy.InsertJobTx(ctx, db.Bun(), legacyJob))
	stored, err := db.JobByID(ctx, legacyJob.JobID)
	require.NoError(t, err)
	require.Nil(t, stored.IdempotencyKey)
	require.Nil(t, stored.RequestDigest)
	require.Equal(t, model.AdmissionQueue, stored.Admission)
}

// unknownCommitHandler is a handler that commits a command job and records what Run dispatched.
func unknownCommitHandler(
	t *testing.T, s *Submission, owner model.UserID, dispatched *[]model.JobID,
) Handler[*testResponse] {
	return Handler[*testResponse]{
		AuthorizeReplay: func(context.Context, *model.Job) error { return nil },
		Replayed:        func(result *apiv1.SubmitResult) *testResponse { return &testResponse{result: result} },
		Prepare:         func(context.Context) error { return nil },
		Commit: func(ctx context.Context, tx bun.Tx) error {
			return s.InsertJobTx(ctx, tx, commandJob(owner))
		},
		Dispatch: func(ctx context.Context, jobID model.JobID) error {
			require.NoError(t, ctx.Err(), "a dispatch runs detached from the request")
			*dispatched = append(*dispatched, jobID)
			return nil
		},
		Respond: func(_ context.Context, result *apiv1.SubmitResult) (*testResponse, error) {
			return &testResponse{result: result}, nil
		},
	}
}

func TestRunUnknownCommit(t *testing.T) {
	ctx := context.Background()
	owner := db.RequireMockUser(t, db.SingleDB()).ID
	lost := errors.New("connection lost during COMMIT")
	newSubmission := func(key string) *Submission {
		s, err := NewCommand(owner, &apiv1.LaunchCommandRequest{
			Submit: &apiv1.SubmitOptions{IdempotencyKey: key},
		}, nil)
		require.NoError(t, err)
		return s
	}
	deleteJob := func(ctx context.Context, jobID model.JobID) {
		_, err := db.Bun().NewDelete().Table("jobs").Where("job_id = ?", jobID).Exec(ctx)
		require.NoError(t, err)
	}

	t.Run("the job was committed", func(t *testing.T) {
		defer SetAfterCommitHook(func(context.Context) error { return lost })()
		s := newSubmission(uuid.NewString())
		var dispatched []model.JobID
		resp, err := Run(ctx, s, unknownCommitHandler(t, s, owner, &dispatched))
		require.NoError(t, err)
		require.False(t, resp.result.Replayed)
		require.Equal(t, s.jobID.String(), resp.result.JobId)
		require.Equal(t, []model.JobID{s.jobID}, dispatched, "the handler dispatches its job")
	})

	t.Run("the job was not committed", func(t *testing.T) {
		for _, key := range []string{"", uuid.NewString()} {
			s := newSubmission(key)
			remove := SetAfterCommitHook(func(ctx context.Context) error {
				// As if the commit had rolled back.
				deleteJob(ctx, s.jobID)
				return lost
			})
			var dispatched []model.JobID
			_, err := Run(ctx, s, unknownCommitHandler(t, s, owner, &dispatched))
			remove()
			require.Equal(t, codes.Unavailable, status.Code(err), "key %q", key)
			require.Empty(t, dispatched)
		}
	})

	t.Run("another submit of the key committed", func(t *testing.T) {
		key := uuid.NewString()
		s := newSubmission(key)
		winner := newSubmission(key)
		remove := SetAfterCommitHook(func(ctx context.Context) error {
			deleteJob(ctx, s.jobID)
			require.NoError(t, winner.InsertJobTx(ctx, db.Bun(), commandJob(owner)))
			return lost
		})
		var dispatched []model.JobID
		resp, err := Run(ctx, s, unknownCommitHandler(t, s, owner, &dispatched))
		remove()
		require.NoError(t, err)
		require.True(t, resp.result.Replayed)
		require.Equal(t, winner.jobID.String(), resp.result.JobId)
		require.Equal(t, []model.JobID{winner.jobID}, dispatched, "a replay dispatches its job")
	})

	t.Run("the client disconnects after the commit", func(t *testing.T) {
		reqCtx, cancel := context.WithCancel(ctx)
		defer SetAfterCommitHook(func(context.Context) error {
			cancel()
			return nil
		})()
		s := newSubmission(uuid.NewString())
		var dispatched []model.JobID
		_, err := Run(reqCtx, s, unknownCommitHandler(t, s, owner, &dispatched))
		require.NoError(t, err)
		require.Equal(t, []model.JobID{s.jobID}, dispatched)
	})
}
