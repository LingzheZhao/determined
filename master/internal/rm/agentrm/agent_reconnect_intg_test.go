//go:build integration
// +build integration

package agentrm

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/determined-ai/determined/master/internal/config"
	"github.com/determined-ai/determined/master/pkg/aproto"
	"github.com/determined-ai/determined/master/pkg/device"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/master/pkg/syncx/queue"
	"github.com/determined-ai/determined/master/pkg/ws"
)

// An agent that is stopped while it awaits a reconnect (here: it comes back with a different device
// count) must not arm a new reconnect timer when stop closes its socket. Such a timer stopped the
// agent a second time after the reconnect wait, and its unregister removed the new agent that had
// registered under the same ID in the meantime.
func TestAgentStoppedDuringReconnectUnregistersOnce(t *testing.T) {
	const reconnectWait = 200 * time.Millisecond
	var unregistered atomic.Int32
	a := newAgent(
		aproto.ID("reconnect-race-"+time.Now().Format("150405.000000")),
		queue.New[agentUpdatedEvent](),
		"default",
		&config.ResourcePoolConfig{AgentReconnectWait: model.Duration(reconnectWait)},
		&aproto.MasterSetAgentOptions{
			LoggingOptions: model.LoggingConfig{
				DefaultLoggingConfig: &model.DefaultLoggingConfig{},
			},
		},
		nil,
		func() { unregistered.Add(1) },
	)

	e := echo.New()
	e.GET("/", func(c echo.Context) error {
		require.NoError(t, a.HandleWebsocketConnection(webSocketRequest{echoCtx: c}))
		return nil
	})
	server := httptest.NewServer(e.Server.Handler)
	defer server.Close()
	url := fmt.Sprintf("ws://%s", strings.TrimPrefix(server.URL, "http://"))

	connect := func(devices int) *websocket.Conn {
		var dialer websocket.Dialer
		conn, _, err := dialer.Dial(url, nil)
		require.NoError(t, err)
		socket, err := ws.Wrap[aproto.AgentMessage, *aproto.MasterMessage]("test", conn)
		require.NoError(t, err)
		started := &aproto.AgentStarted{Version: "test"}
		for i := 0; i < devices; i++ {
			started.Devices = append(started.Devices, device.Device{
				ID: device.ID(i), Brand: "test", UUID: fmt.Sprintf("GPU-%d", i), Type: device.CUDA,
			})
		}
		socket.Outbox <- &aproto.MasterMessage{AgentStarted: started}
		return conn
	}
	waitFor := func(cond func() bool, what string) {
		deadline := time.Now().Add(5 * time.Second)
		for !cond() {
			require.True(t, time.Now().Before(deadline), "timed out waiting for %s", what)
			time.Sleep(10 * time.Millisecond)
		}
	}
	locked := func(f func() bool) func() bool {
		return func() bool {
			a.mu.Lock()
			defer a.mu.Unlock()
			return f()
		}
	}

	// First connection with one device; then lose it: the agent awaits a reconnect.
	first := connect(1)
	waitFor(locked(func() bool { return a.started }), "the first AgentStarted")
	require.NoError(t, first.UnderlyingConn().Close())
	waitFor(locked(func() bool { return a.awaitingReconnect }), "the disconnect")

	// The agent comes back within the wait with two devices: the master stops it.
	second := connect(2)
	defer second.Close()
	waitFor(locked(func() bool { return a.stopped }), "stop after the device count change")
	require.Equal(t, int32(1), unregistered.Load())

	// Past the reconnect wait, nothing may stop or unregister the agent again.
	time.Sleep(3 * reconnectWait)
	require.Equal(t, int32(1), unregistered.Load(), "unregister ran again after the reconnect wait")
	a.mu.Lock()
	defer a.mu.Unlock()
	require.Empty(t, a.reconnectTimers, "a stopped agent armed a reconnect timer")
}
