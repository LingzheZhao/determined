//go:build integration
// +build integration

package internal

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/grpc-ecosystem/grpc-gateway/runtime"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	authz2 "github.com/determined-ai/determined/master/internal/authz"
	"github.com/determined-ai/determined/master/internal/command"
	"github.com/determined-ai/determined/master/internal/config"
	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/internal/job/jobservice"
	"github.com/determined-ai/determined/master/internal/mocks"
	"github.com/determined-ai/determined/master/internal/rbac"
	"github.com/determined-ai/determined/master/internal/rm"
	"github.com/determined-ai/determined/master/internal/rm/rmevents"
	"github.com/determined-ai/determined/master/internal/rm/tasklist"
	"github.com/determined-ai/determined/master/internal/sproto"
	"github.com/determined-ai/determined/master/internal/submission"
	"github.com/determined-ai/determined/master/internal/task"
	"github.com/determined-ai/determined/master/internal/workspace"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/master/pkg/ptrs"
	"github.com/determined-ai/determined/master/pkg/schemas/expconf"
	"github.com/determined-ai/determined/proto/pkg/apiv1"
	"github.com/determined-ai/determined/proto/pkg/rbacv1"
	"github.com/determined-ai/determined/proto/pkg/utilv1"
	"github.com/determined-ai/determined/proto/pkg/workspacev1"
)

// submissionMockRM is MockRM with allocate deciding how the resource manager answers Allocate.
func submissionMockRM(
	allocate func(sproto.AllocateRequest) (*sproto.ResourcesSubscription, error),
) *mocks.ResourceManager {
	var mockRM mocks.ResourceManager
	mockRM.On("DeleteJob", mock.Anything).Return(func(sproto.DeleteJob) sproto.DeleteJobResponse {
		return sproto.EmptyDeleteJobResponse()
	}, nil)
	mockRM.On("ResolveResourcePool", mock.Anything, mock.Anything, mock.Anything).Return(
		func(name rm.ResourcePoolName, _, _ int) rm.ResourcePoolName { return name }, nil,
	)
	mockRM.On("ValidateResources", mock.Anything).Return(nil, nil)
	mockRM.On("TaskContainerDefaults", mock.Anything, mock.Anything).Return(
		func(_ rm.ResourcePoolName, def model.TaskContainerDefaultsConfig) model.TaskContainerDefaultsConfig {
			return def
		}, nil,
	)
	mockRM.On("SetGroupMaxSlots", mock.Anything).Return()
	mockRM.On("SetGroupWeight", mock.Anything).Return(nil)
	mockRM.On("SmallerValueIsHigherPriority").Return(true, nil)
	mockRM.On("SetGroupPriority", mock.Anything).Return(nil)
	mockRM.On("Allocate", mock.Anything).Return(allocate, nil)
	return &mockRM
}

func subscribe(msg sproto.AllocateRequest) (*sproto.ResourcesSubscription, error) {
	return rmevents.Subscribe(msg.AllocationID), nil
}

func setupSubmissionTest(
	t *testing.T, mockRM *mocks.ResourceManager,
) (*apiServer, model.User, context.Context) {
	if mockRM == nil {
		mockRM = submissionMockRM(subscribe)
	}
	api, curUser, ctx := setupAPITest(t, nil, mockRM)
	cs, err := command.NewService(api.m.db, api.m.rm)
	require.NoError(t, err)
	command.SetDefaultService(cs)
	jobservice.SetDefaultService(mockRM)
	return api, curUser, ctx
}

func commandConfig(t *testing.T, entrypoint string) *structpb.Struct {
	config, err := structpb.NewStruct(map[string]any{
		"entrypoint": []any{"sh", "-c", entrypoint},
		"resources":  map[string]any{"slots": 0},
	})
	require.NoError(t, err)
	return config
}

// submissionRows counts the rows a submission of the user would write, and the jobs registered
// for priority changes. Other tests' jobs may leave the registry at any time, so it is compared by
// the jobs that joined it.
type submissionRows struct {
	Jobs, Tasks, Allocations, CommandStates, Sessions, Experiments int
	registry                                                       map[model.JobID]func(int) error
}

// registeredSince returns the jobs that joined the priority change registry since rows were
// counted.
func (rows submissionRows) registeredSince() []model.JobID {
	var joined []model.JobID
	for jobID := range tasklist.GroupPriorityChangeRegistry.Snapshot() {
		if _, ok := rows.registry[jobID]; !ok {
			joined = append(joined, jobID)
		}
	}
	return joined
}

// requireSameRows checks that no rows were written and no job registered since rows were counted.
func requireSameRows(ctx context.Context, t *testing.T, userID model.UserID, rows submissionRows) {
	after := countSubmissionRows(ctx, t, userID)
	after.registry = rows.registry
	require.Equal(t, rows, after)
	require.Empty(t, rows.registeredSince())
}

func countSubmissionRows(ctx context.Context, t *testing.T, userID model.UserID) submissionRows {
	count := func(query string) int {
		var n int
		require.NoError(t, db.Bun().NewRaw(query, userID).Scan(ctx, &n))
		return n
	}
	return submissionRows{
		Jobs:  count(`SELECT count(*) FROM jobs WHERE owner_id = ?`),
		Tasks: count(`SELECT count(*) FROM tasks t JOIN jobs j ON t.job_id = j.job_id WHERE j.owner_id = ?`),
		Allocations: count(`SELECT count(*) FROM allocations a JOIN tasks t ON a.task_id = t.task_id
			JOIN jobs j ON t.job_id = j.job_id WHERE j.owner_id = ?`),
		CommandStates: count(`SELECT count(*) FROM command_state c JOIN tasks t ON c.task_id = t.task_id
			JOIN jobs j ON t.job_id = j.job_id WHERE j.owner_id = ?`),
		Sessions:    count(`SELECT count(*) FROM user_sessions WHERE user_id = ?`),
		Experiments: count(`SELECT count(*) FROM experiments WHERE owner_id = ?`),
		registry:    tasklist.GroupPriorityChangeRegistry.Snapshot(),
	}
}

func jobByID(ctx context.Context, t *testing.T, jobID string) *model.Job {
	j, err := db.JobByID(ctx, model.JobID(jobID))
	require.NoError(t, err)
	return j
}

func taskOfJob(ctx context.Context, t *testing.T, jobID string) model.TaskID {
	var taskID model.TaskID
	require.NoError(t, db.Bun().NewSelect().Table("tasks").Column("task_id").
		Where("job_id = ?", jobID).Scan(ctx, &taskID))
	return taskID
}

