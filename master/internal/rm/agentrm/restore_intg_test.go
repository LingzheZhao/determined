//go:build integration

package agentrm

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/determined-ai/determined/master/internal/config"
	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/internal/rm/rmevents"
	"github.com/determined-ai/determined/master/internal/sproto"
	"github.com/determined-ai/determined/master/internal/task/taskmodel"
	"github.com/determined-ai/determined/master/pkg/aproto"
	"github.com/determined-ai/determined/master/pkg/cproto"
	"github.com/determined-ai/determined/master/pkg/device"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/master/pkg/ptrs"
	"github.com/determined-ai/determined/master/pkg/syncx/queue"
	"github.com/determined-ai/determined/master/pkg/ws"
)

type fakeAgentSocket = ws.WebSocket[aproto.AgentMessage, *aproto.MasterMessage]

// restoreTestPool connects the test database, which an earlier test may have replaced, and returns
// a pool whose restored agents wait long enough to reconnect for any test.
func restoreTestPool(t *testing.T) config.ResourcePoolConfig {
	pgDB, closeDB := db.MustResolveTestPostgres(t)
	t.Cleanup(closeDB)
	db.MustMigrateTestPostgres(t, pgDB, "file://../../../static/migrations")

	return config.ResourcePoolConfig{
		PoolName:           "restore-" + uuid.NewString(),
		AgentReconnectWait: model.Duration(time.Hour),
		Scheduler: &config.SchedulerConfig{
			FairShare:     &config.FairShareSchedulerConfig{},
			FittingPolicy: best,
		},
	}
}

func restoreTestDevices(n int) []device.Device {
	devices := make([]device.Device, 0, n)
	for i := 0; i < n; i++ {
		devices = append(devices, device.Device{
			ID: device.ID(i), Brand: "nvda", UUID: uuid.NewString(), Type: device.CUDA,
		})
	}
	return devices
}

// startedAgentState returns the state of a new agent with devices, persisted as the agent's first
// connection persists it.
func startedAgentState(
	t *testing.T, pool string, devices []device.Device,
) *agentState {
	state := newAgentState(aproto.ID(uuid.NewString()), 0)
	state.handler = &agent{}
	state.resourcePoolName = pool
	state.agentStarted(&aproto.AgentStarted{Devices: devices, ResourcePoolName: pool})
	t.Cleanup(func() { _ = state.delete() })
	return state
}

// addRestoreTestAllocation adds a task and an open allocation in state.
func addRestoreTestAllocation(
	t *testing.T, state model.AllocationState,
) model.AllocationID {
	ctx := context.Background()
	taskID := model.TaskID(uuid.NewString())
	require.NoError(t, db.AddTask(ctx, &model.Task{
		TaskID:     taskID,
		TaskType:   model.TaskTypeCommand,
		StartTime:  time.Now(),
		LogVersion: model.CurrentTaskLogVersion,
	}))
	allocationID := model.AllocationID(fmt.Sprintf("%s.1", taskID))
	require.NoError(t, db.AddAllocation(ctx, &model.Allocation{
		AllocationID: allocationID,
		TaskID:       taskID,
		Slots:        1,
		ResourcePool: "default",
		State:        ptrs.Ptr(state),
		Ports:        map[string]int{},
	}))
	return allocationID
}

// allocateInTick writes what the scheduler tick writes when it places an allocation on an agent: it
// takes free devices in memory and persists the allocation's resources and container records.
func allocateInTick(
	t *testing.T, state *agentState, allocationID model.AllocationID, slots int,
) cproto.Container {
	containerID := cproto.NewID()
	devices, err := state.allocateFreeDevices(slots, containerID)
	require.NoError(t, err)
	cr := &containerResources{
		req:         &sproto.AllocateRequest{AllocationID: allocationID},
		agent:       state,
		containerID: containerID,
		devices:     devices,
	}
	rs := taskmodel.NewResourcesState(cr, -1)
	require.NoError(t, rs.Persist())
	require.NoError(t, cr.persist())
	return cproto.Container{ID: containerID, State: cproto.Assigned, Devices: devices}
}

