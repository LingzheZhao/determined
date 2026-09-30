//go:build integration
// +build integration

package internal

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gopkg.in/guregu/null.v3"

	authz2 "github.com/determined-ai/determined/master/internal/authz"
	"github.com/determined-ai/determined/master/internal/command"
	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/internal/experiment"
	"github.com/determined-ai/determined/master/internal/mocks"
	"github.com/determined-ai/determined/master/internal/rm/rmevents"
	"github.com/determined-ai/determined/master/internal/sproto"
	"github.com/determined-ai/determined/master/internal/submission"
	"github.com/determined-ai/determined/master/internal/task"
	"github.com/determined-ai/determined/master/internal/user"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/master/pkg/ptrs"
	"github.com/determined-ai/determined/master/pkg/ssh"
	"github.com/determined-ai/determined/master/pkg/tasks"
	"github.com/determined-ai/determined/proto/pkg/apiv1"
	"github.com/determined-ai/determined/proto/pkg/taskv1"
	"github.com/determined-ai/determined/proto/pkg/utilv1"
)

// releasingRM is the submission test resource manager that also releases the resources of an
// allocation that exits, so that killed allocations end.
func releasingRM(
	allocate func(sproto.AllocateRequest) (*sproto.ResourcesSubscription, error),
) *mocks.ResourceManager {
	mockRM := submissionMockRM(allocate)
	mockRM.On("Release", mock.Anything).Return().Run(func(args mock.Arguments) {
		if msg := args[0].(sproto.ResourcesReleased); msg.ResourcesID == nil {
			rmevents.Publish(msg.AllocationID, sproto.ResourcesReleasedEvent{})
		}
	})
	return mockRM
}

// addUser adds a user and returns it with a context that authenticates as it.
func addUser(t *testing.T, api *apiServer, admin bool) (model.User, context.Context) {
	username := uuid.NewString()
	_, err := user.Add(context.TODO(), &model.User{
		Username:     username,
		PasswordHash: null.NewString("", false),
		Active:       true,
		Admin:        admin,
	}, nil)
	require.NoError(t, err)
	resp, err := api.Login(context.TODO(), &apiv1.LoginRequest{Username: username})
	require.NoError(t, err)
	u, err := user.ByUsername(context.TODO(), username)
	require.NoError(t, err)
	return *u, metadata.NewIncomingContext(context.TODO(),
		metadata.Pairs("x-user-token", fmt.Sprintf("Bearer %s", resp.Token)))
}

// commitCommand commits a command or shell as LaunchCommand and LaunchShell do, without starting
// it, as in the window between the commit and the allocation's registration.
func commitCommand(
	ctx context.Context, t *testing.T, api *apiServer, owner model.User, shell bool,
) (*command.Command, model.TaskID, model.JobID) {
	var s *submission.Submission
	var launchReq *command.CreateGeneric
	var err error
	taskType, jobType := model.TaskTypeCommand, model.JobTypeCommand
	if shell {
		taskType, jobType = model.TaskTypeShell, model.JobTypeShell
		req := &apiv1.LaunchShellRequest{
			Config: commandConfig(t, "true"),
			Submit: &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
		}
		s, err = submission.NewShell(owner.ID, req, nil)
		require.NoError(t, err)
		launchReq, _, err = api.prepareLaunchShell(ctx, req, &owner, nil, nil)
		require.NoError(t, err)
		keys, err := ssh.GenerateKey(launchReq.Spec.Base.SSHConfig)
		require.NoError(t, err)
		launchReq.Spec.Metadata.PrivateKey = ptrs.Ptr(string(keys.PrivateKey))
		launchReq.Spec.Metadata.PublicKey = ptrs.Ptr(string(keys.PublicKey))
		launchReq.Spec.Keys = &keys
	} else {
		req := &apiv1.LaunchCommandRequest{
			Config: commandConfig(t, "true"),
			Submit: &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
		}
		s, err = submission.NewCommand(owner.ID, req, nil)
		require.NoError(t, err)
		launchReq, _, err = api.prepareLaunchCommand(ctx, req, &owner, nil, nil)
		require.NoError(t, err)
	}
	cmd := command.DefaultCmdService.NewGenericCommand(taskType, jobType, launchReq)
	require.NoError(t, db.Bun().RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		return commitCommandTx(ctx, tx, s, cmd, &owner)
	}))
	jobID := cmd.Job().JobID
	return cmd, taskOfJob(ctx, t, jobID.String()), jobID
}

func getSubmission(ctx context.Context, t *testing.T, api *apiServer, jobID model.JobID) *apiv1.Submission {
	resp, err := api.GetSubmission(ctx, &apiv1.GetSubmissionRequest{JobId: jobID.String()})
	require.NoError(t, err)
	return resp.Submission
}

func requireSubmissionState(
	ctx context.Context, t *testing.T, api *apiServer, jobID model.JobID, want apiv1.SubmissionState,
) *apiv1.Submission {
	var s *apiv1.Submission
	require.Eventually(t, func() bool {
		s = getSubmission(ctx, t, api, jobID)
		return s.State == want
	}, 10*time.Second, 10*time.Millisecond, "job %s never reached %s", jobID, want)
	return s
}

func cancelRequestedAt(ctx context.Context, t *testing.T, jobID model.JobID) *time.Time {
	return jobByID(ctx, t, jobID.String()).CancelRequestedAt
}

