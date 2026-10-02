package task

import (
	"fmt"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/determined-ai/determined/master/internal/sproto"
	"github.com/determined-ai/determined/master/internal/task/taskmodel"
	"github.com/determined-ai/determined/master/pkg/model"
)

func newExitTestAllocation(t *testing.T) *allocation {
	return &allocation{
		syslog: logrus.NewEntry(logrus.New()),
		req: sproto.AllocateRequest{
			AllocationID: model.AllocationID(fmt.Sprintf("%s.0", t.Name())),
		},
		resources: resourcesList{},
	}
}

// TestCalculateExitStatusReportsUnexpectedFailures checks that failures the allocation does not
// expect are reported as errors instead of panicking.
func TestCalculateExitStatusReportsUnexpectedFailures(t *testing.T) {
	cases := map[string]struct {
		failureType  sproto.FailureType
		reasonPrefix string
	}{
		"missing resources": {
			sproto.ResourcesMissing, "allocation failed due to missing resources: ",
		},
		"unknown agent failure": {
			sproto.UnknownError, "allocation failed due to agent failure: ",
		},
		"unrecognized failure type": {
			sproto.FailureType("from a newer agent"), "allocation failed: ",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			a := newExitTestAllocation(t)
			failure := sproto.ResourcesFailedError{FailureType: tc.failureType, ErrMsg: "boom"}
			a.exitErr = failure

			var (
				reason            string
				userRequestedStop bool
				severity          logrus.Level
				err               error
			)
			require.NotPanics(t, func() {
				reason, userRequestedStop, severity, err = a.calculateExitStatus("test")
			})
			require.Equal(t, tc.reasonPrefix+failure.Error(), reason)
			require.False(t, userRequestedStop)
			require.Equal(t, logrus.ErrorLevel, severity)
			require.Equal(t, failure, err)
		})
	}
}

// TestCalculateExitStatusWithoutReason checks that an allocation that exits with resources that
// neither exited nor failed reports an error instead of panicking or reading as a success.
func TestCalculateExitStatusWithoutReason(t *testing.T) {
	a := newExitTestAllocation(t)
	a.resources[sproto.ResourcesID("r0")] = &taskmodel.ResourcesWithState{}

	var (
		reason            string
		userRequestedStop bool
		severity          logrus.Level
		err               error
	)
	require.NotPanics(t, func() {
		reason, userRequestedStop, severity, err = a.calculateExitStatus("test")
	})
	require.ErrorContains(t, err, "allocation exited early without a valid reason after test")
	require.Equal(t, err.Error(), reason)
	require.False(t, userRequestedStop)
	require.Equal(t, logrus.ErrorLevel, severity)
}
