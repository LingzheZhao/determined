package internal

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/determined-ai/determined/master/internal/api"
	"github.com/determined-ai/determined/master/internal/api/apiutils"
	"github.com/determined-ai/determined/master/internal/authz"
	"github.com/determined-ai/determined/master/internal/command"
	"github.com/determined-ai/determined/master/internal/config"
	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/internal/experiment"
	"github.com/determined-ai/determined/master/internal/grpcutil"
	"github.com/determined-ai/determined/master/internal/rbac/audit"
	"github.com/determined-ai/determined/master/internal/submission"
	"github.com/determined-ai/determined/master/internal/task"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/master/pkg/ptrs"
	"github.com/determined-ai/determined/proto/pkg/apiv1"
	"github.com/determined-ai/determined/proto/pkg/rbacv1"
	"github.com/determined-ai/determined/proto/pkg/taskv1"
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

// GetSubmission returns a job of a managed-create kind from the database. A job the caller may not
// read is not found.
func (a *apiServer) GetSubmission(
	ctx context.Context, req *apiv1.GetSubmissionRequest,
) (*apiv1.GetSubmissionResponse, error) {
	curUser, _, err := grpcutil.GetUser(ctx)
	if err != nil {
		return nil, err
	}
	admin, err := submissionAdmin(ctx, *curUser)
	if err != nil {
		return nil, err
	}
	record, err := readableSubmission(ctx, *curUser, admin, model.JobID(req.JobId))
	if err != nil {
		return nil, err
	}
	return &apiv1.GetSubmissionResponse{
		Submission: record.Proto(showsSubmissionIdentity(*curUser, admin, record)),
	}, nil
}

// ListSubmissions lists the jobs of managed-create kinds of one owner, the caller by default, that
// the caller may read, newest first.
func (a *apiServer) ListSubmissions(
	ctx context.Context, req *apiv1.ListSubmissionsRequest,
) (*apiv1.ListSubmissionsResponse, error) {
	curUser, _, err := grpcutil.GetUser(ctx)
	if err != nil {
		return nil, err
	}
	if _, ok := apiv1.SubmissionState_name[int32(req.State)]; !ok {
		return nil, status.Errorf(codes.InvalidArgument, "unknown state %v", req.State)
	}
	admin, err := submissionAdmin(ctx, *curUser)
	if err != nil {
		return nil, err
	}
	limit := int(req.Limit)
	switch {
	case limit < 0:
		return nil, status.Error(codes.InvalidArgument, "limit must not be negative")
	case limit == 0:
		limit = submission.DefaultListLimit
	case limit > submission.MaxListLimit:
		limit = submission.MaxListLimit
	}

	filter := submission.Filter{OwnerID: &curUser.ID, Kind: req.Kind, State: req.State}
	if req.OwnerId != nil {
		filter.OwnerID = ptrs.Ptr(model.UserID(*req.OwnerId))
	}
	if req.SubmittedAfter != nil {
		filter.SubmittedAfter = ptrs.Ptr(req.SubmittedAfter.AsTime())
	}
	records, next, err := submission.List(ctx, filter, req.PageToken, limit,
		func(ctx context.Context, r *submission.Record) (bool, error) {
			return canReadSubmission(ctx, *curUser, admin, r)
		})
	if err != nil {
		return nil, err
	}

	resp := &apiv1.ListSubmissionsResponse{
		Submissions:   make([]*apiv1.Submission, 0, len(records)),
		NextPageToken: next,
	}
	for _, r := range records {
		resp.Submissions = append(resp.Submissions, r.Proto(showsSubmissionIdentity(*curUser, admin, r)))
	}
	return resp, nil
}

