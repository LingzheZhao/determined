package task

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/determined-ai/determined/master/internal/sproto"
	"github.com/determined-ai/determined/master/internal/task/preemptible"
	"github.com/determined-ai/determined/master/internal/task/taskmodel"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/master/pkg/ptrs"
	"github.com/determined-ai/determined/proto/pkg/taskv1"
)

// failureTypeCases lists every sproto.FailureType with how an allocation that exits with it is
// reported. TestFailureTypeCasesAreExhaustive keeps it in sync with the constants.
var failureTypeCases = map[sproto.FailureType]struct {
	reasonPrefix string
	severity     logrus.Level
	class        model.ExitClass
}{
	sproto.ResourcesFailed: {
		"allocation failed: ", logrus.ErrorLevel, model.ExitClassWorkloadFailed,
	},
	sproto.TaskError: {
		"allocation failed: ", logrus.ErrorLevel, model.ExitClassWorkloadFailed,
	},
	sproto.ResourcesAborted: {
		"allocation aborted: ", logrus.InfoLevel, model.ExitClassNone,
	},
	sproto.TaskAborted: {
		"allocation aborted: ", logrus.InfoLevel, model.ExitClassNone,
	},
	sproto.ResourcesMissing: {
		"allocation failed due to missing resources: ", logrus.ErrorLevel,
		model.ExitClassInfrastructureFailed,
	},
	sproto.AgentFailed: {
		"allocation failed due to agent failure: ", logrus.ErrorLevel,
		model.ExitClassInfrastructureFailed,
	},
	sproto.AgentError: {
		"allocation failed due to agent failure: ", logrus.ErrorLevel,
		model.ExitClassInfrastructureFailed,
	},
	sproto.RestoreError: {
		"allocation failed due to restore error: ", logrus.ErrorLevel,
		model.ExitClassInfrastructureFailed,
	},
	sproto.UnknownError: {
		"allocation failed due to agent failure: ", logrus.ErrorLevel,
		model.ExitClassInfrastructureFailed,
	},
	sproto.PlacementUnsatisfied: {
		"allocation could not be placed: ", logrus.ErrorLevel, model.ExitClassPlacementUnsatisfied,
	},
	sproto.PreflightFailed: {
		"allocation failed node preflight: ", logrus.ErrorLevel, model.ExitClassNodePreflightFailed,
	},
}

// TestFailureTypeCasesAreExhaustive fails when a sproto.FailureType constant is added without a
// case above, so the classifier is tested against every failure type.
func TestFailureTypeCasesAreExhaustive(t *testing.T) {
	pkgs, err := parser.ParseDir(token.NewFileSet(), "../sproto", nil, 0)
	require.NoError(t, err)

	var declared []string
	for _, p := range pkgs {
		for _, f := range p.Files {
			ast.Inspect(f, func(n ast.Node) bool {
				vs, ok := n.(*ast.ValueSpec)
				if !ok {
					return true
				}
				if ident, ok := vs.Type.(*ast.Ident); !ok || ident.Name != "FailureType" {
					return true
				}
				for _, v := range vs.Values {
					lit, ok := v.(*ast.BasicLit)
					require.True(t, ok && lit.Kind == token.STRING,
						"FailureType constants must be string literals")
					value, err := strconv.Unquote(lit.Value)
					require.NoError(t, err)
					declared = append(declared, value)
				}
				return true
			})
		}
	}
	require.NotEmpty(t, declared)

	var tested []string
	for ft := range failureTypeCases {
		tested = append(tested, string(ft))
	}
	sort.Strings(declared)
	sort.Strings(tested)
	require.Equal(t, declared, tested, "failureTypeCases must list every sproto.FailureType")
}

func newExitTestAllocation(t *testing.T) *allocation {
	id := model.AllocationID(fmt.Sprintf("%s.0", t.Name()))
	return &allocation{
		syslog: logrus.NewEntry(logrus.New()),
		req: sproto.AllocateRequest{
			AllocationID: id,
			Preemption:   sproto.PreemptionConfig{Preemptible: true, TimeoutDuration: time.Hour},
		},
		resources: resourcesList{},
		model:     model.Allocation{AllocationID: id},
	}
}

// withResource adds one started resource, exited with stopped when it is not nil.
func withResource(a *allocation, stopped *sproto.ResourcesStopped) {
	id := sproto.ResourcesID(fmt.Sprintf("r%d", len(a.resources)))
	a.resources[id] = &taskmodel.ResourcesWithState{
		Started: &sproto.ResourcesStarted{},
		Exited:  stopped,
	}
	a.resourcesStarted = true
}