func allocateInAgent(t *testing.T, a *agent, allocationID model.AllocationID) cproto.Container {
	a.mu.Lock()
	defer a.mu.Unlock()
	return allocateInTick(t, a.agentState, allocationID, 1)
}

func launch(allocationID model.AllocationID, c cproto.Container) sproto.StartTaskContainer {
	return sproto.StartTaskContainer{
		AllocationID:   allocationID,
		StartContainer: aproto.StartContainer{Container: c, Spec: cproto.Spec{}},
	}
}

// restartAgentService returns an agent service that holds only the given agent, restored from its
// persisted snapshot as a restarted master restores it, and a resource pool over it.
func restartAgentService(
	t *testing.T, conf config.ResourcePoolConfig, agentID aproto.ID,
) (*agents, *agent, *resourcePool) {
	registry, err := newPoolRegistry([]config.ResourcePoolConfig{conf})
	require.NoError(t, err)
	svc, _ := newAgentService(registry, &aproto.MasterSetAgentOptions{
		LoggingOptions: model.LoggingConfig{DefaultLoggingConfig: &model.DefaultLoggingConfig{}},
	}, false)

	var snapshot agentSnapshot
	require.NoError(t, db.Bun().NewSelect().Model(&snapshot).
		Where("agent_id = ?", agentID).Scan(context.Background()))
	state, err := newAgentStateFromSnapshot(snapshot)
	require.NoError(t, err)
	ref, err := svc.createAgent(agentID, conf.PoolName, svc.opts, state, func() {
		_ = svc.agents.Delete(agentID)
	})
	require.NoError(t, err)
	require.NoError(t, svc.agents.Add(agentID, ref))

	scheduler, err := MakeScheduler(conf.Scheduler)
	require.NoError(t, err)
	rp, err := newResourcePool(
		&conf, nil, nil, scheduler, MakeFitFunction(conf.Scheduler.FittingPolicy), svc)
	require.NoError(t, err)
	t.Cleanup(rp.stop)
	return svc, ref, rp
}

// restoreInPool asks the pool to restore an allocation and returns the pool's answer.
func restoreInPool(
	t *testing.T, rp *resourcePool, allocationID model.AllocationID,
) sproto.ResourcesEvent {
	sub := rmevents.Subscribe(allocationID)
	defer sub.Close()
	rp.Allocate(sproto.AllocateRequest{
		AllocationID: allocationID,
		TaskID:       allocationID.ToTaskID(),
		JobID:        model.JobID(allocationID.ToTaskID()),
		SlotsNeeded:  1,
		Restore:      true,
	})
	return requireEvent(t, sub)
}

func requireEvent(t *testing.T, sub *sproto.ResourcesSubscription) sproto.ResourcesEvent {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	event, err := sub.GetWithContext(ctx)
	require.NoError(t, err, "no resource manager event")
	return event
}

func requireRestoredContainers(
	t *testing.T, event sproto.ResourcesEvent, containers ...cproto.ID,
) {
	allocated, ok := event.(*sproto.ResourcesAllocated)
	require.True(t, ok, "expected restored resources, got %#v", event)
	require.True(t, allocated.Recovered)
	var restored []cproto.ID
	for _, r := range allocated.Resources {
		restored = append(restored, *r.Summary().ContainerID)
	}
	require.ElementsMatch(t, containers, restored)
}

func requireRestoreError(t *testing.T, event sproto.ResourcesEvent, message string) {
	failed, ok := event.(*sproto.ResourcesFailedError)
	require.True(t, ok, "expected a restore error, got %#v", event)
	require.Equal(t, sproto.RestoreError, failed.FailureType)
	require.Contains(t, failed.ErrMsg, message)
}

