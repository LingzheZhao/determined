package command

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/master/pkg/ptrs"
)

func TestDecideRestore(t *testing.T) {
	now := time.Now()
	const (
		pending    = model.AllocationStatePending
		assigned   = model.AllocationStateAssigned
		terminated = model.AllocationStateTerminated
	)
	allocation := func(state model.AllocationState, start, end *time.Time) *model.Allocation {
		a := &model.Allocation{StartTime: start, EndTime: end}
		if state != "" {
			a.State = ptrs.Ptr(state)
		}
		return a
	}
	for _, tc := range []struct {
		name          string
		allocation    *model.Allocation
		stopRequested bool
		action        RestoreAction
	}{
		{"queued", allocation(pending, nil, nil), false, RestoreRequeue},
		{"queued, stop requested", allocation(pending, nil, nil), true, RestoreStopQueued},
		// Placement is the ASSIGNED write; a start time is no evidence either way.
		{"assigned", allocation(assigned, nil, nil), false, RestorePlaced},
		{"assigned, stop requested", allocation(assigned, nil, nil), true, RestorePlaced},
		{"queued with a stamped start time", allocation(pending, &now, nil), false, RestoreRequeue},
		{"pulling", allocation(model.AllocationStatePulling, &now, nil), false, RestorePlaced},
		{"running", allocation(model.AllocationStateRunning, &now, nil), false, RestorePlaced},
		{"terminating", allocation(model.AllocationStateTerminating, &now, nil), false, RestorePlaced},
		{"no recorded state", allocation("", nil, nil), false, RestorePlaced},
		// An allocation that exits before it starts records no end time.
		{"terminated without an end time", allocation(terminated, nil, nil), false, RestoreEnded},
		{"ended", allocation(model.AllocationStateRunning, &now, &now), false, RestoreEnded},
		{"ended while queued, stop requested", allocation(pending, &now, &now), true, RestoreEnded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.action, DecideRestore(tc.allocation, tc.stopRequested))
		})
	}
}
