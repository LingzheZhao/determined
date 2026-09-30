package task

import (
	"context"

	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/pkg/model"
)

// CancelKillReason is the reason a job's allocation is killed when the job was asked to stop.
const CancelKillReason = "the job was asked to stop"

// KillIfCancelRequested kills an allocation that was just registered if its job was asked to stop.
// Every start calls it after registering its allocation. A cancel records the request before it
// signals the registered allocation, so either the cancel finds the allocation or this finds the
// request.
func KillIfCancelRequested(
	ctx context.Context, jobID model.JobID, allocationID model.AllocationID,
) error {
	requested, err := db.JobCancelRequested(ctx, jobID)
	if err != nil || !requested {
		return err
	}
	// A signal waits for a restored allocation to be reattached, which must not hold up the start
	// that registered it.
	service := DefaultService
	go func() {
		if err := service.Signal(allocationID, KillAllocation, CancelKillReason); err != nil {
			syslog.WithError(err).WithField("allocation-id", allocationID).
				Warn("killing an allocation whose job was asked to stop")
		}
	}()
	return nil
}
