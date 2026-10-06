package controller

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelHasSensitiveChanges(t *testing.T) {
	baseURL := "https://api.example.com"
	headerOverride := `{"Authorization":"Bearer {api_key}"}`
	origin := &model.Channel{
		Type:           1,
		Key:            "old-key",
		BaseURL:        &baseURL,
		HeaderOverride: &headerOverride,
		Models:         "gpt-4o",
		Group:          "default",
	}

	t.Run("non-sensitive routing fields", func(t *testing.T) {
		updated := PatchChannel{Channel: *origin}
		updated.Models = "gpt-4o,gpt-4o-mini"
		updated.Group = "vip"

		assert.False(t, channelHasSensitiveChanges(&updated, origin, map[string]any{
			"models": updated.Models,
			"group":  updated.Group,
		}))
	})

	t.Run("provider metadata is editable without secret access", func(t *testing.T) {
		updated := PatchChannel{Channel: *origin}
		updated.ProviderID = 7
		ratio := 0.5
		updated.ModelConfigs = model.ChannelModelConfigs{"model-a": {UpstreamDiscount: &ratio}}
		assert.False(t, channelHasSensitiveChanges(&updated, origin, map[string]any{"provider_id": 7, "model_configs": updated.ModelConfigs}))
		updated.Key = "new-key"
		assert.True(t, channelHasSensitiveChanges(&updated, origin, map[string]any{"provider_id": 7, "key": updated.Key}))
	})

	t.Run("display and observation fields do not require secret access", func(t *testing.T) {
		for _, field := range []string{"model_configs", "model_configs_revision"} {
			assert.False(t, channelHasSensitiveChanges(&PatchChannel{}, origin, map[string]any{field: nil}), field)
		}
	})

	t.Run("key change", func(t *testing.T) {
		updated := PatchChannel{Channel: *origin}
		updated.Key = "new-key"

		assert.True(t, channelHasSensitiveChanges(&updated, origin, map[string]any{"key": updated.Key}))
	})

	t.Run("base url change", func(t *testing.T) {
		updated := PatchChannel{Channel: *origin}
		newBaseURL := "https://leak.example.com"
		updated.BaseURL = &newBaseURL

		assert.True(t, channelHasSensitiveChanges(&updated, origin, map[string]any{"base_url": newBaseURL}))
	})

	t.Run("header override change", func(t *testing.T) {
		updated := PatchChannel{Channel: *origin}
		newHeaderOverride := `{"X-Key":"{api_key}"}`
		updated.HeaderOverride = &newHeaderOverride

		assert.True(t, channelHasSensitiveChanges(&updated, origin, map[string]any{"header_override": newHeaderOverride}))
	})

	t.Run("omitted sensitive fields do not use zero values", func(t *testing.T) {
		updated := PatchChannel{}
		updated.Id = origin.Id
		updated.Priority = origin.Priority

		assert.False(t, channelHasSensitiveChanges(&updated, origin, map[string]any{"priority": 10}))
	})

	t.Run("unknown field fails closed", func(t *testing.T) {
		updated := PatchChannel{Channel: *origin}

		assert.True(t, channelHasSensitiveChanges(&updated, origin, map[string]any{"future_secret_field": "x"}))
	})

	t.Run("status is operational", func(t *testing.T) {
		updated := PatchChannel{Channel: *origin}
		updated.Status = common.ChannelStatusManuallyDisabled

		assert.False(t, channelHasSensitiveChanges(&updated, origin, map[string]any{"status": updated.Status}))
	})

	t.Run("read-only fields are ignored by sensitivity check", func(t *testing.T) {
		updated := PatchChannel{Channel: *origin}
		updated.Balance = 99
		updated.UsedQuota = 100
		updated.ResponseTime = 200

		assert.False(t, channelHasSensitiveChanges(&updated, origin, map[string]any{
			"balance":       updated.Balance,
			"used_quota":    updated.UsedQuota,
			"response_time": updated.ResponseTime,
		}))
	})
}