func TestRestoreRequiresRecordedLaunch(t *testing.T) {
	conf := restoreTestPool(t)
	devices := restoreTestDevices(2)
	state := startedAgentState(t, conf.PoolName, devices)

	// The master crashed after the allocation was placed, before it launched the container.
	unlaunched := addRestoreTestAllocation(t, model.AllocationStateAssigned)
	unlaunchedContainer := allocateInTick(t, state, unlaunched, 1)
	// Another container's launch persists the agent snapshot, which then lists the unlaunched
	// container too.
	launched := addRestoreTestAllocation(t, model.AllocationStateAssigned)
	launchedContainer := allocateInTick(t, state, launched, 1)
	require.NoError(t, state.startContainer(launch(launched, launchedContainer)))

	_, _, rp := restartAgentService(t, conf, state.id)
	requireRestoreError(t, restoreInPool(t, rp, unlaunched),
		fmt.Sprintf("container %s has no recorded launch", unlaunchedContainer.ID))
	requireRestoredContainers(t, restoreInPool(t, rp, launched), launchedContainer.ID)
}

func TestRestoreAfterQueuedPurgeFindsOneContainer(t *testing.T) {
	for _, purge := range []bool{true, false} {
		t.Run(fmt.Sprintf("purge=%t", purge), func(t *testing.T) {
			ctx := context.Background()
			conf := restoreTestPool(t)
			state := startedAgentState(t, conf.PoolName, restoreTestDevices(2))

			// The tick records resources for a queued allocation, and the master crashes before
			// the allocation receives them; the agent snapshot happens to list the container.
			allocationID := addRestoreTestAllocation(t, model.AllocationStatePending)
			stale := allocateInTick(t, state, allocationID, 1)
			require.NoError(t, state.persist())

			// The restarted master requests the allocation again under its ID, which a new tick
			// places and launches.
			restarted, err := newAgentStateFromSnapshotOf(state.id)
			require.NoError(t, err)
			if purge {
				require.NoError(t, db.PurgeAllocationResources(ctx, db.Bun(), allocationID))
			}
			placed := allocateInTick(t, restarted, allocationID, 1)
			require.NoError(t, restarted.startContainer(launch(allocationID, placed)))
			_, err = db.Bun().NewUpdate().Table("allocations").
				Set("state = ?", model.AllocationStateAssigned).
				Where("allocation_id = ?", allocationID).Exec(ctx)
			require.NoError(t, err)

			// A later restart restores exactly the launched container.
			_, _, rp := restartAgentService(t, conf, state.id)
			event := restoreInPool(t, rp, allocationID)
			if purge {
				requireRestoredContainers(t, event, placed.ID)
			} else {
				requireRestoreError(t, event,
					fmt.Sprintf("container %s has no recorded launch", stale.ID))
			}
		})
	}
}

func newAgentStateFromSnapshotOf(agentID aproto.ID) (*agentState, error) {
	var snapshot agentSnapshot
	if err := db.Bun().NewSelect().Model(&snapshot).
		Where("agent_id = ?", agentID).Scan(context.Background()); err != nil {
		return nil, err
	}
	state, err := newAgentStateFromSnapshot(snapshot)
	if err != nil {
		return nil, err
	}
	state.handler = &agent{}
	return state, nil
}

// connectFakeAgent connects a websocket to the agent as the agent process would, and returns the
// agent's end of it after the master's first message.
func connectFakeAgent(t *testing.T, a *agent) (*fakeAgentSocket, *aproto.MasterSetAgentOptions) {
	e := echo.New()
	e.GET("/", func(c echo.Context) error {
		return a.HandleWebsocketConnection(webSocketRequest{echoCtx: c})
	})
	server := httptest.NewServer(e.Server.Handler)
	t.Cleanup(server.Close)

	var dialer websocket.Dialer
	conn, _, err := dialer.Dial(fmt.Sprintf("ws://%s", strings.TrimPrefix(server.URL, "http://")), nil)
	require.NoError(t, err)
	socket, err := ws.Wrap[aproto.AgentMessage, *aproto.MasterMessage]("fake-agent", conn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = socket.Close() })

	msg := requireAgentMessage(t, socket)
	require.NotNil(t, msg.MasterSetAgentOptions)
	return socket, msg.MasterSetAgentOptions
}

