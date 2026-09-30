package command

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/sirupsen/logrus"
	"github.com/uptrace/bun"

	"github.com/determined-ai/determined/master/internal/api"
	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/internal/rm"
	"github.com/determined-ai/determined/master/internal/task"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/master/pkg/protoutils"
	"github.com/determined-ai/determined/proto/pkg/apiv1"
)

// DefaultCmdService is the global command service singleton.
var DefaultCmdService *CommandService

// CommandService tracks the different NTSC commands in the system.
type CommandService struct {
	db       *db.PgDB
	rm       rm.ResourceManager
	mu       sync.Mutex
	commands map[model.TaskID]*Command
	syslog   *logrus.Entry
}

// NewService returns a new CommandService.
func NewService(db *db.PgDB, rm rm.ResourceManager) (*CommandService, error) {
	return &CommandService{
		db:       db,
		rm:       rm,
		commands: make(map[model.TaskID]*Command),
		syslog:   logrus.WithField("component", "command-service"),
	}, nil
}

// SetDefaultService initializes & returns a new CommandService.
func SetDefaultService(cs *CommandService) {
	if DefaultCmdService != nil {
		logrus.Warn(
			"detected re-initialization of Command Service that should never occur outside of tests",
		)
	}

	DefaultCmdService = cs
}

// RestoreAllCommands restores every command, notebook, shell, and TensorBoard whose task has not
// ended, from the persisted state of the allocation that its command_state names.
func (cs *CommandService) RestoreAllCommands(
	ctx context.Context,
) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	snapshots := []CommandSnapshot{}
	err := SelectLiveSnapshots(&snapshots).
		Where("command_snapshot.generic_task_spec IS NULL").
		Scan(ctx)
	if err != nil {
		cs.syslog.Errorf("failed to remake commands: %s", err)
		return nil
	}

	for i := range snapshots {
		snapshot := &snapshots[i]
		if snapshot.Task.Job == nil {
			err := fmt.Errorf("task %s has no job", snapshot.TaskID)
			cs.syslog.WithError(err).Error("failed to restore from snapshot")
			if err := db.FailTaskAllocation(
				ctx, snapshot.TaskID, snapshot.AllocationID, nil, RestoreFailedReason, err,
			); err != nil {
				cs.syslog.WithError(err).Error("ending a command that failed to restore")
			}
			continue
		}

		cmd := commandFromSnapshot(cs.db, cs.rm, snapshot)
		if !cmd.restore(ctx, snapshot) {
			continue
		}
		// Restore to the command service registry.
		cs.commands[cmd.taskID] = cmd
		cs.syslog.Debugf("restored & started generic command %s", cmd.taskID)
	}
	return nil
}

// NewGenericCommand returns a new NTSC command for a launch request. The caller persists it with
// PersistTx in its commit transaction, together with its job row, and then starts it with
// StartCommand.
func (cs *CommandService) NewGenericCommand(
	taskType model.TaskType,
	jobType model.JobType,
	req *CreateGeneric,
) *Command {
	return newCommand(cs.db, cs.rm, taskType, jobType, req)
}

// StartCommand starts a committed command and adds it to the registry. If the start fails, the
// command is ended so that it does not stay open.
func (cs *CommandService) StartCommand(cmd *Command) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if err := cmd.Start(context.TODO()); err != nil {
		cmd.fail(context.TODO(), "the allocation failed to start", err)
		return err
	}

	// Add it to the registry.
	cs.commands[cmd.taskID] = cmd
	return nil
}

// persistNewCommand commits a new command with its job row.
func persistNewCommand(ctx context.Context, cmd *Command) error {
	return db.Bun().RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := db.AddJobTx(ctx, tx, cmd.Job()); err != nil {
			return fmt.Errorf("persisting job %v: %w", cmd.taskID, err)
		}
		return cmd.PersistTx(ctx, tx)
	})
}

// LaunchGenericCommand creates NTSC commands and persists them to the database.
func (cs *CommandService) LaunchGenericCommand(
	taskType model.TaskType,
	jobType model.JobType,
	req *CreateGeneric,
) (*Command, error) {
	cmd := cs.NewGenericCommand(taskType, jobType, req)
	if err := persistNewCommand(context.TODO(), cmd); err != nil {
		return nil, err
	}
	if err := cs.StartCommand(cmd); err != nil {
		return nil, err
	}
	return cmd, nil
}