func TestLaunchCommandWithoutSubmitOptions(t *testing.T) {
	api, curUser, ctx := setupSubmissionTest(t, nil)

	resp, err := api.LaunchCommand(ctx, &apiv1.LaunchCommandRequest{Config: commandConfig(t, "true")})
	require.NoError(t, err)
	require.Nil(t, resp.Submission)
	require.NotEmpty(t, resp.Command.Id)
	require.NotNil(t, resp.Config)

	j := jobByID(ctx, t, resp.Command.JobId)
	require.Nil(t, j.IdempotencyKey)
	require.Nil(t, j.RequestDigest)
	require.Nil(t, j.CancelRequestedAt)
	require.Equal(t, model.AdmissionQueue, j.Admission)
	require.Equal(t, curUser.ID, *j.OwnerID)

	// The first allocation and command_state are committed with the task.
	var snapshot command.CommandSnapshot
	require.NoError(t, db.Bun().NewSelect().Model(&snapshot).
		Where("task_id = ?", resp.Command.Id).Scan(ctx))
	require.Equal(t, model.AllocationID(resp.Command.Id+".1"), snapshot.AllocationID)
	require.NotEmpty(t, snapshot.GenericCommandSpec.Base.UserSessionToken)
	alloc, err := db.AllocationByID(ctx, snapshot.AllocationID)
	require.NoError(t, err)
	require.Equal(t, model.AllocationStatePending, *alloc.State)
	require.Nil(t, alloc.EndTime)

	// A job row inserted outside the submission, as for a TensorBoard, gets the column default.
	tb, err := command.DefaultCmdService.LaunchGenericCommand(
		model.TaskTypeTensorboard, model.JobTypeTensorboard, mockGenericReq(t, api.m.db))
	require.NoError(t, err)
	j = jobByID(ctx, t, tb.ToV1Tensorboard().JobId)
	require.Equal(t, model.AdmissionQueue, j.Admission)
	require.Nil(t, j.IdempotencyKey)
}

func TestLaunchCommandReplay(t *testing.T) {
	api, curUser, ctx := setupSubmissionTest(t, nil)
	key := uuid.NewString()
	req := func(entrypoint string) *apiv1.LaunchCommandRequest {
		return &apiv1.LaunchCommandRequest{
			Config: commandConfig(t, entrypoint),
			Submit: &apiv1.SubmitOptions{IdempotencyKey: key},
		}
	}

	first, err := api.LaunchCommand(ctx, req("true"))
	require.NoError(t, err)
	require.NotNil(t, first.Submission)
	require.False(t, first.Submission.Replayed)
	require.Equal(t, first.Command.JobId, first.Submission.JobId)
	require.Equal(t, apiv1.AdmissionOutcome_ADMISSION_OUTCOME_QUEUED, first.Submission.Outcome)
	require.Len(t, first.Submission.RequestDigest, 64)

	j := jobByID(ctx, t, first.Submission.JobId)
	require.Equal(t, key, *j.IdempotencyKey)
	require.Equal(t, first.Submission.RequestDigest, *j.RequestDigest)
	rows := countSubmissionRows(ctx, t, curUser.ID)

	second, err := api.LaunchCommand(ctx, req("true"))
	require.NoError(t, err)
	require.True(t, second.Submission.Replayed)
	require.Equal(t, first.Submission.JobId, second.Submission.JobId)
	require.Equal(t, first.Submission.RequestDigest, second.Submission.RequestDigest)
	require.Zero(t, second.Command.Id, "a replay carries its data only in the submission")
	requireSameRows(ctx, t, curUser.ID, rows)

	// The same key with different content conflicts and names the job; nothing is written.
	_, err = api.LaunchCommand(ctx, req("false"))
	require.Equal(t, codes.AlreadyExists, status.Code(err))
	require.Contains(t, err.Error(), first.Submission.JobId)
	requireSameRows(ctx, t, curUser.ID, rows)

	// So does a request of another kind.
	_, err = api.LaunchShell(ctx, &apiv1.LaunchShellRequest{
		Config: commandConfig(t, "true"),
		Submit: &apiv1.SubmitOptions{IdempotencyKey: key, ExpectedDigest: first.Submission.RequestDigest},
	})
	require.Equal(t, codes.AlreadyExists, status.Code(err))
	requireSameRows(ctx, t, curUser.ID, rows)

	// Keys are scoped to their owner.
	other, _, otherCtx := setupSubmissionTest(t, nil)
	third, err := other.LaunchCommand(otherCtx, req("true"))
	require.NoError(t, err)
	require.False(t, third.Submission.Replayed)
	require.NotEqual(t, first.Submission.JobId, third.Submission.JobId)
}

func TestSubmitReplayRechecksReadAccess(t *testing.T) {
	api, _, ctx := setupSubmissionTest(t, nil)
	req := &apiv1.LaunchCommandRequest{
		Config: commandConfig(t, "true"),
		Submit: &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
	}
	first, err := api.LaunchCommand(ctx, req)
	require.NoError(t, err)

	// Access revoked since the submit is honored.
	authZNSC = &mocks.NSCAuthZ{}
	command.AuthZProvider.RegisterOverride("mock", authZNSC)
	nscAuthZ := authZNSC
	config.GetMasterConfig().Security.AuthZ = config.AuthZConfig{Type: "mock"}
	defer func() {
		config.GetMasterConfig().Security.AuthZ = config.AuthZConfig{Type: "basic"}
	}()
	nscAuthZ.On("CanGetNSC", mock.Anything, mock.Anything, mock.Anything).
		Return(authz2.PermissionDeniedError{}).Once()

	_, err = api.LaunchCommand(ctx, req)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Contains(t, err.Error(), first.Submission.JobId)
}

