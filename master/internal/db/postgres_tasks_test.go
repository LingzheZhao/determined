package db

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/master/pkg/ptrs"
)

func TestGenericTaskExitState(t *testing.T) {
	tests := []struct {
		name            string
		state           *model.TaskState
		cancelRequested bool
		failed          bool
		want            model.TaskState
	}{
		{"completed", ptrs.Ptr(model.TaskStateActive), false, false, model.TaskStateCompleted},
		{"failed", ptrs.Ptr(model.TaskStateActive), false, true, model.TaskStateError},
		{"paused", ptrs.Ptr(model.TaskStateStoppingPaused), false, false, model.TaskStatePaused},
		{"failed while pausing", ptrs.Ptr(model.TaskStateStoppingPaused), false, true, model.TaskStateError},
		{"killed", ptrs.Ptr(model.TaskStateStoppingCanceled), false, true, model.TaskStateCanceled},
		{"asked to stop", ptrs.Ptr(model.TaskStateActive), true, false, model.TaskStateCanceled},
		{"asked to stop and failed", ptrs.Ptr(model.TaskStateActive), true, true, model.TaskStateCanceled},
		{"asked to stop while pausing", ptrs.Ptr(model.TaskStateStoppingPaused), true, false, model.TaskStateCanceled},
		{"no state", nil, false, false, model.TaskStateCompleted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, genericTaskExitState(tt.state, tt.cancelRequested, tt.failed))
		})
	}
}
