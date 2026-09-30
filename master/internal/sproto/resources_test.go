package sproto

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/determined-ai/determined/master/pkg/aproto"
	"github.com/determined-ai/determined/proto/pkg/taskv1"
)

func TestFailureTypeProto(t *testing.T) {
	cases := map[FailureType]taskv1.FailureType{
		ResourcesFailed:      taskv1.FailureType_FAILURE_TYPE_RESOURCES_FAILED,
		ResourcesAborted:     taskv1.FailureType_FAILURE_TYPE_RESOURCES_ABORTED,
		ResourcesMissing:     taskv1.FailureType_FAILURE_TYPE_RESOURCES_MISSING,
		TaskAborted:          taskv1.FailureType_FAILURE_TYPE_TASK_ABORTED,
		TaskError:            taskv1.FailureType_FAILURE_TYPE_TASK_ERROR,
		AgentFailed:          taskv1.FailureType_FAILURE_TYPE_AGENT_FAILED,
		AgentError:           taskv1.FailureType_FAILURE_TYPE_AGENT_ERROR,
		RestoreError:         taskv1.FailureType_FAILURE_TYPE_RESTORE_ERROR,
		PlacementUnsatisfied: taskv1.FailureType_FAILURE_TYPE_PLACEMENT_UNSATISFIED,
		PreflightFailed:      taskv1.FailureType_FAILURE_TYPE_PREFLIGHT_FAILED,
		UnknownError:         taskv1.FailureType_FAILURE_TYPE_UNKNOWN_ERROR,
		"bogus":              taskv1.FailureType_FAILURE_TYPE_UNSPECIFIED,
	}
	for ft, expected := range cases {
		require.Equal(t, expected, ft.Proto(), ft)
	}
}

func TestFromContainerFailureType(t *testing.T) {
	cases := map[aproto.FailureType]FailureType{
		aproto.ContainerFailed:  ResourcesFailed,
		aproto.ContainerAborted: ResourcesAborted,
		aproto.ContainerMissing: ResourcesMissing,
		aproto.TaskAborted:      TaskAborted,
		aproto.TaskError:        TaskError,
		aproto.AgentFailed:      AgentFailed,
		aproto.AgentError:       AgentError,
		aproto.RestoreError:     RestoreError,
		aproto.PreflightFailed:  PreflightFailed,
		"from a newer agent":    UnknownError,
	}
	for in, expected := range cases {
		require.Equal(t, expected, FromContainerFailureType(in), in)
	}
}

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
