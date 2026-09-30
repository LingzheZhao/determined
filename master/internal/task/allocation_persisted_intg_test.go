//go:build integration

package task

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/internal/mocks"
	"github.com/determined-ai/determined/master/internal/sproto"
	"github.com/determined-ai/determined/master/pkg/aproto"
	"github.com/determined-ai/determined/master/pkg/cproto"
	"github.com/determined-ai/determined/master/pkg/device"
	"github.com/determined-ai/determined/master/pkg/logger"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/master/pkg/ptrs"
	"github.com/determined-ai/determined/master/pkg/tasks"
)

func TestPersistedAllocationIsLoaded(t *testing.T) {
	pgDB, closeDB := requireDeps(t)
	defer closeDB()

	taskModel := db.RequireMockTask(t, pgDB, nil)
	ar := stubAllocateRequest(taskModel)
	ar.Persisted = true
	ar.JobID = *taskModel.JobID

	// The committed row differs from the request, so an insert in place of the load would show.
	require.NoError(t, db.AddAllocation(context.TODO(), &model.Allocation{
		AllocationID: ar.AllocationID,
		TaskID:       ar.TaskID,
		Slots:        7,
		ResourcePool: "committed-pool",
		State:        ptrs.Ptr(model.AllocationStatePending),
		Ports:        map[string]int{},
	}))

	closeDB, _, id, q, exitFuture := requireStarted(t, func(a *sproto.AllocateRequest) { *a = ar })
	defer closeDB()
	defer requireKilled(t, id, exitFuture)

	row := requireDBState(t, id, model.AllocationStatePending)
	require.Equal(t, 7, row.Slots)
	require.Equal(t, "committed-pool", row.ResourcePool)

	// The task spec that the resources start with carries the job ID.
	specs := make(chan tasks.TaskSpec, 1)
	rID := sproto.ResourcesID(cproto.NewID())
	var r mocks.Resources
	r.On("Start", mock.Anything, mock.Anything, mock.Anything).Return(nil).Run(func(args mock.Arguments) {
		specs <- args.Get(1).(tasks.TaskSpec)
	}).Times(1)
	r.On("Summary").Return(sproto.ResourcesSummary{
		AllocationID:  id,
		ResourcesID:   rID,
		ResourcesType: sproto.ResourcesTypeDockerContainer,
		AgentDevices:  map[aproto.ID][]device.Device{stubAgentName: nil},
	})
	r.On("Kill", mock.Anything).Return().Run(func(_ mock.Arguments) {
		q.Put(&sproto.ResourcesStateChanged{
			ResourcesID:      rID,
			ResourcesState:   sproto.Terminated,
			ResourcesStopped: &sproto.ResourcesStopped{},
		})
	})
	q.Put(&sproto.ResourcesAllocated{
		ID:           id,
		ResourcePool: ar.ResourcePool,
		Resources:    map[sproto.ResourcesID]sproto.Resources{rID: &r},
	})

	select {
	case spec := <-specs:
		require.Equal(t, ar.JobID.String(), spec.JobID)
		require.Equal(t, ar.JobID.String(), spec.EnvVars()["DET_JOB_ID"])
	case <-time.After(5 * time.Second):
		t.Fatal("the resources never started")
	}
}

func TestPersistedAllocationMustBePending(t *testing.T) {
	pgDB, closeDB := requireDeps(t)
	defer closeDB()

	var rm mocks.ResourceManager
	start := func(ar sproto.AllocateRequest) error {
		return DefaultService.StartAllocation(
			logger.Context{}, ar, pgDB, &rm, mockTaskSpecifier{}, func(*AllocationExited) {},
		)
	}

	// No committed row.
	ar := stubAllocateRequest(db.RequireMockTask(t, pgDB, nil))
	ar.Persisted = true
	require.ErrorContains(t, start(ar), "loading persisted allocation")

	// A committed row that already ended.
	ar = stubAllocateRequest(db.RequireMockTask(t, pgDB, nil))
	ar.Persisted = true
	require.NoError(t, db.AddAllocation(context.TODO(), &model.Allocation{
		AllocationID: ar.AllocationID,
		TaskID:       ar.TaskID,
		Slots:        ar.SlotsNeeded,
		ResourcePool: ar.ResourcePool,
		State:        ptrs.Ptr(model.AllocationStateTerminated),
		Ports:        map[string]int{},
	}))
	require.ErrorContains(t, start(ar), "not pending")
	require.NotContains(t, DefaultService.GetAllAllocationIDs(), ar.AllocationID)
	rm.AssertNotCalled(t, "Allocate", mock.Anything)
}
