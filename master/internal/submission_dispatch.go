package internal

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/determined-ai/determined/master/internal/command"
	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/internal/experiment"
	"github.com/determined-ai/determined/master/internal/job/jobservice"
	"github.com/determined-ai/determined/master/internal/rm/tasklist"
	"github.com/determined-ai/determined/master/internal/submission"
	"github.com/determined-ai/determined/master/internal/task"
	"github.com/determined-ai/determined/master/internal/telemetry"
	"github.com/determined-ai/determined/master/internal/user"
	"github.com/determined-ai/determined/master/internal/webhooks"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/master/pkg/ptrs"
)

// Dispatch starts committed jobs; see submission.Dispatch. Commands and shells are dispatched by
// the command service, and the rest here. Each start reads what it starts from the database after
// it checks what is registered: an allocation leaves the allocation service only after it records
// its end, so the read sees an end that the check missed.

// dispatchGenericTask starts the attempt that command_state names for the committed generic task
// of a job, as the command service's dispatch does for commands. A task that an unpause is
// resuming is left to the unpause, which registers the new allocation before command_state names
// it.
func (m *Master) dispatchGenericTask(ctx context.Context, jobID model.JobID) error {
	snapshot, err := liveGenericTaskSnapshot(ctx, jobID)
	if err != nil || snapshot == nil || task.IsRegistered(snapshot.AllocationID) {
		return err
	}
	attempt := snapshot.AllocationID
	if snapshot, err = liveGenericTaskSnapshot(ctx, jobID); err != nil || snapshot == nil ||
		snapshot.AllocationID != attempt {
		return err
	}

	cancelRequested := snapshot.Task.Job.CancelRequestedAt != nil
	taskState := model.TaskStateActive
	if snapshot.Task.State != nil {
		taskState = *snapshot.Task.State
	}
	switch command.DecideRestore(
		&snapshot.Allocation, cancelRequested || genericTaskStopping(taskState),
	) {
	case command.RestoreRequeue:
		err := m.startRestoredGenericTask(ctx, snapshot, false)
		if errors.Is(err, task.ErrAllocationRegistered) {
			// Another instance of the allocation runs, and its record is not this start's to end.
			return nil
		} else if err != nil {
			failGenericTaskStart(ctx, snapshot, err)
			return err
		}
		jobservice.DefaultService.RegisterJob(jobID, snapshot.GenericTaskSpec)
	case command.RestoreStopQueued:
		endQueuedGenericTask(ctx, snapshot, taskState, cancelRequested)
	case command.RestoreEnded, command.RestorePlaced:
		// An ended attempt is its exit handler's to finish, and a placed one is restored only when
		// the master starts.
	}
	return nil
}

// liveGenericTaskSnapshot returns the command_state of the generic task of a job, with its
// attempt, if the task has not ended and no unpause is resuming it.
func liveGenericTaskSnapshot(
	ctx context.Context, jobID model.JobID,
) (*command.CommandSnapshot, error) {
	var snapshots []command.CommandSnapshot
	if err := command.SelectLiveSnapshots(&snapshots).
		Where("command_snapshot.generic_task_spec IS NOT NULL").
		Where("task.job_id = ?", jobID).
		Where(`NOT EXISTS (SELECT 1 FROM generic_task_resume AS r
			WHERE r.task_id = command_snapshot.task_id AND NOT r.completed)`).
		Scan(ctx); err != nil {
		return nil, fmt.Errorf("reading the generic task of job %s: %w", jobID, err)
	}
	if len(snapshots) == 0 || snapshots[0].Task.Job == nil {
		return nil, nil
	}
	return &snapshots[0], nil
}

// failGenericTaskStart ends a committed generic task whose start failed, so that it reads as ended
// rather than open, and deletes its user session.
func failGenericTaskStart(ctx context.Context, snapshot *command.CommandSnapshot, cause error) {
	syslog := log.WithField("task-id", snapshot.TaskID).WithError(cause)
	syslog.Error("generic task failed to start")
	if err := db.FailTaskStart(
		ctx, snapshot.TaskID, snapshot.AllocationID, ptrs.Ptr(model.TaskStateError), cause,
	); err != nil {
		syslog.WithError(err).Error("ending a generic task that failed to start")
	}
	if err := user.DeleteSessionByToken(
		ctx, snapshot.GenericTaskSpec.Base.UserSessionToken,
	); err != nil {
		syslog.WithError(err).Error("deleting the user session of a generic task that failed to start")
	}
}

// registeredExperiments holds the ID of every experiment registered since the master started. An
// experiment leaves the registry before it records its terminal state, so an experiment that
// left the registry can read as live; a dispatch never starts an experiment that was registered.
var registeredExperiments sync.Map

// unstartedExperiment returns the committed experiment of a job if it was never started by this
// master: it is not registered and never was, and it is managed and ACTIVE or PAUSED, the states
// a create commits. It returns nil otherwise.
func unstartedExperiment(ctx context.Context, jobID model.JobID) (*model.Experiment, error) {
	var id int
	err := db.Bun().NewSelect().Table("experiments").Column("id").
		Where("job_id = ?", jobID).
		Scan(ctx, &id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("finding the experiment of job %s: %w", jobID, err)
	}
	if _, ok := experiment.ExperimentRegistry.Load(id); ok {
		return nil, nil
	}
	if _, ok := registeredExperiments.Load(id); ok {
		return nil, nil
	}

	exp, err := db.ExperimentByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("reading experiment %d: %w", id, err)
	}
	if exp.Unmanaged || exp.State != model.ActiveState && exp.State != model.PausedState {
		return nil, nil
	}
	return exp, nil
}

