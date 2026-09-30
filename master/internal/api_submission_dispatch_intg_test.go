//go:build integration
// +build integration

package internal

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/determined-ai/determined/master/internal/command"
	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/internal/experiment"
	"github.com/determined-ai/determined/master/internal/job/jobservice"
	"github.com/determined-ai/determined/master/internal/rm/rmevents"
	"github.com/determined-ai/determined/master/internal/sproto"
	"github.com/determined-ai/determined/master/internal/submission"
	"github.com/determined-ai/determined/master/internal/task"
	"github.com/determined-ai/determined/master/pkg/etc"
	"github.com/determined-ai/determined/master/pkg/logger"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/master/pkg/ptrs"
	"github.com/determined-ai/determined/proto/pkg/apiv1"
	"github.com/determined-ai/determined/proto/pkg/utilv1"
)

// allocateCounter counts the resource manager's Allocate calls for each allocation.
type allocateCounter struct {
	mu     sync.Mutex
	counts map[model.AllocationID]int
}

func newAllocateCounter() *allocateCounter {
	return &allocateCounter{counts: map[model.AllocationID]int{}}
}

func (c *allocateCounter) allocate(msg sproto.AllocateRequest) (*sproto.ResourcesSubscription, error) {
	c.mu.Lock()
	c.counts[msg.AllocationID]++
	c.mu.Unlock()
	return rmevents.Subscribe(msg.AllocationID), nil
}

func (c *allocateCounter) count(id model.AllocationID) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[id]
}

// errLostCommit is what a handler sees when the response to its COMMIT is lost.
var errLostCommit = errors.New("connection lost during COMMIT")

func genericTaskRequest(key string) *apiv1.CreateGenericTaskRequest {
	return &apiv1.CreateGenericTaskRequest{
		Config:    "entrypoint: [sh, -c, 'true']\nresources: {slots: 0}\n",
		ProjectId: ptrs.Ptr(int32(1)),
		Submit:    &apiv1.SubmitOptions{IdempotencyKey: key},
	}
}

func experimentRequest(t *testing.T, key string) *apiv1.CreateExperimentRequest {
	return &apiv1.CreateExperimentRequest{
		ModelDefinition: []*utilv1.File{{Content: []byte{1}}},
		Config:          minExpConfToYaml(t),
		Activate:        true,
		ProjectId:       1,
		Submit:          &apiv1.SubmitOptions{IdempotencyKey: key},
	}
}

// commitGenericTask commits a generic task as CreateGenericTask does, without starting it.
func commitGenericTask(
	ctx context.Context, t *testing.T, api *apiServer, owner model.User,
) (model.AllocationID, model.JobID) {
	req := genericTaskRequest(uuid.NewString())
	s, err := submission.NewGenericTask(owner.ID, req)
	require.NoError(t, err)
	prepared, err := api.prepareGenericTask(ctx, req)
	require.NoError(t, err)
	require.NoError(t, db.Bun().RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		return prepared.commitTx(ctx, tx, s, req, &owner)
	}))
	return prepared.allocationID, prepared.jobID
}

// commitExperiment commits an activated experiment as CreateExperiment does, without starting it.
func commitExperiment(
	ctx context.Context, t *testing.T, api *apiServer, owner model.User,
) (int, model.JobID) {
	req := experimentRequest(t, uuid.NewString())
	s, err := submission.NewExperiment(owner.ID, req, nil)
	require.NoError(t, err)
	p, err := api.prepareExperiment(ctx, req, &owner, nil, nil)
	require.NoError(t, err)
	require.NoError(t, db.Bun().RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		return p.commitTx(ctx, tx, s, true)
	}))
	return p.dbExp.ID, p.dbExp.JobID
}

// registeredExperiment returns the registered instance of an experiment, or nil.
func registeredExperiment(id int) experiment.Experiment {
	e, ok := experiment.ExperimentRegistry.Load(id)
	if !ok {
		return nil
	}
	return e
}

