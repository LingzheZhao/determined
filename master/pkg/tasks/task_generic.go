package tasks

import (
	"archive/tar"
	"strconv"
	"time"

	"github.com/determined-ai/determined/master/pkg/archive"
	"github.com/determined-ai/determined/master/pkg/cproto"
	"github.com/determined-ai/determined/master/pkg/etc"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/master/pkg/schemas"
	"github.com/determined-ai/determined/master/pkg/schemas/expconf"
)

// GenericTaskSpec is the generic task spec.
type GenericTaskSpec struct {
	Base           TaskSpec
	ProjectID      int
	WorkspaceID    int
	RegisteredTime time.Time
	JobID          model.JobID

	GenericTaskConfig model.GenericTaskConfig
}

// ToTaskSpec converts the generic task spec to the common task spec.
func (s GenericTaskSpec) ToTaskSpec() TaskSpec {
	res := s.Base

	commandEntrypoint := "/run/determined/generic-task-entrypoint.sh"
	res.Entrypoint = []string{commandEntrypoint}
	res.Entrypoint = append(res.Entrypoint, s.GenericTaskConfig.Entrypoint...)
	commandEntryArchive := wrapArchive(archive.Archive{
		res.AgentUserGroup.OwnedArchiveItem(
			commandEntrypoint,
			etc.MustStaticFile("generic-task-entrypoint.sh"),
			0o700,
			tar.TypeReg,
		),
	}, "/")

	res.PbsConfig = s.GenericTaskConfig.Pbs
	res.SlurmConfig = s.GenericTaskConfig.Slurm

	res.ExtraArchives = []cproto.RunArchive{commandEntryArchive}
	res.Environment = s.GenericTaskConfig.Environment.ToExpconf()

	res.WorkDir = DefaultWorkDir
	if s.GenericTaskConfig.WorkDir != nil {
		res.WorkDir = *s.GenericTaskConfig.WorkDir
	}
	res.ResolveWorkDir()

	res.ResourcesConfig = s.GenericTaskConfig.Resources

	res.Description = s.DisplayName()

	res.Mounts = ToDockerMounts(s.GenericTaskConfig.BindMounts.ToExpconf(), res.WorkDir)

	if shm := s.GenericTaskConfig.Resources.ShmSize(); shm != nil {
		res.ShmSize = int64(*shm)
	}

	res.TaskType = model.TaskTypeGeneric

	return res
}

// DisplayName is the task's configured name, or "Generic Task <task id>".
func (s GenericTaskSpec) DisplayName() string {
	switch {
	case s.GenericTaskConfig.Name != "":
		return s.GenericTaskConfig.Name
	case s.Base.TaskID != "":
		return "Generic Task " + s.Base.TaskID
	default:
		return "Generic Task"
	}
}

// MakeEnvPorts adds the proxy ports to `Environment.Ports`, the ports exposed in the container
// config, keeping ports the config already lists. It runs when the task is created; the result is
// persisted with the spec, so an unpaused or restored task exposes the same ports.
func (s *GenericTaskSpec) MakeEnvPorts() {
	if s.GenericTaskConfig.Environment.Ports == nil {
		s.GenericTaskConfig.Environment.Ports = map[string]int{}
	}
	for _, pp := range s.ProxyPorts() {
		port := pp.ProxyPort()
		s.GenericTaskConfig.Environment.Ports[strconv.Itoa(port)] = port
	}
}

// ProxyPorts combines the system proxy ports and the ports of `environment.proxy_ports`.
func (s *GenericTaskSpec) ProxyPorts() expconf.ProxyPortsConfig {
	env := schemas.WithDefaults(s.GenericTaskConfig.Environment.ToExpconf())
	epp := schemas.WithDefaults(s.Base.ExtraProxyPorts)
	out := make(expconf.ProxyPortsConfig, 0, len(epp)+len(env.ProxyPorts()))

	out = append(out, epp...)
	out = append(out, env.ProxyPorts()...)

	return out
}