func TestCalculateExitStatusFailureTypes(t *testing.T) {
	for ft, tc := range failureTypeCases {
		t.Run(string(ft), func(t *testing.T) {
			failure := sproto.ResourcesFailedError{
				FailureType: ft,
				ErrMsg:      "boom",
				ExitCode:    ptrs.Ptr(sproto.ExitCode(3)),
			}
			a := newExitTestAllocation(t)
			withResource(a, &sproto.ResourcesStopped{Failure: &failure})
			a.exitErr = failure

			var status exitStatus
			require.NotPanics(t, func() { status = a.calculateExitStatus("all resources exited") })

			require.Contains(t, status.reason, tc.reasonPrefix)
			require.Equal(t, tc.severity, status.severity)
			require.False(t, status.userRequestedStop)
			require.Equal(t, failure, status.err)
			require.Equal(t, tc.class, status.class)
			require.Equal(t, &model.ExitDetail{
				FailureType: ft.Proto().String(),
				ExitCode:    ptrs.Ptr(int32(3)),
				Message:     "boom",
			}, status.detail)
			require.NotEqual(t, taskv1.FailureType_FAILURE_TYPE_UNSPECIFIED, ft.Proto(),
				"failure type %q has no proto mapping", ft)

			known, ok := failureExitClass(ft)
			require.True(t, ok)
			require.Equal(t, tc.class, known)
		})
	}
}

func TestCalculateExitStatusExistingReasons(t *testing.T) {
	// The reasons the classifier kept from before exit classes existed.
	failure := func(ft sproto.FailureType) sproto.ResourcesFailedError {
		return sproto.ResourcesFailedError{FailureType: ft, ErrMsg: "boom"}
	}
	cases := map[sproto.FailureType]string{
		sproto.ResourcesFailed:  "allocation failed: " + failure(sproto.ResourcesFailed).Error(),
		sproto.TaskError:        "allocation failed: " + failure(sproto.TaskError).Error(),
		sproto.AgentError:       "allocation failed due to agent failure: " + failure(sproto.AgentError).Error(),
		sproto.AgentFailed:      "allocation failed due to agent failure: " + failure(sproto.AgentFailed).Error(),
		sproto.TaskAborted:      "allocation aborted: " + string(sproto.TaskAborted),
		sproto.ResourcesAborted: "allocation aborted: " + string(sproto.ResourcesAborted),
		sproto.RestoreError: "allocation failed due to restore error: " +
			failure(sproto.RestoreError).Error(),
	}
	for ft, reason := range cases {
		a := newExitTestAllocation(t)
		a.exitErr = failure(ft)
		require.Equal(t, reason, a.calculateExitStatus("x").reason, ft)
	}
}

func TestCalculateExitStatusUnknownFailureType(t *testing.T) {
	failure := sproto.ResourcesFailedError{FailureType: "bogus", ErrMsg: "boom"}
	a := newExitTestAllocation(t)
	withResource(a, &sproto.ResourcesStopped{Failure: &failure})
	a.exitErr = failure

	var status exitStatus
	require.NotPanics(t, func() { status = a.calculateExitStatus("all resources exited") })
	require.Equal(t, exitStatus{
		reason:   "allocation failed: bogus: boom",
		severity: logrus.ErrorLevel,
		err:      failure,
		class:    model.ExitClassInfrastructureFailed,
		detail: &model.ExitDetail{
			FailureType: "FAILURE_TYPE_UNSPECIFIED",
			Message:     "bogus: boom",
		},
	}, status)

	_, known := failureExitClass("bogus")
	require.False(t, known)
}

