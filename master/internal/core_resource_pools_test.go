package internal

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/docker/docker/api/types/registry"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/determined-ai/determined/master/internal/cluster"
	detContext "github.com/determined-ai/determined/master/internal/context"
	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/pkg/model"
)

func TestValidateDynamicPoolRequestJSON(t *testing.T) {
	valid := []byte(`{
        "idempotency_key":"request-1",
        "config":{
            "pool_name":"online",
            "scheduler":{"type":"priority","default_priority":42},
            "task_container_defaults":{"force_pull_image":true,
                "registry_auth":{"username":"u","password":"p"}}
        }
    }`)
	require.NoError(t, validateDynamicPoolRequestJSON(valid))

	tests := []string{
		`{"unknown":true,"idempotency_key":"x","config":{"pool_name":"p"}}`,
		`{"idempotency_key":"x","config":{"pool_name":"p","unknown":true}}`,
		`{"idempotency_key":"x","config":{"pool_name":"p","scheduler":{"unknown":true}}}`,
		`{"idempotency_key":"x","config":{"pool_name":"p","task_container_defaults":{"unknown":true}}}`,
		`{"idempotency_key":"x","config":{"pool_name":"p","task_container_defaults":{"registry_auth":{"unknown":true}}}}`,
		`{"idempotency_key":"x","config":{"pool_name":"p","provider":{"type":"aws"}}}`,
	}
	for _, body := range tests {
		require.Error(t, validateDynamicPoolRequestJSON([]byte(body)), body)
	}
}

func TestPrintableDynamicResourcePoolRedactsRegistryCredentials(t *testing.T) {
	cfg := map[string]interface{}{
		"pool_name": "online",
		"task_container_defaults": model.TaskContainerDefaultsConfig{
			RegistryAuth: &registry.AuthConfig{
				Username:      "user",
				Password:      "password-secret",
				IdentityToken: "identity-secret",
				RegistryToken: "registry-secret",
			},
		},
	}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	printed := printableDynamicResourcePool(db.DynamicResourcePool{Config: raw})
	require.NotContains(t, string(printed.Config), "password-secret")
	require.NotContains(t, string(printed.Config), "identity-secret")
	require.NotContains(t, string(printed.Config), "registry-secret")
	require.Contains(t, string(printed.Config), "********")

	printed = printableDynamicResourcePool(db.DynamicResourcePool{Config: json.RawMessage(`{`)})
	require.JSONEq(t, `{"redacted":true}`, string(printed.Config))
}

func TestDynamicPoolRouteExplicitlyAuthenticates(t *testing.T) {
	original := dynamicPoolRequestUser
	t.Cleanup(func() { dynamicPoolRequestUser = original })
	dynamicPoolRequestUser = func(
		*http.Request,
	) (*model.User, *model.UserSession, error) {
		return nil, nil, echo.ErrUnauthorized
	}

	e := echo.New()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/resource-pools/dynamic", nil)
	recorder := httptest.NewRecorder()
	ctx := &detContext.DetContext{Context: e.NewContext(request, recorder)}
	called := false
	handler := (&Master{}).dynamicPoolAuth(false)(func(echo.Context) error {
		called = true
		return nil
	})
	err := handler(ctx)
	var httpErr *echo.HTTPError
	require.ErrorAs(t, err, &httpErr)
	require.Equal(t, http.StatusUnauthorized, httpErr.Code)
	require.False(t, called)
}

func TestDynamicPoolRouteAuthorization(t *testing.T) {
	originalUser := dynamicPoolRequestUser
	originalAuthorize := authorizeDynamicPoolRequest
	t.Cleanup(func() {
		dynamicPoolRequestUser = originalUser
		authorizeDynamicPoolRequest = originalAuthorize
	})
	for _, test := range []struct {
		name       string
		active     bool
		admin      bool
		update     bool
		wantCode   int
		wantCalled bool
	}{
		{name: "admin create", active: true, admin: true, update: true, wantCalled: true},
		{name: "admin list", active: true, admin: true, wantCalled: true},
		{name: "read-only create", active: true, update: true, wantCode: http.StatusForbidden},
		{name: "viewer list", active: true, wantCode: http.StatusForbidden},
		{name: "inactive admin", admin: true, update: true, wantCode: http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			dynamicPoolRequestUser = func(
				*http.Request,
			) (*model.User, *model.UserSession, error) {
				return &model.User{Active: test.active, Admin: test.admin}, &model.UserSession{}, nil
			}
			authorizeDynamicPoolRequest = func(
				request *http.Request, currentUser *model.User, update bool,
			) (error, error) {
				return authorizeDynamicPoolWithProvider(
					&cluster.MiscAuthZBasic{}, request, currentUser, update,
				)
			}
			e := echo.New()
			request := httptest.NewRequest(http.MethodGet, "/api/v1/resource-pools/dynamic", nil)
			ctx := &detContext.DetContext{Context: e.NewContext(request, httptest.NewRecorder())}
			called := false
			err := (&Master{}).dynamicPoolAuth(test.update)(func(echo.Context) error {
				called = true
				return nil
			})(ctx)
			require.Equal(t, test.wantCalled, called)
			if test.wantCode == 0 {
				require.NoError(t, err)
				return
			}
			var httpErr *echo.HTTPError
			require.ErrorAs(t, err, &httpErr)
			require.Equal(t, test.wantCode, httpErr.Code)
		})
	}
}