func TestSubmitLostCommitResponseStartsTheJob(t *testing.T) {
	allocs := newAllocateCounter()
	api, _, ctx := setupSubmissionTest(t, releasingRM(allocs.allocate))

	// The COMMIT succeeds, but the handler sees an error: it looks the job up, finds it, and
	// starts it without a restart.
	remove := submission.SetAfterCommitHook(func(context.Context) error { return errLostCommit })
	defer remove()
	cmdReq := &apiv1.LaunchCommandRequest{
		Config: commandConfig(t, "true"),
		Submit: &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
	}
	cmd, err := api.LaunchCommand(ctx, cmdReq)
	require.NoError(t, err)
	shell, err := api.LaunchShell(ctx, &apiv1.LaunchShellRequest{
		Config: commandConfig(t, "true"),
		Submit: &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
	})
	require.NoError(t, err)
	generic, err := api.CreateGenericTask(ctx, genericTaskRequest(uuid.NewString()))
	require.NoError(t, err)
	expReq := experimentRequest(t, uuid.NewString())
	exp, err := api.CreateExperiment(ctx, expReq)
	require.NoError(t, err)
	remove()

	require.False(t, cmd.Submission.Replayed)
	require.Equal(t, cmd.Command.JobId, cmd.Submission.JobId)
	cmdAttempt := model.AllocationID(cmd.Command.Id + ".1")
	require.True(t, task.IsRegistered(cmdAttempt))
	require.Equal(t, 1, allocs.count(cmdAttempt))

	// The shell is reported from the command that the dispatch started from command_state.
	require.False(t, shell.Submission.Replayed)
	require.Equal(t, shell.Shell.JobId, shell.Submission.JobId)
	require.NotEmpty(t, shell.Shell.PrivateKey)
	require.Equal(t, 1, allocs.count(model.AllocationID(shell.Shell.Id+".1")))

	require.False(t, generic.Submission.Replayed)
	genericAttempt := model.AllocationID(generic.TaskId + ".1")
	require.True(t, task.IsRegistered(genericAttempt))
	require.Equal(t, 1, allocs.count(genericAttempt))

	require.False(t, exp.Submission.Replayed)
	started := registeredExperiment(int(exp.Experiment.Id))
	require.NotNil(t, started)

	// A replay returns the job and starts nothing again.
	replayed, err := api.LaunchCommand(ctx, cmdReq)
	require.NoError(t, err)
	require.True(t, replayed.Submission.Replayed)
	require.Equal(t, cmd.Submission.JobId, replayed.Submission.JobId)
	require.Equal(t, 1, allocs.count(cmdAttempt))

	replayedExp, err := api.CreateExperiment(ctx, expReq)
	require.NoError(t, err)
	require.True(t, replayedExp.Submission.Replayed)
	require.Same(t, started, registeredExperiment(int(exp.Experiment.Id)))
}

func TestSubmitReplaysStartALostStart(t *testing.T) {
	allocs := newAllocateCounter()
	api, curUser, ctx := setupSubmissionTest(t, releasingRM(allocs.allocate))

	// hold stops each handler right after its commit, as if its start were lost, until released.
	hold := func() (committed <-chan struct{}, release func(), remove func()) {
		c, r := make(chan struct{}), make(chan struct{})
		var once sync.Once
		remove = submission.SetAfterCommitHook(func(context.Context) error {
			once.Do(func() { close(c) })
			<-r
			return errLostCommit
		})
		return c, func() { close(r) }, remove
	}
	replayConcurrently := func(n int, replay func() string) []string {
		jobs := make([]string, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				jobs[i] = replay()
			}(i)
		}
		wg.Wait()
		return jobs
	}

	t.Run("command", func(t *testing.T) {
		req := &apiv1.LaunchCommandRequest{
			Config: commandConfig(t, "true"),
			Submit: &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
		}
		committed, release, remove := hold()
		defer remove()
		var first *apiv1.LaunchCommandResponse
		var firstErr error
		done := make(chan struct{})
		go func() {
			defer close(done)
			first, firstErr = api.LaunchCommand(ctx, req)
		}()
		<-committed

		jobs := replayConcurrently(8, func() string {
			resp, err := api.LaunchCommand(ctx, req)
			require.NoError(t, err)
			require.True(t, resp.Submission.Replayed)
			return resp.Submission.JobId
		})
		jobID := jobs[0]
		for _, j := range jobs {
			require.Equal(t, jobID, j)
		}
		// The replays started the job while its handler was still stuck.
		attempt := model.AllocationID(taskOfJob(ctx, t, jobID) + ".1")
		require.True(t, task.IsRegistered(attempt))
		require.Equal(t, 1, allocs.count(attempt), "concurrent replays register one allocation")

		release()
		<-done
		require.NoError(t, firstErr)
		require.False(t, first.Submission.Replayed)
		require.Equal(t, jobID, first.Submission.JobId)
		require.Equal(t, 1, allocs.count(attempt), "the handler's own dispatch starts nothing again")
		// No start that found the job running ended its record.
		a := allocationOf(ctx, t, attempt)
		require.Equal(t, model.AllocationStatePending, *a.State)
		require.Nil(t, a.EndTime)
		requireSubmissionState(ctx, t, api, model.JobID(jobID),
			apiv1.SubmissionState_SUBMISSION_STATE_QUEUED)

		n, err := db.Bun().NewSelect().Table("jobs").
			Where("idempotency_key = ?", req.Submit.IdempotencyKey).Count(ctx)
		require.NoError(t, err)
		require.Equal(t, 1, n)
	})

	t.Run("experiment", func(t *testing.T) {
		req := experimentRequest(t, uuid.NewString())
		sessions := countSubmissionRows(ctx, t, curUser.ID).Sessions
		committed, release, remove := hold()
		defer remove()
		var first *apiv1.CreateExperimentResponse
		var firstErr error
		done := make(chan struct{})
		go func() {
			defer close(done)
			first, firstErr = api.CreateExperiment(ctx, req)
		}()
		<-committed

		jobs := replayConcurrently(4, func() string {
			resp, err := api.CreateExperiment(ctx, req)
			require.NoError(t, err)
			require.True(t, resp.Submission.Replayed)
			return resp.Submission.JobId
		})
		var expID int
		require.NoError(t, db.Bun().NewSelect().Table("experiments").Column("id").
			Where("job_id = ?", jobs[0]).Scan(ctx, &expID))
		started := registeredExperiment(expID)
		require.NotNil(t, started, "a replay started the experiment as a restore does")

		release()
		<-done
		require.NoError(t, firstErr)
		require.Equal(t, jobs[0], first.Submission.JobId)
		require.Same(t, started, registeredExperiment(expID), "one instance of the experiment runs")
		require.Equal(t, sessions+1, countSubmissionRows(ctx, t, curUser.ID).Sessions,
			"only the start that ran minted a session")
	})
}