func TestSubmitExpectedDigest(t *testing.T) {
	api, curUser, ctx := setupSubmissionTest(t, nil)

	plan, err := api.LaunchCommand(ctx, &apiv1.LaunchCommandRequest{
		Config: commandConfig(t, "true"),
		Submit: &apiv1.SubmitOptions{DryRun: true},
	})
	require.NoError(t, err)
	digest := plan.Submission.RequestDigest

	// Content that changed after the plan fails with plan_changed, writes nothing, and leaves the
	// key free.
	key := uuid.NewString()
	rows := countSubmissionRows(ctx, t, curUser.ID)
	_, err = api.LaunchCommand(ctx, &apiv1.LaunchCommandRequest{
		Config: commandConfig(t, "false"),
		Submit: &apiv1.SubmitOptions{IdempotencyKey: key, ExpectedDigest: digest},
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "plan_changed")
	requireSameRows(ctx, t, curUser.ID, rows)

	launched, err := api.LaunchCommand(ctx, &apiv1.LaunchCommandRequest{
		Config: commandConfig(t, "true"),
		Submit: &apiv1.SubmitOptions{IdempotencyKey: key, ExpectedDigest: digest},
	})
	require.NoError(t, err)
	require.False(t, launched.Submission.Replayed)
	require.Equal(t, digest, launched.Submission.RequestDigest)

	// A retry bound to the plan replays the job even if the content changed since.
	retried, err := api.LaunchCommand(ctx, &apiv1.LaunchCommandRequest{
		Config: commandConfig(t, "false"),
		Submit: &apiv1.SubmitOptions{IdempotencyKey: key, ExpectedDigest: digest},
	})
	require.NoError(t, err)
	require.True(t, retried.Submission.Replayed)
	require.Equal(t, launched.Submission.JobId, retried.Submission.JobId)

	// A different expected digest conflicts.
	_, err = api.LaunchCommand(ctx, &apiv1.LaunchCommandRequest{
		Config: commandConfig(t, "true"),
		Submit: &apiv1.SubmitOptions{IdempotencyKey: key, ExpectedDigest: "other"},
	})
	require.Equal(t, codes.AlreadyExists, status.Code(err))
	require.Contains(t, err.Error(), launched.Submission.JobId)
}

func TestSubmitDryRunCreatesNothing(t *testing.T) {
	api, curUser, ctx := setupSubmissionTest(t, nil)
	key := uuid.NewString()
	dryRun := &apiv1.SubmitOptions{IdempotencyKey: key, DryRun: true}
	rows := countSubmissionRows(ctx, t, curUser.ID)

	var digests []string
	for i := 0; i < 2; i++ {
		cmd, err := api.LaunchCommand(ctx, &apiv1.LaunchCommandRequest{
			Config: commandConfig(t, "true"), Submit: dryRun,
		})
		require.NoError(t, err)
		require.Empty(t, cmd.Submission.JobId)
		require.False(t, cmd.Submission.Replayed)
		require.NotNil(t, cmd.Config)
		digests = append(digests, cmd.Submission.RequestDigest)

		shell, err := api.LaunchShell(ctx, &apiv1.LaunchShellRequest{
			Config: commandConfig(t, "true"), Submit: dryRun,
		})
		require.NoError(t, err)
		require.Empty(t, shell.Submission.JobId)
		require.Empty(t, shell.Shell.PrivateKey, "a dry run generates no SSH keys")
		digests = append(digests, shell.Submission.RequestDigest)

		generic, err := api.CreateGenericTask(ctx, &apiv1.CreateGenericTaskRequest{
			Config: "entrypoint: [sh, -c, 'true']\nresources: {slots: 0}\n", Submit: dryRun,
		})
		require.NoError(t, err)
		require.Empty(t, generic.TaskId)
		require.Empty(t, generic.Submission.JobId)
		digests = append(digests, generic.Submission.RequestDigest)

		exp, err := api.CreateExperiment(ctx, &apiv1.CreateExperimentRequest{
			ModelDefinition: []*utilv1.File{{Content: []byte{1}}},
			Config:          minExpConfToYaml(t),
			Activate:        true,
			ProjectId:       1,
			Submit:          dryRun,
		})
		require.NoError(t, err)
		require.Zero(t, exp.Experiment.Id)
		require.Empty(t, exp.Submission.JobId)
		require.NotNil(t, exp.Config)
		digests = append(digests, exp.Submission.RequestDigest)

		requireSameRows(ctx, t, curUser.ID, rows)
	}
	require.Equal(t, digests[:4], digests[4:], "repeated dry runs return the same digests")

	// The dry runs left the key free.
	resp, err := api.LaunchCommand(ctx, &apiv1.LaunchCommandRequest{
		Config: commandConfig(t, "true"),
		Submit: &apiv1.SubmitOptions{IdempotencyKey: key},
	})
	require.NoError(t, err)
	require.False(t, resp.Submission.Replayed)
	require.Equal(t, digests[0], resp.Submission.RequestDigest)
}

func TestCreateExperimentValidateOnly(t *testing.T) {
	api, curUser, ctx := setupSubmissionTest(t, nil)
	rows := countSubmissionRows(ctx, t, curUser.ID)

	resp, err := api.CreateExperiment(ctx, &apiv1.CreateExperimentRequest{
		ModelDefinition: []*utilv1.File{{Content: []byte{1}}},
		Config:          minExpConfToYaml(t),
		Activate:        true,
		ProjectId:       1,
		ValidateOnly:    true,
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Experiment)
	require.Zero(t, resp.Experiment.Id)
	require.Nil(t, resp.Config)
	require.Nil(t, resp.Submission)
	requireSameRows(ctx, t, curUser.ID, rows) // validate_only mints no session

	// A config to fix is InvalidArgument, never Internal, so clients can tell it from a master
	// failure.
	for _, config := range []string{"entrypoint: test\n", "searcher: [\n"} {
		_, err = api.CreateExperiment(ctx, &apiv1.CreateExperimentRequest{
			Config:       config,
			ProjectId:    1,
			ValidateOnly: true,
		})
		require.Equal(t, codes.InvalidArgument, status.Code(err), "%q: %v", config, err)
		require.ErrorContains(t, err, "invalid experiment configuration")
	}
	requireSameRows(ctx, t, curUser.ID, rows)
}

func TestSubmitConcurrentSameKey(t *testing.T) {
	api, curUser, ctx := setupSubmissionTest(t, nil)
	rows := countSubmissionRows(ctx, t, curUser.ID)
	key := uuid.NewString()

	const submitters = 8
	responses := make([]*apiv1.LaunchCommandResponse, submitters)
	errs := make([]error, submitters)
	var wg sync.WaitGroup
	for i := 0; i < submitters; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			responses[i], errs[i] = api.LaunchCommand(ctx, &apiv1.LaunchCommandRequest{
				Config: commandConfig(t, "true"),
				Submit: &apiv1.SubmitOptions{IdempotencyKey: key},
			})
		}(i)
	}
	wg.Wait()

	created := 0
	for i := 0; i < submitters; i++ {
		require.NoError(t, errs[i])
		require.Equal(t, responses[0].Submission.JobId, responses[i].Submission.JobId)
		if !responses[i].Submission.Replayed {
			created++
		}
	}
	require.Equal(t, 1, created, "exactly one submit creates the job")

	// Only the winner wrote rows, minted a session, and registered its job.
	after := countSubmissionRows(ctx, t, curUser.ID)
	after.registry = rows.registry
	require.Equal(t, submissionRows{
		Jobs:          rows.Jobs + 1,
		Tasks:         rows.Tasks + 1,
		Allocations:   rows.Allocations + 1,
		CommandStates: rows.CommandStates + 1,
		Sessions:      rows.Sessions + 1,
		registry:      rows.registry,
	}, after)
	require.Equal(t, []model.JobID{model.JobID(responses[0].Submission.JobId)}, rows.registeredSince())
}