// dispatchExperiment starts the committed experiment of a job that was never started, from the
// database, as a restore does. It is the dispatch of replays and the sweep; the create starts its
// experiment from the plan it holds, which also carries the environment of the caller's session.
// If the start fails, the experiment is marked ERROR.
func (m *Master) dispatchExperiment(ctx context.Context, jobID model.JobID) error {
	exp, err := unstartedExperiment(ctx, jobID)
	if err != nil || exp == nil {
		return err
	}

	if err := m.restoreExperiment(exp); err != nil {
		m.endFailedExperimentStart(exp.ID, jobID, err)
		return err
	}

	// The create reports the experiment as it starts it, so an experiment started here was never
	// reported.
	activeConfig, err := m.db.ActiveExperimentConfig(exp.ID)
	if err != nil {
		log.WithField("experiment-id", exp.ID).WithError(err).
			Error("reading the config of a dispatched experiment")
		return nil
	}
	telemetry.ReportExperimentCreated(exp.ID, activeConfig)
	if exp.State == model.ActiveState {
		telemetry.ReportExperimentStateChanged(m.db, exp)
		if err := webhooks.ReportExperimentStateChanged(ctx, *exp, activeConfig); err != nil {
			log.WithError(err).Error("failed to send experiment state change webhook")
		}
	}
	return nil
}

// dispatchCreatedExperiment starts the experiment that a create committed, from the plan the
// create holds, unless a replay or the sweep already started it.
func (a *apiServer) dispatchCreatedExperiment(
	ctx context.Context, jobID model.JobID, p *preparedExperiment, owner *model.User, activate bool,
) error {
	exp, err := unstartedExperiment(ctx, jobID)
	if err != nil || exp == nil {
		return err
	}
	return a.startExperiment(ctx, p, owner, activate)
}

// endFailedExperimentStart marks a committed experiment whose start failed ERROR, unless the failed
// start already ended it, and releases its registrations.
func (m *Master) endFailedExperimentStart(experimentID int, jobID model.JobID, cause error) {
	syslog := log.WithField("experiment-id", experimentID).WithError(cause)
	syslog.Error("experiment failed to start")

	exp, err := db.ExperimentByID(context.TODO(), experimentID)
	if err != nil {
		syslog.WithError(err).Error("reading an experiment that failed to start")
	} else if !model.TerminalStates[exp.State] {
		if err := m.db.TerminateExperimentInRestart(exp.ID, model.ErrorState); err != nil {
			syslog.WithError(err).Error("marking an experiment that failed to start as errored")
		}
		exp.State = model.ErrorState
		telemetry.ReportExperimentStateChanged(m.db, exp)
	}

	if _, ok := tasklist.GroupPriorityChangeRegistry.Load(jobID); ok {
		if err := tasklist.GroupPriorityChangeRegistry.Delete(jobID); err != nil {
			syslog.WithError(err).Error("deleting group priority change registry")
		}
	}
	jobservice.DefaultService.UnregisterJob(jobID)
}

// sweepSubmissions dispatches, every submission.SweepInterval, the committed jobs that nothing
// started, so that a committed submission always progresses without a master restart: a create
// that lost its start and was never replayed is started here.
func (m *Master) sweepSubmissions(ctx context.Context) {
	t := time.NewTicker(submission.SweepInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-ctx.Done():
			return
		}
		m.dispatchUnstarted(ctx, time.Now().Add(-submission.SweepInterval))
	}
}

// dispatchUnstarted dispatches the committed jobs submitted before a time whose current attempt is
// PENDING and not registered, or whose experiment is not registered. A dispatch that fails ends
// its job, so it is logged and the pass goes on.
func (m *Master) dispatchUnstarted(ctx context.Context, before time.Time) {
	jobs, err := submission.ListUnstarted(ctx, before)
	if err != nil {
		log.WithError(err).Error("listing committed jobs to dispatch")
		return
	}
	registered := map[model.AllocationID]bool{}
	for _, id := range task.DefaultService.GetAllAllocationIDs() {
		registered[id] = true
	}

	for _, j := range jobs {
		var start submission.StartFunc
		switch {
		case j.AllocationID != nil && registered[*j.AllocationID]:
			continue
		case j.ExperimentID != nil:
			if _, ok := experiment.ExperimentRegistry.Load(*j.ExperimentID); ok {
				continue
			}
			start = m.dispatchExperiment
		case j.JobType == model.JobTypeGeneric:
			start = m.dispatchGenericTask
		default:
			start = command.DefaultCmdService.Dispatch
		}
		if err := submission.Dispatch(ctx, j.JobID, start); err != nil {
			log.WithField("job-id", j.JobID).WithError(err).Error("dispatching a committed job")
		}
	}
}
