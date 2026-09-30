package submission

import (
	"crypto/ed25519"
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/exp/slices"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/master/pkg/ptrs"
	"github.com/determined-ai/determined/proto/pkg/apiv1"
	"github.com/determined-ai/determined/proto/pkg/taskv1"
)

type allocationOpt func(*AllocationRecord)

func withEnd(a *AllocationRecord) {
	a.EndTime = ptrs.Ptr(time.Unix(200, 0).UTC())
	a.State = ptrs.Ptr(model.AllocationStateTerminated)
}

func withClass(class model.ExitClass) allocationOpt {
	return func(a *AllocationRecord) {
		withEnd(a)
		a.ExitClass = &class
	}
}

func withExitErr(a *AllocationRecord) {
	a.ExitErr = ptrs.Ptr("container failed")
}

func allocation(id string, state model.AllocationState, opts ...allocationOpt) AllocationRecord {
	a := AllocationRecord{AllocationID: model.AllocationID(id), State: &state}
	for _, opt := range opts {
		opt(&a)
	}
	return a
}

// taskRecord returns the record of a live command whose command_state names its last allocation.
func taskRecord(jobType model.JobType, allocations ...AllocationRecord) *Record {
	r := &Record{
		JobID:   "job",
		JobType: jobType,
		TaskID:  ptrs.Ptr(model.TaskID("task")),
		Tasks:   []TaskRecord{{TaskID: "task", Allocations: allocations}},
	}
	if len(allocations) > 0 {
		r.AttemptID = &allocations[len(allocations)-1].AllocationID
	}
	return r
}

func ended(r *Record) *Record {
	r.TaskEndTime = ptrs.Ptr(time.Unix(300, 0).UTC())
	return r
}

func canceled(r *Record) *Record {
	r.CancelRequestedAt = ptrs.Ptr(time.Unix(100, 0).UTC())
	return r
}

func withTaskState(state model.TaskState, r *Record) *Record {
	r.TaskState = &state
	return r
}

