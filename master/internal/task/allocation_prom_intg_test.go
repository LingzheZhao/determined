//go:build integration
// +build integration

package task

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/determined-ai/determined/master/internal/mocks"
	"github.com/determined-ai/determined/master/internal/prom"
	"github.com/determined-ai/determined/master/internal/sproto"
	"github.com/determined-ai/determined/master/pkg/aproto"
	"github.com/determined-ai/determined/master/pkg/cproto"
	"github.com/determined-ai/determined/master/pkg/device"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/master/pkg/ptrs"
)

// metricValue reads only this test's unique label set from the dedicated
// DetStateMetrics registry, independently of other allocation metrics.
func allocationMetricValue(t *testing.T, name string, labels map[string]string) (float64, bool) {
	t.Helper()
	families, err := prom.DetStateMetrics.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			found := make(map[string]string, len(metric.GetLabel()))
			for _, pair := range metric.GetLabel() {
				found[pair.GetName()] = pair.GetValue()
			}
			match := true
			for key, value := range labels {
				if found[key] != value {
					match = false
					break
				}
			}
			if match {
				return metric.GetGauge().GetValue(), true
			}
		}
	}
	return 0, false
}

func TestRestoredAllocationRebuildsResourceMetricsOnce(t *testing.T) {
	rm, _, closeDB, a := setup(t)
	defer closeDB()
	defer a.detach()
	a.req.Restore = true
	a.model.State = ptrs.Ptr(model.AllocationStateRunning)
	a.req.Name = "trial"

	resources := make(map[sproto.ResourcesID]sproto.Resources)
	ids := make([]sproto.ResourcesID, 0, 2)
	for range 2 {
		containerID := cproto.ID(uuid.NewString())
		rID := sproto.ResourcesID(containerID)
		gpuUUID := uuid.NewString()
		started := &sproto.ResourcesStarted{NativeResourcesID: uuid.NewString()}
		summary := sproto.ResourcesSummary{
			AllocationID: a.req.AllocationID, ResourcesID: rID,
			ResourcesType: sproto.ResourcesTypeDockerContainer,
			ContainerID:   &containerID, Started: started,
			AgentDevices: map[aproto.ID][]device.Device{
				"agent": {{UUID: gpuUUID, Type: device.CUDA}},
			},
		}
		resource := &mocks.Resources{}
		resource.On("Summary").Return(summary)
		resources[rID] = resource
		ids = append(ids, rID)
	}
	// A restored allocation can also contain a resource that never started.
	// Its termination must not decrement a mapping that was never registered.
	unstartedID := sproto.ResourcesID(cproto.ID(uuid.NewString()))
	unstarted := &mocks.Resources{}
	unstarted.On("Summary").Return(sproto.ResourcesSummary{
		AllocationID: a.req.AllocationID, ResourcesID: unstartedID,
		ResourcesType: sproto.ResourcesTypeDockerContainer,
	})
	resources[unstartedID] = unstarted
	require.NoError(t, a.resourcesAllocated(&sproto.ResourcesAllocated{
		ID: a.req.AllocationID, ResourcePool: a.req.ResourcePool, Resources: resources,
	}))
	allocationLabels := map[string]string{
		"allocation_id": a.req.AllocationID.String(), "task_id": a.req.TaskID.String(),
		"task_actor": a.req.Name, "job_id": a.req.JobID.String(),
	}
	allocationMetric := "det_allocation_id_task_id_task_actor"
	value, ok := allocationMetricValue(t, allocationMetric, allocationLabels)
	require.True(t, ok)
	require.Equal(t, float64(2), value)

	// A resent Running notification for a restored resource has Started set and
	// must not count again. Explicit registration is also idempotent.
	a.HandleRMEvent(&sproto.ResourcesStateChanged{ResourcesID: ids[0], ResourcesState: sproto.Running,
		ResourcesStarted: &sproto.ResourcesStarted{}})
	a.registerResourceMetrics(ids[0])
	value, _ = allocationMetricValue(t, allocationMetric, allocationLabels)
	require.Equal(t, float64(2), value)

	rm.On("Release", mock.Anything, mock.Anything).Return(nil)
	a.HandleRMEvent(&sproto.ResourcesStateChanged{ResourcesID: unstartedID,
		ResourcesState:   sproto.Terminated,
		ResourcesStopped: &sproto.ResourcesStopped{}})
	value, _ = allocationMetricValue(t, allocationMetric, allocationLabels)
	require.Equal(t, float64(2), value, "unstarted resource must not decrement mappings")
	for i, id := range ids {
		summary := resources[id].Summary()
		containerLabels := map[string]string{
			"allocation_id": a.req.AllocationID.String(), "container_id": summary.ContainerID.String(),
		}
		value, ok = allocationMetricValue(t, "det_container_id_allocation_id", containerLabels)
		require.True(t, ok)
		require.Equal(t, float64(1), value)
		runtimeLabels := map[string]string{
			"container_runtime_id": summary.Started.NativeResourcesID,
			"container_id":         summary.ContainerID.String(),
		}
		value, ok = allocationMetricValue(t, "det_container_id_runtime_container_id", runtimeLabels)
		require.True(t, ok)
		require.Equal(t, float64(1), value)
		gpuLabels := map[string]string{
			"gpu_uuid":     summary.AgentDevices["agent"][0].UUID,
			"container_id": summary.ContainerID.String(),
		}
		value, ok = allocationMetricValue(t, "det_gpu_uuid_container_id", gpuLabels)
		require.True(t, ok)
		require.Equal(t, float64(1), value)

		a.HandleRMEvent(&sproto.ResourcesStateChanged{ResourcesID: id,
			ResourcesState:   sproto.Terminated,
			ResourcesStopped: &sproto.ResourcesStopped{}})
		value, ok = allocationMetricValue(t, allocationMetric, allocationLabels)
		require.True(t, ok)
		require.Equal(t, float64(1-i), value)
		value, ok = allocationMetricValue(t, "det_container_id_allocation_id", containerLabels)
		require.True(t, ok)
		require.Zero(t, value)
		_, ok = allocationMetricValue(t, "det_container_id_runtime_container_id", runtimeLabels)
		require.False(t, ok)
		_, ok = allocationMetricValue(t, "det_gpu_uuid_container_id", gpuLabels)
		require.False(t, ok)
	}
}
