package sproto

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/determined-ai/determined/master/pkg/aproto"
	"github.com/determined-ai/determined/proto/pkg/taskv1"
)

func TestFromContainerStoppedUnknownFailureType(t *testing.T) {
	code := aproto.ExitCode(1)
	stopped := FromContainerStopped(&aproto.ContainerStopped{Failure: &aproto.ContainerFailureError{
		FailureType: "from a newer agent",
		ErrMsg:      "boom",
		ExitCode:    &code,
	}})
	require.Equal(t, &ResourcesFailedError{
		FailureType: UnknownError,
		ErrMsg:      "from a newer agent: boom",
		ExitCode:    FromContainerExitCode(&code),
	}, stopped.Failure)
	require.Equal(t, taskv1.FailureType_FAILURE_TYPE_UNKNOWN_ERROR, stopped.Failure.Proto().FailureType)

	stopped = FromContainerStopped(&aproto.ContainerStopped{Failure: &aproto.ContainerFailureError{
		FailureType: aproto.ContainerFailed,
		ErrMsg:      "boom",
	}})
	require.Equal(t, &ResourcesFailedError{FailureType: ResourcesFailed, ErrMsg: "boom"}, stopped.Failure)
}
