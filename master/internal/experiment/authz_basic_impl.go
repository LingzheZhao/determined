package experiment

import (
	"context"
	"slices"

	"github.com/uptrace/bun"

	"github.com/determined-ai/determined/master/internal/authz"
	"github.com/determined-ai/determined/master/pkg/model"
	"github.com/determined-ai/determined/proto/pkg/projectv1"
	"github.com/determined-ai/determined/proto/pkg/rbacv1"
)

// ExperimentAuthZBasic is basic OSS controls.
type ExperimentAuthZBasic struct{}

// CanGetExperiment always returns true and a nil error.
func (a *ExperimentAuthZBasic) CanGetExperiment(
	ctx context.Context, curUser model.User, e *model.Experiment,
) error {
	return nil
}

// CanGetExperimentArtifacts always returns a nil error.
func (a *ExperimentAuthZBasic) CanGetExperimentArtifacts(
	ctx context.Context, curUser model.User, e *model.Experiment,
) error {
	return nil
}

// CanDeleteExperiment allows only the owner or an administrator.
func (a *ExperimentAuthZBasic) CanDeleteExperiment(
	ctx context.Context, curUser model.User, e *model.Experiment,
) error {
	return a.CanEditExperiment(ctx, curUser, e)
}

// FilterExperimentsQuery limits destructive and control operations to owned experiments.
// Read-only queries retain the normal OSS visibility behavior.
func (a *ExperimentAuthZBasic) FilterExperimentsQuery(
	ctx context.Context, curUser model.User, proj *projectv1.Project, query *bun.SelectQuery,
	permissions []rbacv1.PermissionType,
) (*bun.SelectQuery, error) {
	if !curUser.Admin && (slices.Contains(permissions,
		rbacv1.PermissionType_PERMISSION_TYPE_UPDATE_EXPERIMENT) ||
		slices.Contains(permissions, rbacv1.PermissionType_PERMISSION_TYPE_UPDATE_EXPERIMENT_METADATA) ||
		slices.Contains(permissions, rbacv1.PermissionType_PERMISSION_TYPE_DELETE_EXPERIMENT)) {
		query = query.Where("e.owner_id = ?", curUser.ID)
	}
	return query, nil
}

// FilterExperimentLabelsQuery returns the query unmodified and a nil error.
func (a *ExperimentAuthZBasic) FilterExperimentLabelsQuery(
	ctx context.Context, curUser model.User, proj *projectv1.Project, query *bun.SelectQuery,
) (*bun.SelectQuery, error) {
	return query, nil
}

// CanPreviewHPSearch always returns a nil error.
func (a *ExperimentAuthZBasic) CanPreviewHPSearch(
	ctx context.Context, curUser model.User,
) error {
	return nil
}

// CanEditExperiment allows only the owner or an administrator. An absent owner
// is not authority for a non-administrator to change an experiment.
func (a *ExperimentAuthZBasic) CanEditExperiment(
	ctx context.Context, curUser model.User, e *model.Experiment,
) error {
	if curUser.Admin || e != nil && e.OwnerID != nil && *e.OwnerID == curUser.ID {
		return nil
	}
	return authz.PermissionDeniedError{}.WithPrefix(
		"non-admin users may not control other users' experiments",
	)
}

// CanEditExperimentsMetadata follows the same owner rule as other mutations.
func (a *ExperimentAuthZBasic) CanEditExperimentsMetadata(
	ctx context.Context, curUser model.User, e *model.Experiment,
) error {
	return a.CanEditExperiment(ctx, curUser, e)
}

// CanCreateExperiment always returns a nil error.
func (a *ExperimentAuthZBasic) CanCreateExperiment(
	ctx context.Context, curUser model.User, proj *projectv1.Project,
) error {
	return nil
}

// CanForkFromExperiment always returns a nil error.
func (a *ExperimentAuthZBasic) CanForkFromExperiment(
	ctx context.Context, curUser model.User, e *model.Experiment,
) error {
	return nil
}

// CanSetExperimentsMaxSlots follows the experiment owner rule.
func (a *ExperimentAuthZBasic) CanSetExperimentsMaxSlots(
	ctx context.Context, curUser model.User, e *model.Experiment, slots int,
) error {
	return a.CanEditExperiment(ctx, curUser, e)
}

// CanSetExperimentsWeight follows the experiment owner rule.
func (a *ExperimentAuthZBasic) CanSetExperimentsWeight(
	ctx context.Context, curUser model.User, e *model.Experiment, weight float64,
) error {
	return a.CanEditExperiment(ctx, curUser, e)
}

// CanSetExperimentsPriority follows the experiment owner rule.
func (a *ExperimentAuthZBasic) CanSetExperimentsPriority(
	ctx context.Context, curUser model.User, e *model.Experiment, priority int,
) error {
	return a.CanEditExperiment(ctx, curUser, e)
}

// CanSetExperimentsCheckpointGCPolicy follows the experiment owner rule.
func (a *ExperimentAuthZBasic) CanSetExperimentsCheckpointGCPolicy(
	ctx context.Context, curUser model.User, e *model.Experiment,
) error {
	return a.CanEditExperiment(ctx, curUser, e)
}

func init() {
	AuthZProvider.Register("basic", &ExperimentAuthZBasic{})
}