func TestCancelBeforeRegistration(t *testing.T) {
	api, curUser, ctx := setupSubmissionTest(t, releasingRM(subscribe))

	for _, shell := range []bool{false, true} {
		t.Run(fmt.Sprintf("shell=%v", shell), func(t *testing.T) {
			cmd, taskID, jobID := commitCommand(ctx, t, api, curUser, shell)
			attempt := model.AllocationID(taskID + ".1")

			// The job is committed but neither registered nor started: the kill is recorded, not
			// refused.
			if shell {
				resp, err := api.KillShell(ctx, &apiv1.KillShellRequest{ShellId: taskID.String()})
				require.NoError(t, err)
				require.Equal(t, taskv1.State_STATE_QUEUED, resp.Shell.State)
				require.Equal(t, jobID.String(), resp.Shell.JobId)
			} else {
				resp, err := api.KillCommand(ctx, &apiv1.KillCommandRequest{CommandId: taskID.String()})
				require.NoError(t, err)
				require.Equal(t, taskv1.State_STATE_QUEUED, resp.Command.State)
				require.Equal(t, jobID.String(), resp.Command.JobId)
			}
			first := cancelRequestedAt(ctx, t, jobID)
			require.NotNil(t, first)
			require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_QUEUED,
				getSubmission(ctx, t, api, jobID).State)

			// A second cancel keeps the time of the first.
			_, err := api.CancelSubmission(ctx, &apiv1.CancelSubmissionRequest{JobId: jobID.String()})
			require.NoError(t, err)
			require.True(t, first.Equal(*cancelRequestedAt(ctx, t, jobID)))

			// The start registers the allocation, finds the cancel, and kills it.
			require.NoError(t, command.DefaultCmdService.StartCommand(cmd))
			s := requireSubmissionState(ctx, t, api, jobID, apiv1.SubmissionState_SUBMISSION_STATE_CANCELED)
			require.NotNil(t, s.EndedAt)
			require.Equal(t, taskv1.ExitClass_EXIT_CLASS_NONE, s.ExitClass)
			require.Len(t, s.Tasks, 1)
			require.Len(t, s.Tasks[0].Allocations, 1, "the kill never requests another allocation")
			require.Equal(t, attempt.String(), s.Tasks[0].Allocations[0].AllocationId)
			require.Eventually(t, func() bool {
				for _, id := range task.DefaultService.GetAllAllocationIDs() {
					if id == attempt {
						return false
					}
				}
				return true
			}, 10*time.Second, 10*time.Millisecond)
		})
	}
}

func TestCancelThenCrashBeforeKill(t *testing.T) {
	api, _, ctx := setupSubmissionTest(t, releasingRM(subscribe))
	launch := func() (model.TaskID, model.AllocationID, model.JobID) {
		resp, err := api.LaunchCommand(ctx, &apiv1.LaunchCommandRequest{
			Config: commandConfig(t, "true"),
			Submit: &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
		})
		require.NoError(t, err)
		return model.TaskID(resp.Command.Id), model.AllocationID(resp.Command.Id + ".1"),
			model.JobID(resp.Command.JobId)
	}
	cancel := func(jobID model.JobID) {
		resp, err := api.CancelSubmission(ctx, &apiv1.CancelSubmissionRequest{JobId: jobID.String()})
		require.NoError(t, err)
		require.NotEqual(t, apiv1.SubmissionState_SUBMISSION_STATE_CANCELED, resp.Submission.State,
			"the crash came before the kill landed")
		require.NotNil(t, cancelRequestedAt(ctx, t, jobID))
	}

	// Queued: the master crashed after the cancel committed and before the kill landed.
	queuedTask, queued, queuedJob := launch()
	crashCommand(ctx, t, queuedTask)
	cancel(queuedJob)

	// Placed: likewise, after the allocation recorded ASSIGNED.
	placedTask, placed, placedJob := launch()
	rID := sproto.ResourcesID(uuid.NewString())
	var firstStarts, restoredStarts atomic.Int32
	rmevents.Publish(placed, &sproto.ResourcesAllocated{
		ID: placed, ResourcePool: "default",
		Resources: sproto.ResourceList{rID: countingResources(placed, rID, &firstStarts)},
	})
	requireAllocationState(ctx, t, placed, model.AllocationStateAssigned)
	crashCommand(ctx, t, placedTask)
	cancel(placedJob)

	recorder := restartCommands(ctx, t, api)

	// The queued attempt is ended, not requested again.
	require.Empty(t, recorder.requested(queued))
	s := getSubmission(ctx, t, api, queuedJob)
	require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_CANCELED, s.State)
	require.Len(t, s.Tasks[0].Allocations, 1)
	require.Equal(t, command.StopQueuedReason, s.ExitReason)

	// The placed attempt is restored under its ID and killed once it is reattached.
	requests := recorder.requested(placed)
	require.Len(t, requests, 1)
	require.True(t, requests[0].Restore)
	rmevents.Publish(placed, &sproto.ResourcesAllocated{
		ID: placed, ResourcePool: "default", Recovered: true,
		Resources: sproto.ResourceList{rID: countingResources(placed, rID, &restoredStarts)},
	})
	s = requireSubmissionState(ctx, t, api, placedJob, apiv1.SubmissionState_SUBMISSION_STATE_CANCELED)
	require.Len(t, s.Tasks[0].Allocations, 1)
	require.Zero(t, restoredStarts.Load(), "a restored allocation starts no container")
	_ = placedTask
}

