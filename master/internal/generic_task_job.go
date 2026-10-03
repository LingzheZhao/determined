package internal

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/determined-ai/determined/master/internal/configpolicy"
	"github.com/determined-ai/determined/master/internal/job/jobservice"
	"github.com/determined-ai/determined/master/internal/rm"
	"github.com/determined-ai/determined/master/internal/rm/rmerrors"
	"github.com/determined-ai/determined/master/internal/rm/tasklist"
	"github.com/determined-ai/determined/master/internal/sproto"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/master/pkg/tasks"
	"github.com/determined-ai/determined/proto/pkg/jobv1"
)

// genericTaskJobs holds the registered job of each generic task by job ID, so an allocation that
// exits after its task was unpaused (and a new allocation registered) does not unregister the new one.
var (
	genericTaskJobsMu sync.Mutex
	genericTaskJobs   = map[model.JobID]*genericTaskJob{}
)

// defaultGenericTaskWeight is the fair-share weight of a generic task that sets none, as for commands.
const defaultGenericTaskWeight = 1.0

// genericTaskJob is a running generic task in the job queue: the job service and the scheduler's
// priority-change callbacks reach the task through it. Changes are applied to the resource manager
// and to the spec, and the spec is persisted, so an unpaused or restored allocation keeps them.
type genericTaskJob struct {
	mu sync.Mutex

	rm           rm.ResourceManager
	taskID       model.TaskID
	allocationID model.AllocationID
	jobID        model.JobID
	spec         *tasks.GenericTaskSpec
	syslog       *logrus.Entry
}

// registerGenericTaskJob makes a generic task allocation visible to the job service and to the
// scheduler's priority changes. It replaces an earlier registration of the same job, e.g. of the
// allocation before a pause.
func registerGenericTaskJob(
	resourceManager rm.ResourceManager,
	taskID model.TaskID,
	allocationID model.AllocationID,
	jobID model.JobID,
	spec *tasks.GenericTaskSpec,
) error {
	// The job keeps its own copy: the allocation reads its spec when it starts the container, while
	// the job changes priority and weight; the copy is what gets persisted.
	own := *spec
	j := &genericTaskJob{
		rm:           resourceManager,
		taskID:       taskID,
		allocationID: allocationID,
		jobID:        jobID,
		spec:         &own,
		syslog: logrus.WithFields(logrus.Fields{
			"component": "genericTaskJob", "task-id": taskID, "job-id": jobID,
		}),
	}

	genericTaskJobsMu.Lock()
	defer genericTaskJobsMu.Unlock()
	_ = tasklist.GroupPriorityChangeRegistry.Delete(jobID)
	if err := tasklist.GroupPriorityChangeRegistry.Add(jobID, j.onPriorityChange); err != nil {
		return fmt.Errorf("registering priority changes of generic task %s: %w", taskID, err)
	}
	jobservice.DefaultService.RegisterJob(jobID, j)
	genericTaskJobs[jobID] = j
	return nil
}

// unregisterGenericTaskJob removes a generic task from the job service and the priority-change
// registry when its allocation exits. It does nothing if the job is not registered or is registered
// for another allocation of the task (an unpause started a new one).
func unregisterGenericTaskJob(jobID model.JobID, allocationID model.AllocationID) {
	genericTaskJobsMu.Lock()
	defer genericTaskJobsMu.Unlock()
	if j, ok := genericTaskJobs[jobID]; !ok || j.allocationID != allocationID {
		return
	}
	delete(genericTaskJobs, jobID)
	jobservice.DefaultService.UnregisterJob(jobID)
	_ = tasklist.GroupPriorityChangeRegistry.Delete(jobID)
}

// ToV1Job implements jobservice.Job.
func (j *genericTaskJob) ToV1Job() (*jobv1.Job, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	res := j.spec.GenericTaskConfig.Resources
	out := &jobv1.Job{
		JobId:          j.jobID.String(),
		EntityId:       string(j.taskID),
		Type:           model.JobTypeGeneric.Proto(),
		SubmissionTime: timestamppb.New(j.spec.RegisteredTime),
		Username:       j.spec.Base.Owner.Username,
		UserId:         int32(j.spec.Base.Owner.ID),
		Name:           j.spec.DisplayName(),
		WorkspaceId:    int32(j.spec.WorkspaceID),
		ResourcePool:   res.ResourcePool(),
		Weight:         j.weightLocked(),
		IsPreemptible:  false,
	}
	if p := res.Priority(); p != nil {
		out.Priority = int32(*p)
	}
	return out, nil
}

