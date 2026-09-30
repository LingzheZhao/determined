package model

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/uptrace/bun"

	"github.com/determined-ai/determined/proto/pkg/jobv1"
)

// JobID is the unique ID of a job among all jobs.
type JobID string

// String represents the job ID as a string.
func (id JobID) String() string {
	return string(id)
}

// JobType is the type of a job.
type JobType string

// NewJobID returns a random, globally unique job ID.
func NewJobID() JobID {
	return JobID(uuid.New().String())
}

const (
	// JobTypeNotebook is the "NOTEBOOK" job type for the enum public.job_type in Postgres.
	JobTypeNotebook JobType = "NOTEBOOK"
	// JobTypeShell is the "SHELL" job type for the enum public.job_type in Postgres.
	JobTypeShell JobType = "SHELL"
	// JobTypeCommand is the "COMMAND" job type for the enum public.job_type in Postgres.
	JobTypeCommand JobType = "COMMAND"
	// JobTypeTensorboard is the "TENSORBOARD" job type for the enum.job_type in Postgres.
	JobTypeTensorboard JobType = "TENSORBOARD"
	// JobTypeExperiment is the "EXPERIMENT" job type for the enum.job_type in Postgres.
	JobTypeExperiment JobType = "EXPERIMENT"
	// JobTypeCheckpointGC is the "CheckpointGC" job type for enum.job_type in Postgres.
	JobTypeCheckpointGC JobType = "CHECKPOINT_GC"
	// JobTypeGeneric is the "GENERIC" job type for enum.job_type in Postgres.
	JobTypeGeneric JobType = "GENERIC"
)

// Proto returns the proto representation of the job type.
func (jt JobType) Proto() jobv1.Type {
	switch jt {
	case JobTypeExperiment:
		return jobv1.Type_TYPE_EXPERIMENT
	case JobTypeCommand:
		return jobv1.Type_TYPE_COMMAND
	case JobTypeShell:
		return jobv1.Type_TYPE_SHELL
	case JobTypeNotebook:
		return jobv1.Type_TYPE_NOTEBOOK
	case JobTypeTensorboard:
		return jobv1.Type_TYPE_TENSORBOARD
	case JobTypeCheckpointGC:
		return jobv1.Type_TYPE_CHECKPOINT_GC
	case JobTypeGeneric:
		return jobv1.Type_TYPE_GENERIC
	default:
		panic("unknown job type")
	}
}

// JobTypeFromProto maps a jobv1.Type to JobType.
func JobTypeFromProto(t jobv1.Type) JobType {
	switch t {
	case jobv1.Type_TYPE_EXPERIMENT:
		return JobTypeExperiment
	case jobv1.Type_TYPE_COMMAND:
		return JobTypeCommand
	case jobv1.Type_TYPE_SHELL:
		return JobTypeShell
	case jobv1.Type_TYPE_NOTEBOOK:
		return JobTypeNotebook
	case jobv1.Type_TYPE_TENSORBOARD:
		return JobTypeTensorboard
	case jobv1.Type_TYPE_CHECKPOINT_GC:
		return JobTypeCheckpointGC
	default:
		panic("unknown job type")
	}
}

// Admission is how the master admits a submitted job to the scheduler, stored in jobs.admission.
type Admission string

const (
	// AdmissionQueue queues the job until the scheduler places it. It is the default.
	AdmissionQueue Admission = "QUEUE"
	// AdmissionImmediate places the job now or fails it, never queueing it.
	AdmissionImmediate Admission = "IMMEDIATE"
)

// Job is the model for a job in the database.
type Job struct {
	bun.BaseModel `bun:"table:jobs"`

	JobID   JobID           `db:"job_id" bun:"job_id,pk"`
	JobType JobType         `db:"job_type" bun:"job_type"`
	OwnerID *UserID         `db:"owner_id" bun:"owner_id"`
	QPos    decimal.Decimal `db:"q_position" bun:"q_position"`

	// IdempotencyKey and RequestDigest identify a managed create. Both are nil for jobs created
	// without a key or digest, including every job created before they were recorded.
	IdempotencyKey *string `db:"idempotency_key" bun:"idempotency_key"`
	RequestDigest  *string `db:"request_digest" bun:"request_digest"`
	// CancelRequestedAt is when the job was first asked to stop.
	CancelRequestedAt *time.Time `db:"cancel_requested_at" bun:"cancel_requested_at"`
	// Admission is empty on insert for the column default, QUEUE.
	Admission Admission `db:"admission" bun:"admission,nullzero"`
}
