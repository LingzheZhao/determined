package model

import (
	"database/sql/driver"
	"encoding/json"
	"unicode/utf8"

	"github.com/pkg/errors"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/determined-ai/determined/proto/pkg/taskv1"
)

// ExitClass is the outcome class of an allocation's exit, stored in allocations.exit_class. It
// records how an allocation ended, not who asked it to stop. A NULL class means the allocation
// has not exited, or ended before exit classes were recorded.
type ExitClass string

const (
	// ExitClassNone denotes that the allocation did not fail: it completed, stopped early, was
	// preempted or killed, was aborted before it started, or was still queued when the master
	// restarted.
	ExitClassNone ExitClass = "NONE"
	// ExitClassPlacementUnsatisfied denotes that the scheduler could not place the allocation as
	// requested.
	ExitClassPlacementUnsatisfied ExitClass = "PLACEMENT_UNSATISFIED"
	// ExitClassNodePreflightFailed denotes that a node rejected the allocation before starting
	// its containers.
	ExitClassNodePreflightFailed ExitClass = "NODE_PREFLIGHT_FAILED"
	// ExitClassWorkloadInitializationFailed denotes that the workload failed before it finished
	// initializing.
	ExitClassWorkloadInitializationFailed ExitClass = "WORKLOAD_INITIALIZATION_FAILED"
	// ExitClassWorkloadFailed denotes that the workload failed after it started.
	ExitClassWorkloadFailed ExitClass = "WORKLOAD_FAILED"
	// ExitClassInfrastructureFailed denotes that an agent, the connection to it, the master, or
	// the resource manager failed.
	ExitClassInfrastructureFailed ExitClass = "INFRASTRUCTURE_FAILED"
)

// Proto returns the proto representation of the exit class.
func (c *ExitClass) Proto() taskv1.ExitClass {
	if c == nil {
		return taskv1.ExitClass_EXIT_CLASS_UNSPECIFIED
	}
	return taskv1.ExitClass(taskv1.ExitClass_value["EXIT_CLASS_"+string(*c)])
}

// exitDetailMessageLimit bounds the message kept in an exit detail, in bytes.
const exitDetailMessageLimit = 4096

// ExitDetail is the structured detail of an allocation's exit, stored in allocations.exit_detail.
type ExitDetail struct {
	// FailureType is the taskv1.FailureType name of the failure, if the exit had one.
	FailureType string `json:"failure_type,omitempty"`
	// ExitCode is the container exit code, if there was one.
	ExitCode *int32 `json:"exit_code,omitempty"`
	// Message describes the failure, truncated to a few KiB.
	Message string `json:"message,omitempty"`
}

// NewExitDetail returns an exit detail, truncating the message to a bounded size.
func NewExitDetail(failureType string, exitCode *int32, message string) *ExitDetail {
	if len(message) > exitDetailMessageLimit {
		cut := exitDetailMessageLimit
		for cut > 0 && !utf8.RuneStart(message[cut]) {
			cut--
		}
		message = message[:cut]
	}
	return &ExitDetail{FailureType: failureType, ExitCode: exitCode, Message: message}
}

// Value marshals the exit detail to JSON text, which both bun and database/sql write to a
// jsonb column unchanged.
func (d ExitDetail) Value() (driver.Value, error) {
	bytes, err := json.Marshal(d)
	if err != nil {
		return nil, errors.Wrap(err, "error marshaling exit detail")
	}
	return string(bytes), nil
}

// Scan unmarshals the exit detail from JSON.
func (d *ExitDetail) Scan(src interface{}) error {
	if src == nil {
		*d = ExitDetail{}
		return nil
	}
	var bytes []byte
	switch src := src.(type) {
	case []byte:
		bytes = src
	case string:
		bytes = []byte(src)
	default:
		return errors.Errorf("unable to convert exit detail to []byte: %v", src)
	}
	if err := json.Unmarshal(bytes, d); err != nil {
		return errors.Wrapf(err, "unable to unmarshal exit detail: %s", bytes)
	}
	return nil
}

// Proto returns the proto representation of the exit detail.
func (d *ExitDetail) Proto() *structpb.Struct {
	if d == nil {
		return nil
	}
	fields := map[string]*structpb.Value{}
	if d.FailureType != "" {
		fields["failure_type"] = structpb.NewStringValue(d.FailureType)
	}
	if d.ExitCode != nil {
		fields["exit_code"] = structpb.NewNumberValue(float64(*d.ExitCode))
	}
	if d.Message != "" {
		fields["message"] = structpb.NewStringValue(d.Message)
	}
	return &structpb.Struct{Fields: fields}
}