func TestSubmitClientDisconnectAfterCommitStillStarts(t *testing.T) {
	allocs := newAllocateCounter()
	api, _, ctx := setupSubmissionTest(t, releasingRM(allocs.allocate))
	disconnect := func() (context.Context, func()) {
		reqCtx, cancel := context.WithCancel(ctx)
		remove := submission.SetAfterCommitHook(func(context.Context) error {
			cancel()
			return nil
		})
		t.Cleanup(remove)
		return reqCtx, remove
	}

	reqCtx, remove := disconnect()
	cmdKey := uuid.NewString()
	_, err := api.LaunchCommand(reqCtx, &apiv1.LaunchCommandRequest{
		Config: commandConfig(t, "true"),
		Submit: &apiv1.SubmitOptions{IdempotencyKey: cmdKey},
	})
	remove()
	require.NoError(t, err)
	cmdJob := jobByKey(ctx, t, cmdKey)
	cmdAttempt := model.AllocationID(taskOfJob(ctx, t, cmdJob.String()) + ".1")
	require.True(t, task.IsRegistered(cmdAttempt))
	require.Equal(t, 1, allocs.count(cmdAttempt))

	reqCtx, remove = disconnect()
	genericKey := uuid.NewString()
	_, err = api.CreateGenericTask(reqCtx, genericTaskRequest(genericKey))
	remove()
	require.NoError(t, err)
	genericAttempt := model.AllocationID(
		taskOfJob(ctx, t, jobByKey(ctx, t, genericKey).String()) + ".1")
	require.True(t, task.IsRegistered(genericAttempt))

	// The experiment starts from the create's plan; only the response, read after the start, may
	// fail with the request's context.
	reqCtx, remove = disconnect()
	expKey := uuid.NewString()
	_, _ = api.CreateExperiment(reqCtx, experimentRequest(t, expKey))
	remove()
	var expID int
	require.NoError(t, db.Bun().NewSelect().Table("experiments").Column("id").
		Where("job_id = ?", jobByKey(ctx, t, expKey)).Scan(ctx, &expID))
	require.NotNil(t, registeredExperiment(expID))
}

func jobByKey(ctx context.Context, t *testing.T, key string) model.JobID {
	var jobID model.JobID
	require.NoError(t, db.Bun().NewSelect().Table("jobs").Column("job_id").
		Where("idempotency_key = ?", key).Scan(ctx, &jobID))
	return jobID
}