func TestClearChannelReadOnlyFields(t *testing.T) {
	channel := PatchChannel{Channel: model.Channel{
		CreatedTime:        11,
		TestTime:           22,
		ResponseTime:       33,
		Balance:            44.5,
		BalanceUpdatedTime: 55,
		UsedQuota:          66,
		Models:             "gpt-4o",
		Group:              "default",
	}}

	clearChannelReadOnlyFields(&channel, map[string]any{
		"created_time":         channel.CreatedTime,
		"test_time":            channel.TestTime,
		"response_time":        channel.ResponseTime,
		"balance":              channel.Balance,
		"balance_updated_time": channel.BalanceUpdatedTime,
		"used_quota":           channel.UsedQuota,
		"models":               channel.Models,
		"group":                channel.Group,
	})

	assert.Zero(t, channel.CreatedTime)
	assert.Zero(t, channel.TestTime)
	assert.Zero(t, channel.ResponseTime)
	assert.Zero(t, channel.Balance)
	assert.Zero(t, channel.BalanceUpdatedTime)
	assert.Zero(t, channel.UsedQuota)
	assert.Equal(t, "gpt-4o", channel.Models)
	assert.Equal(t, "default", channel.Group)
}

func TestUpdateChannelRejectsStatusField(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(
		http.MethodPut,
		"/api/channel/",
		bytes.NewBufferString(`{"id":1,"status":2}`),
	)
	ctx.Request.Header.Set("Content-Type", "application/json")

	UpdateChannel(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	assert.False(t, response.Success)
}

func TestChannelProviderFiltering(t *testing.T) {
	setupTaskPluginBindChannelTest(t)
	previousType := common.MainDatabaseType()
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	t.Cleanup(func() { common.SetMainDatabaseType(previousType) })
	provider := model.Provider{Name: "reseller-a"}
	require.NoError(t, provider.Save())
	other := model.Provider{Name: "reseller-b"}
	require.NoError(t, other.Save())
	tag := "shared-tag"
	channels := []model.Channel{
		{Name: "match-one", Type: 1, Key: "secret-one", ProviderID: provider.ID, Models: "model-a", Group: "default", Tag: &tag},
		{Name: "match-two", Type: 1, Key: "secret-two", ProviderID: provider.ID, Models: "model-a", Group: "default", Tag: &tag},
		{Name: "match-other", Type: 2, Key: "secret-other", ProviderID: other.ID, Models: "model-a", Group: "default", Tag: &tag},
	}
	require.NoError(t, model.BatchInsertChannels(channels))
	for _, search := range []bool{false, true} {
		for _, tagMode := range []bool{false, true} {
			t.Run(fmt.Sprintf("search=%t/tag=%t", search, tagMode), func(t *testing.T) {
				url := fmt.Sprintf("/api/channel/?provider_id=%d&page_size=1&tag_mode=%t", provider.ID, tagMode)
				recorder := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(recorder)
				ctx.Request = httptest.NewRequest(http.MethodGet, url, nil)
				if search {
					SearchChannels(ctx)
				} else {
					GetAllChannels(ctx)
				}
				var response struct {
					Success bool `json:"success"`
					Data    struct {
						Items      []model.Channel `json:"items"`
						Total      int             `json:"total"`
						TypeCounts map[string]int  `json:"type_counts"`
					} `json:"data"`
				}
				require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
				require.True(t, response.Success, recorder.Body.String())
				wantTotal, wantItems := 2, 1
				if tagMode && !search {
					wantTotal, wantItems = 1, 2 // list tag mode paginates tags
				}
				assert.Equal(t, wantTotal, response.Data.Total)
				require.Len(t, response.Data.Items, wantItems)
				assert.Equal(t, map[string]int{"1": 2}, response.Data.TypeCounts)
				for _, channel := range response.Data.Items {
					assert.Equal(t, provider.ID, channel.ProviderID)
					assert.Empty(t, channel.Key)
					assert.Equal(t, 1.0, channel.ModelConfig("model-a").Discount())
				}
			})
		}
	}
	for _, invalid := range []string{"", "0", "-1", "abc"} {
		for _, handler := range []gin.HandlerFunc{GetAllChannels, SearchChannels} {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodGet, "/api/channel/?provider_id="+invalid, nil)
			handler(ctx)
			assert.Contains(t, recorder.Body.String(), `"success":false`)
		}
	}
}

