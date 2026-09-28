package experiment

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/determined-ai/determined/master/internal/authz"
	"github.com/determined-ai/determined/master/pkg/model"
)

func TestBasicExperimentControlRequiresOwnerOrAdmin(t *testing.T) {
	ctx := context.Background()
	ownerID := model.UserID(11)
	auth := &ExperimentAuthZBasic{}
	checks := map[string]func(model.User, *model.Experiment) error{
		"edit":      func(u model.User, e *model.Experiment) error { return auth.CanEditExperiment(ctx, u, e) },
		"metadata":  func(u model.User, e *model.Experiment) error { return auth.CanEditExperimentsMetadata(ctx, u, e) },
		"delete":    func(u model.User, e *model.Experiment) error { return auth.CanDeleteExperiment(ctx, u, e) },
		"max slots": func(u model.User, e *model.Experiment) error { return auth.CanSetExperimentsMaxSlots(ctx, u, e, 2) },
		"weight":    func(u model.User, e *model.Experiment) error { return auth.CanSetExperimentsWeight(ctx, u, e, 2) },
		"priority":  func(u model.User, e *model.Experiment) error { return auth.CanSetExperimentsPriority(ctx, u, e, 2) },
		"checkpoint": func(u model.User, e *model.Experiment) error {
			return auth.CanSetExperimentsCheckpointGCPolicy(ctx, u, e)
		},
	}
	tests := []struct {
		name    string
		user    model.User
		ownerID *model.UserID
		allowed bool
	}{
		{"owner", model.User{ID: ownerID}, &ownerID, true},
		{"admin", model.User{ID: 12, Admin: true}, &ownerID, true},
		{"other user", model.User{ID: 12}, &ownerID, false},
		{"missing owner", model.User{ID: ownerID}, nil, false},
		{"admin with missing owner", model.User{ID: 12, Admin: true}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for name, check := range checks {
				t.Run(name, func(t *testing.T) {
					err := check(tt.user, &model.Experiment{OwnerID: tt.ownerID})
					if tt.allowed {
						require.NoError(t, err)
					} else {
						require.True(t, authz.IsPermissionDenied(err), "error: %v", err)
					}
				})
			}
		})
	}
}
