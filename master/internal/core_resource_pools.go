package internal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/determined-ai/determined/master/internal/cluster"
	"github.com/determined-ai/determined/master/internal/config"
	detContext "github.com/determined-ai/determined/master/internal/context"
	"github.com/determined-ai/determined/master/internal/db"
	"github.com/determined-ai/determined/master/internal/rm/agentrm"
	"github.com/determined-ai/determined/master/internal/user"
	"github.com/determined-ai/determined/master/pkg/model"
)

const maxDynamicPoolRequestBytes = 1 << 20

var dynamicPoolRequestUser = func(
	request *http.Request,
) (*model.User, *model.UserSession, error) {
	return user.GetService().UserAndSessionFromRequest(request)
}

type createDynamicResourcePoolRequest struct {
	ClusterName    string                    `json:"cluster_name,omitempty"`
	IdempotencyKey string                    `json:"idempotency_key"`
	Config         config.ResourcePoolConfig `json:"config"`
}

type dynamicResourcePoolListResponse struct {
	ResourcePools []db.DynamicResourcePool `json:"resource_pools"`
}

func (m *Master) registerDynamicResourcePoolRoutes() {
	group := m.echo.Group("/api/v1/resource-pools/dynamic")
	group.POST("", m.createDynamicResourcePool, m.dynamicPoolAuth(true))
	group.GET("", m.listDynamicResourcePools, m.dynamicPoolAuth(false))
	group.POST("/:name/retry", m.retryDynamicResourcePool, m.dynamicPoolAuth(true))
}

// dynamicPoolAuth authenticates direct /api/v1 Echo routes explicitly. Generic Echo auth exempts
// /api/v1 because normal routes there are authenticated by gRPC interceptors; these routes do not
// pass through gRPC.
func (m *Master) dynamicPoolAuth(update bool) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			currentUser, session, err := dynamicPoolRequestUser(c.Request())
			switch {
			case errors.Is(err, db.ErrNotFound):
				return echo.NewHTTPError(http.StatusUnauthorized, "invalid authentication")
			case err != nil:
				var httpErr *echo.HTTPError
				if errors.As(err, &httpErr) && httpErr.Code == http.StatusUnauthorized {
					return httpErr
				}
				return err
			case !currentUser.Active:
				return echo.NewHTTPError(http.StatusForbidden, "user not active")
			}

			ctx := c.(*detContext.DetContext)
			ctx.SetUser(*currentUser)
			ctx.SetUserSession(*session)
			var permErr error
			if update {
				permErr, err = cluster.AuthZProvider.Get().CanUpdateMasterConfig(
					c.Request().Context(), currentUser,
				)
			} else {
				permErr, err = cluster.AuthZProvider.Get().CanGetMasterConfig(
					c.Request().Context(), currentUser,
				)
			}
			if err != nil {
				return err
			}
			if permErr != nil {
				return echo.NewHTTPError(http.StatusForbidden, permErr.Error())
			}
			return next(c)
		}
	}
}

func (m *Master) createDynamicResourcePool(c echo.Context) error {
	var request createDynamicResourcePoolRequest
	if err := decodeStrictBoundedJSON(c, &request); err != nil {
		return err
	}
	resourceManager, clusterName, err := m.selectDynamicAgentRM(request.ClusterName)
	if err != nil {
		return err
	}
	if staticCluster, ok := m.staticResourcePoolCluster(request.Config.PoolName); ok {
		return echo.NewHTTPError(http.StatusConflict, fmt.Sprintf(
			"resource pool name %q conflicts with static pool in resource manager %q",
			request.Config.PoolName, staticCluster,
		))
	}
	record, created, err := resourceManager.CreateDynamicResourcePool(
		c.Request().Context(), request.IdempotencyKey, request.Config,
		m.config.TaskContainerDefaults,
	)
	if err != nil {
		return dynamicPoolHTTPError(err)
	}
	if record.ClusterName != clusterName {
		return echo.NewHTTPError(http.StatusInternalServerError, "resource manager mismatch")
	}
	if record.State == db.DynamicResourcePoolReady &&
		!resourceManager.IsDynamicResourcePoolReady(record.PoolName) {
		record.State = db.DynamicResourcePoolPending
	}
	statusCode := http.StatusOK
	if created {
		statusCode = http.StatusCreated
	}
	return c.JSON(statusCode, printableDynamicResourcePool(record))
}

