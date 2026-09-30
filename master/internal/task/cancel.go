package task

import (
	"context"
	"time"

	"github.com/sirupsen/logrus"
	"golang.org/x/exp/slices"

	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/pkg/model"
)

// CancelKillReason is the reason a job's allocation is killed when the job was asked to stop.
const CancelKillReason = "the job was asked to stop"

// readCancelRequested reads whether a job was asked to stop. Tests replace it to fail a read.
var readCancelRequested = db.JobCancelRequested

// WhenCancelRequested calls kill if the job was asked to stop. Every start calls it after it
// registers what it started. A cancel records the request before it signals what is registered,
// so either the cancel finds it or this finds the request. kill runs in the background: a kill
// waits for a restored allocation to be reattached, which must not hold up the start. A read that
// fails is retried in the background while live reports that what was started is still there,
// because a cancel that found nothing registered relies on this check.
func WhenCancelRequested(
	ctx context.Context, jobID model.JobID, live func() bool, kill func(), log *logrus.Entry,
) {
	ctx = context.WithoutCancel(ctx)
	read := readCancelRequested
	requested, err := read(ctx, jobID)
	if err == nil && !requested {
		return
	}
	go func() {
		for backoff := time.Second; err != nil; backoff = min(2*backoff, 30*time.Second) {
			log.WithError(err).Warn("retrying whether a started job was asked to stop")
			time.Sleep(backoff)
			if !live() {
				return
			}
			requested, err = read(ctx, jobID)
		}
		if requested {
			kill()
		}
	}()
}

// KillIfCancelRequested kills an allocation that was just registered if its job was asked to stop.
func KillIfCancelRequested(ctx context.Context, jobID model.JobID, allocationID model.AllocationID) {
	service := DefaultService
	log := syslog.WithField("allocation-id", allocationID)
	WhenCancelRequested(ctx, jobID, func() bool {
		return slices.Contains(service.GetAllAllocationIDs(), allocationID)
	}, func() {
		if err := service.Signal(allocationID, KillAllocation, CancelKillReason); err != nil {
			log.WithError(err).Warn("killing an allocation whose job was asked to stop")
		}
	}, log)
}
