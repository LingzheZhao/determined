//go:build integration

package task

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/internal/sproto"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/master/pkg/ptrs"
)

// The agent resource manager kills a container whose state changed while the master was away, and
// reports its exit with a restore failure. That exit is an infrastructure failure; the same exit
// without a failure would read as a stop that someone asked for.
func TestReattachedContainerExitClass(t *testing.T) {
	restoreFailure := &sproto.ResourcesFailedError{
		FailureType: sproto.RestoreError,
		ErrMsg:      "container changed state from ASSIGNED to RUNNING while the master was away",
	}
	for _, tc := range []struct {
		name    string
		failure *sproto.ResourcesFailedError
		class   model.ExitClass
	}{
		{name: "restore failure", failure: restoreFailure, class: model.ExitClassInfrastructureFailed},
		{name: "no failure", failure: nil, class: model.ExitClassNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			closeDB, _, id, q, exitFuture := requireStarted(t)
			defer closeDB()

			rID, _ := requireAssigned(t, id, q)
			requireRunning(t, id, q, rID)
			q.Put(&sproto.ResourcesStateChanged{
				ResourcesID:      rID,
				ResourcesState:   sproto.Terminated,
				ResourcesStopped: &sproto.ResourcesStopped{Failure: tc.failure},
			})
			requireTerminated(t, id, exitFuture)

			persisted, err := db.AllocationByID(context.Background(), id)
			require.NoError(t, err)
			require.Equal(t, ptrs.Ptr(tc.class), persisted.ExitClass)
		})
	}
}
