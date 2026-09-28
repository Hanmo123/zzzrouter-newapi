package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestChannelDefaultBaseURLsRequireReadPermission(t *testing.T) {
	assertChannelRoutePermission(t, http.MethodGet, "/default_base_urls", authz.ChannelRead, controller.GetChannelDefaultBaseURLs)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	registerChannelRoutes(engine.Group("/api"))
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/channel/default_base_urls", nil))
	assert.Equal(t, http.StatusUnauthorized, recorder.Code)
}

func TestChannelStatusRoutesUseExpectedPermissions(t *testing.T) {
	assertChannelRoutePermission(t, http.MethodGet, "/:id/vllm/status", authz.ChannelRead, controller.GetVLLMChannelStatus)
	assertChannelRoutePermission(t, http.MethodGet, "/:id/sglang/status", authz.ChannelRead, controller.GetSGLangChannelStatus)
	assertChannelRoutePermission(t, http.MethodPost, "/:id/status", authz.ChannelOperate, controller.UpdateChannelStatus)
	assertChannelRoutePermission(t, http.MethodPost, "/status/batch", authz.ChannelOperate, controller.BatchUpdateChannelStatus)
	assertChannelRoutePermission(t, http.MethodPut, "/", authz.ChannelWrite, controller.UpdateChannel)
	assertChannelRoutePermission(t, http.MethodGet, "/:id/degradation-exclusion", authz.ChannelRead, controller.GetChannelDegradationExclusion)
	assertChannelRoutePermission(t, http.MethodPut, "/:id/degradation-exclusion", authz.ChannelWrite, controller.SetChannelDegradationExclusion)
}

func TestChannelDeleteRoutesUseSensitiveWritePermission(t *testing.T) {
	assertChannelRoutePermission(t, http.MethodDelete, "/:id", authz.ChannelSensitiveWrite, controller.DeleteChannel)
	assertChannelRoutePermission(t, http.MethodPost, "/batch", authz.ChannelSensitiveWrite, controller.DeleteChannelBatch)
	assertChannelRoutePermission(t, http.MethodDelete, "/disabled", authz.ChannelSensitiveWrite, controller.DeleteDisabledChannel)
	assertChannelRoutePermission(t, http.MethodPut, "/", authz.ChannelWrite, controller.UpdateChannel)
	assertChannelRoutePermission(t, http.MethodPut, "/tag", authz.ChannelWrite, controller.EditTagChannels)
	assertChannelRoutePermission(t, http.MethodPost, "/batch/tag", authz.ChannelWrite, controller.BatchSetChannelTag)
}

func TestChannelStatusRoutesRegisterWithoutConflict(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	api := engine.Group("/api")

	require.NotPanics(t, func() {
		registerChannelRoutes(api)
	})
}

