package submission

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/master/pkg/ptrs"
	"github.com/determined-ai/determined/proto/pkg/apiv1"
	"github.com/determined-ai/determined/proto/pkg/taskv1"
)

const (
	// DefaultListLimit is the page size of ListSubmissions when the request sets none.
	DefaultListLimit = 100
	// MaxListLimit is the largest page size of ListSubmissions.
	MaxListLimit = 1000
	// listScanBatches bounds how many batches one page scans for jobs that pass its filters, so
	// that a filter few jobs pass returns a short page and a token instead of reading every job.
	listScanBatches = 10
)

var queries db.StaticQueryMap

// Record is a job of a managed-create kind as the database records it.
type Record struct {
	JobID             model.JobID         `bun:"job_id"`
	JobType           model.JobType       `bun:"job_type"`
	OwnerID           *model.UserID       `bun:"owner_id"`
	Owner             *string             `bun:"owner"`
	IdempotencyKey    *string             `bun:"idempotency_key"`
	RequestDigest     *string             `bun:"request_digest"`
	Admission         model.Admission     `bun:"admission"`
	CancelRequestedAt *time.Time          `bun:"cancel_requested_at"`
	TaskID            *model.TaskID       `bun:"task_id"`
	TaskState         *model.TaskState    `bun:"task_state"`
	TaskEndTime       *time.Time          `bun:"task_end_time"`
	AttemptID         *model.AllocationID `bun:"attempt_id"`
	ExperimentID      *int                `bun:"experiment_id"`
	ExperimentState   *model.State        `bun:"experiment_state"`
	ExperimentEndTime *time.Time          `bun:"experiment_end_time"`
	WorkspaceID       *int                `bun:"workspace_id"`
	ProjectID         *int                `bun:"project_id"`
	Name              *string             `bun:"name"`
	SubmittedAt       *time.Time          `bun:"submitted_at"`
	Tasks             []TaskRecord        `bun:"tasks"`
}

// TaskRecord is a task of a submitted job with its allocations, oldest first.
type TaskRecord struct {
	TaskID      model.TaskID       `json:"task_id"`
	TrialID     *int32             `json:"trial_id"`
	Allocations []AllocationRecord `json:"allocations"`
}

// AllocationRecord is an allocation of a submitted job's task.
type AllocationRecord struct {
	AllocationID model.AllocationID     `json:"allocation_id"`
	State        *model.AllocationState `json:"state"`
	IsReady      *bool                  `json:"is_ready"`
	StartTime    *time.Time             `json:"start_time"`
	EndTime      *time.Time             `json:"end_time"`
	Slots        int32                  `json:"slots"`
	ResourcePool string                 `json:"resource_pool"`
	ExitReason   *string                `json:"exit_reason"`
	ExitErr      *string                `json:"exit_error"`
	StatusCode   *int32                 `json:"status_code"`
	ExitClass    *model.ExitClass       `json:"exit_class"`
	ExitDetail   *model.ExitDetail      `json:"exit_detail"`
	Placements   []PlacementRecord      `json:"placements"`
}

// PlacementRecord is where a container of an allocation was placed.
type PlacementRecord struct {
	Node             string   `json:"node"`
	AcceleratorUUIDs []string `json:"accelerator_uuids"`
}

// placed reports whether the allocation was ever placed. A state the allocation never recorded is
// taken as placed, as restore takes it.
func (a *AllocationRecord) placed() bool {
	return a.State == nil || *a.State != model.AllocationStatePending
}

// ended reports whether the allocation has ended.
func (a *AllocationRecord) ended() bool {
	return a.EndTime != nil || a.State != nil && *a.State == model.AllocationStateTerminated
}

// failed reports whether the allocation ended with a failure. An allocation that ended before exit
// classes were recorded failed only if it recorded an error.
func (a *AllocationRecord) failed() bool {
	if a.ExitClass == nil || *a.ExitClass == "" {
		return a.ExitErr != nil
	}
	return *a.ExitClass != model.ExitClassNone
}

// Filter selects the jobs that List returns. Every field is optional.
type Filter struct {
	OwnerID        *model.UserID
	Kind           apiv1.SubmissionKind
	State          apiv1.SubmissionState
	SubmittedAfter *time.Time
}

// queryArgs are the named arguments of get_submissions.sql.
type queryArgs struct {
	JobID            *string    `bun:"job_id"`
	OwnerID          *int       `bun:"owner_id"`
	JobType          *string    `bun:"job_type"`
	SubmittedAfter   *time.Time `bun:"submitted_after"`
	AfterSubmittedAt *time.Time `bun:"after_submitted_at"`
	AfterJobID       *string    `bun:"after_job_id"`
	Limit            int        `bun:"limit"`
}

