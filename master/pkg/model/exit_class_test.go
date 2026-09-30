package model

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/determined-ai/determined/master/pkg/ptrs"
	"github.com/determined-ai/determined/proto/pkg/taskv1"
)

func TestExitClassProto(t *testing.T) {
	cases := map[ExitClass]taskv1.ExitClass{
		ExitClassNone:                         taskv1.ExitClass_EXIT_CLASS_NONE,
		ExitClassPlacementUnsatisfied:         taskv1.ExitClass_EXIT_CLASS_PLACEMENT_UNSATISFIED,
		ExitClassNodePreflightFailed:          taskv1.ExitClass_EXIT_CLASS_NODE_PREFLIGHT_FAILED,
		ExitClassWorkloadInitializationFailed: taskv1.ExitClass_EXIT_CLASS_WORKLOAD_INITIALIZATION_FAILED,
		ExitClassWorkloadFailed:               taskv1.ExitClass_EXIT_CLASS_WORKLOAD_FAILED,
		ExitClassInfrastructureFailed:         taskv1.ExitClass_EXIT_CLASS_INFRASTRUCTURE_FAILED,
		"bogus":                               taskv1.ExitClass_EXIT_CLASS_UNSPECIFIED,
	}
	for c, expected := range cases {
		require.Equal(t, expected, ptrs.Ptr(c).Proto(), c)
	}
	require.Equal(t, taskv1.ExitClass_EXIT_CLASS_UNSPECIFIED, (*ExitClass)(nil).Proto())
	// Every proto class except UNSPECIFIED has a model class.
	require.Len(t, taskv1.ExitClass_name, len(cases))
}

func TestExitDetail(t *testing.T) {
	d := NewExitDetail("FAILURE_TYPE_RESOURCES_FAILED", ptrs.Ptr(int32(137)), "killed")
	require.Equal(t, &structpb.Struct{Fields: map[string]*structpb.Value{
		"failure_type": structpb.NewStringValue("FAILURE_TYPE_RESOURCES_FAILED"),
		"exit_code":    structpb.NewNumberValue(137),
		"message":      structpb.NewStringValue("killed"),
	}}, d.Proto())
	require.Nil(t, (*ExitDetail)(nil).Proto())

	v, err := d.Value()
	require.NoError(t, err)
	require.JSONEq(t,
		`{"failure_type":"FAILURE_TYPE_RESOURCES_FAILED","exit_code":137,"message":"killed"}`,
		v.(string))
	var scanned ExitDetail
	require.NoError(t, scanned.Scan([]byte(v.(string))))
	require.Equal(t, *d, scanned)

	v, err = NewExitDetail("", nil, "only a message").Value()
	require.NoError(t, err)
	require.JSONEq(t, `{"message":"only a message"}`, v.(string))

	long := NewExitDetail("", nil, strings.Repeat("é", exitDetailMessageLimit))
	require.True(t, utf8.ValidString(long.Message))
	require.Len(t, long.Message, exitDetailMessageLimit)

	// The limit falls inside a rune, so the cut backs up to the rune's start.
	split := NewExitDetail("", nil, "a"+strings.Repeat("é", exitDetailMessageLimit))
	require.True(t, utf8.ValidString(split.Message))
	require.Len(t, split.Message, exitDetailMessageLimit-1)
}
