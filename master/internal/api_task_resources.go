package internal

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/golang/protobuf/ptypes/wrappers"
	"github.com/labstack/echo/v4"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/determined-ai/determined/master/internal/config"
	"github.com/determined-ai/determined/master/internal/grpcutil"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/proto/pkg/apiv1"
)

func (a *apiServer) GetTaskResourcesCapability(
	ctx context.Context, _ *apiv1.GetTaskResourcesCapabilityRequest,
) (*apiv1.GetTaskResourcesCapabilityResponse, error) {
	if _, _, err := grpcutil.GetUser(ctx); err != nil {
		return nil, err
	}
	return &apiv1.GetTaskResourcesCapabilityResponse{
		Enabled: a.m.config.Integrations.TaskResources.Enabled(),
	}, nil
}

func (a *apiServer) GetTaskResources(
	ctx context.Context, req *apiv1.GetTaskResourcesRequest,
) (*apiv1.GetTaskResourcesResponse, error) {
	conf := a.m.config.Integrations.TaskResources
	if !conf.Enabled() {
		return nil, status.Error(codes.NotFound, "task resources are disabled")
	}
	user, _, err := grpcutil.GetUser(ctx)
	if err != nil {
		return nil, err
	}
	return getTaskResourcesForAPI(ctx, *user, req, conf, a.m.taskResourceDependencies())
}

func getTaskResourcesForAPI(ctx context.Context, user model.User,
	req *apiv1.GetTaskResourcesRequest, conf config.TaskResourcesConfig,
	deps taskResourceDependencies,
) (*apiv1.GetTaskResourcesResponse, error) {
	params := url.Values{}
	if req.Start != nil {
		params.Set("start", strconv.FormatInt(req.GetStart(), 10))
	}
	if req.End != nil {
		params.Set("end", strconv.FormatInt(req.GetEnd(), 10))
	}
	if req.Step != nil {
		params.Set("step", strconv.FormatInt(req.GetStep(), 10))
	}
	if req.AllocationId != nil {
		params.Set("allocation_id", req.GetAllocationId())
	}
	result, err := collectTaskResources(ctx, user, req.GetTaskId(), params, conf, deps)
	if err != nil {
		return nil, taskResourceGRPCError(err)
	}
	resp := &apiv1.GetTaskResourcesResponse{Enabled: result.Enabled,
		Series:   make([]*apiv1.TaskResourceSeries, 0, len(result.Series)),
		Warnings: make([]*apiv1.TaskResourceWarning, 0, len(result.Warnings))}
	for _, s := range result.Series {
		series := &apiv1.TaskResourceSeries{
			Metric: s.Metric,
			Labels: &apiv1.TaskResourceLabels{
				AllocationId: s.Labels.AllocationID,
				Node:         s.Labels.Node,
				GpuUuid:      s.Labels.GPUUUID,
			},
			Samples: make([]*apiv1.TaskResourceSample, 0, len(s.Samples)),
		}
		for _, pair := range s.Samples {
			sample := &apiv1.TaskResourceSample{TimestampSeconds: pair[0].(float64)}
			if value, ok := pair[1].(float64); ok {
				sample.Value = &wrappers.DoubleValue{Value: value}
			}
			series.Samples = append(series.Samples, sample)
		}
		resp.Series = append(resp.Series, series)
	}
	for _, w := range result.Warnings {
		resp.Warnings = append(resp.Warnings, &apiv1.TaskResourceWarning{
			Code: w.Code, Message: w.Message,
		})
	}
	return resp, nil
}

func taskResourceGRPCError(err error) error {
	if httpErr, ok := err.(*echo.HTTPError); ok {
		message, _ := httpErr.Message.(string)
		switch httpErr.Code {
		case http.StatusBadRequest:
			return status.Error(codes.InvalidArgument, message)
		case http.StatusNotFound:
			return status.Error(codes.NotFound, message)
		case http.StatusServiceUnavailable, http.StatusBadGateway:
			return status.Error(codes.Unavailable, message)
		}
	}
	return status.Error(codes.Internal, "task resource metrics are unavailable")
}