func (m *Master) listDynamicResourcePools(c echo.Context) error {
	clusterName := c.QueryParam("cluster_name")
	if clusterName != "" {
		if _, ok := m.allRms[clusterName]; !ok {
			return echo.NewHTTPError(
				http.StatusNotFound, fmt.Sprintf("resource manager %q not found", clusterName),
			)
		}
	}
	records, err := m.db.ListDynamicResourcePools(c.Request().Context(), clusterName)
	if err != nil {
		return err
	}
	for i := range records {
		if records[i].State == db.DynamicResourcePoolReady {
			if resourceManager, ok := m.allRms[records[i].ClusterName].(*agentrm.ResourceManager); ok &&
				!resourceManager.IsDynamicResourcePoolReady(records[i].PoolName) {
				records[i].State = db.DynamicResourcePoolPending
			}
		}
		records[i] = printableDynamicResourcePool(records[i])
	}
	return c.JSON(http.StatusOK, dynamicResourcePoolListResponse{ResourcePools: records})
}

func (m *Master) retryDynamicResourcePool(c echo.Context) error {
	if err := requireEmptyBody(c); err != nil {
		return err
	}
	resourceManager, _, err := m.selectDynamicAgentRM(c.QueryParam("cluster_name"))
	if err != nil {
		return err
	}
	record, err := resourceManager.RetryDynamicResourcePool(
		c.Request().Context(), c.Param("name"),
	)
	if err != nil {
		return dynamicPoolHTTPError(err)
	}
	return c.JSON(http.StatusOK, printableDynamicResourcePool(record))
}

func (m *Master) selectDynamicAgentRM(
	requestedCluster string,
) (*agentrm.ResourceManager, string, error) {
	if requestedCluster != "" {
		resourceManager, ok := m.allRms[requestedCluster]
		if !ok {
			return nil, "", echo.NewHTTPError(
				http.StatusNotFound,
				fmt.Sprintf("resource manager %q not found", requestedCluster),
			)
		}
		agentRM, ok := resourceManager.(*agentrm.ResourceManager)
		if !ok {
			return nil, "", echo.NewHTTPError(
				http.StatusBadRequest,
				fmt.Sprintf("resource manager %q is not an agent resource manager", requestedCluster),
			)
		}
		return agentRM, requestedCluster, nil
	}

	var selected *agentrm.ResourceManager
	var clusterName string
	for name, resourceManager := range m.allRms {
		agentRM, ok := resourceManager.(*agentrm.ResourceManager)
		if !ok {
			continue
		}
		if selected != nil {
			return nil, "", echo.NewHTTPError(
				http.StatusBadRequest,
				"cluster_name is required when multiple agent resource managers are configured",
			)
		}
		selected = agentRM
		clusterName = name
	}
	if selected == nil {
		return nil, "", echo.NewHTTPError(
			http.StatusBadRequest, "dynamic pools require an agent resource manager",
		)
	}
	return selected, clusterName, nil
}

func (m *Master) staticResourcePoolCluster(poolName string) (string, bool) {
	for _, rmConfig := range m.config.ResourceManagers() {
		for _, pool := range rmConfig.ResourcePools {
			if pool.PoolName == poolName {
				return rmConfig.ResourceManager.ClusterName(), true
			}
		}
	}
	return "", false
}

func decodeStrictBoundedJSON(c echo.Context, target interface{}) error {
	contentType := c.Request().Header.Get(echo.HeaderContentType)
	if !strings.HasPrefix(strings.ToLower(contentType), echo.MIMEApplicationJSON) {
		return echo.NewHTTPError(http.StatusUnsupportedMediaType, "Content-Type must be application/json")
	}
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, maxDynamicPoolRequestBytes)
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return echo.NewHTTPError(http.StatusRequestEntityTooLarge, "request body exceeds 1 MiB")
		}
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("reading JSON body: %v", err))
	}
	if err = validateDynamicPoolRequestJSON(body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("invalid JSON body: %v", err))
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "JSON body must contain exactly one value")
		}
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("invalid JSON body: %v", err))
	}
	return nil
}