// CancelSubmission records that a job was asked to stop and then signals it. A job that has ended
// is returned unchanged. A generic task is canceled with its descendants, as KillGenericTask
// cancels it.
func (a *apiServer) CancelSubmission(
	ctx context.Context, req *apiv1.CancelSubmissionRequest,
) (resp *apiv1.CancelSubmissionResponse, err error) {
	defer func() {
		if status.Code(err) == codes.Unknown {
			err = apiutils.MapAndFilterErrors(err, nil, nil)
		}
	}()
	curUser, _, err := grpcutil.GetUser(ctx)
	if err != nil {
		return nil, err
	}
	admin, err := submissionAdmin(ctx, *curUser)
	if err != nil {
		return nil, err
	}
	jobID := model.JobID(req.JobId)
	record, err := readableSubmission(ctx, *curUser, admin, jobID)
	if err != nil {
		return nil, err
	}

	switch record.JobType {
	case model.JobTypeCommand, model.JobTypeShell:
		err = cancelNTSC(ctx, *curUser, record)
	case model.JobTypeGeneric:
		err = a.cancelGenericTaskJob(ctx, record)
	case model.JobTypeExperiment:
		err = cancelExperimentJob(ctx, *curUser, record)
	default:
		err = fmt.Errorf("cannot cancel a job of type %s", record.JobType)
	}
	if err != nil {
		return nil, err
	}

	if record, err = submission.Get(ctx, jobID); err != nil {
		return nil, err
	}
	return &apiv1.CancelSubmissionResponse{
		Submission: record.Proto(showsSubmissionIdentity(*curUser, admin, record)),
	}, nil
}

// readableSubmission returns a job of a managed-create kind that the caller may read. Any other
// job is not found.
func readableSubmission(
	ctx context.Context, curUser model.User, admin bool, jobID model.JobID,
) (*submission.Record, error) {
	notFound := api.NotFoundErrs("submission", jobID.String(), true)
	record, err := submission.Get(ctx, jobID)
	if errors.Is(err, db.ErrNotFound) {
		return nil, notFound
	} else if err != nil {
		return nil, err
	}
	switch ok, err := canReadSubmission(ctx, curUser, admin, record); {
	case err != nil:
		return nil, err
	case !ok:
		return nil, notFound
	}
	return record, nil
}

// canReadSubmission applies the read authorization of the job's kind. A deleted experiment has no
// experiment left to authorize against, so only its owner and admins may read it.
func canReadSubmission(
	ctx context.Context, curUser model.User, admin bool, r *submission.Record,
) (bool, error) {
	var err error
	switch {
	case r.JobType == model.JobTypeExperiment && r.ExperimentID != nil:
		exp := &model.Experiment{ID: *r.ExperimentID, JobID: r.JobID, OwnerID: r.OwnerID}
		if r.ProjectID != nil {
			exp.ProjectID = *r.ProjectID
		}
		err = experiment.AuthZProvider.Get().CanGetExperiment(ctx, curUser, exp)
	case r.JobType != model.JobTypeExperiment && r.TaskID != nil:
		var workspaceID model.AccessScopeID
		if r.WorkspaceID != nil {
			workspaceID = model.AccessScopeID(*r.WorkspaceID)
		}
		err = command.AuthZProvider.Get().CanGetNSC(
			audit.SupplyEntityID(ctx, r.TaskID.String()), curUser, workspaceID)
	default:
		return ownsSubmission(curUser, admin, r), nil
	}
	if authz.IsPermissionDenied(err) {
		return false, nil
	}
	return err == nil, err
}

// submissionAdmin reports whether the caller is an admin under the active authorization: the
// admin flag under basic authorization and under permissive, which enforces basic, and a global
// ADMINISTRATE_USER permission, as ClusterAdmin holds, under RBAC, where the flag has no effect.
func submissionAdmin(ctx context.Context, curUser model.User) (bool, error) {
	if !config.GetAuthZConfig().IsRBACEnabled() {
		return curUser.Admin, nil
	}
	err := db.DoesPermissionMatch(ctx, curUser.ID, nil,
		rbacv1.PermissionType_PERMISSION_TYPE_ADMINISTRATE_USER)
	if authz.IsPermissionDenied(err) {
		return false, nil
	}
	return err == nil, err
}

// ownsSubmission reports whether the caller owns a job or is an admin, as submissionAdmin decides.
func ownsSubmission(curUser model.User, admin bool, r *submission.Record) bool {
	return admin || r.OwnerID != nil && *r.OwnerID == curUser.ID
}

// showsSubmissionIdentity reports whether the caller sees a job's idempotency key and request
// digest: only its owner and admins do.
func showsSubmissionIdentity(curUser model.User, admin bool, r *submission.Record) bool {
	return ownsSubmission(curUser, admin, r)
}