func TestCancelRacesCompletion(t *testing.T) {
	api, curUser, ctx := setupSubmissionTest(t, releasingRM(subscribe))

	// commitEnded commits a command whose only allocation exited without failing, so that ending
	// its task is the exit decision that completes it.
	commitEnded := func() (model.TaskID, model.JobID) {
		_, taskID, jobID := commitCommand(ctx, t, api, curUser, false)
		now := time.Now().UTC()
		_, err := db.Bun().NewUpdate().Table("allocations").
			Set("state = ?", model.AllocationStateTerminated).
			Set("start_time = ?", now).Set("end_time = ?", now).
			Set("exit_class = ?", model.ExitClassNone).
			Where("task_id = ?", taskID).Exec(ctx)
		require.NoError(t, err)
		return taskID, jobID
	}
	endTask := func(taskID model.TaskID) {
		require.NoError(t, db.EndLiveTask(context.Background(), taskID, time.Now().UTC(), nil))
	}
	cancel := func(jobID model.JobID) {
		_, err := api.CancelSubmission(ctx, &apiv1.CancelSubmissionRequest{JobId: jobID.String()})
		require.NoError(t, err)
	}

	// The end commits first: the late cancel changes nothing.
	taskID, jobID := commitEnded()
	endTask(taskID)
	cancel(jobID)
	require.Nil(t, cancelRequestedAt(ctx, t, jobID), "cancel never marks an ended job")
	require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_COMPLETED,
		getSubmission(ctx, t, api, jobID).State)

	// The cancel commits first: the end reads as a cancel.
	taskID, jobID = commitEnded()
	cancel(jobID)
	endTask(taskID)
	require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_CANCELED,
		getSubmission(ctx, t, api, jobID).State)

	// Released together, whichever commits first decides, and the state always agrees with the
	// cancel request.
	for i := 0; i < 10; i++ {
		taskID, jobID := commitEnded()
		tx, err := db.Bun().BeginTx(ctx, nil)
		require.NoError(t, err)
		require.NoError(t, db.LockJobTx(ctx, tx, jobID))

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			cancel(jobID)
		}()
		go func() {
			defer wg.Done()
			endTask(taskID)
		}()
		time.Sleep(20 * time.Millisecond)
		require.NoError(t, tx.Commit())
		wg.Wait()

		state := getSubmission(ctx, t, api, jobID).State
		requested := cancelRequestedAt(ctx, t, jobID) != nil
		require.Contains(t, []apiv1.SubmissionState{
			apiv1.SubmissionState_SUBMISSION_STATE_COMPLETED,
			apiv1.SubmissionState_SUBMISSION_STATE_CANCELED,
		}, state)
		require.Equal(t, requested, state == apiv1.SubmissionState_SUBMISSION_STATE_CANCELED)
	}
}

func TestFailedRestoreReadsFailed(t *testing.T) {
	api, _, ctx := setupSubmissionTest(t, releasingRM(subscribe))
	resp, err := api.LaunchCommand(ctx, &apiv1.LaunchCommandRequest{
		Config: commandConfig(t, "true"),
		Submit: &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
	})
	require.NoError(t, err)
	taskID, jobID := model.TaskID(resp.Command.Id), model.JobID(resp.Command.JobId)
	attempt := model.AllocationID(taskID + ".1")
	crashCommand(ctx, t, taskID)

	restartCommands(ctx, t, api, attempt)
	requireFailedRestore(ctx, t, attempt)
	s := getSubmission(ctx, t, api, jobID)
	require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_FAILED, s.State)
	require.Equal(t, taskv1.ExitClass_EXIT_CLASS_INFRASTRUCTURE_FAILED, s.ExitClass)
	require.Equal(t, command.RestoreFailedReason, s.ExitReason)
	require.NotNil(t, s.EndedAt)
}

func TestGetSubmissionReadsTheDatabase(t *testing.T) {
	api, curUser, ctx := setupSubmissionTest(t, releasingRM(subscribe))
	key := uuid.NewString()
	resp, err := api.LaunchCommand(ctx, &apiv1.LaunchCommandRequest{
		Config: commandConfig(t, "true"),
		Submit: &apiv1.SubmitOptions{IdempotencyKey: key},
	})
	require.NoError(t, err)
	jobID, taskID := model.JobID(resp.Command.JobId), model.TaskID(resp.Command.Id)
	attempt := model.AllocationID(taskID + ".1")

	s := getSubmission(ctx, t, api, jobID)
	require.Equal(t, apiv1.SubmissionKind_SUBMISSION_KIND_COMMAND, s.Kind)
	require.Equal(t, taskID.String(), s.EntityId)
	require.Equal(t, int32(curUser.ID), s.OwnerId)
	require.Equal(t, curUser.Username, s.Owner)
	require.Equal(t, resp.Command.WorkspaceId, s.WorkspaceId)
	require.Nil(t, s.ProjectId)
	require.Equal(t, resp.Command.Description, s.Name)
	require.Equal(t, key, *s.IdempotencyKey)
	require.Equal(t, resp.Submission.RequestDigest, *s.RequestDigest)
	require.Equal(t, apiv1.Admission_ADMISSION_QUEUE, s.Admission)
	require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_QUEUED, s.State)
	require.NotNil(t, s.SubmittedAt)
	require.Nil(t, s.EndedAt)
	require.Len(t, s.Tasks, 1)
	require.Nil(t, s.Tasks[0].TrialId)
	alloc := s.Tasks[0].Allocations[0]
	require.Equal(t, attempt.String(), alloc.AllocationId)
	require.Equal(t, taskv1.State_STATE_QUEUED, alloc.State)
	require.Equal(t, resp.Command.ResourcePool, alloc.ResourcePool)
	require.Empty(t, alloc.Placements)

	// Placement makes it run, and the containers' accelerators are its placements.
	rID := sproto.ResourcesID(uuid.NewString())
	var starts atomic.Int32
	rmevents.Publish(attempt, &sproto.ResourcesAllocated{
		ID: attempt, ResourcePool: "default",
		Resources: sproto.ResourceList{rID: countingResources(attempt, rID, &starts)},
	})
	requireAllocationState(ctx, t, attempt, model.AllocationStateAssigned)
	_, err = db.Bun().NewInsert().Model(&model.AcceleratorData{
		ContainerID: uuid.NewString(), AllocationID: attempt, NodeName: "node-a",
		AcceleratorType: "cuda", AcceleratorUuids: []string{"GPU-a", "GPU-b"},
	}).Exec(ctx)
	require.NoError(t, err)
	s = getSubmission(ctx, t, api, jobID)
	require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_RUNNING, s.State)
	placements := s.Tasks[0].Allocations[0].Placements
	require.Len(t, placements, 1)
	require.Equal(t, "node-a", placements[0].Node)
	require.Equal(t, []string{"GPU-a", "GPU-b"}, placements[0].AcceleratorUuids)

	// GetTask reports the pool and placements too.
	got, err := api.GetTask(ctx, &apiv1.GetTaskRequest{TaskId: taskID.String()})
	require.NoError(t, err)
	require.Equal(t, resp.Command.ResourcePool, got.Task.Allocations[0].ResourcePool)
	require.Equal(t, "node-a", got.Task.Allocations[0].Placements[0].Node)

	// The cancel kills the running allocation, and the job ends CANCELED.
	canceled, err := api.CancelSubmission(ctx, &apiv1.CancelSubmissionRequest{JobId: jobID.String()})
	require.NoError(t, err)
	require.NotNil(t, cancelRequestedAt(ctx, t, jobID))
	require.Equal(t, jobID.String(), canceled.Submission.JobId)
	s = requireSubmissionState(ctx, t, api, jobID, apiv1.SubmissionState_SUBMISSION_STATE_CANCELED)
	require.NotNil(t, s.EndedAt)

	// Canceling an ended job changes nothing.
	requested := cancelRequestedAt(ctx, t, jobID)
	again, err := api.CancelSubmission(ctx, &apiv1.CancelSubmissionRequest{JobId: jobID.String()})
	require.NoError(t, err)
	require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_CANCELED, again.Submission.State)
	require.True(t, requested.Equal(*cancelRequestedAt(ctx, t, jobID)))

	_, err = api.GetSubmission(ctx, &apiv1.GetSubmissionRequest{JobId: uuid.NewString()})
	require.Equal(t, codes.NotFound, status.Code(err))
}

