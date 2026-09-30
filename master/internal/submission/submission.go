// Package submission implements the order that the managed creates (LaunchCommand, LaunchShell,
// CreateGenericTask, and CreateExperiment) follow: digest the request, replay a job submitted
// under the same idempotency key, check the plan's digest, parse, return a dry run, commit the job
// in one transaction, and dispatch it.
package submission

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"

	"github.com/jackc/pgconn"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.com/uptrace/bun"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/proto/pkg/apiv1"
)

// Protocol is the submission protocol version that GetMaster reports. Clients refuse masters
// below the version they need. It is 1 once exit classes and the submission ledger are in place;
// each later capability raises it when it lands.
const Protocol = 1

const (
	maxKeyLength = 128
	// keyIndex is the unique index on (owner_id, idempotency_key) that guards keys.
	keyIndex = "jobs_owner_idempotency_key"
)

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

// errKeyInUse is returned from the commit transaction when another submission committed the same
// key first.
var errKeyInUse = errors.New("idempotency key in use")

// ValidateKey checks that an idempotency key has at most 128 characters of [A-Za-z0-9._:-].
func ValidateKey(key string) error {
	if len(key) > maxKeyLength || !keyPattern.MatchString(key) {
		return status.Errorf(codes.InvalidArgument,
			"idempotency_key must be 1 to %d characters of [A-Za-z0-9._:-]", maxKeyLength)
	}
	return nil
}

// Submission is one managed create going through the handler order.
type Submission struct {
	kind    model.JobType
	ownerID model.UserID

	// envelope is whether the request carried submit options. Without them, a create keeps its
	// previous behavior: it has no digest and its response no submission result.
	envelope       bool
	key            string
	admission      model.Admission
	dryRun         bool
	expectedDigest string
	digest         string
	// template is the template whose content the digest binds, which the create applies instead of
	// reading the template again. It is nil without submit options or a readable template.
	template *model.Template

	jobID model.JobID
}

// NewCommand starts the submission of a LaunchCommandRequest.
func NewCommand(
	ownerID model.UserID, req *apiv1.LaunchCommandRequest, readTemplate TemplateReader,
) (*Submission, error) {
	return newSubmission(model.JobTypeCommand, ownerID, req.Submit, false, req.TemplateName,
		readTemplate, func() (map[string]any, error) {
			return commandFields(req.WorkspaceId, req.TemplateName, req.Config, req.Files), nil
		})
}

// NewShell starts the submission of a LaunchShellRequest.
func NewShell(
	ownerID model.UserID, req *apiv1.LaunchShellRequest, readTemplate TemplateReader,
) (*Submission, error) {
	return newSubmission(model.JobTypeShell, ownerID, req.Submit, false, req.TemplateName,
		readTemplate, func() (map[string]any, error) {
			return commandFields(req.WorkspaceId, req.TemplateName, req.Config, req.Files), nil
		})
}

// NewGenericTask starts the submission of a CreateGenericTaskRequest.
func NewGenericTask(ownerID model.UserID, req *apiv1.CreateGenericTaskRequest) (*Submission, error) {
	return newSubmission(model.JobTypeGeneric, ownerID, req.Submit, false, "", nil,
		func() (map[string]any, error) { return genericTaskFields(req) })
}

// NewExperiment starts the submission of a CreateExperimentRequest. validate_only is a dry run.
func NewExperiment(
	ownerID model.UserID, req *apiv1.CreateExperimentRequest, readTemplate TemplateReader,
) (*Submission, error) {
	if req.Submit != nil && req.GetUnmanaged() {
		return nil, status.Error(codes.InvalidArgument,
			"submit options apply only to managed experiments")
	}
	return newSubmission(model.JobTypeExperiment, ownerID, req.Submit, req.ValidateOnly,
		req.GetTemplate(), readTemplate,
		func() (map[string]any, error) { return experimentFields(req) })
}