func TestChannelProviderPatchRejectsExplicitEmptyValues(t *testing.T) {
	for _, body := range []string{`{"id":1,"provider_id":0}`, `{"id":1,"provider_id":null}`, `{"id":1,"upstream_ratio":null}`} {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPut, "/api/channel/", strings.NewReader(body))
		UpdateChannel(ctx)
		assert.Contains(t, recorder.Body.String(), `"success":false`)
	}
}

func TestChannelObservationPatchContract(t *testing.T) {
	setupTaskPluginBindChannelTest(t)
	require.NoError(t, authz.SetUserPermissions(2, authz.PermissionsMap{"channel": {"read": true, "write": true, "sensitive_write": false}}))
	require.False(t, authz.Can(2, common.RoleAdminUser, authz.ChannelSensitiveWrite))
	channel := model.Channel{Name: "observed", Type: 1, Key: "preserved-secret", Models: "model-a", Group: "default", Status: common.ChannelStatusManuallyDisabled}
	require.NoError(t, channel.Insert())
	for _, scenario := range []struct {
		name, fields string
		success      bool
	}{
		{"observed false and zero", `"model_configs":{"model-a":{"public_ratio":0.25,"is_degraded":true,"is_online":false,"ttft_ms":0,"last_checked_at":1700000123}}`, true},
		{"omitted observations", `"name":"renamed"`, true},
		{"invalid ratio", `"public_ratio":0`, false},
		{"null ratio", `"public_ratio":null`, false},
		{"negative ttft", `"ttft_ms":-1`, false},
		{"fractional ttft", `"ttft_ms":0.5`, false},
		{"invalid time", `"last_checked_at":-1`, false},
		{"invalid boolean", `"is_online":"false"`, false},
		{"cannot change credentials", `"is_online":true,"key":"forbidden-secret"`, false},
		{"cannot change status", `"status":1,"is_online":true`, false},
		{"clear observations", `"model_configs":{"model-a":{"public_ratio":0.25,"is_degraded":null,"is_online":null,"ttft_ms":null,"last_checked_at":null}}`, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Set("id", 2)
			ctx.Set("role", common.RoleAdminUser)
			ctx.Request = httptest.NewRequest(http.MethodPut, "/api/channel/", strings.NewReader(fmt.Sprintf(`{"id":%d,%s}`, channel.Id, scenario.fields)))
			UpdateChannel(ctx)
			var response struct {
				Success bool          `json:"success"`
				Data    model.Channel `json:"data"`
			}
			require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
			require.Equal(t, scenario.success, response.Success, recorder.Body.String())
			loaded, err := model.GetChannelById(channel.Id, true)
			require.NoError(t, err)
			assert.Equal(t, channel.Key, loaded.Key)
			assert.Equal(t, channel.Status, loaded.Status)
			config := loaded.ModelConfig("model-a")
			assert.Equal(t, 0.25, config.ChargeRatio())
			if scenario.name == "clear observations" {
				assert.Nil(t, config.IsDegraded)
				assert.Nil(t, config.IsOnline)
				assert.Nil(t, config.TTFTMs)
				assert.Nil(t, config.LastCheckedAt)
			} else {
				require.NotNil(t, config.IsDegraded)
				require.NotNil(t, config.IsOnline)
				require.NotNil(t, config.TTFTMs)
				require.NotNil(t, config.LastCheckedAt)
				assert.True(t, *config.IsDegraded)
				assert.False(t, *config.IsOnline)
				assert.Zero(t, *config.TTFTMs)
				assert.EqualValues(t, 1700000123, *config.LastCheckedAt)
			}
			if scenario.success {
				assert.Empty(t, response.Data.Key)
				assert.Equal(t, config, response.Data.ModelConfig("model-a"))
			}
		})
	}
}