func TestTaskState(t *testing.T) {
	const (
		queued    = apiv1.SubmissionState_SUBMISSION_STATE_QUEUED
		running   = apiv1.SubmissionState_SUBMISSION_STATE_RUNNING
		paused    = apiv1.SubmissionState_SUBMISSION_STATE_PAUSED
		completed = apiv1.SubmissionState_SUBMISSION_STATE_COMPLETED
		failed    = apiv1.SubmissionState_SUBMISSION_STATE_FAILED
		canceledS = apiv1.SubmissionState_SUBMISSION_STATE_CANCELED
	)
	cmd, generic := model.JobTypeCommand, model.JobTypeGeneric
	tests := []struct {
		name   string
		record *Record
		want   apiv1.SubmissionState
	}{
		{"never placed", taskRecord(cmd, allocation("a.1", model.AllocationStatePending)), queued},
		{"no allocation", taskRecord(cmd), queued},
		{"assigned", taskRecord(cmd, allocation("a.1", model.AllocationStateAssigned)), running},
		{"pulling", taskRecord(cmd, allocation("a.1", model.AllocationStatePulling)), running},
		{"running", taskRecord(cmd, allocation("a.1", model.AllocationStateRunning)), running},
		{"terminating", taskRecord(cmd, allocation("a.1", model.AllocationStateTerminating)), running},
		{
			"state never recorded is placed",
			taskRecord(cmd, AllocationRecord{AllocationID: "a.1"}), running,
		},
		{
			"attempt ended before the task",
			taskRecord(cmd, allocation("a.1", model.AllocationStateTerminated)), queued,
		},
		{
			"attempt with end time before the task",
			taskRecord(cmd, allocation("a.1", model.AllocationStateRunning, withEnd)), queued,
		},
		{
			"live and asked to stop",
			canceled(taskRecord(cmd, allocation("a.1", model.AllocationStateRunning))), running,
		},
		{
			"ended without failing",
			ended(taskRecord(cmd, allocation("a.1", "", withClass(model.ExitClassNone)))), completed,
		},
		{
			"ended in user code",
			ended(taskRecord(cmd, allocation("a.1", "", withClass(model.ExitClassWorkloadFailed)))),
			failed,
		},
		{
			"ended with the infrastructure",
			ended(taskRecord(cmd,
				allocation("a.1", "", withClass(model.ExitClassInfrastructureFailed)))),
			failed,
		},
		{
			"ended unplaced",
			ended(taskRecord(cmd,
				allocation("a.1", "", withClass(model.ExitClassPlacementUnsatisfied)))),
			failed,
		},
		{
			"asked to stop before it ended",
			canceled(ended(taskRecord(cmd,
				allocation("a.1", "", withClass(model.ExitClassWorkloadFailed))))),
			canceledS,
		},
		{
			"asked to stop before it was placed",
			canceled(ended(taskRecord(cmd,
				allocation("a.1", model.AllocationStatePending, withClass(model.ExitClassNone))))),
			canceledS,
		},
		{
			"pre-upgrade with an error",
			ended(taskRecord(cmd, allocation("a.1", "", withEnd, withExitErr))), failed,
		},
		{
			"pre-upgrade without an error",
			ended(taskRecord(cmd, allocation("a.1", "", withEnd))), completed,
		},
		{"ended with no allocation", ended(taskRecord(cmd)), completed},
		{
			"paused",
			withTaskState(model.TaskStatePaused, ended(taskRecord(generic,
				allocation("a.1", "", withClass(model.ExitClassNone))))),
			paused,
		},
		{
			"pausing",
			withTaskState(model.TaskStateStoppingPaused, taskRecord(generic,
				allocation("a.1", model.AllocationStateRunning))),
			paused,
		},
		{
			"unpausing before the next attempt is named",
			withTaskState(model.TaskStateActive, taskRecord(generic,
				allocation("a.1", "", withClass(model.ExitClassNone)))),
			queued,
		},
		{
			"unpausing with the next attempt queued",
			withTaskState(model.TaskStateActive, taskRecord(generic,
				allocation("a.1", "", withClass(model.ExitClassNone)),
				allocation("a.2", model.AllocationStatePending))),
			queued,
		},
		{
			"resumed",
			withTaskState(model.TaskStateActive, taskRecord(generic,
				allocation("a.1", "", withClass(model.ExitClassNone)),
				allocation("a.2", model.AllocationStateRunning))),
			running,
		},
		{
			"generic task canceled while paused",
			canceled(withTaskState(model.TaskStateCanceled, ended(taskRecord(generic,
				allocation("a.1", "", withClass(model.ExitClassNone)))))),
			canceledS,
		},
		{
			"generic task that failed to restore",
			withTaskState(model.TaskStateError, ended(taskRecord(generic,
				allocation("a.1", "", withClass(model.ExitClassInfrastructureFailed))))),
			failed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.record.State())
		})
	}
}

func TestTaskStateUsesNamedAttempt(t *testing.T) {
	r := ended(taskRecord(model.JobTypeShell,
		allocation("a.1", "", withClass(model.ExitClassNone)),
		allocation("a.2", "", withClass(model.ExitClassWorkloadFailed))))
	require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_FAILED, r.State())

	r.AttemptID = ptrs.Ptr(model.AllocationID("a.1"))
	require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_COMPLETED, r.State())

	// Without command_state, the last allocation is the attempt.
	r.AttemptID = nil
	require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_FAILED, r.State())
}

