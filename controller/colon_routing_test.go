package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayServiceFeeConfiguration(t *testing.T) {
	setupResponsesWSRequestTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.Option{}, &model.Task{}))
	oldFee, err := common.Marshal(operation_setting.GetRelayServiceFee())
	require.NoError(t, err)
	common.OptionMapRWMutex.Lock()
	oldOptions := common.OptionMap
	common.OptionMap = make(map[string]string)
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() {
		require.NoError(t, operation_setting.LoadRelayServiceFee(string(oldFee)))
		common.OptionMapRWMutex.Lock()
		common.OptionMap = oldOptions
		common.OptionMapRWMutex.Unlock()
		require.NoError(t, model.DB.Where(map[string]any{"key": operation_setting.RelayServiceFeeOption}).Delete(&model.Option{}).Error)
	})
	require.NoError(t, model.UpdateOption(operation_setting.RelayServiceFeeOption, `{"enabled":true,"rate":0.125}`))
	assert.Equal(t, operation_setting.RelayServiceFee{Enabled: true, Rate: 0.125}, operation_setting.GetRelayServiceFee())
	for _, raw := range []string{`{}`, `{"enabled":true,"rate":-1}`, `{"enabled":false,"rate":2}`, `{"enabled":true,"rate":null}`, `{"enabled":true,"rate":"5%"}`} {
		assert.Error(t, model.UpdateOption(operation_setting.RelayServiceFeeOption, raw))
	}
	var option model.Option
	require.NoError(t, model.DB.Where(map[string]any{"key": operation_setting.RelayServiceFeeOption}).First(&option).Error)
	assert.JSONEq(t, `{"enabled":true,"rate":0.125}`, option.Value)
	engine := gin.New()
	engine.GET("/status", GetStatus)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/status", nil))
	assert.Contains(t, response.Body.String(), `"relay_service_fee":{"enabled":true,"rate":0.125}`)
	charge := &hosttypes.RelayCharge{BaseGroupRatio: 2, ChannelPublicRatio: 0.5, ServiceFeeEnabled: true, ServiceFeeRate: 0.125}
	task := &model.Task{TaskID: "fee-snapshot-test", PrivateData: model.TaskPrivateData{BillingContext: &model.TaskBillingContext{RelayCharge: charge, GroupRatio: charge.EffectiveGroupRatio()}}}
	require.NoError(t, model.DB.Create(task).Error)
	t.Cleanup(func() { require.NoError(t, model.DB.Delete(task).Error) })
	require.NoError(t, model.UpdateOption(operation_setting.RelayServiceFeeOption, `{"enabled":false,"rate":0}`))
	var persisted model.Task
	require.NoError(t, model.DB.First(&persisted, task.ID).Error)
	require.NotNil(t, persisted.PrivateData.BillingContext.RelayCharge)
	assert.Equal(t, charge, persisted.PrivateData.BillingContext.RelayCharge)
	assert.Equal(t, 125, persisted.PrivateData.BillingContext.RelayCharge.ServiceFeeQuota(1125))
	assert.Equal(t, 1.125, persisted.PrivateData.BillingContext.GroupRatio)
}