// LaunchNotebookCommand creates notebook commands and persists them to the database.
func (cs *CommandService) LaunchNotebookCommand(
	req *CreateGeneric,
	user *model.User,
) (*Command, error) {
	cmd := cs.NewGenericCommand(model.TaskTypeNotebook, model.JobTypeNotebook, req)

	token, err := db.GenerateNotebookSessionToken(user.ID, cmd.taskID)
	if err != nil {
		return nil, err
	}
	cmd.Base.ExtraEnvVars[model.NotebookSessionEnvVar] = token

	if err := persistNewCommand(context.TODO(), cmd); err != nil {
		return nil, err
	}
	if err := cs.StartCommand(cmd); err != nil {
		return nil, err
	}

	if err := db.StartNotebookSession(context.TODO(), user.ID, cmd.taskID); err != nil {
		return nil, err
	}
	return cmd, nil
}

func (cs *CommandService) unregisterCommand(id model.TaskID) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	delete(cs.commands, id)
}

// getNTSC gets & checks type of a command given its ID.
func (cs *CommandService) getNTSC(cmdID model.TaskID, cmdType model.TaskType) (*Command, error) {
	c, ok := cs.commands[cmdID]
	if !ok {
		return nil, fmt.Errorf("get NTSC %s not found", cmdID)
	}

	if c.taskType != cmdType {
		return nil, fmt.Errorf("getNTSC: type mismatch: %s/%s", cmdType, c.taskType)
	}

	return c, nil
}

// listByType returns a list of NTSCs of one type.
func (cs *CommandService) listByType(
	reqUsers []string,
	reqUserIDs []int32,
	cmdType model.TaskType,
	workspaceID int32,
) []*Command {
	users := make(map[string]bool, len(reqUsers))
	for _, user := range reqUsers {
		users[user] = true
	}
	userIds := make(map[int32]bool, len(reqUserIDs))
	for _, user := range reqUserIDs {
		userIds[user] = true
	}

	cmds := []*Command{}
	for _, c := range cs.commands {
		wID := int32(c.GenericCommandSpec.Metadata.WorkspaceID)
		username := c.Base.Owner.Username
		userID := int32(c.Base.Owner.ID)
		// skip if it doesn't match the requested workspaceID if any.
		if workspaceID != 0 && workspaceID != wID {
			continue
		}
		if c.taskType == cmdType && ((len(users) == 0 && len(userIds) == 0) ||
			users[username] || userIds[userID]) {
			cmds = append(cmds, c)
		}
	}
	return cmds
}

// DeleteWorkspaceNTSC deletes all NTSC associated with a workspace ID.
func (cs *CommandService) DeleteWorkspaceNTSC(req *apiv1.DeleteWorkspaceRequest) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	for _, c := range cs.commands {
		c.DeleteIfInWorkspace(req)
	}
}