func TestSubmitImmediateAdmission(t *testing.T) {
	api, curUser, ctx := setupSubmissionTest(t, nil)
	rows := countSubmissionRows(ctx, t, curUser.ID)
	immediate := &apiv1.SubmitOptions{Admission: apiv1.Admission_ADMISSION_IMMEDIATE}

	_, err := api.LaunchCommand(ctx, &apiv1.LaunchCommandRequest{
		Config: commandConfig(t, "true"), Submit: immediate,
	})
	require.Equal(t, codes.Unimplemented, status.Code(err))
	_, err = api.LaunchShell(ctx, &apiv1.LaunchShellRequest{
		Config: commandConfig(t, "true"), Submit: immediate,
	})
	require.Equal(t, codes.Unimplemented, status.Code(err))
	_, err = api.CreateGenericTask(ctx, &apiv1.CreateGenericTaskRequest{
		Config: "entrypoint: [sh, -c, 'true']\n", Submit: immediate,
	})
	require.Equal(t, codes.Unimplemented, status.Code(err))

	for _, dryRun := range []bool{false, true} {
		_, err = api.CreateExperiment(ctx, &apiv1.CreateExperimentRequest{
			ModelDefinition: []*utilv1.File{{Content: []byte{1}}},
			Config:          minExpConfToYaml(t),
			ProjectId:       1,
			Submit: &apiv1.SubmitOptions{
				Admission: apiv1.Admission_ADMISSION_IMMEDIATE, DryRun: dryRun,
			},
		})
		require.Equal(t, codes.InvalidArgument, status.Code(err))
	}
	requireSameRows(ctx, t, curUser.ID, rows)
}

// allocationSeen is what the resource manager saw committed when it was asked to allocate.
type allocationSeen struct {
	state        model.AllocationState
	snapshot     bool
	genericSpec  bool
	allocationID model.AllocationID
}

func recordingAllocate(seen chan<- allocationSeen) func(
	sproto.AllocateRequest,
) (*sproto.ResourcesSubscription, error) {
	return func(msg sproto.AllocateRequest) (*sproto.ResourcesSubscription, error) {
		ctx := context.Background()
		s := allocationSeen{allocationID: msg.AllocationID}
		if a, err := db.AllocationByID(ctx, msg.AllocationID); err == nil && a.State != nil {
			s.state = *a.State
		}
		var snapshot command.CommandSnapshot
		if err := db.Bun().NewSelect().Model(&snapshot).
			Where("allocation_id = ?", msg.AllocationID).Scan(ctx); err == nil {
			s.snapshot = true
			s.genericSpec = snapshot.GenericTaskSpec != nil
		}
		seen <- s
		return rmevents.Subscribe(msg.AllocationID), nil
	}
}

func TestSubmitCommitsBeforeStart(t *testing.T) {
	seen := make(chan allocationSeen, 4)
	api, _, ctx := setupSubmissionTest(t, submissionMockRM(recordingAllocate(seen)))

	cmd, err := api.LaunchCommand(ctx, &apiv1.LaunchCommandRequest{
		Config: commandConfig(t, "true"),
		Submit: &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
	})
	require.NoError(t, err)
	s := <-seen
	require.Equal(t, model.AllocationID(cmd.Command.Id+".1"), s.allocationID)
	require.Equal(t, model.AllocationStatePending, s.state)
	require.True(t, s.snapshot, "command_state is committed before the allocation starts")
	require.False(t, s.genericSpec)

	generic, err := api.CreateGenericTask(ctx, &apiv1.CreateGenericTaskRequest{
		Config: "entrypoint: [sh, -c, 'true']\nresources: {slots: 0}\n",
		Submit: &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
	})
	require.NoError(t, err)
	s = <-seen
	require.Equal(t, model.AllocationID(generic.TaskId+".1"), s.allocationID)
	require.Equal(t, model.AllocationStatePending, s.state)
	require.True(t, s.snapshot)
	require.True(t, s.genericSpec, "generic_task_spec is committed before the allocation starts")

	var workspaceRecords int
	workspaceRecords, err = db.Bun().NewSelect().Table("allocation_workspace_info").
		Where("allocation_id = ?", s.allocationID).Count(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, workspaceRecords)
}

func TestSubmitFailedStartLeavesTerminalRecord(t *testing.T) {
	api, curUser, ctx := setupSubmissionTest(t, submissionMockRM(
		func(sproto.AllocateRequest) (*sproto.ResourcesSubscription, error) {
			return nil, fmt.Errorf("the resource manager is down")
		},
	))
	rows := countSubmissionRows(ctx, t, curUser.ID)

	requireEnded := func(jobID string, taskState *model.TaskState) {
		taskID := taskOfJob(ctx, t, jobID)
		task, err := db.TaskByID(ctx, taskID)
		require.NoError(t, err)
		require.NotNil(t, task.EndTime)
		if taskState != nil {
			require.Equal(t, *taskState, *task.State)
		}

		alloc, err := db.AllocationByID(ctx, model.AllocationID(taskID+".1"))
		require.NoError(t, err)
		require.Equal(t, model.AllocationStateTerminated, *alloc.State)
		require.NotNil(t, alloc.StartTime)
		require.NotNil(t, alloc.EndTime)
		require.Equal(t, model.ExitClassInfrastructureFailed, *alloc.ExitClass)
		require.Contains(t, alloc.ExitDetail.Message, "the resource manager is down")

		_, registered := tasklist.GroupPriorityChangeRegistry.Load(model.JobID(jobID))
		require.False(t, registered)
	}

	key := uuid.NewString()
	req := &apiv1.LaunchCommandRequest{
		Config: commandConfig(t, "true"),
		Submit: &apiv1.SubmitOptions{IdempotencyKey: key},
	}
	_, err := api.LaunchCommand(ctx, req)
	require.ErrorContains(t, err, "the resource manager is down")

	var jobID string
	require.NoError(t, db.Bun().NewSelect().Table("jobs").Column("job_id").
		Where("owner_id = ? AND idempotency_key = ?", curUser.ID, key).Scan(ctx, &jobID))
	requireEnded(jobID, nil)

	// A retry replays the ended job instead of creating another.
	replayed, err := api.LaunchCommand(ctx, req)
	require.NoError(t, err)
	require.True(t, replayed.Submission.Replayed)
	require.Equal(t, jobID, replayed.Submission.JobId)

	genericKey := uuid.NewString()
	_, err = api.CreateGenericTask(ctx, &apiv1.CreateGenericTaskRequest{
		Config: "entrypoint: [sh, -c, 'true']\nresources: {slots: 0}\n",
		Submit: &apiv1.SubmitOptions{IdempotencyKey: genericKey},
	})
	require.ErrorContains(t, err, "the resource manager is down")
	require.NoError(t, db.Bun().NewSelect().Table("jobs").Column("job_id").
		Where("owner_id = ? AND idempotency_key = ?", curUser.ID, genericKey).Scan(ctx, &jobID))
	requireEnded(jobID, ptrs.Ptr(model.TaskStateError))

	// Neither failed start kept its user session or registry entry.
	require.Equal(t, rows.Sessions, countSubmissionRows(ctx, t, curUser.ID).Sessions)
	require.Empty(t, rows.registeredSince())
}