func TestCalculateExitStatus(t *testing.T) {
	handlerErr := errors.New("database is down")
	taskError := sproto.ResourcesFailedError{
		FailureType: sproto.TaskError,
		ErrMsg:      "exit code 137",
		ExitCode:    ptrs.Ptr(sproto.ExitCode(137)),
	}

	cases := []struct {
		name     string
		setup    func(a *allocation)
		expected exitStatus
	}{
		{
			name: "killed while running",
			setup: func(a *allocation) {
				withResource(a, &sproto.ResourcesStopped{Failure: &taskError})
				a.killedWhileRunning = true
			},
			expected: exitStatus{
				reason:   "allocation killed after reason",
				severity: logrus.InfoLevel,
				class:    model.ExitClassNone,
			},
		},
		{
			name: "killed while running after a handler error",
			setup: func(a *allocation) {
				withResource(a, &sproto.ResourcesStopped{Failure: &taskError})
				a.killedWhileRunning = true
				a.exitErr = handlerErr
			},
			expected: exitStatus{
				reason:   "allocation killed after reason",
				severity: logrus.InfoLevel,
				class:    model.ExitClassInfrastructureFailed,
				detail:   &model.ExitDetail{Message: "database is down"},
			},
		},
		{
			name: "preempted",
			setup: func(a *allocation) {
				withResource(a, &sproto.ResourcesStopped{})
				preemptible.Register(a.req.AllocationID.String())
				t.Cleanup(func() { preemptible.Unregister(a.req.AllocationID.String()) })
				preemptible.Acknowledge(a.req.AllocationID.String())
			},
			expected: exitStatus{
				reason:   "allocation preempted after reason",
				severity: logrus.InfoLevel,
				class:    model.ExitClassNone,
			},
		},
		{
			name: "stopped early",
			setup: func(a *allocation) {
				withResource(a, &sproto.ResourcesStopped{})
			},
			expected: exitStatus{
				reason:            "allocation stopped early after reason",
				userRequestedStop: true,
				severity:          logrus.InfoLevel,
				class:             model.ExitClassNone,
			},
		},
		{
			name: "daemons killed gracefully",
			setup: func(a *allocation) {
				withResource(a, &sproto.ResourcesStopped{})
				withResource(a, &sproto.ResourcesStopped{Failure: &taskError})
				a.killedDaemons = true
				a.killedDaemonsGracefully = true
				a.exitErr = taskError
			},
			expected: exitStatus{
				reason:   "allocation terminated daemon processes as part of normal exit",
				severity: logrus.InfoLevel,
				class:    model.ExitClassNone,
			},
		},
		{
			name: "handler crash",
			setup: func(a *allocation) {
				a.exitErr = handlerErr
			},
			expected: exitStatus{
				reason:   "allocation handler crashed due to error: database is down",
				severity: logrus.ErrorLevel,
				err:      handlerErr,
				class:    model.ExitClassInfrastructureFailed,
				detail:   &model.ExitDetail{Message: "database is down"},
			},
		},
		{
			name:  "no resources",
			setup: func(a *allocation) {},
			expected: exitStatus{
				reason:   "allocation aborted after reason",
				severity: logrus.InfoLevel,
				class:    model.ExitClassNone,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newExitTestAllocation(t)
			tc.setup(a)
			var status exitStatus
			require.NotPanics(t, func() { status = a.calculateExitStatus("reason") })
			require.Equal(t, tc.expected, status)
		})
	}
}

func TestCalculateExitStatusWithoutReason(t *testing.T) {
	// Resources exist, none exited, and nothing failed: a bug, which used to panic.
	a := newExitTestAllocation(t)
	withResource(a, nil)

	var status exitStatus
	require.NotPanics(t, func() { status = a.calculateExitStatus("reason") })
	require.Equal(t, logrus.ErrorLevel, status.severity)
	require.Error(t, status.err)
	require.Equal(t, model.ExitClassInfrastructureFailed, status.class)
	require.Equal(t, status.err.Error(), status.reason)
	require.Equal(t, &model.ExitDetail{Message: status.err.Error()}, status.detail)
}

func TestClassifyExitErr(t *testing.T) {
	class, detail := classifyExitErr(nil)
	require.Equal(t, model.ExitClassNone, class)
	require.Nil(t, detail)

	class, detail = classifyExitErr(errors.New("unknown error occurred"))
	require.Equal(t, model.ExitClassInfrastructureFailed, class)
	require.Equal(t, &model.ExitDetail{Message: "unknown error occurred"}, detail)

	class, detail = classifyExitErr(sproto.ResourcesFailedError{
		FailureType: sproto.ResourcesFailed,
		ErrMsg:      "container failed with non-zero exit code: 2",
		ExitCode:    ptrs.Ptr(sproto.ExitCode(2)),
	})
	require.Equal(t, model.ExitClassWorkloadFailed, class)
	require.Equal(t, &model.ExitDetail{
		FailureType: "FAILURE_TYPE_RESOURCES_FAILED",
		ExitCode:    ptrs.Ptr(int32(2)),
		Message:     "container failed with non-zero exit code: 2",
	}, detail)
}