func TestSweepStartsCommittedJobsThatNothingStarted(t *testing.T) {
	// The sweep reads every job in the database, so it runs on a database of its own.
	require.NoError(t, etc.SetRootPath("../static/srv"))
	pgDB, cleanup := db.MustResolveNewPostgresDatabase(t)
	defer cleanup()
	db.MustMigrateTestPostgres(t, pgDB, "file://../static/migrations")
	allocs := newAllocateCounter()
	mockRM := releasingRM(allocs.allocate)
	api, curUser, ctx := setupAPITest(t, pgDB, mockRM)
	// Experiments are registered by ID in this process, which also runs tests on the shared
	// database, so this database's experiments get IDs far above the shared database's and above
	// every registered experiment's.
	last := 1_000_000_000
	registeredExperiments.Range(func(id, _ any) bool {
		if id.(int) > last {
			last = id.(int)
		}
		return true
	})
	_, err := db.Bun().NewRaw(
		`SELECT setval(pg_get_serial_sequence('experiments', 'id'), ?)`, last).Exec(ctx)
	require.NoError(t, err)
	cs, err := command.NewService(api.m.db, api.m.rm)
	require.NoError(t, err)
	command.SetDefaultService(cs)
	jobservice.SetDefaultService(mockRM)

	// The handlers committed these jobs and never started them, and nobody replays them.
	_, cmdTask, _ := commitCommand(ctx, t, api, curUser, false)
	cmdAttempt := model.AllocationID(cmdTask + ".1")
	genericAttempt, _ := commitGenericTask(ctx, t, api, curUser)
	expID, _ := commitExperiment(ctx, t, api, curUser)

	requireUnstarted := func() {
		require.False(t, task.IsRegistered(cmdAttempt))
		require.Zero(t, allocs.count(cmdAttempt))
		require.False(t, task.IsRegistered(genericAttempt))
		require.Zero(t, allocs.count(genericAttempt))
		require.Nil(t, registeredExperiment(expID))
	}

	// A job younger than the interval may still be started by its handler.
	api.m.dispatchUnstarted(ctx, time.Now().Add(-submission.SweepInterval))
	requireUnstarted()

	api.m.dispatchUnstarted(ctx, time.Now().Add(time.Second))
	require.True(t, task.IsRegistered(cmdAttempt))
	require.Equal(t, 1, allocs.count(cmdAttempt))
	require.True(t, task.IsRegistered(genericAttempt))
	require.Equal(t, 1, allocs.count(genericAttempt))
	started := registeredExperiment(expID)
	require.NotNil(t, started)

	// The sweep never starts a registered job again.
	api.m.dispatchUnstarted(ctx, time.Now().Add(time.Second))
	require.Equal(t, 1, allocs.count(cmdAttempt))
	require.Equal(t, 1, allocs.count(genericAttempt))
	require.Same(t, started, registeredExperiment(expID))
}

