package submission

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/uptrace/bun"

	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/pkg/model"
)

// SweepInterval is how often the master dispatches committed jobs that nothing started, and how
// old such a job must be: a younger one may still be started by the handler that committed it.
const SweepInterval = 30 * time.Second

// StartFunc starts a committed job: the attempt that its command_state names, or its experiment.
// It does nothing if that attempt has ended or is registered with the allocation service, or the
// experiment is registered, so that each job has at most one runtime instance however many times
// it is dispatched. If the start fails in the master, it ends the job, so that it reads as failed
// rather than queued.
type StartFunc func(ctx context.Context, jobID model.JobID) error

// Dispatch runs start for a committed job. Three callers dispatch: the handler after its commit,
// every replay, and the master's sweep. start runs detached from ctx, so a client that disconnects
// after the commit cannot stop it, and serialized with every other dispatch of the job, so that
// its check of what is registered holds while it starts the job.
func Dispatch(ctx context.Context, jobID model.JobID, start StartFunc) error {
	unlock := LockJob(jobID)
	defer unlock()
	return start(context.WithoutCancel(ctx), jobID)
}

var jobLocks = struct {
	sync.Mutex
	held map[model.JobID]*jobLock
}{held: map[model.JobID]*jobLock{}}

type jobLock struct {
	sync.Mutex
	users int
}

// LockJob takes the dispatch lock of a job. A start that does not go through Dispatch, such as
// continuing an ended experiment, holds it so that no dispatch starts the job at the same time.
func LockJob(jobID model.JobID) (unlock func()) {
	jobLocks.Lock()
	l := jobLocks.held[jobID]
	if l == nil {
		l = &jobLock{}
		jobLocks.held[jobID] = l
	}
	l.users++
	jobLocks.Unlock()

	l.Lock()
	return func() {
		l.Unlock()
		jobLocks.Lock()
		if l.users--; l.users == 0 {
			delete(jobLocks.held, jobID)
		}
		jobLocks.Unlock()
	}
}

// commitUnknownError is an error that leaves the outcome of a commit unknown, such as a connection
// lost during COMMIT. It is not a rollback: the job may exist.
type commitUnknownError struct {
	err error
}

func (e *commitUnknownError) Error() string {
	return fmt.Sprintf("committing the submission: %s", e.err)
}

func (e *commitUnknownError) Unwrap() error {
	return e.err
}

var afterCommit struct {
	sync.Mutex
	hook func(ctx context.Context) error
}

// SetAfterCommitHook sets a hook that runs after each commit transaction commits, and returns a
// function that removes it. An error from the hook is handled as an error that leaves the commit's
// outcome unknown. It exists for tests, which use it to lose a commit's response or disconnect
// the client right after a commit.
func SetAfterCommitHook(hook func(ctx context.Context) error) (remove func()) {
	afterCommit.Lock()
	defer afterCommit.Unlock()
	afterCommit.hook = hook
	return func() {
		afterCommit.Lock()
		defer afterCommit.Unlock()
		afterCommit.hook = nil
	}
}

// commit runs fn in a transaction and commits it. An error from fn rolls the transaction back and
// is returned as is; an error from COMMIT itself is a *commitUnknownError.
func commit(ctx context.Context, fn func(ctx context.Context, tx bun.Tx) error) error {
	tx, err := db.Bun().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			_ = tx.Rollback()
		}
	}()
	if err := fn(ctx, tx); err != nil {
		return err
	}

	done = true
	if err := tx.Commit(); err != nil {
		return &commitUnknownError{err: err}
	}

	afterCommit.Lock()
	hook := afterCommit.hook
	afterCommit.Unlock()
	if hook != nil {
		if err := hook(ctx); err != nil {
			return &commitUnknownError{err: err}
		}
	}
	return nil
}

// Unstarted is a committed job that may never have started: a command, shell, or generic task
// with its PENDING attempt, or an experiment.
type Unstarted struct {
	JobID        model.JobID         `bun:"job_id"`
	JobType      model.JobType       `bun:"job_type"`
	AllocationID *model.AllocationID `bun:"allocation_id"`
	ExperimentID *int                `bun:"experiment_id"`
}

// ListUnstarted lists the committed jobs submitted before a time that may never have started:
// commands, shells, and generic tasks whose task is open and whose attempt that command_state
// names is PENDING and open, and managed experiments that are ACTIVE or PAUSED, the states a
// create commits. Most are running; the caller skips the registered ones. A generic task that an
// unpause is resuming is the unpause's to start.
func ListUnstarted(ctx context.Context, before time.Time) ([]Unstarted, error) {
	var jobs []Unstarted
	if err := db.Bun().NewRaw(`
SELECT j.job_id, j.job_type, cs.allocation_id, NULL::int AS experiment_id
FROM jobs j
JOIN tasks t ON t.job_id = j.job_id
JOIN command_state cs ON cs.task_id = t.task_id
JOIN allocations a ON a.allocation_id = cs.allocation_id
WHERE j.job_type IN (?, ?, ?)
	AND t.end_time IS NULL AND t.start_time < ?
	AND a.state = ? AND a.end_time IS NULL
	AND NOT EXISTS (SELECT 1 FROM generic_task_resume r WHERE r.task_id = t.task_id AND NOT r.completed)
UNION ALL
SELECT j.job_id, j.job_type, NULL, e.id
FROM jobs j
JOIN experiments e ON e.job_id = j.job_id
WHERE j.job_type = ? AND NOT e.unmanaged AND e.state IN (?, ?) AND e.start_time < ?`,
		model.JobTypeCommand, model.JobTypeShell, model.JobTypeGeneric, before,
		model.AllocationStatePending,
		model.JobTypeExperiment, model.ActiveState, model.PausedState, before,
	).Scan(ctx, &jobs); err != nil {
		return nil, fmt.Errorf("listing committed jobs that may not have started: %w", err)
	}
	return jobs, nil
}