func requireAgentMessage(t *testing.T, socket *fakeAgentSocket) aproto.AgentMessage {
	select {
	case msg := <-socket.Inbox:
		return msg
	case <-time.After(10 * time.Second):
		t.Fatal("the agent received no message")
		return aproto.AgentMessage{}
	}
}

func requireAgentStarted(t *testing.T, a *agent) {
	require.Eventually(t, func() bool {
		_, err := a.State()
		return err == nil
	}, 10*time.Second, 10*time.Millisecond)
}

// persistedLaunch returns whether the agent snapshot lists a container, and the container's
// recorded state.
func persistedLaunch(t *testing.T, agentID aproto.ID, containerID cproto.ID) (bool, cproto.State) {
	ctx := context.Background()
	var snapshot agentSnapshot
	require.NoError(t, db.Bun().NewSelect().Model(&snapshot).
		Where("agent_id = ?", agentID).Scan(ctx))
	var container containerSnapshot
	require.NoError(t, db.Bun().NewSelect().Model(&container).
		Where("container_id = ?", containerID).Scan(ctx))
	for _, id := range snapshot.Containers {
		if id == containerID {
			return true, container.State
		}
	}
	return false, container.State
}

func failContainerRecord(t *testing.T, containerID cproto.ID) {
	ctx := context.Background()
	name := "fail_launch_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err := db.Bun().ExecContext(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger AS $$
BEGIN RAISE EXCEPTION 'injected launch record failure'; END; $$ LANGUAGE plpgsql`, name))
	require.NoError(t, err)
	_, err = db.Bun().ExecContext(ctx, fmt.Sprintf(`CREATE TRIGGER %[1]s
BEFORE UPDATE ON resourcemanagers_agent_containers
FOR EACH ROW WHEN (NEW.container_id = '%[2]s') EXECUTE FUNCTION %[1]s()`, name, containerID))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := db.Bun().ExecContext(ctx, fmt.Sprintf(
			"DROP TRIGGER IF EXISTS %s ON resourcemanagers_agent_containers", name))
		require.NoError(t, err)
		_, err = db.Bun().ExecContext(ctx, fmt.Sprintf("DROP FUNCTION IF EXISTS %s()", name))
		require.NoError(t, err)
	})
}

func TestLaunchIsRecordedBeforeStartContainer(t *testing.T) {
	conf := restoreTestPool(t)
	devices := restoreTestDevices(2)
	a := newAgent(aproto.ID(uuid.NewString()), queue.New[agentUpdatedEvent](), conf.PoolName,
		&conf, &aproto.MasterSetAgentOptions{
			LoggingOptions: model.LoggingConfig{DefaultLoggingConfig: &model.DefaultLoggingConfig{}},
		}, nil, func() {})
	socket, _ := connectFakeAgent(t, a)
	socket.Outbox <- &aproto.MasterMessage{AgentStarted: &aproto.AgentStarted{
		Devices: devices, ResourcePoolName: conf.PoolName,
	}}
	requireAgentStarted(t, a)
	t.Cleanup(func() { _ = a.agentState.delete() })

	// The agent learns of the container only once the snapshot lists it with its launch state.
	allocationID := addRestoreTestAllocation(t, model.AllocationStateAssigned)
	container := allocateInAgent(t, a, allocationID)
	a.StartTaskContainer(launch(allocationID, container))
	msg := requireAgentMessage(t, socket)
	require.NotNil(t, msg.StartContainer)
	require.Equal(t, container.ID, msg.StartContainer.Container.ID)
	listed, containerState := persistedLaunch(t, a.id, container.ID)
	require.True(t, listed)
	require.Equal(t, cproto.Assigned, containerState)

	// If the launch cannot be recorded, the agent is not told, and the allocation learns that the
	// container failed.
	failedAllocationID := addRestoreTestAllocation(t, model.AllocationStateAssigned)
	failed := allocateInAgent(t, a, failedAllocationID)
	failContainerRecord(t, failed.ID)
	sub := rmevents.Subscribe(failedAllocationID)
	defer sub.Close()
	a.StartTaskContainer(launch(failedAllocationID, failed))
	marker := cproto.NewID()
	a.KillTaskContainer(sproto.KillTaskContainer{ContainerID: marker})
	msg = requireAgentMessage(t, socket)
	require.Nil(t, msg.StartContainer, "a container whose launch was not recorded was started")
	require.NotNil(t, msg.SignalContainer)
	require.Equal(t, marker, msg.SignalContainer.ContainerID)

	changed, ok := requireEvent(t, sub).(*sproto.ResourcesStateChanged)
	require.True(t, ok)
	require.Equal(t, sproto.FromContainerID(failed.ID), changed.ResourcesID)
	require.Equal(t, sproto.Terminated, changed.ResourcesState)
	require.NotNil(t, changed.ResourcesStopped.Failure)
	require.Equal(t, sproto.AgentError, changed.ResourcesStopped.Failure.FailureType)

	listed, containerState = persistedLaunch(t, a.id, failed.ID)
	require.False(t, listed)
	require.Equal(t, cproto.Unknown, containerState)
	state, err := a.State()
	require.NoError(t, err)
	require.NotContains(t, state.containerAllocation, failed.ID)
	require.Nil(t, state.slotStates[failed.Devices[0].ID].containerID)
}

func TestReattachStateMismatchIsARestoreFailure(t *testing.T) {
	conf := restoreTestPool(t)
	devices := restoreTestDevices(1)
	state := startedAgentState(t, conf.PoolName, devices)
	allocationID := addRestoreTestAllocation(t, model.AllocationStateAssigned)
	container := allocateInTick(t, state, allocationID, 1)
	require.NoError(t, state.startContainer(launch(allocationID, container)))

	_, a, _ := restartAgentService(t, conf, state.id)
	sub := rmevents.Subscribe(allocationID)
	defer sub.Close()
	socket, opts := connectFakeAgent(t, a)
	require.Len(t, opts.ContainersToReattach, 1)
	require.Equal(t, cproto.Assigned, opts.ContainersToReattach[0].Container.State)

	// The agent reports the container in another state than the master recorded.
	running := container
	running.State = cproto.Running
	socket.Outbox <- &aproto.MasterMessage{AgentStarted: &aproto.AgentStarted{
		Devices:              devices,
		ResourcePoolName:     conf.PoolName,
		ContainersReattached: []aproto.ContainerReattachAck{{Container: running}},
	}}

	msg := requireAgentMessage(t, socket)
	require.NotNil(t, msg.SignalContainer)
	require.Equal(t, container.ID, msg.SignalContainer.ContainerID)
	require.Equal(t, syscall.SIGKILL, msg.SignalContainer.Signal)

	changed, ok := requireEvent(t, sub).(*sproto.ResourcesStateChanged)
	require.True(t, ok)
	require.Equal(t, sproto.Terminated, changed.ResourcesState)
	require.NotNil(t, changed.ResourcesStopped.Failure, "the killed container must carry a failure")
	require.Equal(t, sproto.RestoreError, changed.ResourcesStopped.Failure.FailureType)
	require.Contains(t, changed.ResourcesStopped.Failure.ErrMsg,
		"changed state from ASSIGNED to RUNNING")
}