func TestExperimentState(t *testing.T) {
	tests := []struct {
		state *model.State
		want  apiv1.SubmissionState
	}{
		{nil, apiv1.SubmissionState_SUBMISSION_STATE_DELETED},
		{ptrs.Ptr(model.ActiveState), apiv1.SubmissionState_SUBMISSION_STATE_RUNNING},
		{ptrs.Ptr(model.StoppingKilledState), apiv1.SubmissionState_SUBMISSION_STATE_RUNNING},
		{ptrs.Ptr(model.StoppingCanceledState), apiv1.SubmissionState_SUBMISSION_STATE_RUNNING},
		{ptrs.Ptr(model.StoppingCompletedState), apiv1.SubmissionState_SUBMISSION_STATE_RUNNING},
		{ptrs.Ptr(model.StoppingErrorState), apiv1.SubmissionState_SUBMISSION_STATE_RUNNING},
		{ptrs.Ptr(model.PausedState), apiv1.SubmissionState_SUBMISSION_STATE_PAUSED},
		{ptrs.Ptr(model.CompletedState), apiv1.SubmissionState_SUBMISSION_STATE_COMPLETED},
		{ptrs.Ptr(model.CanceledState), apiv1.SubmissionState_SUBMISSION_STATE_CANCELED},
		{ptrs.Ptr(model.ErrorState), apiv1.SubmissionState_SUBMISSION_STATE_FAILED},
		{ptrs.Ptr(model.DeletingState), apiv1.SubmissionState_SUBMISSION_STATE_DELETED},
		{ptrs.Ptr(model.DeleteFailedState), apiv1.SubmissionState_SUBMISSION_STATE_DELETED},
	}
	for _, tt := range tests {
		name := "deleted"
		if tt.state != nil {
			name = string(*tt.state)
		}
		t.Run(name, func(t *testing.T) {
			r := &Record{JobType: model.JobTypeExperiment, ExperimentState: tt.state}
			if tt.state != nil {
				r.ExperimentID = ptrs.Ptr(1)
			}
			require.Equal(t, tt.want, r.State())
		})
	}
}

func TestSubmissionProto(t *testing.T) {
	r := canceled(ended(taskRecord(model.JobTypeGeneric,
		allocation("a.1", "", withClass(model.ExitClassNone)))))
	r.TaskState = ptrs.Ptr(model.TaskStateCanceled)
	r.OwnerID = ptrs.Ptr(model.UserID(7))
	r.Owner = ptrs.Ptr("owner")
	r.WorkspaceID = ptrs.Ptr(3)
	r.ProjectID = ptrs.Ptr(4)
	r.IdempotencyKey = ptrs.Ptr("key")
	r.RequestDigest = ptrs.Ptr("digest")
	r.Admission = model.AdmissionQueue
	r.SubmittedAt = ptrs.Ptr(time.Unix(50, 0).UTC())
	r.Tasks[0].Allocations[0].ExitReason = ptrs.Ptr("user requested kill")
	r.Tasks[0].Allocations[0].Placements = []PlacementRecord{
		{Node: "node", AcceleratorUUIDs: []string{"GPU-1"}},
	}

	s := r.Proto(true)
	require.Equal(t, "job", s.JobId)
	require.Equal(t, apiv1.SubmissionKind_SUBMISSION_KIND_GENERIC, s.Kind)
	require.Equal(t, "task", s.EntityId)
	require.Equal(t, "Generic Task task", s.Name)
	require.Equal(t, int32(7), s.OwnerId)
	require.Equal(t, int32(3), s.WorkspaceId)
	require.Equal(t, int32(4), *s.ProjectId)
	require.Equal(t, "key", *s.IdempotencyKey)
	require.Equal(t, "digest", *s.RequestDigest)
	require.Equal(t, apiv1.Admission_ADMISSION_QUEUE, s.Admission)
	require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_CANCELED, s.State)
	require.Equal(t, int64(300), s.EndedAt.Seconds)
	require.Equal(t, taskv1.ExitClass_EXIT_CLASS_NONE, s.ExitClass)
	require.Equal(t, "user requested kill", s.ExitReason)
	require.Len(t, s.Tasks, 1)
	alloc := s.Tasks[0].Allocations[0]
	require.Equal(t, "a.1", alloc.AllocationId)
	require.Equal(t, taskv1.State_STATE_TERMINATED, alloc.State)
	require.Equal(t, "node", alloc.Placements[0].Node)
	require.Equal(t, []string{"GPU-1"}, alloc.Placements[0].AcceleratorUuids)

	hidden := r.Proto(false)
	require.Nil(t, hidden.IdempotencyKey)
	require.Nil(t, hidden.RequestDigest)

	// A live job reports no end and no exit.
	r.TaskEndTime, r.CancelRequestedAt, r.TaskState = nil, nil, ptrs.Ptr(model.TaskStateActive)
	r.Tasks[0].Allocations = append(r.Tasks[0].Allocations,
		allocation("a.2", model.AllocationStatePending))
	r.AttemptID = ptrs.Ptr(model.AllocationID("a.2"))
	live := r.Proto(true)
	require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_QUEUED, live.State)
	require.Nil(t, live.EndedAt)
	require.Equal(t, taskv1.ExitClass_EXIT_CLASS_UNSPECIFIED, live.ExitClass)
	require.Equal(t, taskv1.State_STATE_QUEUED, live.Tasks[0].Allocations[1].State)
}