// SetJobPriority implements jobservice.Job: it validates the priority against the workspace's task
// config policy, applies it in the resource manager and persists it.
func (j *genericTaskJob) SetJobPriority(priority int) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	if priority < 1 || priority > 99 {
		return fmt.Errorf("priority must be between 1 and 99")
	}
	if smallerHigher, err := j.rm.SmallerValueIsHigherPriority(); err == nil {
		ok, err := configpolicy.PriorityUpdateAllowed(j.spec.WorkspaceID, model.NTSCType, priority, smallerHigher)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("priority exceeds task config policy's priority_limit")
		}
	}

	switch err := j.rm.SetGroupPriority(sproto.SetGroupPriority{
		Priority:     priority,
		ResourcePool: j.spec.GenericTaskConfig.Resources.ResourcePool(),
		JobID:        j.jobID,
	}).(type) {
	case nil:
	case rmerrors.UnsupportedError:
		j.syslog.WithError(err).Debug("ignoring unsupported call to set group priority")
	default:
		return fmt.Errorf("setting group priority for generic task: %w", err)
	}
	return j.setPriorityLocked(priority)
}

// SetWeight implements jobservice.Job: it applies the fair-share weight in the resource manager and
// persists it.
func (j *genericTaskJob) SetWeight(weight float64) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	switch err := j.rm.SetGroupWeight(sproto.SetGroupWeight{
		Weight:       weight,
		ResourcePool: j.spec.GenericTaskConfig.Resources.ResourcePool(),
		JobID:        j.jobID,
	}).(type) {
	case nil:
	case rmerrors.UnsupportedError:
		j.syslog.WithError(err).Debug("ignoring unsupported call to set group weight")
	default:
		return fmt.Errorf("setting group weight for generic task: %w", err)
	}

	old := j.spec.GenericTaskConfig.Resources.RawWeight
	j.spec.GenericTaskConfig.Resources.RawWeight = &weight
	if err := j.persistLocked(); err != nil {
		j.spec.GenericTaskConfig.Resources.RawWeight = old
		return err
	}
	return nil
}

// SetResourcePool implements jobservice.Job. Moving a generic task to another pool is not supported,
// as for commands: kill it and create it again, or fork it with a different `resources.resource_pool`.
func (j *genericTaskJob) SetResourcePool(string) error {
	return fmt.Errorf("setting resource pool for job type %s is not supported", model.JobTypeGeneric)
}

// ResourcePool implements jobservice.Job.
func (j *genericTaskJob) ResourcePool() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.spec.GenericTaskConfig.Resources.ResourcePool()
}

// onPriorityChange is the scheduler's callback for a priority set on the job's group, e.g. from the
// job queue of the web UI. The resource manager already applied it; the task records and persists it.
func (j *genericTaskJob) onPriorityChange(priority int) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.setPriorityLocked(priority)
}

func (j *genericTaskJob) setPriorityLocked(priority int) error {
	old := j.spec.GenericTaskConfig.Resources.RawPriority
	j.spec.GenericTaskConfig.Resources.RawPriority = &priority
	if err := j.persistLocked(); err != nil {
		j.spec.GenericTaskConfig.Resources.RawPriority = old
		return err
	}
	return nil
}

func (j *genericTaskJob) weightLocked() float64 {
	if w := j.spec.GenericTaskConfig.Resources.RawWeight; w != nil {
		return *w
	}
	return defaultGenericTaskWeight
}

// persistLocked writes the spec back to the task's snapshot, which unpause and master restore start
// new allocations from.
func (j *genericTaskJob) persistLocked() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return persistGenericTaskSpec(ctx, j.taskID, *j.spec, j.allocationID)
}