// cancelNTSC cancels a command or shell, authorizing the caller from the database. It is the path
// of CancelSubmission, KillCommand, and KillShell, so a job that is not registered yet, or no
// longer, is canceled all the same.
func cancelNTSC(ctx context.Context, curUser model.User, r *submission.Record) error {
	var workspaceID model.AccessScopeID
	if r.WorkspaceID != nil {
		workspaceID = model.AccessScopeID(*r.WorkspaceID)
	}
	var ownerID int32
	if r.OwnerID != nil {
		ownerID = int32(*r.OwnerID)
	}
	if r.TaskID != nil {
		ctx = audit.SupplyEntityID(ctx, r.TaskID.String())
	}
	if err := command.AuthZProvider.Get().CanTerminateNSC(ctx, curUser, workspaceID); err != nil {
		return err
	}
	if err := authorizeNSCControl(ctx, curUser, workspaceID, ownerID); err != nil {
		return err
	}

	attempt, live, err := submission.CancelTask(ctx, r.JobID)
	if err != nil || !live || attempt == "" {
		return err
	}
	// An allocation that is not registered yet reads the cancel when it starts.
	err = task.DefaultService.Signal(attempt, task.KillAllocation, "user requested kill")
	if status.Code(err) == codes.NotFound {
		return nil
	}
	return err
}

// cancelGenericTaskJob cancels the generic task of a job with its descendants.
func (a *apiServer) cancelGenericTaskJob(ctx context.Context, r *submission.Record) error {
	if r.TaskID == nil {
		return nil
	}
	if !genericTaskMutation.TryLock() {
		return errGenericTaskMutationBusy
	}
	defer genericTaskMutation.Unlock()
	members, err := a.authorizedGenericTaskTree(ctx, *r.TaskID, *r.TaskID)
	if err != nil {
		return err
	}
	return cancelGenericTaskTree(ctx, members)
}

// cancelExperimentJob cancels an experiment, authorizing the caller as KillExperiment does.
func cancelExperimentJob(ctx context.Context, curUser model.User, r *submission.Record) error {
	if r.ExperimentID == nil {
		return nil
	}
	exp, err := db.ExperimentByID(ctx, *r.ExperimentID)
	if errors.Is(err, db.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	if err := experiment.AuthZProvider.Get().CanEditExperiment(ctx, curUser, exp); err != nil {
		return err
	}

	experimentID, live, err := submission.CancelExperiment(ctx, r.JobID)
	if err != nil || !live {
		return err
	}
	// An experiment that is not registered yet reads the cancel when it starts.
	if e, ok := experiment.ExperimentRegistry.Load(experimentID); ok && e != nil {
		return e.KillExperiment()
	}
	return nil
}

// ntscSubmission returns the job of a command or shell that the caller may read, for KillCommand
// and KillShell. A task the caller may not read is not found.
func ntscSubmission(
	ctx context.Context, curUser model.User, taskID string, taskType model.TaskType,
) (*submission.Record, error) {
	notFound := api.NotFoundErrs(strings.ToLower(string(taskType)), taskID, true)
	var row struct {
		JobID *model.JobID `bun:"job_id"`
	}
	err := db.Bun().NewSelect().Table("tasks").Column("job_id").
		Where("task_id = ?", taskID).
		Where("task_type = ?", taskType).
		Scan(ctx, &row)
	if errors.Is(err, sql.ErrNoRows) || err == nil && row.JobID == nil {
		return nil, notFound
	} else if err != nil {
		return nil, err
	}
	admin, err := submissionAdmin(ctx, curUser)
	if err != nil {
		return nil, err
	}
	record, err := readableSubmission(ctx, curUser, admin, *row.JobID)
	if status.Code(err) == codes.NotFound {
		return nil, notFound
	}
	return record, err
}

// ntscStateProto is the state a killed command or shell that is missing from the registry reports.
func ntscStateProto(r *submission.Record) taskv1.State {
	switch r.State() {
	case apiv1.SubmissionState_SUBMISSION_STATE_QUEUED:
		return taskv1.State_STATE_QUEUED
	case apiv1.SubmissionState_SUBMISSION_STATE_RUNNING:
		return taskv1.State_STATE_RUNNING
	default:
		return taskv1.State_STATE_TERMINATED
	}
}

// valueOf returns the value p points to, or the zero value if p is nil.
func valueOf[T any](p *T) T {
	var v T
	if p != nil {
		v = *p
	}
	return v
}
