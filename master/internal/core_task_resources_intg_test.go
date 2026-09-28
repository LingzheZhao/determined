//go:build integration
// +build integration

package internal

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/determined-ai/determined/master/internal/config"
	detcontext "github.com/determined-ai/determined/master/internal/context"
	expauth "github.com/determined-ai/determined/master/internal/experiment"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/proto/pkg/apiv1"
)

func TestTaskResourcesUsesGetTaskRBACBeforePrometheus(t *testing.T) {
	api, authZExp, _, curUser, grpcCtx := setupExpAuthTest(t, nil)
	_, task := createTestTrial(t, api, curUser)
	taskID := task.TaskID.String()

	// The same artifact permission blocks the existing GetTask API and this Echo route.
	authZExp.On("CanGetExperiment", mock.Anything, curUser, mock.Anything).Return(nil).Twice()
	authZExp.On("CanGetExperimentArtifacts", mock.Anything, curUser, mock.Anything).
		Return(fmt.Errorf("artifact access denied")).Twice()
	_, err := api.GetTask(grpcCtx, &apiv1.GetTaskRequest{TaskId: taskID})
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	now := time.Now().Unix()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/ui/task-resources/%s?start=%d&end=%d&step=15", taskID, now-60, now), nil)
	c := &detcontext.DetContext{Context: e.NewContext(req, httptest.NewRecorder())}
	c.SetUser(curUser)
	c.SetParamNames("task_id")
	c.SetParamValues(taskID)
	queried := false
	err = serveTaskResources(c, config.TaskResourcesConfig{DetCluster: "cvgl"}, taskResourceDependencies{
		authorize: func(ctx context.Context, user model.User, id string) error {
			_, _, err := api.canDoActionsOnTaskForUser(ctx, model.TaskID(id), user,
				expauth.AuthZProvider.Get().CanGetExperimentArtifacts)
			return err
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
	require.False(t, queried)
	he, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	require.Equal(t, http.StatusNotFound, he.Code)
}