func TestSubmissionIdentityVisibility(t *testing.T) {
	api, _, ctx := setupSubmissionTest(t, releasingRM(subscribe))
	resp, err := api.LaunchCommand(ctx, &apiv1.LaunchCommandRequest{
		Config: commandConfig(t, "true"),
		Submit: &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
	})
	require.NoError(t, err)
	jobID := model.JobID(resp.Command.JobId)

	// Another admin sees the key and digest.
	_, adminCtx := addUser(t, api, true)
	s := getSubmission(adminCtx, t, api, jobID)
	require.NotNil(t, s.IdempotencyKey)
	require.NotNil(t, s.RequestDigest)

	// A reader who is neither the owner nor an admin sees the job but not its key or digest.
	_, readerCtx := addUser(t, api, false)
	s = getSubmission(readerCtx, t, api, jobID)
	require.Equal(t, jobID.String(), s.JobId)
	require.Nil(t, s.IdempotencyKey)
	require.Nil(t, s.RequestDigest)
	list, err := api.ListSubmissions(readerCtx, &apiv1.ListSubmissionsRequest{
		OwnerId: ptrs.Ptr(s.OwnerId),
	})
	require.NoError(t, err)
	require.NotEmpty(t, list.Submissions)
	for _, listed := range list.Submissions {
		require.Nil(t, listed.IdempotencyKey)
		require.Nil(t, listed.RequestDigest)
	}

	// Nor may that reader cancel the job, which stays live.
	_, err = api.CancelSubmission(readerCtx, &apiv1.CancelSubmissionRequest{JobId: jobID.String()})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Nil(t, cancelRequestedAt(ctx, t, jobID))
	require.NoError(t, task.DefaultService.Signal(
		model.AllocationID(resp.Command.Id+".1"), task.KillAllocation, "test cleanup"))
}

func TestSubmissionReadAuthorization(t *testing.T) {
	api, authZ, curUser, ctx := setupNTSCAuthzTest(t)
	cmd, err := command.DefaultCmdService.LaunchGenericCommand(
		model.TaskTypeCommand, model.JobTypeCommand, mockGenericReq(t, api.m.db))
	require.NoError(t, err)
	jobID := model.JobID(cmd.ToV1Command().JobId)
	ownerID := cmd.ToV1Command().UserId

	// A job the caller may not read is not found, listed, or canceled.
	authZ.On("CanGetNSC", mock.Anything, curUser, mock.Anything).
		Return(authz2.PermissionDeniedError{}).Times(3)
	_, err = api.GetSubmission(ctx, &apiv1.GetSubmissionRequest{JobId: jobID.String()})
	require.Equal(t, codes.NotFound, status.Code(err))
	_, err = api.CancelSubmission(ctx, &apiv1.CancelSubmissionRequest{JobId: jobID.String()})
	require.Equal(t, codes.NotFound, status.Code(err))
	list, err := api.ListSubmissions(ctx, &apiv1.ListSubmissionsRequest{OwnerId: &ownerID})
	require.NoError(t, err)
	require.Empty(t, list.Submissions)

	// A reader who may not terminate it cannot cancel it either, and nothing is recorded.
	authZ.On("CanGetNSC", mock.Anything, curUser, mock.Anything).Return(nil)
	authZ.On("CanTerminateNSC", mock.Anything, curUser, mock.Anything).
		Return(authz2.PermissionDeniedError{}).Once()
	_, err = api.CancelSubmission(ctx, &apiv1.CancelSubmissionRequest{JobId: jobID.String()})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	require.Nil(t, cancelRequestedAt(ctx, t, jobID))
	authZ.AssertExpectations(t)
}