func TestExperimentSubmissionExitsWithLastEndedAllocation(t *testing.T) {
	r := &Record{
		JobID:             "job",
		JobType:           model.JobTypeExperiment,
		ExperimentID:      ptrs.Ptr(9),
		ExperimentState:   ptrs.Ptr(model.ErrorState),
		ExperimentEndTime: ptrs.Ptr(time.Unix(400, 0).UTC()),
		Name:              ptrs.Ptr("exp"),
		Tasks: []TaskRecord{
			{TaskID: "t1", TrialID: ptrs.Ptr(int32(1)), Allocations: []AllocationRecord{
				allocation("t1.1", "", withClass(model.ExitClassWorkloadFailed)),
			}},
			{TaskID: "t2", TrialID: ptrs.Ptr(int32(2)), Allocations: []AllocationRecord{
				allocation("t2.1", "", withClass(model.ExitClassNone)),
			}},
		},
	}
	r.Tasks[0].Allocations[0].EndTime = ptrs.Ptr(time.Unix(350, 0).UTC())
	s := r.Proto(false)
	require.Equal(t, "9", s.EntityId)
	require.Equal(t, apiv1.SubmissionState_SUBMISSION_STATE_FAILED, s.State)
	require.Equal(t, taskv1.ExitClass_EXIT_CLASS_WORKLOAD_FAILED, s.ExitClass)
	require.Equal(t, int64(400), s.EndedAt.Seconds)
	require.Equal(t, int32(2), *s.Tasks[1].TrialId)
}

// setTokenKeys gives the master a token key for the test.
func setTokenKeys(t *testing.T) {
	old := db.GetTokenKeys()
	t.Cleanup(func() { db.SetTokenKeys(old) })
	public, private, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	db.SetTokenKeys(&model.AuthTokenKeypair{PublicKey: public, PrivateKey: private})
}

func TestPageToken(t *testing.T) {
	setTokenKeys(t)
	at := time.Date(2026, 10, 1, 12, 0, 0, 123456000, time.UTC)
	for _, token := range []pageToken{
		{SubmittedAt: &at, JobID: "job-1"},
		{JobID: "job-2"},
	} {
		encoded, err := token.encode()
		require.NoError(t, err)
		got, err := decodePageToken(encoded)
		require.NoError(t, err)
		require.Equal(t, token.JobID, got.JobID)
		if token.SubmittedAt == nil {
			require.Nil(t, got.SubmittedAt)
		} else {
			require.True(t, token.SubmittedAt.Equal(*got.SubmittedAt))
		}

		// The token names no job, and a token changed in any byte is refused.
		sealed, err := base64.RawURLEncoding.DecodeString(encoded)
		require.NoError(t, err)
		require.NotContains(t, string(sealed), string(token.JobID))
		for i := range sealed {
			changed := slices.Clone(sealed)
			changed[i] ^= 1
			_, err := decodePageToken(base64.RawURLEncoding.EncodeToString(changed))
			require.Equal(t, codes.InvalidArgument, status.Code(err))
		}
	}

	got, err := decodePageToken("")
	require.NoError(t, err)
	require.Nil(t, got)
	// Neither garbage nor a token in the clear, as a client could forge one, is accepted.
	forged := base64.RawURLEncoding.EncodeToString([]byte(`{"j":"job-1"}`))
	for _, bad := range []string{"not base64!", "e30", forged} {
		_, err := decodePageToken(bad)
		require.Equal(t, codes.InvalidArgument, status.Code(err), bad)
	}

	// A token sealed under another master's key is refused.
	encoded, err := pageToken{JobID: "job-1"}.encode()
	require.NoError(t, err)
	setTokenKeys(t)
	_, err = decodePageToken(encoded)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