func newSubmission(
	kind model.JobType, ownerID model.UserID, opts *apiv1.SubmitOptions, dryRun bool,
	templateName string, readTemplate TemplateReader, fields func() (map[string]any, error),
) (*Submission, error) {
	s := &Submission{kind: kind, ownerID: ownerID, admission: model.AdmissionQueue, dryRun: dryRun}
	if opts == nil {
		return s, nil
	}

	switch opts.Admission {
	case apiv1.Admission_ADMISSION_UNSPECIFIED, apiv1.Admission_ADMISSION_QUEUE:
	case apiv1.Admission_ADMISSION_IMMEDIATE:
		// An experiment requests its trials' allocations after the submit returns, so immediate
		// admission could only ever decide part of it.
		if kind == model.JobTypeExperiment {
			return nil, status.Error(codes.InvalidArgument,
				"experiments do not support immediate admission")
		}
		return nil, status.Error(codes.Unimplemented, "immediate admission is not supported yet")
	default:
		return nil, status.Errorf(codes.InvalidArgument, "unknown admission %v", opts.Admission)
	}
	if opts.IdempotencyKey != "" {
		if err := ValidateKey(opts.IdempotencyKey); err != nil {
			return nil, err
		}
	}

	s.envelope = true
	s.key = opts.IdempotencyKey
	s.dryRun = s.dryRun || opts.DryRun
	s.expectedDigest = opts.ExpectedDigest

	f, err := fields()
	if err != nil {
		return nil, err
	}
	if templateName != "" {
		if s.template, err = templateFields(f, templateName, readTemplate); err != nil {
			return nil, err
		}
	}
	if s.digest, err = requestDigest(kind, s.admission, f); err != nil {
		return nil, err
	}
	return s, nil
}

// DryRun is whether the request is a dry run, through dry_run or its alias validate_only.
func (s *Submission) DryRun() bool {
	return s.dryRun
}

// Template returns the template that the request digest binds, or nil if the create reads its
// template itself: without submit options, or when the caller may not read the template.
func (s *Submission) Template() *model.Template {
	return s.template
}

// Handler supplies the steps of the handler order that depend on the kind of create.
type Handler[R any] struct {
	// AuthorizeReplay checks that the caller may still read a job it submitted earlier.
	AuthorizeReplay func(ctx context.Context, job *model.Job) error
	// Replayed builds the response that replays a job, which carries its data only in the
	// submission result. Message fields that clients decode as required are set, but empty.
	Replayed func(result *apiv1.SubmitResult) R
	// Prepare parses, merges, and authorizes the request and applies config policy. It has no
	// side effects: no row, session, key, or registry entry.
	Prepare func(ctx context.Context) error
	// DryRun builds the response to a dry run. result is nil when the request had no submit
	// options.
	DryRun func(ctx context.Context, result *apiv1.SubmitResult) (R, error)
	// Commit writes the job in the commit transaction. A create that can carry a key inserts its
	// job row through InsertJobTx before anything else, so a key in use aborts it before it writes
	// more.
	Commit func(ctx context.Context, tx bun.Tx) error
	// Dispatch starts a committed job of the create's kind from what the database holds; see
	// Dispatch for what it must and must not start. A replay passes its job to it.
	Dispatch StartFunc
	// Start is Dispatch for the job that this request committed, which it may start from what the
	// handler holds in memory. nil uses Dispatch.
	Start StartFunc
	// Respond builds the response once the committed job was dispatched. result is nil when the
	// request had no submit options.
	Respond func(ctx context.Context, result *apiv1.SubmitResult) (R, error)
}

// Run takes a submission through the handler order.
func Run[R any](ctx context.Context, s *Submission, h Handler[R]) (R, error) {
	var zero R

	if resp, replayed, err := replay(ctx, s, h); replayed || err != nil {
		return resp, err
	}

	if s.expectedDigest != "" && s.expectedDigest != s.digest {
		return zero, status.Errorf(codes.FailedPrecondition,
			"plan_changed: the request digest %s differs from the expected digest %s",
			s.digest, s.expectedDigest)
	}

	if err := h.Prepare(ctx); err != nil {
		return zero, err
	}

	if s.dryRun {
		return h.DryRun(ctx, s.result())
	}

	var unknown *commitUnknownError
	switch err := commit(ctx, h.Commit); {
	case err == nil:
	case errors.Is(err, errKeyInUse):
		// The unique index, not a lock, decides between concurrent submits of one key; the
		// loser rolled back everything it wrote and returns the winner's job.
		if resp, replayed, err := replay(ctx, s, h); replayed || err != nil {
			return resp, err
		}
		return zero, status.Errorf(codes.Aborted,
			"idempotency key %q was committed concurrently; retry the request", s.key)
	case errors.As(err, &unknown):
		// The job may have been committed, so nothing is cleaned up: whether it exists decides.
		if resp, replayed, err := recoverUnknownCommit(ctx, s, h, unknown); replayed || err != nil {
			return resp, err
		}
	default:
		return zero, err
	}

	// A create that commits no job through InsertJobTx, such as an unmanaged experiment, has
	// nothing to dispatch.
	if s.jobID != "" {
		start := h.Start
		if start == nil {
			start = h.Dispatch
		}
		if err := Dispatch(ctx, s.jobID, start); err != nil {
			return zero, err
		}
	}
	return h.Respond(ctx, s.result())
}