func TestListSubmissions(t *testing.T) {
	api, _, _ := setupSubmissionTest(t, releasingRM(subscribe))
	owner, ctx := addUser(t, api, true)

	// Jobs are ordered by their submit time, which is kept to the millisecond.
	var jobs []model.JobID
	for i := 0; i < 3; i++ {
		time.Sleep(5 * time.Millisecond)
		resp, err := api.LaunchCommand(ctx, &apiv1.LaunchCommandRequest{
			Config: commandConfig(t, "true"),
			Submit: &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
		})
		require.NoError(t, err)
		jobs = append(jobs, model.JobID(resp.Command.JobId))
	}
	time.Sleep(5 * time.Millisecond)
	generic, err := api.CreateGenericTask(ctx, &apiv1.CreateGenericTaskRequest{
		Config:    "entrypoint: [sh, -c, 'true']\nresources: {slots: 0}\n",
		ProjectId: ptrs.Ptr(int32(1)),
		Submit:    &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
	})
	require.NoError(t, err)
	jobs = append(jobs, model.JobID(generic.Submission.JobId))
	newestFirst := []string{jobs[3].String(), jobs[2].String(), jobs[1].String(), jobs[0].String()}

	ids := func(submissions []*apiv1.Submission) []string {
		var out []string
		for _, s := range submissions {
			out = append(out, s.JobId)
		}
		return out
	}

	// The caller's jobs, newest first, two to a page.
	var listed []string
	token := ""
	for pages := 0; ; pages++ {
		require.Less(t, pages, 4)
		resp, err := api.ListSubmissions(ctx, &apiv1.ListSubmissionsRequest{Limit: 2, PageToken: token})
		require.NoError(t, err)
		require.LessOrEqual(t, len(resp.Submissions), 2)
		listed = append(listed, ids(resp.Submissions)...)
		if token = resp.NextPageToken; token == "" {
			break
		}
	}
	require.Equal(t, newestFirst, listed)

	resp, err := api.ListSubmissions(ctx, &apiv1.ListSubmissionsRequest{
		Kind: apiv1.SubmissionKind_SUBMISSION_KIND_GENERIC,
	})
	require.NoError(t, err)
	require.Equal(t, []string{jobs[3].String()}, ids(resp.Submissions))
	require.Equal(t, int32(1), *resp.Submissions[0].ProjectId)
	require.Equal(t, "Generic Task "+generic.TaskId, resp.Submissions[0].Name)

	second := getSubmission(ctx, t, api, jobs[1])
	resp, err = api.ListSubmissions(ctx, &apiv1.ListSubmissionsRequest{
		SubmittedAfter: second.SubmittedAt,
	})
	require.NoError(t, err)
	require.Equal(t, newestFirst[:2], ids(resp.Submissions))

	// The state filter reads the derived state.
	_, err = api.CancelSubmission(ctx, &apiv1.CancelSubmissionRequest{JobId: jobs[0].String()})
	require.NoError(t, err)
	requireSubmissionState(ctx, t, api, jobs[0], apiv1.SubmissionState_SUBMISSION_STATE_CANCELED)
	resp, err = api.ListSubmissions(ctx, &apiv1.ListSubmissionsRequest{
		State: apiv1.SubmissionState_SUBMISSION_STATE_CANCELED,
	})
	require.NoError(t, err)
	require.Equal(t, []string{jobs[0].String()}, ids(resp.Submissions))
	resp, err = api.ListSubmissions(ctx, &apiv1.ListSubmissionsRequest{
		State: apiv1.SubmissionState_SUBMISSION_STATE_QUEUED, Limit: 1,
	})
	require.NoError(t, err)
	require.Equal(t, newestFirst[:1], ids(resp.Submissions))
	resp, err = api.ListSubmissions(ctx, &apiv1.ListSubmissionsRequest{
		State: apiv1.SubmissionState_SUBMISSION_STATE_QUEUED, PageToken: resp.NextPageToken,
	})
	require.NoError(t, err)
	require.Equal(t, newestFirst[1:3], ids(resp.Submissions))

	// Another user lists the owner's jobs by owner, and by default lists their own.
	_, otherCtx := addUser(t, api, false)
	resp, err = api.ListSubmissions(otherCtx, &apiv1.ListSubmissionsRequest{
		OwnerId: ptrs.Ptr(int32(owner.ID)),
	})
	require.NoError(t, err)
	require.Equal(t, newestFirst, ids(resp.Submissions))
	resp, err = api.ListSubmissions(otherCtx, &apiv1.ListSubmissionsRequest{})
	require.NoError(t, err)
	require.Empty(t, resp.Submissions)
	require.Empty(t, resp.NextPageToken)

	_, err = api.ListSubmissions(ctx, &apiv1.ListSubmissionsRequest{PageToken: "not a token"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = api.ListSubmissions(ctx, &apiv1.ListSubmissionsRequest{Limit: -1})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = api.ListSubmissions(ctx, &apiv1.ListSubmissionsRequest{
		State: apiv1.SubmissionState(99),
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	for _, jobID := range jobs[1:3] {
		_, err = api.CancelSubmission(ctx, &apiv1.CancelSubmissionRequest{JobId: jobID.String()})
		require.NoError(t, err)
	}
	_, err = api.KillGenericTask(ctx, &apiv1.KillGenericTaskRequest{TaskId: generic.TaskId})
	require.NoError(t, err)
}

// addLiveGenericTask adds a generic task with its job, spec, and one allocation in the given state,
// as a master would have left it, without registering anything.
func addLiveGenericTask(
	ctx context.Context, t *testing.T, owner model.User, parent *model.TaskID,
	taskState model.TaskState, allocationState model.AllocationState, noPause bool,
) (model.TaskID, model.JobID, model.AllocationID) {
	jobID := model.NewJobID()
	require.NoError(t, db.AddJob(&model.Job{
		JobID: jobID, JobType: model.JobTypeGeneric, OwnerID: &owner.ID,
	}))
	taskID := model.NewTaskID()
	require.NoError(t, db.AddTask(ctx, &model.Task{
		TaskID: taskID, TaskType: model.TaskTypeGeneric, JobID: &jobID, StartTime: time.Now().UTC(),
		ParentID: parent, State: &taskState, NoPause: &noPause,
	}))
	allocationID := model.AllocationID(taskID.String() + ".1")
	require.NoError(t, db.AddAllocation(ctx, &model.Allocation{
		AllocationID: allocationID, TaskID: taskID, Slots: 0, ResourcePool: "default",
		State: &allocationState, Ports: map[string]int{},
	}))
	config := model.DefaultConfigGenericTaskConfig(nil)
	config.Resources.SetResourcePool("default")
	config.Resources.RawSlots = ptrs.Ptr(0)
	require.NoError(t, persistGenericTaskSpec(ctx, taskID, tasks.GenericTaskSpec{
		Base: tasks.TaskSpec{Owner: &owner}, WorkspaceID: 1, ProjectID: 1, JobID: jobID,
		GenericTaskConfig: config,
	}, allocationID))
	return taskID, jobID, allocationID
}

// pauseGenericTask ends a generic task's allocation and pauses the task as a pause does.
func pauseGenericTask(ctx context.Context, t *testing.T, taskID model.TaskID, allocationID model.AllocationID) {
	now := time.Now().UTC()
	_, err := db.Bun().NewUpdate().Table("allocations").
		Set("state = ?", model.AllocationStateTerminated).Set("end_time = ?", now).
		Set("exit_class = ?", model.ExitClassNone).
		Where("allocation_id = ?", allocationID).Exec(ctx)
	require.NoError(t, err)
	require.NoError(t, db.SetPausedState(taskID, now))
}

func TestPausedGenericTaskSubmission(t *testing.T) {
	api, curUser, ctx := setupSubmissionTest(t, releasingRM(subscribe))

	pausedTask, pausedJob, pausedAllocation := addLiveGenericTask(ctx, t, curUser, nil,
		model.TaskStateActive, model.AllocationStateRunning, false)
	pauseGenericTask(ctx, t, pausedTask, pausedAllocation)
	s := getSubmission(ctx, t, api, pausedJob)
	require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_PAUSED, s.State)
	require.Nil(t, s.EndedAt, "a paused task has not ended")

	_, pausingJob, _ := addLiveGenericTask(ctx, t, curUser, nil,
		model.TaskStateStoppingPaused, model.AllocationStateRunning, false)
	require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_PAUSED,
		getSubmission(ctx, t, api, pausingJob).State)

	// An unpause that claimed the task but has not started its next allocation is queued.
	resumingTask, resumingJob, resumingAllocation := addLiveGenericTask(ctx, t, curUser, nil,
		model.TaskStateActive, model.AllocationStateRunning, false)
	pauseGenericTask(ctx, t, resumingTask, resumingAllocation)
	members, err := api.GetTaskChildren(ctx, resumingTask, nil)
	require.NoError(t, err)
	plan, err := makeGenericTaskResumePlan(ctx, resumingTask, members)
	require.NoError(t, err)
	claimed, err := claimPausedGenericTask(ctx, resumingTask, plan[0].OldAllocationID)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_QUEUED,
		getSubmission(ctx, t, api, resumingJob).State)

	// Canceling a paused task ends it CANCELED, as nothing runs to exit.
	resp, err := api.CancelSubmission(ctx, &apiv1.CancelSubmissionRequest{JobId: pausedJob.String()})
	require.NoError(t, err)
	require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_CANCELED, resp.Submission.State)
	require.NotNil(t, resp.Submission.EndedAt)
	pausedModel, err := db.TaskByID(ctx, pausedTask)
	require.NoError(t, err)
	require.Equal(t, model.TaskStateCanceled, *pausedModel.State)
	require.NotNil(t, cancelRequestedAt(ctx, t, pausedJob))

	// Canceling the task an unpause claimed ends it too, and its resume is not continued.
	resp, err = api.CancelSubmission(ctx, &apiv1.CancelSubmissionRequest{JobId: resumingJob.String()})
	require.NoError(t, err)
	require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_CANCELED, resp.Submission.State)
	remaining, err := pendingGenericTaskResume(ctx, resumingTask)
	require.NoError(t, err)
	require.Empty(t, remaining)
}

