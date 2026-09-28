package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/determined-ai/determined/master/internal/config"
	detcontext "github.com/determined-ai/determined/master/internal/context"
	"github.com/determined-ai/determined/master/pkg/model"
)

func taskResourceTestContext(t *testing.T, query string) (*detcontext.DetContext, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	rec := httptest.NewRecorder()
	ctx := &detcontext.DetContext{Context: e.NewContext(
		httptest.NewRequest(http.MethodGet, "/ui/task-resources/task.1?"+query, nil), rec,
	)}
	ctx.SetParamNames("task_id")
	ctx.SetParamValues("task.1")
	ctx.SetUser(model.User{})
	return ctx, rec
}

func TestTaskResourcesAuthorizeBeforeQuery(t *testing.T) {
	c, _ := taskResourceTestContext(t, "start=bad&end=bad&step=bad")
	called := false
	err := serveTaskResources(c, config.TaskResourcesConfig{DetCluster: "cvgl"}, taskResourceDependencies{
		authorize: func(context.Context, model.User, string) error {
			return status.Error(codes.PermissionDenied, "secret permission")
		},
		allocationBelongs: func(context.Context, string, string) (bool, error) {
			called = true
			return true, nil
		},
		query: func(context.Context, string, taskResourceRange) ([]prometheusTaskSeries, error) {
			called = true
			return nil, nil
		},
	})
	require.False(t, called)
	he, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusNotFound, he.Code)
	require.NotContains(t, fmt.Sprint(he.Message), "secret permission")
}

func TestTaskResourcesBoundsAndFixedQueryIdentity(t *testing.T) {
	now := time.Unix(2000000000, 0)
	base := url.Values{"start": {"1999996400"}, "end": {"2000000000"}, "step": {"15"}}
	r, allocation, err := parseTaskResourceRange(base, now)
	require.NoError(t, err)
	require.Equal(t, 15, r.Step)
	require.Empty(t, allocation)
	for _, change := range []url.Values{
		{"start": {"1999000000"}, "end": {"2000000000"}, "step": {"60"}},
		{"start": {"1999996400"}, "end": {"2000000000"}, "step": {"1"}},
		{"start": {"1999996400"}, "end": {"2000000000"}, "step": {"15"}, "query": {"up"}},
		{"start": {"1999996400", "1"}, "end": {"2000000000"}, "step": {"15"}},
	} {
		_, _, err := parseTaskResourceRange(change, now)
		require.Error(t, err)
	}
	taskID := `task.1"} or up{job="evil`
	queries := taskResourceQueries("cvgl", taskID, "allocation.1")
	require.Len(t, queries, 8)
	quotedTask := "task_id=" + strconv.Quote(taskID)
	for _, q := range queries {
		require.Contains(t, q.Expr, quotedTask)
		require.Contains(t, q.Expr, `allocation_id="allocation.1"`)
		require.NotContains(t, q.Expr, "det:runtime_without_allocation")
	}
}

func TestTaskResourcesNullSamplesAndWarnings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/query_range", r.URL.Path)
		require.Equal(t, "up", r.URL.Query().Get("query"))
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"det_cluster":"cvgl","task_id":"task.1","allocation_id":"allocation.1"},"values":[[100,"0"],[115,"NaN"],[130,"+Inf"]]}]}}`))
	}))
	defer server.Close()
	series, err := queryTaskPrometheus(context.Background(), server.URL, "up", taskResourceRange{Start: 100, End: 130, Step: 15})
	require.NoError(t, err)
	require.Len(t, series, 1)
	require.Equal(t, float64(0), series[0].Samples[0][1])
	require.Nil(t, series[0].Samples[1][1])
	require.Nil(t, series[0].Samples[2][1])
	warnings := taskResourceWarnings([]taskResourceSeries{
		{Metric: "memory_rss_bytes", Samples: series[0].Samples},
		{Metric: "gpu_utilization_percent", Samples: [][2]interface{}{{100, float64(50)}}},
	})
	require.Equal(t, []string{"rss_unverified", "gpu_full_device"}, []string{warnings[0].Code, warnings[1].Code})
}

func TestTaskResourcesRejectsCrossTaskAllocationBeforePrometheus(t *testing.T) {
	now := time.Now().Unix()
	query := fmt.Sprintf("start=%d&end=%d&step=15&allocation_id=other.1", now-60, now)
	c, _ := taskResourceTestContext(t, query)
	queried := false
	err := serveTaskResources(c, config.TaskResourcesConfig{DetCluster: "cvgl"}, taskResourceDependencies{
		authorize:         func(context.Context, model.User, string) error { return nil },
		allocationBelongs: func(context.Context, string, string) (bool, error) { return false, nil },
		query: func(context.Context, string, taskResourceRange) ([]prometheusTaskSeries, error) {
			queried = true
			return nil, nil
		},
	})
	require.False(t, queried)
	he, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusNotFound, he.Code)
}

func TestTaskResourcesResponseKeepsOnlyAllowlistedLabels(t *testing.T) {
	now := time.Now().Unix()
	c, rec := taskResourceTestContext(t, fmt.Sprintf("start=%d&end=%d&step=15", now-60, now))
	err := serveTaskResources(c, config.TaskResourcesConfig{DetCluster: "cvgl"}, taskResourceDependencies{
		authorize:         func(context.Context, model.User, string) error { return nil },
		allocationBelongs: func(context.Context, string, string) (bool, error) { return true, nil },
		query: func(context.Context, string, taskResourceRange) ([]prometheusTaskSeries, error) {
			return []prometheusTaskSeries{{Metric: map[string]string{
				"det_cluster": "cvgl", "task_id": "task.1", "allocation_id": "task.1.1",
				"node": "node-a", "secret": "do-not-return",
			}, Samples: [][2]interface{}{{float64(now), float64(1)}}}}, nil
		},
	})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "do-not-return")
	var response taskResourceResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	require.True(t, response.Enabled)
	require.Len(t, response.Series, 8)
	require.Equal(t, "node-a", response.Series[0].Labels.Node)
	require.False(t, strings.Contains(rec.Body.String(), "prometheus_url"))
}