func TestCopyChannelResetsObservations(t *testing.T) {
	setupTaskPluginBindChannelTest(t)
	ratio, flag, ttft, checked := 0.25, true, int64(12), int64(1700000123)
	channel := model.Channel{Name: "copy source", Type: 1, Key: "secret-key", Models: "model-a", Group: "default", ModelConfigs: model.ChannelModelConfigs{"model-a": {PublicRatio: &ratio, IsDegraded: &flag, IsOnline: &flag, TTFTMs: &ttft, LastCheckedAt: &checked}}}
	require.NoError(t, channel.Insert())
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Set("id", 1)
	ctx.Set("role", common.RoleRootUser)
	ctx.Params = gin.Params{{Key: "id", Value: fmt.Sprint(channel.Id)}}
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/channel/copy/"+fmt.Sprint(channel.Id), nil)
	CopyChannel(ctx)
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			ID int `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success, recorder.Body.String())
	clone, err := model.GetChannelById(response.Data.ID, true)
	require.NoError(t, err)
	assert.NotEqual(t, channel.Id, clone.Id)
	assert.Equal(t, channel.ProviderID, clone.ProviderID)
	assert.Equal(t, channel.ModelConfig("model-a").ChargeRatio(), clone.ModelConfig("model-a").ChargeRatio())
	assert.Equal(t, channel.Key, clone.Key)
	assert.Nil(t, clone.ModelConfig("model-a").IsDegraded)
	assert.Nil(t, clone.ModelConfig("model-a").IsOnline)
	assert.Nil(t, clone.ModelConfig("model-a").TTFTMs)
	assert.Nil(t, clone.ModelConfig("model-a").LastCheckedAt)
}

func TestChannelStatusValidation(t *testing.T) {
	assert.True(t, isManageableChannelStatus(common.ChannelStatusEnabled))
	assert.True(t, isManageableChannelStatus(common.ChannelStatusManuallyDisabled))
	assert.False(t, isManageableChannelStatus(common.ChannelStatusAutoDisabled))
	assert.False(t, isManageableChannelStatus(0))
}

// TestChannelFieldsAreClassified guards the fail-closed sensitivity check: every
// JSON field of PatchChannel (including the embedded model.Channel) must be listed
// in channelSensitiveFields, channelNonSensitiveFields, or
// channelOperationalFields. A newly added field that is left unclassified will
// fail this test, forcing a conscious permission decision instead of silently
// defaulting either way.
func TestChannelFieldsAreClassified(t *testing.T) {
	classified := func(name string) bool {
		if _, ok := channelSensitiveFields[name]; ok {
			return true
		}
		if _, ok := channelNonSensitiveFields[name]; ok {
			return true
		}
		if _, ok := channelOperationalFields[name]; ok {
			return true
		}
		_, ok := channelReadOnlyFields[name]
		return ok
	}

	var collect func(rt reflect.Type) []string
	collect = func(rt reflect.Type) []string {
		var names []string
		for i := 0; i < rt.NumField(); i++ {
			field := rt.Field(i)
			if field.Anonymous && field.Type.Kind() == reflect.Struct {
				names = append(names, collect(field.Type)...)
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "" || name == "-" {
				continue
			}
			names = append(names, name)
		}
		return names
	}

	for _, name := range collect(reflect.TypeFor[PatchChannel]()) {
		assert.Truef(t, classified(name),
			"channel field %q is not classified; add it to channelSensitiveFields, channelNonSensitiveFields, channelOperationalFields, or channelReadOnlyFields in channel_authz.go", name)
	}
}
