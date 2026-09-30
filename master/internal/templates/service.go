package templates

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/ghodss/yaml"
	"github.com/pkg/errors"

	"github.com/determined-ai/determined/master/internal/api"
	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/pkg/model"
)

// TemplateByName looks up a config template by name in a database.
func TemplateByName(ctx context.Context, name string) (model.Template, error) {
	var dest model.Template
	err := db.Bun().NewSelect().Table("templates").
		ColumnExpr("*").
		Where("name = ?", name).
		Scan(ctx, &dest)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return model.Template{}, db.ErrNotFound
	case err != nil:
		return dest, fmt.Errorf("fetching template %s from database: %w", name, err)
	}
	return dest, nil
}

// UnmarshalTemplateConfig unmarshals the template config into `o` and returns api-ready errors.
func UnmarshalTemplateConfig(
	ctx context.Context,
	name string,
	user *model.User,
	out interface{},
	disallowUnknownFields bool,
) error {
	tpl, err := ViewableTemplate(ctx, name, user)
	if err != nil {
		return err
	}
	return UnmarshalConfig(tpl, out, disallowUnknownFields)
}

// ViewableTemplate returns the template of a name if the user may view it. A template that does
// not exist and one the user may not view are both not found.
func ViewableTemplate(ctx context.Context, name string, user *model.User) (model.Template, error) {
	tpl, err := TemplateByName(ctx, name)
	switch {
	case errors.Is(err, db.ErrNotFound):
		return model.Template{}, api.NotFoundErrs("template", name, true)
	case err != nil:
		return model.Template{}, err
	}

	permErr, err := AuthZProvider.Get().CanViewTemplate(
		ctx,
		user,
		model.AccessScopeID(tpl.WorkspaceID),
	)
	switch {
	case err != nil:
		return model.Template{}, err
	case permErr != nil:
		return model.Template{}, api.NotFoundErrs("template", name, true)
	}
	return tpl, nil
}

// UnmarshalConfig unmarshals the config of a template into out.
func UnmarshalConfig(tpl model.Template, out interface{}, disallowUnknownFields bool) error {
	var opts []yaml.JSONOpt
	if disallowUnknownFields {
		opts = append(opts, yaml.DisallowUnknownFields)
	}
	if err := yaml.Unmarshal(tpl.Config, out, opts...); err != nil {
		return fmt.Errorf("yaml.Unmarshal(template=%s): %w", tpl.Name, err)
	}
	return nil
}

// DeleteWorkspaceTemplates deletes all the templates in a workspace.
func DeleteWorkspaceTemplates(ctx context.Context, workspaceID int) error {
	_, err := db.Bun().NewDelete().
		Model(&model.Template{}).
		Where("workspace_id = ?", workspaceID).
		Exec(ctx)
	return err
}