// SetNTSCPriority sets the NTSC's resource manager group priority.
func (cs *CommandService) SetNTSCPriority(
	id string, priority int, taskType model.TaskType,
) (*Command, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	c, err := cs.getNTSC(model.TaskID(id), taskType)
	if err != nil {
		return nil, err
	}

	err = c.setNTSCPriority(priority, true)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// KillNTSC sends a kill signal to the command's allocation.
func (cs *CommandService) KillNTSC(id string, taskType model.TaskType) (*Command, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	tID := model.TaskID(id)

	c, err := cs.getNTSC(tID, taskType)
	if err != nil {
		return nil, err
	}

	completed, err := db.TaskCompleted(context.TODO(), tID)
	if err != nil {
		return nil, err
	}

	if !completed {
		err = task.DefaultService.Signal(c.allocationID, task.KillAllocation, "user requested kill")
		if err != nil && !strings.Contains(err.Error(), "not found") {
			return nil, fmt.Errorf("failed to kill allocation: %w", err)
		}
	}

	return c, nil
}

// GetCommands returns all commands in the command service registry matching the workspace ID.
func (cs *CommandService) GetCommands(req *apiv1.GetCommandsRequest) (*apiv1.GetCommandsResponse, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	resp := &apiv1.GetCommandsResponse{}
	cmds := cs.listByType(req.Users, req.UserIds, model.TaskTypeCommand, req.WorkspaceId)
	for _, c := range cmds {
		resp.Commands = append(resp.Commands, c.ToV1Command())
	}
	return resp, nil
}

// GetCommand looks up a command by ID returns a summary of the its state and configuration.
func (cs *CommandService) GetCommand(req *apiv1.GetCommandRequest) (*apiv1.GetCommandResponse, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	c, err := cs.getNTSC(model.TaskID(req.CommandId), model.TaskTypeCommand)
	if err != nil {
		return nil, api.NotFoundErrs("command", req.CommandId, true)
	}

	return &apiv1.GetCommandResponse{
		Command: c.ToV1Command(),
		Config:  protoutils.ToStruct(c.Config),
	}, nil
}

// GetNotebooks returns all notebooks in the command service registry matching the workspace ID.
func (cs *CommandService) GetNotebooks(req *apiv1.GetNotebooksRequest) (*apiv1.GetNotebooksResponse, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	resp := &apiv1.GetNotebooksResponse{}
	cmds := cs.listByType(req.Users, req.UserIds, model.TaskTypeNotebook, req.WorkspaceId)
	for _, c := range cmds {
		resp.Notebooks = append(resp.Notebooks, c.ToV1Notebook())
	}
	return resp, nil
}

// GetNotebook looks up a notebook by ID returns a summary of the its state and configuration.
func (cs *CommandService) GetNotebook(req *apiv1.GetNotebookRequest) (*apiv1.GetNotebookResponse, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	c, err := cs.getNTSC(model.TaskID(req.NotebookId), model.TaskTypeNotebook)
	if err != nil {
		return nil, api.NotFoundErrs("notebook", req.NotebookId, true)
	}

	return &apiv1.GetNotebookResponse{
		Notebook: c.ToV1Notebook(),
		Config:   protoutils.ToStruct(c.Config),
	}, nil
}

// GetShells returns all shells in the command service registry matching the workspace ID.
func (cs *CommandService) GetShells(req *apiv1.GetShellsRequest) (*apiv1.GetShellsResponse, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	resp := &apiv1.GetShellsResponse{}
	cmds := cs.listByType(req.Users, req.UserIds, model.TaskTypeShell, req.WorkspaceId)
	for _, c := range cmds {
		resp.Shells = append(resp.Shells, c.ToV1Shell())
	}
	return resp, nil
}

// GetShell looks up a shell by ID returns a summary of the its state and configuration.
func (cs *CommandService) GetShell(req *apiv1.GetShellRequest) (*apiv1.GetShellResponse, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	c, err := cs.getNTSC(model.TaskID(req.ShellId), model.TaskTypeShell)
	if err != nil {
		return nil, api.NotFoundErrs("shell", req.ShellId, true)
	}

	return &apiv1.GetShellResponse{
		Shell:  c.ToV1Shell(),
		Config: protoutils.ToStruct(c.Config),
	}, nil
}

// GetTensorboards returns all tbs in the command service registry matching the workspace ID.
func (cs *CommandService) GetTensorboards(req *apiv1.GetTensorboardsRequest) (*apiv1.GetTensorboardsResponse, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	resp := &apiv1.GetTensorboardsResponse{}
	cmds := cs.listByType(req.Users, req.UserIds, model.TaskTypeTensorboard, req.WorkspaceId)
	for _, c := range cmds {
		resp.Tensorboards = append(resp.Tensorboards, c.ToV1Tensorboard())
	}
	return resp, nil
}

// GetTensorboard looks up a tensorboard by ID returns a summary of the its state and configuration.
func (cs *CommandService) GetTensorboard(req *apiv1.GetTensorboardRequest) (*apiv1.GetTensorboardResponse, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	c, err := cs.getNTSC(model.TaskID(req.TensorboardId), model.TaskTypeTensorboard)
	if err != nil {
		return nil, api.NotFoundErrs("tensorboard", req.TensorboardId, true)
	}

	return &apiv1.GetTensorboardResponse{
		Tensorboard: c.ToV1Tensorboard(),
		Config:      protoutils.ToStruct(c.Config),
	}, nil
}