func TestCancelPausedGenericParentThenRestart(t *testing.T) {
	api, curUser, ctx := setupSubmissionTest(t, releasingRM(subscribe))

	// A paused parent with an unfinished resume, and its no_pause child, which runs on a placed
	// allocation that the master lost in a crash.
	newTree := func(childOwner model.User) (model.TaskID, model.JobID, model.TaskID, model.JobID, model.AllocationID) {
		parent, parentJob, parentAllocation := addLiveGenericTask(ctx, t, curUser, nil,
			model.TaskStateActive, model.AllocationStateRunning, false)
		pauseGenericTask(ctx, t, parent, parentAllocation)
		child, childJob, childAllocation := addLiveGenericTask(ctx, t, childOwner, &parent,
			model.TaskStateActive, model.AllocationStateAssigned, true)
		members, err := api.GetTaskChildren(ctx, parent, nil)
		require.NoError(t, err)
		plan, err := makeGenericTaskResumePlan(ctx, parent, members)
		require.NoError(t, err)
		require.Len(t, plan, 1, "only the paused parent resumes")
		return parent, parentJob, child, childJob, childAllocation
	}

	// Every member is authorized before anything changes: a child the caller may not control
	// fails the whole cancel.
	other, _ := addUser(t, api, false)
	parent, parentJob, child, childJob, _ := newTree(other)
	_, err := db.Bun().NewUpdate().Table("users").Set("admin = false").
		Where("id = ?", curUser.ID).Exec(ctx)
	require.NoError(t, err)
	_, err = api.CancelSubmission(ctx, &apiv1.CancelSubmissionRequest{JobId: parentJob.String()})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	for _, jobID := range []model.JobID{parentJob, childJob} {
		require.Nil(t, cancelRequestedAt(ctx, t, jobID))
	}
	parentModel, err := db.TaskByID(ctx, parent)
	require.NoError(t, err)
	require.Equal(t, model.TaskStatePaused, *parentModel.State)
	childModel, err := db.TaskByID(ctx, child)
	require.NoError(t, err)
	require.Equal(t, model.TaskStateActive, *childModel.State)
	plan, err := pendingGenericTaskResume(ctx, parent)
	require.NoError(t, err)
	require.Len(t, plan, 1)
	require.False(t, plan[0].Canceled)
	_, err = db.Bun().NewUpdate().Table("users").Set("admin = true").
		Where("id = ?", curUser.ID).Exec(ctx)
	require.NoError(t, err)
	// That tree takes no part in the restart below.
	_, err = db.Bun().NewDelete().Table("generic_task_resume").Where("root_task_id = ?", parent).Exec(ctx)
	require.NoError(t, err)
	_, err = db.Bun().NewUpdate().Table("tasks").Set("end_time = now()").
		Set("task_state = ?", model.TaskStateCompleted).
		Where("task_id IN (?)", bun.In([]model.TaskID{parent, child})).Exec(ctx)
	require.NoError(t, err)

	// Cancel a tree the caller controls, then restart.
	parent, parentJob, child, childJob, childAllocation := newTree(curUser)
	resp, err := api.CancelSubmission(ctx, &apiv1.CancelSubmissionRequest{JobId: parentJob.String()})
	require.NoError(t, err)
	require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_CANCELED, resp.Submission.State,
		"nothing runs for the paused parent, so it ends CANCELED at once")
	require.NotNil(t, cancelRequestedAt(ctx, t, childJob))
	require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_RUNNING,
		getSubmission(ctx, t, api, childJob).State, "the lost child is killed once restored")
	plan, err = pendingGenericTaskResume(ctx, parent)
	require.NoError(t, err)
	require.Empty(t, plan)

	recorder := &recordingRM{
		requests: map[model.AllocationID][]allocateRequest{},
		fail:     map[model.AllocationID]bool{},
	}
	api.m.rm = releasingRM(recorder.allocate)
	require.NoError(t, api.m.restoreGenericTasks(ctx))
	require.NoError(t, api.m.recoverGenericTaskResumes(ctx))
	require.NoError(t, api.m.closeOpenAllocations(ctx))

	require.Empty(t, recorder.requested(model.AllocationID(parent.String()+".2")),
		"the parent's resume is not continued")
	requests := recorder.requested(childAllocation)
	require.Len(t, requests, 1)
	require.True(t, requests[0].Restore)
	rID := sproto.ResourcesID(uuid.NewString())
	var starts atomic.Int32
	rmevents.Publish(childAllocation, &sproto.ResourcesAllocated{
		ID: childAllocation, ResourcePool: "default", Recovered: true,
		Resources: sproto.ResourceList{rID: countingResources(childAllocation, rID, &starts)},
	})
	requireSubmissionState(ctx, t, api, childJob, apiv1.SubmissionState_SUBMISSION_STATE_CANCELED)
	require.Zero(t, starts.Load())
	childModel, err = db.TaskByID(ctx, child)
	require.NoError(t, err)
	require.Equal(t, model.TaskStateCanceled, *childModel.State)
	require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_CANCELED,
		getSubmission(ctx, t, api, parentJob).State)
	parentModel, err = db.TaskByID(ctx, parent)
	require.NoError(t, err)
	require.Equal(t, model.TaskStateCanceled, *parentModel.State)
}