func TestDispatchStartsOnlyUnstartedAttempts(t *testing.T) {
	allocs := newAllocateCounter()
	api, curUser, ctx := setupSubmissionTest(t, releasingRM(allocs.allocate))
	dispatchCommand := func(jobID model.JobID) {
		require.NoError(t, submission.Dispatch(ctx, jobID, command.DefaultCmdService.Dispatch))
	}

	t.Run("registered", func(t *testing.T) {
		resp, err := api.LaunchCommand(ctx, &apiv1.LaunchCommandRequest{
			Config: commandConfig(t, "true"),
			Submit: &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
		})
		require.NoError(t, err)
		attempt := model.AllocationID(resp.Command.Id + ".1")
		dispatchCommand(model.JobID(resp.Command.JobId))
		require.Equal(t, 1, allocs.count(attempt))

		generic, err := api.CreateGenericTask(ctx, genericTaskRequest(uuid.NewString()))
		require.NoError(t, err)
		genericAttempt := model.AllocationID(generic.TaskId + ".1")
		require.NoError(t, submission.Dispatch(ctx, model.JobID(generic.Submission.JobId),
			api.m.dispatchGenericTask))
		require.Equal(t, 1, allocs.count(genericAttempt))

		exp, err := api.CreateExperiment(ctx, experimentRequest(t, uuid.NewString()))
		require.NoError(t, err)
		started := registeredExperiment(int(exp.Experiment.Id))
		require.NoError(t, submission.Dispatch(ctx, model.JobID(exp.Experiment.JobId),
			api.m.dispatchExperiment))
		require.Same(t, started, registeredExperiment(int(exp.Experiment.Id)))
	})

	t.Run("ended while its task is open", func(t *testing.T) {
		// The allocation recorded its end, and its exit handler has not ended the task yet.
		_, taskID, jobID := commitCommand(ctx, t, api, curUser, false)
		attempt := model.AllocationID(taskID + ".1")
		_, err := db.Bun().NewUpdate().Table("allocations").
			Set("state = ?", model.AllocationStateTerminated).
			Set("end_time = now()").
			Where("allocation_id = ?", attempt).Exec(ctx)
		require.NoError(t, err)
		dispatchCommand(jobID)
		require.Zero(t, allocs.count(attempt))
		tk, err := db.TaskByID(ctx, taskID)
		require.NoError(t, err)
		require.Nil(t, tk.EndTime, "the task is its exit handler's to end")
	})

	t.Run("placed and not registered", func(t *testing.T) {
		_, taskID, jobID := commitCommand(ctx, t, api, curUser, false)
		attempt := model.AllocationID(taskID + ".1")
		_, err := db.Bun().NewUpdate().Table("allocations").
			Set("state = ?", model.AllocationStateAssigned).
			Where("allocation_id = ?", attempt).Exec(ctx)
		require.NoError(t, err)
		dispatchCommand(jobID)
		require.Zero(t, allocs.count(attempt), "only a restore continues a placed allocation")
		require.Equal(t, model.AllocationStateAssigned, *allocationOf(ctx, t, attempt).State)
	})

	t.Run("task ended", func(t *testing.T) {
		resp, err := api.LaunchCommand(ctx, &apiv1.LaunchCommandRequest{
			Config: commandConfig(t, "true"),
			Submit: &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
		})
		require.NoError(t, err)
		jobID := model.JobID(resp.Command.JobId)
		_, err = api.CancelSubmission(ctx, &apiv1.CancelSubmissionRequest{JobId: jobID.String()})
		require.NoError(t, err)
		requireSubmissionState(ctx, t, api, jobID, apiv1.SubmissionState_SUBMISSION_STATE_CANCELED)
		dispatchCommand(jobID)
		require.Equal(t, 1, allocs.count(model.AllocationID(resp.Command.Id+".1")))
	})

	t.Run("asked to stop before its start", func(t *testing.T) {
		_, taskID, jobID := commitCommand(ctx, t, api, curUser, false)
		_, err := api.CancelSubmission(ctx, &apiv1.CancelSubmissionRequest{JobId: jobID.String()})
		require.NoError(t, err)
		dispatchCommand(jobID)
		require.Zero(t, allocs.count(model.AllocationID(taskID+".1")),
			"a job asked to stop is ended, not started")
		s := getSubmission(ctx, t, api, jobID)
		require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_CANCELED, s.State)
		require.Equal(t, command.StopQueuedReason, s.ExitReason)

		genericAttempt, genericJob := commitGenericTask(ctx, t, api, curUser)
		_, err = api.CancelSubmission(ctx, &apiv1.CancelSubmissionRequest{JobId: genericJob.String()})
		require.NoError(t, err)
		require.NoError(t, submission.Dispatch(ctx, genericJob, api.m.dispatchGenericTask))
		require.Zero(t, allocs.count(genericAttempt))
		require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_CANCELED,
			getSubmission(ctx, t, api, genericJob).State)
	})

	t.Run("experiment that left the registry while it stops", func(t *testing.T) {
		// An experiment unregisters before it records its terminal state, so for a moment it
		// reads as live and unregistered; a dispatch then must not start it again.
		expID, jobID := commitExperiment(ctx, t, api, curUser)
		require.NoError(t, submission.Dispatch(ctx, jobID, api.m.dispatchExperiment))
		started := registeredExperiment(expID)
		require.NotNil(t, started)
		require.NoError(t, experiment.ExperimentRegistry.Delete(expID))
		defer func() { require.NoError(t, experiment.ExperimentRegistry.Add(expID, started)) }()

		require.NoError(t, submission.Dispatch(ctx, jobID, api.m.dispatchExperiment))
		require.Nil(t, registeredExperiment(expID), "the experiment was not started again")
	})
}

func TestAllocationServiceRefusesASecondRegistration(t *testing.T) {
	allocs := newAllocateCounter()
	api, _, ctx := setupSubmissionTest(t, releasingRM(allocs.allocate))
	resp, err := api.LaunchCommand(ctx, &apiv1.LaunchCommandRequest{
		Config: commandConfig(t, "true"),
		Submit: &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
	})
	require.NoError(t, err)
	attempt := model.AllocationID(resp.Command.Id + ".1")

	err = task.DefaultService.StartAllocation(logger.Context{}, sproto.AllocateRequest{
		AllocationID: attempt,
		TaskID:       model.TaskID(resp.Command.Id),
		JobID:        model.JobID(resp.Command.JobId),
		Persisted:    true,
	}, api.m.db, api.m.rm, nil, func(*task.AllocationExited) {})
	require.ErrorIs(t, err, task.ErrAllocationRegistered)
	require.Equal(t, 1, allocs.count(attempt), "the refused start asked for no resources")
	require.True(t, task.IsRegistered(attempt))
	a := allocationOf(ctx, t, attempt)
	require.Equal(t, model.AllocationStatePending, *a.State)
	require.Nil(t, a.EndTime)
}