// ResourcePoolConfig and TaskContainerDefaultsConfig implement custom JSON unmarshaling, so the
// standard DisallowUnknownFields option cannot see unknown nested fields. Validate the API schema
// at each configuration object boundary before invoking those unmarshallers. Kubernetes pod specs
// remain ordinary Kubernetes JSON objects and are validated by their own decoder.
func validateDynamicPoolRequestJSON(body []byte) error {
	var request map[string]json.RawMessage
	if err := json.Unmarshal(body, &request); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	if err := rejectUnknownJSONFields(request, map[string]bool{
		"cluster_name": true, "idempotency_key": true, "config": true,
	}, "request"); err != nil {
		return err
	}
	configRaw, ok := request["config"]
	if !ok {
		return fmt.Errorf("config is required")
	}
	var rawConfig map[string]json.RawMessage
	if err := json.Unmarshal(configRaw, &rawConfig); err != nil {
		return fmt.Errorf("config must be a JSON object: %w", err)
	}
	if err := rejectUnknownJSONFields(
		rawConfig, jsonFieldsForType(reflect.TypeOf(config.ResourcePoolConfig{})), "config",
	); err != nil {
		return err
	}
	if raw, ok := rawConfig["provider"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("config.provider is not supported")
	}
	if raw, ok := rawConfig["scheduler"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		var scheduler map[string]json.RawMessage
		if err := json.Unmarshal(raw, &scheduler); err != nil {
			return fmt.Errorf("config.scheduler must be a JSON object: %w", err)
		}
		allowed := jsonFieldsForType(reflect.TypeOf(config.SchedulerConfig{}))
		allowed["type"] = true
		allowed["preemption"] = true
		allowed["default_priority"] = true
		if err := rejectUnknownJSONFields(scheduler, allowed, "config.scheduler"); err != nil {
			return err
		}
	}
	if raw, ok := rawConfig["task_container_defaults"]; ok &&
		!bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		var defaults map[string]json.RawMessage
		if err := json.Unmarshal(raw, &defaults); err != nil {
			return fmt.Errorf("config.task_container_defaults must be a JSON object: %w", err)
		}
		defaultsType := reflect.TypeOf(model.TaskContainerDefaultsConfig{})
		if err := rejectUnknownJSONFields(
			defaults, jsonFieldsForType(defaultsType), "config.task_container_defaults",
		); err != nil {
			return err
		}
		for _, field := range []string{"registry_auth", "kubernetes"} {
			nestedRaw, exists := defaults[field]
			if !exists || bytes.Equal(bytes.TrimSpace(nestedRaw), []byte("null")) {
				continue
			}
			nestedType, exists := jsonFieldType(defaultsType, field)
			if !exists {
				continue
			}
			var nested map[string]json.RawMessage
			if err := json.Unmarshal(nestedRaw, &nested); err != nil {
				return fmt.Errorf("config.task_container_defaults.%s must be a JSON object: %w", field, err)
			}
			if err := rejectUnknownJSONFields(
				nested, jsonFieldsForType(nestedType), "config.task_container_defaults."+field,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func rejectUnknownJSONFields(
	object map[string]json.RawMessage, allowed map[string]bool, path string,
) error {
	for field := range object {
		if !allowed[field] {
			return fmt.Errorf("unknown field %q in %s", field, path)
		}
	}
	return nil
}

func jsonFieldsForType(typ reflect.Type) map[string]bool {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	result := make(map[string]bool)
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name != "" && name != "-" {
			result[name] = true
		}
	}
	return result
}

func jsonFieldType(typ reflect.Type, jsonName string) (reflect.Type, bool) {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == jsonName {
			return field.Type, true
		}
	}
	return nil, false
}

func requireEmptyBody(c echo.Context) error {
	c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, maxDynamicPoolRequestBytes)
	body, err := io.ReadAll(c.Request().Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return echo.NewHTTPError(http.StatusRequestEntityTooLarge, "request body exceeds 1 MiB")
		}
		return err
	}
	if len(bytes.TrimSpace(body)) != 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "retry request body must be empty")
	}
	return nil
}

func dynamicPoolHTTPError(err error) error {
	switch {
	case errors.Is(err, agentrm.ErrInvalidDynamicResourcePool):
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	case errors.Is(err, agentrm.ErrStaticResourcePoolConflict),
		errors.Is(err, db.ErrDynamicResourcePoolConflict),
		errors.Is(err, db.ErrDynamicResourcePoolNotFailed):
		return echo.NewHTTPError(http.StatusConflict, err.Error())
	case errors.Is(err, db.ErrDynamicResourcePoolNotFound):
		return echo.NewHTTPError(http.StatusNotFound, err.Error())
	default:
		return err
	}
}

func printableDynamicResourcePool(record db.DynamicResourcePool) db.DynamicResourcePool {
	var cfg config.ResourcePoolConfig
	if err := json.Unmarshal(record.Config, &cfg); err != nil {
		record.Config = json.RawMessage(`{"redacted":true}`)
		return record
	}
	if cfg.TaskContainerDefaults != nil && cfg.TaskContainerDefaults.RegistryAuth != nil {
		auth := *cfg.TaskContainerDefaults.RegistryAuth
		if auth.Password != "" {
			auth.Password = "********"
		}
		if auth.Auth != "" {
			auth.Auth = "********"
		}
		if auth.IdentityToken != "" {
			auth.IdentityToken = "********"
		}
		if auth.RegistryToken != "" {
			auth.RegistryToken = "********"
		}
		cfg.TaskContainerDefaults.RegistryAuth = &auth
	}
	printable, err := json.Marshal(cfg.Printable())
	if err == nil {
		record.Config = printable
	}
	return record
}