func TestKillGenericTaskFromRootCancelsEveryJob(t *testing.T) {
	api, _, ctx := setupSubmissionTest(t, releasingRM(subscribe))
	create := func(parent *string) *apiv1.CreateGenericTaskResponse {
		resp, err := api.CreateGenericTask(ctx, &apiv1.CreateGenericTaskRequest{
			Config:    "entrypoint: [sh, -c, 'true']\nresources: {slots: 0}\n",
			ProjectId: ptrs.Ptr(int32(1)),
			ParentId:  parent,
			Submit:    &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
		})
		require.NoError(t, err)
		return resp
	}
	root := create(nil)
	child := create(&root.TaskId)

	_, err := api.KillGenericTask(ctx, &apiv1.KillGenericTaskRequest{
		TaskId: child.TaskId, KillFromRoot: true,
	})
	require.NoError(t, err)
	for _, resp := range []*apiv1.CreateGenericTaskResponse{root, child} {
		jobID := model.JobID(resp.Submission.JobId)
		require.NotNil(t, cancelRequestedAt(ctx, t, jobID))
		requireSubmissionState(ctx, t, api, jobID, apiv1.SubmissionState_SUBMISSION_STATE_CANCELED)
		taskModel, err := db.TaskByID(ctx, model.TaskID(resp.TaskId))
		require.NoError(t, err)
		require.Equal(t, model.TaskStateCanceled, *taskModel.State)
	}

	// A busy mutation lock is a retryable error.
	require.True(t, genericTaskMutation.TryLock())
	_, err = api.CancelSubmission(ctx, &apiv1.CancelSubmissionRequest{JobId: root.Submission.JobId})
	genericTaskMutation.Unlock()
	require.Equal(t, codes.Unavailable, status.Code(err))
}

// killRecordingExperiment is an experiment that records its kills.
type killRecordingExperiment struct {
	experiment.Experiment
	kills atomic.Int32
}

func (e *killRecordingExperiment) KillExperiment() error {
	e.kills.Add(1)
	return nil
}

func TestExperimentSubmission(t *testing.T) {
	api, curUser, ctx := setupSubmissionTest(t, releasingRM(subscribe))
	key := uuid.NewString()
	resp, err := api.CreateExperiment(ctx, &apiv1.CreateExperimentRequest{
		ModelDefinition: []*utilv1.File{{Content: []byte{1}}},
		Config:          minExpConfToYaml(t),
		ProjectId:       1,
		Submit:          &apiv1.SubmitOptions{IdempotencyKey: key},
	})
	require.NoError(t, err)
	jobID := model.JobID(resp.Submission.JobId)

	s := getSubmission(ctx, t, api, jobID)
	require.Equal(t, apiv1.SubmissionKind_SUBMISSION_KIND_EXPERIMENT, s.Kind)
	require.Equal(t, strconv.Itoa(int(resp.Experiment.Id)), s.EntityId)
	require.Equal(t, int32(1), *s.ProjectId)
	require.Equal(t, resp.Experiment.WorkspaceId, s.WorkspaceId)
	require.Equal(t, resp.Experiment.Name, s.Name)
	require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_PAUSED, s.State)
	require.Equal(t, key, *s.IdempotencyKey)

	// The cancel kills the registered experiment.
	_, err = api.CancelSubmission(ctx, &apiv1.CancelSubmissionRequest{JobId: jobID.String()})
	require.NoError(t, err)
	require.NotNil(t, cancelRequestedAt(ctx, t, jobID))
	s = requireSubmissionState(ctx, t, api, jobID, apiv1.SubmissionState_SUBMISSION_STATE_CANCELED)
	require.NotNil(t, s.EndedAt)

	// A deleted experiment keeps its job, which only its owner and admins see.
	require.NoError(t, api.m.db.DeleteExperiments(ctx, []int{int(resp.Experiment.Id)}))
	s = getSubmission(ctx, t, api, jobID)
	require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_DELETED, s.State)
	require.Empty(t, s.EntityId)
	require.Equal(t, int32(curUser.ID), s.OwnerId)
	_, adminCtx := addUser(t, api, true)
	require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_DELETED,
		getSubmission(adminCtx, t, api, jobID).State)
	_, readerCtx := addUser(t, api, false)
	_, err = api.GetSubmission(readerCtx, &apiv1.GetSubmissionRequest{JobId: jobID.String()})
	require.Equal(t, codes.NotFound, status.Code(err))
	list, err := api.ListSubmissions(readerCtx, &apiv1.ListSubmissionsRequest{
		OwnerId: ptrs.Ptr(int32(curUser.ID)),
		Kind:    apiv1.SubmissionKind_SUBMISSION_KIND_EXPERIMENT,
	})
	require.NoError(t, err)
	for _, listed := range list.Submissions {
		require.NotEqual(t, jobID.String(), listed.JobId)
	}
	canceled, err := api.CancelSubmission(ctx, &apiv1.CancelSubmissionRequest{JobId: jobID.String()})
	require.NoError(t, err)
	require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_DELETED, canceled.Submission.State)

	// An experiment started after its job was asked to stop is killed as it starts.
	started := &killRecordingExperiment{}
	killExperimentIfCancelRequested(started, int(resp.Experiment.Id), jobID)
	require.Eventually(t, func() bool { return started.kills.Load() == 1 },
		10*time.Second, 10*time.Millisecond)
	live, err := api.CreateExperiment(ctx, &apiv1.CreateExperimentRequest{
		ModelDefinition: []*utilv1.File{{Content: []byte{1}}},
		Config:          minExpConfToYaml(t),
		ProjectId:       1,
	})
	require.NoError(t, err)
	notAsked := &killRecordingExperiment{}
	killExperimentIfCancelRequested(notAsked, int(live.Experiment.Id), model.JobID(live.Experiment.JobId))
	time.Sleep(50 * time.Millisecond)
	require.Zero(t, notAsked.kills.Load())
	_, err = api.KillExperiment(ctx, &apiv1.KillExperimentRequest{Id: live.Experiment.Id})
	require.NoError(t, err)
}

