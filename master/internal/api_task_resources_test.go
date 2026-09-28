package internal

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/runtime"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/determined-ai/determined/master/internal/config"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/proto/pkg/apiv1"
)

func TestGetTaskResourcesAPIAuthorizesBeforeRangeValidation(t *testing.T) {
	queried := false
	start, end, step := int64(-1), int64(0), int64(1)
	_, err := getTaskResourcesForAPI(context.Background(), model.User{},
		&apiv1.GetTaskResourcesRequest{TaskId: "task.1", Start: &start, End: &end, Step: &step},
		config.TaskResourcesConfig{DetCluster: "cvgl"}, taskResourceDependencies{
			authorize: func(context.Context, model.User, string) error {
				return status.Error(codes.PermissionDenied, "private reason")
			},
			allocationBelongs: func(context.Context, string, string) (bool, error) {
				queried = true
				return true, nil
			},
			query: func(context.Context, string, taskResourceRange) ([]prometheusTaskSeries, error) {
				queried = true
				return nil, nil
			},
		})
	require.Equal(t, codes.NotFound, status.Code(err))
	require.NotContains(t, err.Error(), "private reason")
	require.False(t, queried)
}

func TestGetTaskResourcesAPITypedSamplesAndBounds(t *testing.T) {
	now := time.Now().Unix()
	start, end, step := now-60, now, int64(15)
	req := &apiv1.GetTaskResourcesRequest{
		TaskId: "task.1", Start: &start, End: &end, Step: &step,
	}
	deps := taskResourceDependencies{
		authorize:         func(context.Context, model.User, string) error { return nil },
		allocationBelongs: func(context.Context, string, string) (bool, error) { return true, nil },
		query: func(context.Context, string, taskResourceRange) ([]prometheusTaskSeries, error) {
			return []prometheusTaskSeries{{Metric: map[string]string{
				"det_cluster": "cvgl", "task_id": "task.1", "allocation_id": "allocation.1",
				"node": "node-a", "secret": "never-expose",
			}, Samples: [][2]interface{}{{float64(start), float64(0)}, {float64(end), nil}}}}, nil
		},
	}
	resp, err := getTaskResourcesForAPI(context.Background(), model.User{}, req,
		config.TaskResourcesConfig{DetCluster: "cvgl"}, deps)
	require.NoError(t, err)
	require.True(t, resp.Enabled)
	require.Len(t, resp.Series, 8)
	require.Equal(t, "allocation.1", resp.Series[0].Labels.AllocationId)
	require.Equal(t, "node-a", resp.Series[0].Labels.Node)
	require.Equal(t, float64(0), resp.Series[0].Samples[0].Value.Value)
	require.Nil(t, resp.Series[0].Samples[1].Value)
	require.Equal(t, float64(end), resp.Series[0].Samples[1].TimestampSeconds)
	serialized, err := (&runtime.JSONPb{EmitDefaults: true}).Marshal(resp)
	require.NoError(t, err)
	var payload struct {
		Series []struct {
			Samples []map[string]json.RawMessage `json:"samples"`
		} `json:"series"`
	}
	require.NoError(t, json.Unmarshal(serialized, &payload))
	require.Equal(t, json.RawMessage("0"), payload.Series[0].Samples[0]["value"])
	missing := payload.Series[0].Samples[1]["value"]
	require.True(t, len(missing) == 0 || string(missing) == "null")

	badStep := int64(1)
	req.Step = &badStep
	_, err = getTaskResourcesForAPI(context.Background(), model.User{}, req,
		config.TaskResourcesConfig{DetCluster: "cvgl"}, deps)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