func TestProviderRoutesEnforcePermissionsAndCRUD(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	previousDB, previousLogDB := model.DB, model.LOG_DB
	previousMaster, previousRedis, previousType := common.IsMasterNode, common.RedisEnabled, common.MainDatabaseType()
	model.DB, model.LOG_DB = db, db
	common.IsMasterNode, common.RedisEnabled = true, false
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() {
		model.DB, model.LOG_DB = previousDB, previousLogDB
		common.IsMasterNode, common.RedisEnabled = previousMaster, previousRedis
		common.SetMainDatabaseType(previousType)
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.AutoMigrate(&model.Provider{}, &model.Channel{}, &model.Ability{}, &model.User{}, &model.CasbinRule{}, &model.AuthzRole{}, &model.AuditLog{}))
	require.NoError(t, authz.Init(db))
	for i, role := range []int{common.RoleCommonUser, common.RoleAdminUser, common.RoleRootUser} {
		token := fmt.Sprintf("provider-route-test-%d", i)
		user := model.User{Id: i + 1, Username: fmt.Sprintf("provider-user-%d", i), AffCode: fmt.Sprintf("prv%d", i), Role: role, Status: common.UserStatusEnabled, AccessToken: &token}
		require.NoError(t, db.Create(&user).Error)
	}
	require.NoError(t, authz.SetUserPermissions(2, authz.PermissionsMap{"channel": {"read": true, "write": false}}))
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	registerProviderRoutes(engine.Group("/api"))
	registerChannelRoutes(engine.Group("/api"))

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/providers/"},
		{http.MethodPost, "/api/providers/"},
		{http.MethodPut, "/api/providers/1"},
		{http.MethodDelete, "/api/providers/1"},
	} {
		for _, token := range []string{"", "provider-route-test-0", "provider-route-test-1"} {
			if token == "provider-route-test-1" && route.method == http.MethodGet {
				continue
			}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(route.method, route.path, strings.NewReader(`{"name":"forbidden"}`))
			if token != "" {
				request.Header.Set("Authorization", "Bearer "+token)
			}
			engine.ServeHTTP(recorder, request)
			want := http.StatusForbidden
			if token == "" {
				want = http.StatusUnauthorized
			}
			assert.Equal(t, want, recorder.Code, "%s %s", token, route.method)
		}
	}
	// Exercise the real auth + permission + handler chain, not a stub handler.
	providerID := 0
	for _, operation := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/providers/", `{"name":"reseller","description":"old","id":999,"code":"default","created_time":1,"deleted_at":"2020-01-01"}`},
		{http.MethodPut, "/api/providers/%d", `{"name":"updated","description":"","skip_degradation_check":true}`},
		{http.MethodGet, "/api/providers/?keyword=updated", ""},
		{http.MethodDelete, "/api/providers/%d", ""},
	} {
		path := operation.path
		if strings.Contains(path, "%d") {
			path = fmt.Sprintf(path, providerID)
		}
		request := httptest.NewRequest(operation.method, path, strings.NewReader(operation.body))
		token := "provider-route-test-2"
		if operation.method == http.MethodGet {
			token = "provider-route-test-1" // read-only admin may list metadata
		}
		request.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		engine.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
		var response struct {
			Success bool `json:"success"`
			Data    struct {
				ID                   int              `json:"id"`
				SkipDegradationCheck bool             `json:"skip_degradation_check"`
				CreatedTime          int64            `json:"created_time"`
				Items                []model.Provider `json:"items"`
			} `json:"data"`
		}
		require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
		require.True(t, response.Success, recorder.Body.String())
		if operation.method == http.MethodPost {
			providerID = response.Data.ID
			assert.Positive(t, providerID)
			assert.NotEqual(t, 999, providerID)
			assert.Greater(t, response.Data.CreatedTime, int64(1))
			assert.False(t, response.Data.SkipDegradationCheck, "new providers must be eligible by default")
		}
		if operation.method == http.MethodGet {
			require.Len(t, response.Data.Items, 1)
			assert.Equal(t, providerID, response.Data.Items[0].ID)
			assert.Empty(t, response.Data.Items[0].Description)
			assert.True(t, response.Data.Items[0].SkipDegradationCheck)
		}
		assert.NotContains(t, recorder.Body.String(), token)
	}
	var count int64
	require.NoError(t, db.Model(&model.Provider{}).Count(&count).Error)
	assert.Zero(t, count, "deleted provider is hidden from normal queries")
	active := model.Provider{Name: "active"}
	require.NoError(t, active.Save())
	channel := model.Channel{Name: "test channel", ProviderID: active.ID, Models: "test-model", Type: 1, Key: "hidden-channel-key"}
	require.NoError(t, channel.Insert())
	path := fmt.Sprintf("/api/channel/%d/degradation-exclusion", channel.Id)
	falseValue, trueValue := false, true
	for _, tc := range []struct {
		method, token, body string
		status              int
		skip                *bool
	}{
		{http.MethodGet, "", "", http.StatusUnauthorized, nil},
		{http.MethodPut, "provider-route-test-1", `{"skip_degradation_check":true}`, http.StatusForbidden, nil},
		{http.MethodGet, "provider-route-test-1", "", http.StatusOK, &falseValue},
		{http.MethodPut, "provider-route-test-2", `{"skip_degradation_check":true}`, http.StatusOK, &trueValue},
		{http.MethodGet, "provider-route-test-1", "", http.StatusOK, &trueValue},
		{http.MethodPut, "provider-route-test-2", `{"skip_degradation_check":null}`, http.StatusOK, nil},
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(tc.method, path, strings.NewReader(tc.body))
		if tc.token != "" {
			request.Header.Set("Authorization", "Bearer "+tc.token)
		}
		engine.ServeHTTP(recorder, request)
		require.Equal(t, tc.status, recorder.Code, recorder.Body.String())
		assert.NotContains(t, recorder.Body.String(), "hidden-channel-key")
		if tc.body == `{"skip_degradation_check":null}` {
			assert.Contains(t, recorder.Body.String(), `"success":false`)
		}
		if tc.skip != nil {
			var payload struct {
				Success bool `json:"success"`
				Data    struct {
					Skip bool `json:"skip_degradation_check"`
				} `json:"data"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &payload))
			require.True(t, payload.Success)
			assert.Equal(t, *tc.skip, payload.Data.Skip)
		}
	}
	var saved model.Channel
	require.NoError(t, db.First(&saved, channel.Id).Error)
	assert.True(t, saved.SkipDegradationCheck)
}

func assertChannelRoutePermission(t *testing.T, method string, path string, permission authz.Permission, handler any) {
	t.Helper()
	for _, route := range channelPermissionRoutes {
		if route.method == method && route.path == path {
			assert.Equal(t, permission, route.permission)
			assert.Equal(t, reflect.ValueOf(handler).Pointer(), reflect.ValueOf(route.handler).Pointer())
			return
		}
	}
	t.Fatalf("route %s %s not found", method, path)
}