func TestColonRoutingBilling(t *testing.T) {
	require.NoError(t, i18n.Init())
	for _, cached := range []bool{false, true} {
		for _, scenario := range []struct {
			name, suffix           string
			failCount              int
			fee                    bool
			tokenQuota             int
			wantStatus, wantCharge int
			wantAttempts           []int
		}{
			{"same_cost_before_higher", ":floor", 1, true, 100000, 200, 6300, []int{0, 1}},
			{"advance_cost_tier", ":floor", 2, true, 100000, 200, 1050, []int{0, 1, 2}},
			{"filtered_cost", ":floor:multiplier>0.2:multiplier<=0.4", 0, true, 100000, 200, 1050, []int{2}},
			{"ordinary_priority", "", 0, true, 100000, 200, 1050, []int{2}},
			{"legacy_request_price", ":floor", 1, true, 100000, 200, 6300, []int{0, 1}},
			{"legacy_token_price", ":floor", 0, true, 100000, 200, 84, []int{0}},
			{"forbidden_model", ":floor", 0, true, 100000, 403, 0, nil},
			{"failed_all", ":floor", 3, true, 100000, 500, 0, []int{0, 1, 2}},
			{"no_fee", ":floor", 0, false, 100000, 200, 2000, []int{0}},
			{"reserve_before_retry", ":floor", 1, true, 4000, 403, 0, []int{0}},
			{"no_match", ":floor:multiplier<0.1", 0, true, 100000, 503, 0, nil},
			{"bad_rule", ":floor:multiplier=NaN", 0, true, 100000, 400, 0, nil},
		} {
			t.Run(fmt.Sprintf("cache_%t/%s", cached, scenario.name), func(t *testing.T) {
				user, token := setupResponsesWSRequestTest(t)
				require.NoError(t, model.DB.AutoMigrate(&model.Channel{}, &model.Ability{}))
				require.NoError(t, model.LOG_DB.AutoMigrate(&model.Log{}))
				saved := map[string]string{}
				require.NoError(t, config.GlobalConfig.SaveToDB(func(k, v string) error { saved[k] = v; return nil }))
				oldFee, _ := common.Marshal(operation_setting.GetRelayServiceFee())
				oldRetry, oldLogs, oldBatch, oldCount, oldQuota := common.RetryTimes, common.LogConsumeEnabled, common.BatchUpdateEnabled, constant.CountToken, common.QuotaPerUnit
				common.RetryTimes, common.LogConsumeEnabled, common.BatchUpdateEnabled, constant.CountToken, common.QuotaPerUnit = 3, true, false, false, 500000
				t.Cleanup(func() {
					require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
					require.NoError(t, operation_setting.LoadRelayServiceFee(string(oldFee)))
					common.RetryTimes, common.LogConsumeEnabled, common.BatchUpdateEnabled, constant.CountToken, common.QuotaPerUnit = oldRetry, oldLogs, oldBatch, oldCount, oldQuota
				})
				require.NoError(t, operation_setting.LoadRelayServiceFee(fmt.Sprintf(`{"enabled":%t,"rate":0.05}`, scenario.fee)))
				require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
					"billing_setting.billing_mode":    `{"route-test":"tiered_expr"}`,
					"billing_setting.billing_expr":    `{"route-test":"tier(\"request\", fixed(0.01))"}`,
					"group_ratio_setting.group_ratio": `{"default":2,"forbidden":1}`,
					"quota_setting.trust_quota_usd":   "0",
				}))
				require.NoError(t, model.DB.Model(user).Updates(map[string]any{"quota": 100000, "setting": `{"billing_preference":"wallet_only"}`}).Error)
				require.NoError(t, model.DB.Model(token).Updates(map[string]any{"remain_quota": scenario.tokenQuota, "model_limits_enabled": true, "model_limits": "route-test"}).Error)
				if scenario.name == "forbidden_model" {
					require.NoError(t, model.DB.Model(token).Update("model_limits", "another-model").Error)
				}
				if scenario.name == "legacy_request_price" || scenario.name == "legacy_token_price" {
					require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"billing_setting.billing_mode": "{}"}))
					previousModelPrice, previousModelRatio := ratio_setting.ModelPrice2JSONString(), ratio_setting.ModelRatio2JSONString()
					require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{}`))
					t.Cleanup(func() {
						require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(previousModelPrice))
						require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(previousModelRatio))
					})
					if scenario.name == "legacy_request_price" {
						require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"route-test":0.01}`))
					} else {
						require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"route-test":10}`))
					}
				}
				var attempts []int
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body struct {
						Model string `json:"model"`
					}
					require.NoError(t, common.DecodeJson(r.Body, &body))
					assert.Equal(t, "mapped-upstream", body.Model)
					var index int
					_, err := fmt.Sscanf(r.Header.Get("Authorization"), "Bearer route-key-%d", &index)
					require.NoError(t, err)
					attempts = append(attempts, index)
					if len(attempts) <= scenario.failCount {
						w.WriteHeader(500)
						_, _ = w.Write([]byte(`{"error":{"message":"test failure","type":"server_error"}}`))
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"id":"test","object":"chat.completion","model":"mapped-upstream","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":10,"total_tokens":20}}`))
				}))
				defer upstream.Close()
				ids := []int{}
				for i, cost := range []float64{0.2, 0.2, 0.4, 0.01} {
					group := "default"
					if i == 3 {
						group = "forbidden"
					}
					public := []float64{0.2, 0.6, 0.1, 0.01}[i]
					weight := uint(0)
					if i != 1 {
						weight = 100
					}
					priority := int64(100 + i)
					mapping := `{"route-test":"mapped-upstream"}`
					channel := &model.Channel{Name: fmt.Sprintf("route-%d", i), Type: constant.ChannelTypeOpenAI, Status: common.ChannelStatusEnabled, Key: fmt.Sprintf("route-key-%d", i), BaseURL: &upstream.URL, Group: group, Models: "route-test", ModelMapping: &mapping, UpstreamRatio: &cost, PublicRatio: &public, Weight: &weight, Priority: &priority}
					require.NoError(t, model.DB.Create(channel).Error)
					require.NoError(t, channel.AddAbilities(nil))
					ids = append(ids, channel.Id)
				}
				t.Cleanup(func() {
					require.NoError(t, model.DB.Where("channel_id IN ?", ids).Delete(&model.Ability{}).Error)
					require.NoError(t, model.DB.Where("id IN ?", ids).Delete(&model.Channel{}).Error)
					require.NoError(t, model.LOG_DB.Where("user_id = ?", user.Id).Delete(&model.Log{}).Error)
				})
				common.MemoryCacheEnabled = cached
				model.InitChannelCache()
				engine := gin.New()
				engine.POST("/v1/chat/completions", middleware.BodyStorageCleanup(), middleware.TokenAuth(), middleware.Distribute(), func(c *gin.Context) { Relay(c, types.RelayFormatOpenAI) })
				body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, "route-test"+scenario.suffix)
				req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", "Bearer sk-"+token.Key)
				result := httptest.NewRecorder()
				engine.ServeHTTP(result, req)
				assert.Equal(t, scenario.wantStatus, result.Code, result.Body.String())
				assert.Equal(t, scenario.wantAttempts, attempts)
				if scenario.wantCharge == 0 && len(scenario.wantAttempts) > 0 {
					require.Eventually(t, func() bool {
						var row model.User
						var key model.Token
						return model.DB.First(&row, user.Id).Error == nil && model.DB.First(&key, token.Id).Error == nil && row.Quota == 100000 && key.RemainQuota == scenario.tokenQuota
					}, 3*time.Second, 10*time.Millisecond, "failed attempts refund wallet and token asynchronously")
				}
				var current model.User
				require.NoError(t, model.DB.First(&current, user.Id).Error)
				assert.Equal(t, 100000-scenario.wantCharge, current.Quota)
				var used model.Token
				require.NoError(t, model.DB.First(&used, token.Id).Error)
				assert.Equal(t, scenario.tokenQuota-scenario.wantCharge, used.RemainQuota)
				var logs []model.Log
				require.NoError(t, model.LOG_DB.Where("user_id = ? AND type = ?", user.Id, model.LogTypeConsume).Find(&logs).Error)
				if scenario.wantCharge > 0 {
					require.Len(t, logs, 1)
					assert.Equal(t, scenario.wantCharge, logs[0].Quota)
					var other map[string]any
					require.NoError(t, common.UnmarshalJsonStr(logs[0].Other, &other))
					assert.Equal(t, scenario.fee, other["service_fee_enabled"])
					assert.EqualValues(t, 2, other["base_group_ratio"])
					assert.NotContains(t, logs[0].Other, "upstream_ratio")
					if scenario.fee {
						assert.EqualValues(t, scenario.wantCharge/21, other["service_fee_quota"])
					} else {
						assert.EqualValues(t, 0, other["service_fee_quota"])
					}
				} else {
					assert.Empty(t, logs)
				}
			})
		}
	}
}