func query(ctx context.Context, args queryArgs) ([]*Record, error) {
	var records []*Record
	if err := db.Bun().NewRaw(queries.GetOrLoad("get_submissions"), &args).
		Scan(ctx, &records); err != nil {
		return nil, fmt.Errorf("reading submissions: %w", err)
	}
	return records, nil
}

// Get returns the job with the given ID if it is of a managed-create kind, or db.ErrNotFound.
func Get(ctx context.Context, jobID model.JobID) (*Record, error) {
	id := jobID.String()
	records, err := query(ctx, queryArgs{JobID: &id, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, db.ErrNotFound
	}
	return records[0], nil
}

// pageToken is the position after which a page of List starts: the last job the previous page
// examined, whether or not it returned that job.
type pageToken struct {
	SubmittedAt *time.Time  `json:"t,omitempty"`
	JobID       model.JobID `json:"j"`
}

func (p pageToken) encode() string {
	b, err := json.Marshal(p)
	if err != nil {
		panic(fmt.Sprintf("encoding a page token: %s", err))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodePageToken(token string) (*pageToken, error) {
	if token == "" {
		return nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(token)
	var p pageToken
	if err == nil {
		err = json.Unmarshal(b, &p)
	}
	if err != nil || p.JobID == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid page_token")
	}
	return &p, nil
}

// List returns up to limit jobs that match the filter and that include accepts, newest first,
// starting after pageToken, and the token of the next page, which is empty on the last page. It
// examines a bounded number of jobs, so a page can be short and still have a next page.
func List(
	ctx context.Context, filter Filter, token string, limit int,
	include func(context.Context, *Record) (bool, error),
) ([]*Record, string, error) {
	after, err := decodePageToken(token)
	if err != nil {
		return nil, "", err
	}
	args := queryArgs{SubmittedAfter: filter.SubmittedAfter, Limit: max(limit, DefaultListLimit)}
	if filter.OwnerID != nil {
		args.OwnerID = (*int)(filter.OwnerID)
	}
	if filter.Kind != apiv1.SubmissionKind_SUBMISSION_KIND_UNSPECIFIED {
		jobType, ok := jobTypes[filter.Kind]
		if !ok {
			return nil, "", status.Errorf(codes.InvalidArgument, "unknown kind %v", filter.Kind)
		}
		args.JobType = (*string)(&jobType)
	}

	var page []*Record
	for batch := 0; batch < listScanBatches; batch++ {
		if after != nil {
			args.AfterSubmittedAt = after.SubmittedAt
			args.AfterJobID = (*string)(&after.JobID)
		}
		records, err := query(ctx, args)
		if err != nil {
			return nil, "", err
		}
		for _, r := range records {
			after = &pageToken{SubmittedAt: r.SubmittedAt, JobID: r.JobID}
			if filter.State != apiv1.SubmissionState_SUBMISSION_STATE_UNSPECIFIED &&
				r.State() != filter.State {
				continue
			}
			ok, err := include(ctx, r)
			if err != nil {
				return nil, "", err
			}
			if !ok {
				continue
			}
			page = append(page, r)
			if len(page) == limit {
				return page, after.encode(), nil
			}
		}
		if len(records) < args.Limit {
			return page, "", nil
		}
	}
	return page, after.encode(), nil
}

var jobTypes = map[apiv1.SubmissionKind]model.JobType{
	apiv1.SubmissionKind_SUBMISSION_KIND_COMMAND:    model.JobTypeCommand,
	apiv1.SubmissionKind_SUBMISSION_KIND_SHELL:      model.JobTypeShell,
	apiv1.SubmissionKind_SUBMISSION_KIND_GENERIC:    model.JobTypeGeneric,
	apiv1.SubmissionKind_SUBMISSION_KIND_EXPERIMENT: model.JobTypeExperiment,
}

// Kind returns the kind of the job.
func (r *Record) Kind() apiv1.SubmissionKind {
	for kind, jobType := range jobTypes {
		if jobType == r.JobType {
			return kind
		}
	}
	return apiv1.SubmissionKind_SUBMISSION_KIND_UNSPECIFIED
}

// attempt returns the allocation that command_state names for a task, or the task's last
// allocation if command_state names none.
func (r *Record) attempt() *AllocationRecord {
	if r.TaskID == nil {
		return nil
	}
	for i := range r.Tasks {
		t := &r.Tasks[i]
		if t.TaskID != *r.TaskID || len(t.Allocations) == 0 {
			continue
		}
		if r.AttemptID != nil {
			for j := range t.Allocations {
				if t.Allocations[j].AllocationID == *r.AttemptID {
					return &t.Allocations[j]
				}
			}
		}
		return &t.Allocations[len(t.Allocations)-1]
	}
	return nil
}

// ResourcePool returns the resource pool of a task's current attempt.
func (r *Record) ResourcePool() string {
	if a := r.attempt(); a != nil {
		return a.ResourcePool
	}
	return ""
}

// lastEnded returns the allocation of the job that ended last, which ended an experiment.
func (r *Record) lastEnded() *AllocationRecord {
	var last *AllocationRecord
	for i := range r.Tasks {
		for j := range r.Tasks[i].Allocations {
			a := &r.Tasks[i].Allocations[j]
			if a.EndTime != nil && (last == nil || a.EndTime.After(*last.EndTime)) {
				last = a
			}
		}
	}
	return last
}

// paused reports whether the job is a paused generic task. A pause ends the task's allocation and
// sets the task's end time, but the task has not ended.
func (r *Record) paused() bool {
	return r.JobType == model.JobTypeGeneric && r.TaskState != nil &&
		(*r.TaskState == model.TaskStatePaused || *r.TaskState == model.TaskStateStoppingPaused)
}

// Ended reports whether the job has ended.
func (r *Record) Ended() bool {
	switch r.State() {
	case apiv1.SubmissionState_SUBMISSION_STATE_COMPLETED,
		apiv1.SubmissionState_SUBMISSION_STATE_FAILED,
		apiv1.SubmissionState_SUBMISSION_STATE_CANCELED,
		apiv1.SubmissionState_SUBMISSION_STATE_DELETED:
		return true
	default:
		return false
	}
}

// State derives the state of the job from the database.
func (r *Record) State() apiv1.SubmissionState {
	if r.JobType == model.JobTypeExperiment {
		return r.experimentState()
	}
	return r.taskState()
}

// taskState derives the state of a command, shell, or generic task. Its job has ended exactly
// when the task's end time is set, except for a paused generic task. A live task runs while the
// attempt that command_state names is placed and has not ended, and is queued otherwise, including
// between attempts and while an unpause starts its next attempt. An ended task was canceled if it
// was asked to stop, and otherwise completed or failed by its last attempt's exit class.
func (r *Record) taskState() apiv1.SubmissionState {
	if r.paused() {
		return apiv1.SubmissionState_SUBMISSION_STATE_PAUSED
	}
	attempt := r.attempt()
	if r.TaskEndTime == nil {
		if attempt != nil && attempt.placed() && !attempt.ended() {
			return apiv1.SubmissionState_SUBMISSION_STATE_RUNNING
		}
		return apiv1.SubmissionState_SUBMISSION_STATE_QUEUED
	}
	switch {
	case r.CancelRequestedAt != nil:
		return apiv1.SubmissionState_SUBMISSION_STATE_CANCELED
	case attempt != nil && attempt.failed():
		return apiv1.SubmissionState_SUBMISSION_STATE_FAILED
	default:
		return apiv1.SubmissionState_SUBMISSION_STATE_COMPLETED
	}
}

// experimentState derives the state of an experiment from experiments.state. Deleting an
// experiment deletes its row but keeps its job.
func (r *Record) experimentState() apiv1.SubmissionState {
	if r.ExperimentState == nil {
		return apiv1.SubmissionState_SUBMISSION_STATE_DELETED
	}
	switch *r.ExperimentState {
	case model.ActiveState, model.RunningState, model.StoppingKilledState,
		model.StoppingCanceledState, model.StoppingCompletedState, model.StoppingErrorState:
		return apiv1.SubmissionState_SUBMISSION_STATE_RUNNING
	case model.PausedState:
		return apiv1.SubmissionState_SUBMISSION_STATE_PAUSED
	case model.CompletedState:
		return apiv1.SubmissionState_SUBMISSION_STATE_COMPLETED
	case model.CanceledState:
		return apiv1.SubmissionState_SUBMISSION_STATE_CANCELED
	case model.ErrorState:
		return apiv1.SubmissionState_SUBMISSION_STATE_FAILED
	case model.DeletingState, model.DeleteFailedState, model.DeletedState,
		model.PartiallyDeletedState:
		return apiv1.SubmissionState_SUBMISSION_STATE_DELETED
	default:
		return apiv1.SubmissionState_SUBMISSION_STATE_UNSPECIFIED
	}
}

// Proto returns the job as a Submission. The idempotency key and request digest are included only
// when showIdentity is set, for the job's owner and admins.
func (r *Record) Proto(showIdentity bool) *apiv1.Submission {
	state := r.State()
	s := &apiv1.Submission{
		JobId:     r.JobID.String(),
		Kind:      r.Kind(),
		Admission: admissionProto(r.Admission),
		State:     state,
	}
	if r.OwnerID != nil {
		s.OwnerId = int32(*r.OwnerID)
	}
	if r.Owner != nil {
		s.Owner = *r.Owner
	}
	if r.WorkspaceID != nil {
		s.WorkspaceId = int32(*r.WorkspaceID)
	}
	if r.ProjectID != nil {
		s.ProjectId = ptrs.Ptr(int32(*r.ProjectID))
	}
	if r.Name != nil {
		s.Name = *r.Name
	}
	if showIdentity {
		s.IdempotencyKey = r.IdempotencyKey
		s.RequestDigest = r.RequestDigest
	}
	if r.SubmittedAt != nil {
		s.SubmittedAt = timestamppb.New(*r.SubmittedAt)
	}

	var ending *AllocationRecord
	switch {
	case r.JobType == model.JobTypeExperiment:
		if r.ExperimentID != nil {
			s.EntityId = strconv.Itoa(*r.ExperimentID)
		}
		if r.Ended() {
			if r.ExperimentEndTime != nil {
				s.EndedAt = timestamppb.New(*r.ExperimentEndTime)
			}
			ending = r.lastEnded()
		}
	case r.TaskID != nil:
		s.EntityId = r.TaskID.String()
		if s.Name == "" && r.JobType == model.JobTypeGeneric {
			// Generic tasks have no name of their own; this is the one their allocations carry.
			s.Name = fmt.Sprintf("Generic Task %s", *r.TaskID)
		}
		if r.Ended() {
			if r.TaskEndTime != nil {
				s.EndedAt = timestamppb.New(*r.TaskEndTime)
			}
			ending = r.attempt()
		}
	}
	if ending != nil {
		s.ExitClass = ending.ExitClass.Proto()
		if ending.ExitReason != nil {
			s.ExitReason = *ending.ExitReason
		}
	}

	s.Tasks = make([]*apiv1.SubmissionTask, 0, len(r.Tasks))
	for _, t := range r.Tasks {
		task := &apiv1.SubmissionTask{
			TaskId:      t.TaskID.String(),
			TrialId:     t.TrialID,
			Allocations: make([]*taskv1.Allocation, 0, len(t.Allocations)),
		}
		for i := range t.Allocations {
			task.Allocations = append(task.Allocations, t.Allocations[i].proto(t.TaskID))
		}
		s.Tasks = append(s.Tasks, task)
	}
	return s
}

func (a *AllocationRecord) proto(taskID model.TaskID) *taskv1.Allocation {
	p := &taskv1.Allocation{
		TaskId:       taskID.String(),
		AllocationId: a.AllocationID.String(),
		IsReady:      a.IsReady,
		Slots:        a.Slots,
		ResourcePool: a.ResourcePool,
		ExitReason:   a.ExitReason,
		StatusCode:   a.StatusCode,
		ExitClass:    a.ExitClass.Proto(),
		ExitDetail:   a.ExitDetail.Proto(),
		Placements:   make([]*taskv1.Placement, 0, len(a.Placements)),
	}
	switch {
	case a.State == nil:
	case *a.State == model.AllocationStatePending || *a.State == model.AllocationStateAssigned:
		// As GetTask reports them.
		p.State = taskv1.State_STATE_QUEUED
	default:
		p.State = a.State.Proto()
	}
	if a.StartTime != nil {
		p.StartTime = ptrs.Ptr(a.StartTime.UTC().Format(time.RFC3339Nano))
	}
	if a.EndTime != nil {
		p.EndTime = ptrs.Ptr(a.EndTime.UTC().Format(time.RFC3339Nano))
	}
	for _, placement := range a.Placements {
		p.Placements = append(p.Placements, &taskv1.Placement{
			Node:             placement.Node,
			AcceleratorUuids: placement.AcceleratorUUIDs,
		})
	}
	return p
}

func admissionProto(admission model.Admission) apiv1.Admission {
	switch admission {
	case model.AdmissionQueue:
		return apiv1.Admission_ADMISSION_QUEUE
	case model.AdmissionImmediate:
		return apiv1.Admission_ADMISSION_IMMEDIATE
	default:
		return apiv1.Admission_ADMISSION_UNSPECIFIED
	}
}