// recoverUnknownCommit looks up the job of a commit whose outcome is unknown, such as one whose
// connection was lost during COMMIT. If the job exists, replayed is false and the handler goes on
// to dispatch it. Otherwise another submit of the key may have committed, which is replayed, and
// without either the request fails with a retryable UNAVAILABLE.
func recoverUnknownCommit[R any](
	ctx context.Context, s *Submission, h Handler[R], unknown *commitUnknownError,
) (resp R, replayed bool, err error) {
	// The request's context may be what failed the commit, and must not fail the lookup too.
	ctx = context.WithoutCancel(ctx)
	unavailable := func(cause error) (R, bool, error) {
		log.WithError(cause).WithField("job-id", s.jobID).
			Warn("the commit of a submission may not have completed")
		return resp, false, status.Errorf(codes.Unavailable,
			"the commit of the submission may not have completed (%s); retry the request", unknown)
	}

	if s.jobID == "" {
		return unavailable(unknown)
	}
	exists, err := db.Bun().NewSelect().Table("jobs").Where("job_id = ?", s.jobID).Exists(ctx)
	switch {
	case err != nil:
		return unavailable(err)
	case exists:
		return resp, false, nil
	}
	if resp, replayed, err := replay(ctx, s, h); replayed || err != nil {
		return resp, replayed, err
	}
	return unavailable(unknown)
}

// InsertJobTx inserts the job row of the submission in the commit transaction, recording its key,
// digest, and admission.
func (s *Submission) InsertJobTx(ctx context.Context, tx bun.IDB, job *model.Job) error {
	if s.key != "" {
		job.IdempotencyKey = &s.key
	}
	if s.digest != "" {
		job.RequestDigest = &s.digest
	}
	job.Admission = s.admission

	if _, err := tx.NewInsert().Model(job).Exec(ctx); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == db.CodeUniqueViolation &&
			pgErr.ConstraintName == keyIndex {
			return errKeyInUse
		}
		return fmt.Errorf("adding job: %w", err)
	}
	s.jobID = job.JobID
	return nil
}

// result returns the submission result of a dry run or a committed job, or nil when the request
// had no submit options.
func (s *Submission) result() *apiv1.SubmitResult {
	if !s.envelope {
		return nil
	}
	result := &apiv1.SubmitResult{RequestDigest: s.digest}
	if !s.dryRun {
		result.JobId = s.jobID.String()
		result.Outcome = outcome(s.admission)
	}
	return result
}

// replay returns the job submitted earlier under the request's key, or an ALREADY_EXISTS error
// if that job's request differs. replayed is false when no job has the key.
func replay[R any](ctx context.Context, s *Submission, h Handler[R]) (resp R, replayed bool, err error) {
	if s.key == "" {
		return resp, false, nil
	}
	job, err := jobByKey(ctx, s.ownerID, s.key)
	if err != nil || job == nil {
		return resp, false, err
	}

	// A plan-bound request replays the job its plan produced even if its content has changed
	// since, as when a client retries a launch whose response was lost.
	want := s.digest
	if s.expectedDigest != "" {
		want = s.expectedDigest
	}
	if job.JobType != s.kind || job.RequestDigest == nil || *job.RequestDigest != want {
		return resp, true, status.Errorf(codes.AlreadyExists,
			"idempotency key %q is already used by job %s for a different request", s.key, job.JobID)
	}
	if err := h.AuthorizeReplay(ctx, job); err != nil {
		return resp, true, err
	}
	// A job whose start was lost, such as with the response to its commit, starts here. A start
	// that fails ends the job, so the replay still returns it.
	if h.Dispatch != nil {
		if err := Dispatch(ctx, job.JobID, h.Dispatch); err != nil {
			log.WithError(err).WithField("job-id", job.JobID).Error("dispatching a replayed job")
		}
	}
	return h.Replayed(&apiv1.SubmitResult{
		JobId:         job.JobID.String(),
		Replayed:      true,
		RequestDigest: *job.RequestDigest,
		Outcome:       outcome(job.Admission),
	}), true, nil
}

func outcome(admission model.Admission) apiv1.AdmissionOutcome {
	switch admission {
	case model.AdmissionQueue:
		return apiv1.AdmissionOutcome_ADMISSION_OUTCOME_QUEUED
	default:
		return apiv1.AdmissionOutcome_ADMISSION_OUTCOME_UNSPECIFIED
	}
}

// jobByKey returns the job that an owner submitted under a key, or nil if there is none.
func jobByKey(ctx context.Context, ownerID model.UserID, key string) (*model.Job, error) {
	var job model.Job
	err := db.Bun().NewSelect().Model(&job).
		Where("owner_id = ?", ownerID).
		Where("idempotency_key = ?", key).
		Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("looking up idempotency key %q: %w", key, err)
	}
	return &job, nil
}