func TestListSubmissionsWithTimestampFilter(t *testing.T) {
	api, _, _ := setupSubmissionTest(t, releasingRM(subscribe))
	_, ctx := addUser(t, api, true)
	resp, err := api.LaunchCommand(ctx, &apiv1.LaunchCommandRequest{
		Config: commandConfig(t, "true"),
		Submit: &apiv1.SubmitOptions{IdempotencyKey: uuid.NewString()},
	})
	require.NoError(t, err)
	list, err := api.ListSubmissions(ctx, &apiv1.ListSubmissionsRequest{
		SubmittedAfter: timestamppb.New(time.Now().Add(time.Hour)),
	})
	require.NoError(t, err)
	require.Empty(t, list.Submissions)
	require.NoError(t, task.DefaultService.Signal(
		model.AllocationID(resp.Command.Id+".1"), task.KillAllocation, "test cleanup"))
}

func TestResumeNeverContinuesCanceledTask(t *testing.T) {
	api, curUser, ctx := setupSubmissionTest(t, releasingRM(subscribe))
	recorder := &recordingRM{
		requests: map[model.AllocationID][]allocateRequest{},
		fail:     map[model.AllocationID]bool{},
	}
	api.m.rm = releasingRM(recorder.allocate)

	// A resume whose row the cancel did not reach, as a restart finds it.
	taskID, jobID, allocationID := addLiveGenericTask(ctx, t, curUser, nil,
		model.TaskStateActive, model.AllocationStateRunning, false)
	pauseGenericTask(ctx, t, taskID, allocationID)
	members, err := api.GetTaskChildren(ctx, taskID, nil)
	require.NoError(t, err)
	plan, err := makeGenericTaskResumePlan(ctx, taskID, members)
	require.NoError(t, err)
	_, err = db.Bun().NewUpdate().Table("jobs").Set("cancel_requested_at = now()").
		Where("job_id = ?", jobID).Exec(ctx)
	require.NoError(t, err)

	require.NoError(t, api.runGenericTaskResume(ctx, plan))
	require.Empty(t, recorder.requested(plan[0].NewAllocationID))
	taskModel, err := db.TaskByID(ctx, taskID)
	require.NoError(t, err)
	require.Equal(t, model.TaskStateCanceled, *taskModel.State)
	require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_CANCELED,
		getSubmission(ctx, t, api, jobID).State)
	remaining, err := pendingGenericTaskResume(ctx, taskID)
	require.NoError(t, err)
	require.Empty(t, remaining)

	// The same holds when the resume had queued its allocation before the restart: the
	// allocation is closed instead of requested.
	taskID, jobID, allocationID = addLiveGenericTask(ctx, t, curUser, nil,
		model.TaskStateActive, model.AllocationStateRunning, false)
	pauseGenericTask(ctx, t, taskID, allocationID)
	members, err = api.GetTaskChildren(ctx, taskID, nil)
	require.NoError(t, err)
	plan, err = makeGenericTaskResumePlan(ctx, taskID, members)
	require.NoError(t, err)
	claimed, err := claimPausedGenericTask(ctx, taskID, plan[0].OldAllocationID)
	require.NoError(t, err)
	require.True(t, claimed)
	pending := model.AllocationStatePending
	require.NoError(t, db.AddAllocation(ctx, &model.Allocation{
		AllocationID: plan[0].NewAllocationID, TaskID: taskID, ResourcePool: "default",
		State: &pending, Ports: map[string]int{},
	}))
	_, err = db.Bun().NewUpdate().Table("jobs").Set("cancel_requested_at = now()").
		Where("job_id = ?", jobID).Exec(ctx)
	require.NoError(t, err)

	require.NoError(t, api.runGenericTaskResume(ctx, plan))
	require.Empty(t, recorder.requested(plan[0].NewAllocationID))
	closed := allocationOf(ctx, t, plan[0].NewAllocationID)
	require.Equal(t, model.ExitClassNone, *closed.ExitClass)
	require.Equal(t, command.StopQueuedReason, *closed.ExitReason)
	require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_CANCELED,
		getSubmission(ctx, t, api, jobID).State)
}
