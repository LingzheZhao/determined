package internal

import (
	"context"
	"strconv"
	"time"

	"github.com/uptrace/bun"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/determined-ai/determined/master/internal/api"
	"github.com/determined-ai/determined/master/internal/command"
	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/internal/grpcutil"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/master/pkg/tasks"
	"github.com/determined-ai/determined/proto/pkg/apiv1"
	"github.com/determined-ai/determined/proto/pkg/taskv1"
)

const genericTaskStateProtoPrefix = "GENERIC_TASK_STATE_"

// genericTaskListRow is a generic task with its owner and its latest snapshot.
type genericTaskListRow struct {
	bun.BaseModel `bun:"table:tasks,alias:t"`

	TaskID       model.TaskID           `bun:"task_id"`
	JobID        *model.JobID           `bun:"job_id"`
	State        *model.TaskState       `bun:"task_state"`
	StartTime    time.Time              `bun:"start_time"`
	EndTime      *time.Time             `bun:"end_time"`
	ParentID     *model.TaskID          `bun:"parent_id"`
	ForkedFrom   *string                `bun:"forked_from"`
	NoPause      *bool                  `bun:"no_pause"`
	OwnerID      *int32                 `bun:"owner_id"`
	Username     *string                `bun:"username"`
	AllocationID *string                `bun:"allocation_id"`
	Spec         *tasks.GenericTaskSpec `bun:"generic_task_spec"`
}

// GetGenericTasks lists generic tasks, newest first, filtered by owner, workspace, state or
// parent, among those in workspaces the user may view.
func (a *apiServer) GetGenericTasks(
	ctx context.Context, req *apiv1.GetGenericTasksRequest,
) (*apiv1.GetGenericTasksResponse, error) {
	curUser, _, err := grpcutil.GetUser(ctx)
	if err != nil {
		return nil, err
	}
	if req.WorkspaceId != 0 {
		if _, err := a.GetWorkspaceByID(ctx, req.WorkspaceId, *curUser, false); err != nil {
			return nil, err
		}
	}
	scopes, err := command.AuthZProvider.Get().AccessibleScopes(
		ctx, *curUser, model.AccessScopeID(req.WorkspaceId),
	)
	if err != nil {
		return nil, err
	}
	if req.WorkspaceId != 0 && len(scopes) == 0 {
		return nil, api.NotFoundErrs("workspace", strconv.Itoa(int(req.WorkspaceId)), true)
	}

	query := db.Bun().NewSelect().Model((*genericTaskListRow)(nil)).
		ColumnExpr("t.task_id, t.job_id, t.task_state, t.start_time, t.end_time").
		ColumnExpr("t.parent_id, t.forked_from, t.no_pause").
		ColumnExpr("j.owner_id, u.username, cs.allocation_id, cs.generic_task_spec").
		Join("JOIN jobs AS j ON j.job_id = t.job_id").
		Join("LEFT JOIN users AS u ON u.id = j.owner_id").
		Join("LEFT JOIN command_state AS cs ON cs.task_id = t.task_id").
		Where("t.task_type = ?", model.TaskTypeGeneric).
		OrderExpr("t.start_time DESC, t.task_id")
	if len(req.Users) > 0 {
		query = query.Where("u.username IN (?)", bun.In(req.Users))
	}
	if len(req.UserIds) > 0 {
		query = query.Where("j.owner_id IN (?)", bun.In(req.UserIds))
	}
	if len(req.States) > 0 {
		states := make([]model.TaskState, 0, len(req.States))
		for _, s := range req.States {
			name := s.String()
			if s == taskv1.GenericTaskState_GENERIC_TASK_STATE_UNSPECIFIED ||
				len(name) <= len(genericTaskStateProtoPrefix) {
				return nil, status.Errorf(codes.InvalidArgument, "invalid generic task state %s", name)
			}
			states = append(states, model.TaskState(name[len(genericTaskStateProtoPrefix):]))
		}
		query = query.Where("t.task_state IN (?)", bun.In(states))
	}
	if req.ParentId != nil {
		query = query.Where("t.parent_id = ?", *req.ParentId)
	}

	var rows []genericTaskListRow
	if err := query.Scan(ctx, &rows); err != nil {
		return nil, err
	}

	resp := &apiv1.GetGenericTasksResponse{Tasks: make([]*taskv1.GenericTask, 0, len(rows))}
	for _, row := range rows {
		if row.Spec == nil || !scopes[model.AccessScopeID(row.Spec.WorkspaceID)] {
			continue
		}
		if req.WorkspaceId != 0 && int32(row.Spec.WorkspaceID) != req.WorkspaceId {
			continue
		}
		resp.Tasks = append(resp.Tasks, row.toProto())
	}
	return resp, api.Paginate(&resp.Pagination, &resp.Tasks, req.Offset, req.Limit)
}

func (row genericTaskListRow) toProto() *taskv1.GenericTask {
	spec := *row.Spec
	if spec.Base.TaskID == "" {
		spec.Base.TaskID = string(row.TaskID)
	}
	res := spec.GenericTaskConfig.Resources
	out := &taskv1.GenericTask{
		TaskId:       string(row.TaskID),
		Name:         spec.DisplayName(),
		Description:  spec.GenericTaskConfig.Description,
		WorkspaceId:  int32(spec.WorkspaceID),
		ProjectId:    int32(spec.ProjectID),
		StartTime:    timestamppb.New(row.StartTime),
		NoPause:      genericTaskNoPause(row.NoPause),
		ForkedFrom:   row.ForkedFrom,
		AllocationId: row.AllocationID,
	}
	if row.JobID != nil {
		out.JobId = row.JobID.String()
	}
	if row.State != nil {
		out.State = taskv1.GenericTaskState(
			taskv1.GenericTaskState_value[genericTaskStateProtoPrefix+string(*row.State)])
	}
	if row.EndTime != nil {
		out.EndTime = timestamppb.New(*row.EndTime)
	}
	if row.ParentID != nil {
		out.ParentId = (*string)(row.ParentID)
	}
	if row.OwnerID != nil {
		out.UserId = *row.OwnerID
	}
	if row.Username != nil {
		out.Username = *row.Username
	}
	// Raw fields: a list must not fail on a spec that was persisted without defaults.
	if res.RawResourcePool != nil {
		out.ResourcePool = *res.RawResourcePool
	}
	if res.RawSlots != nil {
		out.Slots = int32(*res.RawSlots)
	}
	return out
}