func TestGenericTaskSubmitReplay(t *testing.T) {
	api, _, ctx := setupSubmissionTest(t, nil)
	req := &apiv1.CreateGenericTaskRequest{
		Config:    "entrypoint: [sh, -c, 'true']\nresources: {slots: 0}\n",
		ProjectId: ptrs.Ptr(int32(1)),
		Submit:    &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
	}

	first, err := api.CreateGenericTask(ctx, req)
	require.NoError(t, err)
	require.NotEmpty(t, first.TaskId)
	require.False(t, first.Submission.Replayed)
	require.Equal(t, model.TaskID(first.TaskId), taskOfJob(ctx, t, first.Submission.JobId))

	second, err := api.CreateGenericTask(ctx, req)
	require.NoError(t, err)
	require.True(t, second.Submission.Replayed)
	require.Equal(t, first.Submission.JobId, second.Submission.JobId)
	require.Empty(t, second.TaskId)
}

func TestCreateExperimentSubmit(t *testing.T) {
	api, curUser, ctx := setupSubmissionTest(t, nil)
	req := func(activate bool) *apiv1.CreateExperimentRequest {
		return &apiv1.CreateExperimentRequest{
			ModelDefinition: []*utilv1.File{{Content: []byte{1}}},
			Config:          minExpConfToYaml(t),
			Activate:        activate,
			ProjectId:       1,
			Submit:          &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
		}
	}

	active := req(true)
	first, err := api.CreateExperiment(ctx, active)
	require.NoError(t, err)
	require.False(t, first.Submission.Replayed)
	require.Equal(t, first.Experiment.JobId, first.Submission.JobId)
	exp, err := db.ExperimentByID(ctx, int(first.Experiment.Id))
	require.NoError(t, err)
	require.Equal(t, model.ActiveState, exp.State, "an activated experiment is committed ACTIVE")

	rows := countSubmissionRows(ctx, t, curUser.ID)
	second, err := api.CreateExperiment(ctx, active)
	require.NoError(t, err)
	require.True(t, second.Submission.Replayed)
	require.Equal(t, first.Submission.JobId, second.Submission.JobId)
	require.Zero(t, second.Experiment.Id)
	requireSameRows(ctx, t, curUser.ID, rows)

	paused, err := api.CreateExperiment(ctx, req(false))
	require.NoError(t, err)
	exp, err = db.ExperimentByID(ctx, int(paused.Experiment.Id))
	require.NoError(t, err)
	require.Equal(t, model.PausedState, exp.State)

	// Keys are only for managed experiments.
	_, err = api.CreateExperiment(ctx, &apiv1.CreateExperimentRequest{
		Config:    minExpConfToYaml(t),
		ProjectId: 1,
		Unmanaged: ptrs.Ptr(true),
		Submit:    &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = api.PutExperiment(ctx, &apiv1.PutExperimentRequest{
		CreateExperimentRequest: &apiv1.CreateExperimentRequest{
			Config:    minExpConfToYaml(t),
			ProjectId: 1,
			Unmanaged: ptrs.Ptr(true),
			Submit:    &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
		},
		ExternalExperimentId: uuid.NewString(),
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestCreateExperimentFailedStartLeavesTerminalRecord(t *testing.T) {
	mockRM := submissionMockRM(subscribe)
	// A failing weight update fails the experiment's start after it is committed.
	mockRM.ExpectedCalls = mocksWithout(mockRM.ExpectedCalls, "SetGroupWeight")
	mockRM.On("SetGroupWeight", mock.Anything).Return(fmt.Errorf("weights are down"))
	api, curUser, ctx := setupSubmissionTest(t, mockRM)
	rows := countSubmissionRows(ctx, t, curUser.ID)

	req := &apiv1.CreateExperimentRequest{
		ModelDefinition: []*utilv1.File{{Content: []byte{1}}},
		Config:          minExpConfToYaml(t),
		Activate:        true,
		ProjectId:       1,
		Submit:          &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
	}
	_, err := api.CreateExperiment(ctx, req)
	require.ErrorContains(t, err, "weights are down")

	replayed, err := api.CreateExperiment(ctx, req)
	require.NoError(t, err)
	require.True(t, replayed.Submission.Replayed)

	var exp model.Experiment
	require.NoError(t, db.Bun().NewSelect().Model(&exp).ExcludeColumn("username").
		Where("job_id = ?", replayed.Submission.JobId).Scan(ctx))
	require.Equal(t, model.ErrorState, exp.State)
	require.NotNil(t, exp.EndTime)

	require.Equal(t, rows.Sessions, countSubmissionRows(ctx, t, curUser.ID).Sessions)
	require.Empty(t, rows.registeredSince())
}

func mocksWithout(calls []*mock.Call, method string) []*mock.Call {
	var kept []*mock.Call
	for _, c := range calls {
		if c.Method != method {
			kept = append(kept, c)
		}
	}
	return kept
}

// nolint: exhaustruct
func TestCreateExperimentDryRunCheckpointStorage(t *testing.T) {
	api, curUser, ctx := setupSubmissionTest(t, nil)
	api.m.config.CheckpointStorage = expconf.CheckpointStorageConfig{}

	// A job sets only a relative storage path; the host path comes from the workspace or master.
	conf := `
entrypoint: test
checkpoint_storage:
  type: shared_fs
  storage_path: runs/checkpoints
searcher:
  metric: loss
  name: single
resources:
  resource_pool: kubernetes`
	req := func(projectID int) *apiv1.CreateExperimentRequest {
		return &apiv1.CreateExperimentRequest{
			ModelDefinition: []*utilv1.File{{Content: []byte{1}}},
			Config:          conf,
			ProjectId:       int32(projectID),
			Submit:          &apiv1.SubmitOptions{DryRun: true},
		}
	}

	// Without a shared_fs default, the dry run fails the completeness check and creates nothing.
	rows := countSubmissionRows(ctx, t, curUser.ID)
	_, err := api.CreateExperiment(ctx, req(1))
	require.ErrorContains(t, err, "host_path")
	requireSameRows(ctx, t, curUser.ID, rows)

	// A submit fails the same check, creates nothing, and leaves its key free.
	key := uuid.NewString()
	submit := func(projectID int) *apiv1.CreateExperimentRequest {
		r := req(projectID)
		r.Submit = &apiv1.SubmitOptions{IdempotencyKey: key}
		return r
	}
	_, err = api.CreateExperiment(ctx, submit(1))
	require.ErrorContains(t, err, "host_path")
	requireSameRows(ctx, t, curUser.ID, rows)

	// With a workspace default, the host path is inherited.
	workspaceID, projectID := createProjectAndWorkspace(ctx, t, api)
	_, err = api.PatchWorkspace(ctx, &apiv1.PatchWorkspaceRequest{
		Id: int32(workspaceID),
		Workspace: &workspacev1.PatchWorkspace{
			CheckpointStorageConfig: newProtoStruct(t, map[string]any{
				"type":      "shared_fs",
				"host_path": "/mnt/checkpoints",
			}),
		},
	})
	require.NoError(t, err)
	_, err = workspace.WorkspaceByProjectID(ctx, projectID)
	require.NoError(t, err)

	rows = countSubmissionRows(ctx, t, curUser.ID)
	resp, err := api.CreateExperiment(ctx, req(projectID))
	require.NoError(t, err)
	storage := resp.Config.AsMap()["checkpoint_storage"].(map[string]any)
	require.Equal(t, "/mnt/checkpoints", storage["host_path"])
	require.Equal(t, "runs/checkpoints", storage["storage_path"])
	requireSameRows(ctx, t, curUser.ID, rows)

	// The committed experiment stores the inherited host path under the same key.
	created, err := api.CreateExperiment(ctx, submit(projectID))
	require.NoError(t, err)
	require.False(t, created.Submission.Replayed)
	stored, err := api.m.db.ActiveExperimentConfig(int(created.Experiment.Id))
	require.NoError(t, err)
	fs := stored.CheckpointStorage().RawSharedFSConfig
	require.NotNil(t, fs)
	require.Equal(t, "/mnt/checkpoints", fs.HostPath())
	require.Equal(t, "runs/checkpoints", *fs.StoragePath())
	_, err = api.KillExperiment(ctx, &apiv1.KillExperimentRequest{Id: created.Experiment.Id})
	require.NoError(t, err)
}

func TestSubmitTemplateBindsItsContent(t *testing.T) {
	api, curUser, ctx := setupSubmissionTest(t, nil)
	name := uuid.NewString()
	setTemplate := func(config string) {
		_, err := db.Bun().NewRaw(`INSERT INTO templates (name, config, workspace_id)
			VALUES (?, ?::jsonb, ?) ON CONFLICT (name) DO UPDATE SET config = EXCLUDED.config`,
			name, config, model.DefaultWorkspaceID).Exec(ctx)
		require.NoError(t, err)
	}
	req := func(key, expected string) *apiv1.LaunchCommandRequest {
		return &apiv1.LaunchCommandRequest{
			Config:       commandConfig(t, "true"),
			TemplateName: name,
			Submit:       &apiv1.SubmitOptions{IdempotencyKey: key, ExpectedDigest: expected},
		}
	}
	plan := func() string {
		resp, err := api.LaunchCommand(ctx, &apiv1.LaunchCommandRequest{
			Config:       commandConfig(t, "true"),
			TemplateName: name,
			Submit:       &apiv1.SubmitOptions{DryRun: true},
		})
		require.NoError(t, err)
		return resp.Submission.RequestDigest
	}

	setTemplate(`{"description": "planned"}`)
	planned := plan()
	key := uuid.NewString()
	launched, err := api.LaunchCommand(ctx, req(key, planned))
	require.NoError(t, err)
	require.Equal(t, planned, launched.Submission.RequestDigest)
	require.Equal(t, "planned", launched.Command.Description, "the job runs the planned template")

	// An identical template replays.
	replayed, err := api.LaunchCommand(ctx, req(key, ""))
	require.NoError(t, err)
	require.True(t, replayed.Submission.Replayed)

	// A template that changed after the plan fails the plan check and writes nothing.
	setTemplate(`{"description": "changed"}`)
	rows := countSubmissionRows(ctx, t, curUser.ID)
	_, err = api.LaunchCommand(ctx, req(uuid.NewString(), planned))
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "plan_changed")
	requireSameRows(ctx, t, curUser.ID, rows)

	// The launch bound to the plan still replays, and an unbound retry conflicts.
	replayed, err = api.LaunchCommand(ctx, req(key, planned))
	require.NoError(t, err)
	require.True(t, replayed.Submission.Replayed)
	_, err = api.LaunchCommand(ctx, req(key, ""))
	require.Equal(t, codes.AlreadyExists, status.Code(err))

	// A change to master defaults is not bound: it applies to the launch without plan_changed.
	replanned := plan()
	require.NotEqual(t, planned, replanned)
	api.m.config.TaskContainerDefaults.BindMounts = model.BindMountsConfig{{
		HostPath: "/mnt/shared", ContainerPath: "/shared",
	}}
	defer func() { api.m.config.TaskContainerDefaults.BindMounts = nil }()
	require.Equal(t, replanned, plan())
	relaunched, err := api.LaunchCommand(ctx, req(uuid.NewString(), replanned))
	require.NoError(t, err)
	require.Equal(t, "changed", relaunched.Command.Description)
	mounts := relaunched.Config.AsMap()["bind_mounts"].([]any)
	require.Equal(t, "/shared", mounts[0].(map[string]any)["container_path"])

	// An experiment's template is bound the same way.
	setTemplate(`{"description": "planned"}`)
	expReq := func(submit *apiv1.SubmitOptions) *apiv1.CreateExperimentRequest {
		return &apiv1.CreateExperimentRequest{
			ModelDefinition: []*utilv1.File{{Content: []byte{1}}},
			Config:          minExpConfToYaml(t),
			ProjectId:       1,
			Template:        &name,
			Submit:          submit,
		}
	}
	expPlan, err := api.CreateExperiment(ctx, expReq(&apiv1.SubmitOptions{DryRun: true}))
	require.NoError(t, err)
	setTemplate(`{"description": "changed"}`)
	_, err = api.CreateExperiment(ctx, expReq(&apiv1.SubmitOptions{
		IdempotencyKey: uuid.NewString(), ExpectedDigest: expPlan.Submission.RequestDigest,
	}))
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "plan_changed")
}

func TestGetMasterSubmissionProtocol(t *testing.T) {
	api, _, ctx := setupSubmissionTest(t, nil)
	resp, err := api.GetMaster(ctx, &apiv1.GetMasterRequest{})
	require.NoError(t, err)
	require.Equal(t, int32(1), resp.SubmissionProtocol)
}

func TestSubmitConcurrentSameKeyGenericTask(t *testing.T) {
	api, curUser, ctx := setupSubmissionTest(t, nil)
	rows := countSubmissionRows(ctx, t, curUser.ID)
	key := uuid.NewString()

	const submitters = 8
	responses := make([]*apiv1.CreateGenericTaskResponse, submitters)
	errs := make([]error, submitters)
	var wg sync.WaitGroup
	for i := 0; i < submitters; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			responses[i], errs[i] = api.CreateGenericTask(ctx, genericTaskRequest(key))
		}(i)
	}
	wg.Wait()

	created := 0
	for i := 0; i < submitters; i++ {
		require.NoError(t, errs[i])
		require.Equal(t, responses[0].Submission.JobId, responses[i].Submission.JobId)
		if !responses[i].Submission.Replayed {
			created++
		}
	}
	require.Equal(t, 1, created, "exactly one submit creates the job")

	// Only the winner wrote rows, minted a session, and registered its job.
	after := countSubmissionRows(ctx, t, curUser.ID)
	after.registry = rows.registry
	require.Equal(t, submissionRows{
		Jobs:          rows.Jobs + 1,
		Tasks:         rows.Tasks + 1,
		Allocations:   rows.Allocations + 1,
		CommandStates: rows.CommandStates + 1,
		Sessions:      rows.Sessions + 1,
		registry:      rows.registry,
	}, after)
	require.Equal(t, []model.JobID{model.JobID(responses[0].Submission.JobId)}, rows.registeredSince())
}

// nolint: exhaustruct
func TestSubmitDryRunEffectiveConfig(t *testing.T) {
	api, curUser, ctx := setupSubmissionTest(t, nil)
	old := api.m.config.CheckpointStorage
	t.Cleanup(func() { api.m.config.CheckpointStorage = old })
	api.m.config.CheckpointStorage = expconf.CheckpointStorageConfig{
		RawS3Config: &expconf.S3Config{
			RawBucket:    ptrs.Ptr("masterbucket"),
			RawAccessKey: ptrs.Ptr("masteraccess"),
			RawSecretKey: ptrs.Ptr("mastersecret"),
		},
	}
	rows := countSubmissionRows(ctx, t, curUser.ID)

	// An experiment's checkpoint storage comes from the master, whose secrets a dry run hides.
	exp, err := api.CreateExperiment(ctx, &apiv1.CreateExperimentRequest{
		ModelDefinition: []*utilv1.File{{Content: []byte{1}}},
		Config: `
entrypoint: test
searcher:
  metric: loss
  name: single
resources:
  resource_pool: kubernetes`,
		ProjectId: 1,
		Submit:    &apiv1.SubmitOptions{DryRun: true},
	})
	require.NoError(t, err)
	for _, config := range []*structpb.Struct{exp.Submission.EffectiveConfig, exp.Config} {
		storage := config.AsMap()["checkpoint_storage"].(map[string]any)
		require.Equal(t, "masterbucket", storage["bucket"])
		require.Equal(t, "********", storage["access_key"])
		require.Equal(t, "********", storage["secret_key"])
	}
	require.Equal(t, "mastersecret", *api.m.config.CheckpointStorage.RawS3Config.RawSecretKey)

	// Commands, shells, and generic tasks report their config too.
	cmd, err := api.LaunchCommand(ctx, &apiv1.LaunchCommandRequest{
		Config: commandConfig(t, "true"),
		Submit: &apiv1.SubmitOptions{DryRun: true},
	})
	require.NoError(t, err)
	require.Equal(t, cmd.Config.AsMap(), cmd.Submission.EffectiveConfig.AsMap())
	shell, err := api.LaunchShell(ctx, &apiv1.LaunchShellRequest{
		Config: commandConfig(t, "true"),
		Submit: &apiv1.SubmitOptions{DryRun: true},
	})
	require.NoError(t, err)
	require.Equal(t, shell.Config.AsMap(), shell.Submission.EffectiveConfig.AsMap())
	req := genericTaskRequest("")
	req.Submit.DryRun = true
	generic, err := api.CreateGenericTask(ctx, req)
	require.NoError(t, err)
	require.Equal(t, []any{"sh", "-c", "true"},
		generic.Submission.EffectiveConfig.AsMap()["entrypoint"])
	requireSameRows(ctx, t, curUser.ID, rows)

	// A submit that is not a dry run carries none.
	launched, err := api.LaunchCommand(ctx, &apiv1.LaunchCommandRequest{
		Config: commandConfig(t, "true"),
		Submit: &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
	})
	require.NoError(t, err)
	require.Nil(t, launched.Submission.EffectiveConfig)
}

func TestSubmitReplayResponsesKeepRequiredFields(t *testing.T) {
	api, _, ctx := setupSubmissionTest(t, releasingRM(subscribe))
	// As the gateway marshals responses.
	marshaler := &runtime.JSONPb{EmitDefaults: true}
	requireReplayed := func(resp any, field string) {
		b, err := marshaler.Marshal(resp)
		require.NoError(t, err)
		var obj map[string]any
		require.NoError(t, json.Unmarshal(b, &obj))
		require.NotNil(t, obj[field], "a replay leaves %s empty, not null", field)
		require.Equal(t, true, obj["submission"].(map[string]any)["replayed"])
	}

	cmdReq := &apiv1.LaunchCommandRequest{
		Config: commandConfig(t, "true"),
		Submit: &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
	}
	shellReq := &apiv1.LaunchShellRequest{
		Config: commandConfig(t, "true"),
		Submit: &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
	}
	genericReq := genericTaskRequest(uuid.NewString())
	expReq := experimentRequest(t, uuid.NewString())
	for i := 0; i < 2; i++ {
		cmd, err := api.LaunchCommand(ctx, cmdReq)
		require.NoError(t, err)
		shell, err := api.LaunchShell(ctx, shellReq)
		require.NoError(t, err)
		generic, err := api.CreateGenericTask(ctx, genericReq)
		require.NoError(t, err)
		exp, err := api.CreateExperiment(ctx, expReq)
		require.NoError(t, err)
		if i == 0 {
			continue
		}
		requireReplayed(cmd, "command")
		requireReplayed(shell, "shell")
		requireReplayed(generic, "taskId")
		requireReplayed(exp, "experiment")
		_, err = api.KillExperiment(ctx, &apiv1.KillExperimentRequest{
			Id: int32(jobExperimentID(ctx, t, exp.Submission.JobId)),
		})
		require.NoError(t, err)
	}
}

// jobExperimentID returns the ID of the experiment of a job.
func jobExperimentID(ctx context.Context, t *testing.T, jobID string) int {
	var id int
	require.NoError(t, db.Bun().NewSelect().Table("experiments").Column("id").
		Where("job_id = ?", jobID).Scan(ctx, &id))
	return id
}

func TestListSubmissionsPageTokenNamesNoJob(t *testing.T) {
	api, _, ctx := setupSubmissionTest(t, nil)
	owner, _ := addUser(t, api, false)
	_, readerCtx := addUser(t, api, false)

	// More deleted experiments than a page examines, which only their owner and admins may read.
	var jobIDs []string
	require.NoError(t, db.Bun().NewRaw(`INSERT INTO jobs (job_id, job_type, owner_id)
		SELECT gen_random_uuid()::text, ?, ? FROM generate_series(1, 1001) RETURNING job_id`,
		model.JobTypeExperiment, owner.ID).Scan(ctx, &jobIDs))
	require.Len(t, jobIDs, 1001)
	t.Cleanup(func() {
		_, err := db.Bun().NewDelete().Table("jobs").Where("job_id IN (?)", bun.In(jobIDs)).
			Exec(context.Background())
		require.NoError(t, err)
	})

	resp, err := api.ListSubmissions(readerCtx, &apiv1.ListSubmissionsRequest{
		OwnerId: ptrs.Ptr(int32(owner.ID)), Limit: 1,
	})
	require.NoError(t, err)
	require.Empty(t, resp.Submissions)
	require.NotEmpty(t, resp.NextPageToken, "the page examined its bound of jobs")
	sealed, err := base64.RawURLEncoding.DecodeString(resp.NextPageToken)
	require.NoError(t, err)
	for _, jobID := range jobIDs {
		require.NotContains(t, resp.NextPageToken, jobID)
		require.NotContains(t, string(sealed), jobID)
	}

	// The token still pages on, to the one job left.
	resp, err = api.ListSubmissions(readerCtx, &apiv1.ListSubmissionsRequest{
		OwnerId: ptrs.Ptr(int32(owner.ID)), Limit: 1, PageToken: resp.NextPageToken,
	})
	require.NoError(t, err)
	require.Empty(t, resp.Submissions)
	require.Empty(t, resp.NextPageToken)
}

// nolint: exhaustruct
func TestSubmissionAdminUnderRBAC(t *testing.T) {
	api, _, ctx := setupSubmissionTest(t, releasingRM(subscribe))

	// Set up under basic authorization: an owner's command in the default workspace, and its
	// deleted experiment.
	owner, ownerCtx := addUser(t, api, false)
	cmd, err := api.LaunchCommand(ownerCtx, &apiv1.LaunchCommandRequest{
		Config: commandConfig(t, "true"),
		Submit: &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
	})
	require.NoError(t, err)
	cmdJob := model.JobID(cmd.Submission.JobId)
	t.Cleanup(func() {
		_ = task.DefaultService.Signal(
			model.AllocationID(cmd.Command.Id+".1"), task.KillAllocation, "test cleanup")
	})
	expReq := experimentRequest(t, uuid.NewString())
	expReq.Activate = false
	exp, err := api.CreateExperiment(ownerCtx, expReq)
	require.NoError(t, err)
	deletedJob := model.JobID(exp.Submission.JobId)
	require.NoError(t, api.m.db.DeleteExperiments(ctx, []int{int(exp.Experiment.Id)}))

	// A user with the admin flag who is only a viewer of the workspace, a cluster admin without
	// the flag, and a viewer.
	flagged, flaggedCtx := addUser(t, api, true)
	clusterAdmin, clusterAdminCtx := addUser(t, api, false)
	viewer, viewerCtx := addUser(t, api, false)
	assign := func(user model.User, roleID int32, workspaceID *int32) {
		require.NoError(t, rbac.AddRoleAssignments(ctx, nil, []*rbacv1.UserRoleAssignment{{
			UserId: int32(user.ID),
			RoleAssignment: &rbacv1.RoleAssignment{
				Role:             &rbacv1.Role{RoleId: roleID},
				ScopeWorkspaceId: workspaceID,
			},
		}}))
	}
	const clusterAdminRole, viewerRole = 1, 4
	defaultWorkspace := ptrs.Ptr(int32(model.DefaultWorkspaceID))
	assign(flagged, viewerRole, defaultWorkspace)
	assign(clusterAdmin, clusterAdminRole, nil)
	assign(viewer, viewerRole, defaultWorkspace)

	config.GetMasterConfig().Security.AuthZ = config.AuthZConfig{Type: "rbac"}
	t.Cleanup(func() { config.GetMasterConfig().Security.AuthZ = config.AuthZConfig{Type: "basic"} })

	listed := func(ctx context.Context, jobID model.JobID) bool {
		resp, err := api.ListSubmissions(ctx, &apiv1.ListSubmissionsRequest{
			OwnerId: ptrs.Ptr(int32(owner.ID)), Limit: submission.MaxListLimit,
		})
		require.NoError(t, err)
		for _, s := range resp.Submissions {
			if s.JobId == jobID.String() {
				return true
			}
		}
		return false
	}

	// The admin flag counts for nothing: the deleted experiment is not found or listed, and the
	// command's key and digest are hidden.
	for _, readerCtx := range []context.Context{flaggedCtx, viewerCtx} {
		_, err := api.GetSubmission(readerCtx, &apiv1.GetSubmissionRequest{JobId: deletedJob.String()})
		require.Equal(t, codes.NotFound, status.Code(err))
		require.False(t, listed(readerCtx, deletedJob))
		s := getSubmission(readerCtx, t, api, cmdJob)
		require.Nil(t, s.IdempotencyKey)
		require.Nil(t, s.RequestDigest)
		require.True(t, listed(readerCtx, cmdJob))
	}

	// A cluster admin reads both, with their key and digest.
	s := getSubmission(clusterAdminCtx, t, api, deletedJob)
	require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_DELETED, s.State)
	require.NotNil(t, s.IdempotencyKey)
	require.True(t, listed(clusterAdminCtx, deletedJob))
	s = getSubmission(clusterAdminCtx, t, api, cmdJob)
	require.NotNil(t, s.IdempotencyKey)
	require.NotNil(t, s.RequestDigest)

	// The owner still reads their deleted experiment.
	require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_DELETED,
		getSubmission(ownerCtx, t, api, deletedJob).State)
}
