package relay

import (
	"context"
	"math"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	filterdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func channelModelPricingSettings(t *testing.T) {
	t.Helper()
	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(k, v string) error { saved[k] = v; return nil }))
	t.Cleanup(func() { require.NoError(t, config.GlobalConfig.LoadFromDB(saved)) })
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode":         `{"quoted":"tiered_expr"}`,
		"billing_setting.billing_expr":         `{"quoted":"len <= 200000 ? tier(\"base\", p * 25 + c * 100 + cr * 5 + cc * 30 + cc1h * 50) : tier(\"long\", p * 50 + c * 200 + cr * 10 + cc * 60 + cc1h * 100)"}`,
		"group_ratio_setting.group_ratio":      `{"default":2,"free":0}`,
		"quota_setting.pre_consume_multiplier": "1",
	}))
}

func TestChannelModelQuoteAndValidation(t *testing.T) {
	channelModelPricingSettings(t)
	for _, tc := range []struct {
		name, expr string
		want       *model.ModelRatioRange
	}{
		{"flat independent cache", `tier("base", p * 5 + c * 15 + cr * 1 + cc * 6 + cc1h * 10)`, &model.ModelRatioRange{Min: .15, Max: .2}},
		{"base tier only", `len < 200000 ? tier("base", p * 5 + c * 20 + cr * 1 + cc * 6 + cc1h * 10) : tier("long", p * 50 + c * 100)`, &model.ModelRatioRange{Min: .2, Max: .2}},
		{"zero prices", `tier("free", p * 0 + c * 0 + cr * 0 + cc * 0 + cc1h * 0)`, &model.ModelRatioRange{Min: 0, Max: 0}},
		{"request price excluded", `tier("request",fixed(1))`, nil},
		{"conditional request excluded", `param("speed") == "fast" ? tier("fast",p * 5) : tier("base",p * 2)`, nil},
		{"nonlinear excluded", `tier("base",p * p)`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := model.ChannelModelConfig{BillingExpression: tc.expr}
			require.NoError(t, cfg.Validate())
			assert.Equal(t, tc.want, cfg.ModelPublicRatio("quoted"))
		})
	}
	for _, cfg := range []model.ChannelModelConfig{
		{PublicRatio: common.GetPointer(-1.0)}, {UpstreamDiscount: common.GetPointer(1.1)}, {UpstreamDiscount: common.GetPointer(math.NaN())},
		{BillingExpression: `tier("x",p)`, PublicRatio: common.GetPointer(1.0)}, {BillingExpression: `tier("x",p * -1)`}, {TTFTMs: common.GetPointer(int64(-1))},
	} {
		require.Error(t, cfg.Validate())
	}
	cfg := model.ChannelModelConfig{BillingExpression: `tier("base",p * 5 + c * 20)`}
	assert.Nil(t, cfg.ModelPublicRatio("no-system-price"))
	// No manual override can replace the automatically derived quotation.
	assert.Equal(t, 1.0, cfg.ChargeRatio())
	quote := model.ChannelModelConfig{BillingExpression: `tier("x",p*5+c*20+cr*1+cc*6+cc1h*10)`}
	assert.Equal(t, &model.ModelRatioRange{Min: .2, Max: .2}, quote.ModelPublicRatio("quoted"))
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"billing_setting.billing_expr": `{"quoted":"tier(\"base\",p*50+c*200+cr*10+cc*60+cc1h*100)"}`}))
	assert.Equal(t, &model.ModelRatioRange{Min: .1, Max: .1}, quote.ModelPublicRatio("quoted"))
}

func TestChannelModelBillingAndRetry(t *testing.T) {
	channelModelPricingSettings(t)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{OriginModelName: "quoted", UserGroup: "default", UsingGroup: "default", BillingRequestInput: &billingexpr.RequestInput{}, RelayCharge: &hosttypes.RelayCharge{ServiceFeeEnabled: true, ServiceFeeRate: .05}}
	info.SetEstimatePromptTokens(1000)
	cfg := model.ChannelModelConfig{UpstreamDiscount: common.GetPointer(.8), BillingExpression: `tier("quote",p * 5 + c * 20 + cr * 1 + cc * 6 + cc1h * 10)`}
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 1)
	ctx.Set("channel_model_config", cfg)
	ctx.Set("channel_public_ratio", cfg.ChargeRatio())
	price, err := helper.ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Equal(t, 5250, price.QuotaToPreConsume)
	_, quota, result := service.TryTieredSettle(info, billingexpr.TokenParams{P: 1000, C: 100})
	require.NotNil(t, result)
	assert.Equal(t, 7350, quota)
	cost, clamp := info.RelayCharge.CostQuota()
	assert.Nil(t, clamp)
	assert.Equal(t, 2800, cost)
	// Same expression in a free group still incurs procurement cost.
	info.UsingGroup = "free"
	_, err = helper.ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	_, quota, _ = service.TryTieredSettle(info, billingexpr.TokenParams{P: 1000, C: 100})
	assert.Zero(t, quota)
	cost, _ = info.RelayCharge.CostQuota()
	assert.Equal(t, 2800, cost)
	// Moving to a manual-ratio model removes expression pricing, applies public
	// ratio once, then fee and group. Use free group to avoid external funding.
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 2)
	cfg = model.ChannelModelConfig{PublicRatio: common.GetPointer(.2), UpstreamDiscount: common.GetPointer(.5)}
	ctx.Set("channel_model_config", cfg)
	ctx.Set("channel_public_ratio", cfg.ChargeRatio())
	require.Nil(t, PrepareSelectedChannelBilling(ctx, info))
	assert.False(t, info.ChannelTokenPricing)
	_, quota, _ = service.TryTieredSettle(info, billingexpr.TokenParams{P: 1000, C: 100})
	assert.Zero(t, quota)
	cost, _ = info.RelayCharge.CostQuota()
	assert.Equal(t, 1750, cost)
	assert.Equal(t, .2, info.RelayCharge.ChannelPublicRatio)
	// Invalid reference makes an expression ineligible even for direct billing.
	info.OriginModelName = "missing-price"
	ctx.Set("channel_model_config", model.ChannelModelConfig{BillingExpression: `tier("base",p*2)`})
	_, err = helper.ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.Error(t, err)
}

func TestChannelModelServiceTierQuotesAndBilling(t *testing.T) {
	channelModelPricingSettings(t)
	base := `tier("base",p*5+c*20+cr*1+cc*6+cc1h*10)`
	expression := `(` + base + `) * ((param("service_tier") == "fast" || param("service_tier") == "priority") ? 2 : 1) * (param("service_tier") == "flex" ? .5 : 1)`
	cfg := model.ChannelModelConfig{BillingExpression: expression, UpstreamDiscount: common.GetPointer(.8)}
	assert.Equal(t, &model.ModelRatioRange{Min: .2, Max: .2}, cfg.ModelPublicRatio("quoted"))
	for _, tc := range []struct {
		tier   string
		factor float64
	}{{"", 1}, {"default", 1}, {"unknown", 1}, {"fast", 2}, {"priority", 2}, {"flex", .5}} {
		t.Run(tc.tier, func(t *testing.T) {
			quote := cfg.ModelServiceTierRatio("quoted", tc.tier)
			require.NotNil(t, quote)
			assert.InDelta(t, .2*tc.factor, quote.Min, 1e-12)
			assert.InDelta(t, .2*tc.factor, quote.Max, 1e-12)
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			body := `{"model":"quoted","service_tier":"` + tc.tier + `"}`
			ctx.Request = httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
			ctx.Request.Header.Set("Content-Type", "application/json")
			defer common.CleanupBodyStorage(ctx)
			require.NoError(t, service.FreezeRoutingServiceTier(ctx))
			assert.Equal(t, tc.tier, filterdto.RoutingServiceTier(service.GetChannelConstraints(ctx).Filters))
			var reread map[string]any
			require.NoError(t, common.UnmarshalBodyReusable(ctx, &reread))
			assert.Equal(t, tc.tier, reread["service_tier"])
			ctx.Set("channel_model_config", cfg)
			ctx.Set("channel_public_ratio", cfg.ChargeRatio())
			info := &relaycommon.RelayInfo{OriginModelName: "quoted", UserGroup: "default", UsingGroup: "default", BillingRequestInput: &billingexpr.RequestInput{Body: []byte(body)}, RelayCharge: &hosttypes.RelayCharge{ServiceFeeEnabled: true, ServiceFeeRate: .05}}
			_, err := helper.ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
			require.NoError(t, err)
			_, quota, result := service.TryTieredSettle(info, billingexpr.TokenParams{P: 1000, C: 100})
			require.NotNil(t, result)
			assert.Equal(t, common.QuotaRound(7350*tc.factor), quota)
			cost, clamp := info.RelayCharge.CostQuota()
			assert.Nil(t, clamp)
			assert.Equal(t, common.QuotaRound(2800*tc.factor), cost)
			ctx.Request.Body = nil // retries must not re-read a modified body
			require.NoError(t, service.FreezeRoutingServiceTier(ctx))
			assert.Equal(t, tc.tier, filterdto.RoutingServiceTier(service.GetChannelConstraints(ctx).Filters))
		})
	}
	// Regression for channel 8: the built-in reference explicitly inherits the
	// 1h cache-write price from cc in both context tiers.
	astra := model.ChannelModelConfig{BillingExpression: `(len <= 272000 ? tier("standard",p*10+c*50+cr*1+cc*12.5+cc1h*12.5) : tier("long",p*20+c*75+cr*2+cc*25+cc1h*25)) * (param("service_tier") == "fast" ? 2 : 1) * (param("service_tier") == "flex" ? .5 : 1) * .5`}
	assert.Equal(t, &model.ModelRatioRange{Min: .5, Max: .5}, astra.ModelPublicRatio("gpt-6-astra"))
	assert.Equal(t, &model.ModelRatioRange{Min: 1, Max: 1}, astra.ModelServiceTierRatio("gpt-6-astra", "fast"))
	assert.Equal(t, &model.ModelRatioRange{Min: .25, Max: .25}, astra.ModelServiceTierRatio("gpt-6-astra", "flex"))
	builtin, found := billing_setting.GetBillingExpr("gpt-6-astra")
	require.True(t, found)
	for _, tc := range []struct{ length, price float64 }{{272000, 12.5}, {272001, 25}} {
		usage := billingexpr.TokenParams{CC1h: 100, Len: tc.length}
		baseCost, _, err := billingexpr.RunExprWithRequest(builtin, usage, billingexpr.RequestInput{})
		require.NoError(t, err)
		assert.Equal(t, 100*tc.price, baseCost)
		channelCost, _, err := billingexpr.RunExprWithRequest(astra.BillingExpression, usage, billingexpr.RequestInput{})
		require.NoError(t, err)
		assert.Equal(t, baseCost*.5, channelCost)
	}
	// Reference pricing has its own service rule; divide matching request prices.
	reference := `tier("base",p*25+c*100+cr*5+cc*30+cc1h*50) * (param("service_tier") == "fast" ? 4 : 1)`
	encoded, err := common.Marshal(map[string]string{"quoted": reference})
	require.NoError(t, err)
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"billing_setting.billing_expr": string(encoded)}))
	assert.Equal(t, &model.ModelRatioRange{Min: .1, Max: .1}, cfg.ModelServiceTierRatio("quoted", "fast"))
	assert.Equal(t, &model.ModelRatioRange{Min: .2, Max: .2}, cfg.ModelPublicRatio("quoted"))
	for _, expr := range []string{`tier("x",p) * (param("speed") == "fast" ? 2 : 1)`, `tier("x",p) * (hour("UTC") < 10 ? 2 : 1)`, `tier("x",p) * ((param("service_tier") == "fast" || header("x") == "y") ? 2 : 1)`, `tier("x",p*p)`} {
		_, ok := billingexpr.TokenPricesForServiceTier(expr, "fast")
		assert.False(t, ok, expr)
	}
	// Cache entries are isolated by tier and preserve whole-expression variables.
	branch := `param("service_tier") == "fast" ? tier("fast",p*2+cr*.5) : tier("base",p)`
	prices, ok := billingexpr.BaseTokenPrices(branch)
	require.True(t, ok)
	assert.Zero(t, prices["cr"])
	prices["p"] = 999
	fast, ok := billingexpr.TokenPricesForServiceTier(branch, "fast")
	require.True(t, ok)
	assert.Equal(t, .5, fast["cr"])
	prices, ok = billingexpr.BaseTokenPrices(branch)
	require.True(t, ok)
	assert.Equal(t, 1.0, prices["p"])
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"service_tier":42}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	defer common.CleanupBodyStorage(ctx)
	require.Error(t, service.FreezeRoutingServiceTier(ctx))
}

func TestChannelModelDatabaseAndRouting(t *testing.T) {
	channelModelPricingSettings(t)
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(filepath.Join(t.TempDir(), "models.db"))
			case "mysql":
				if os.Getenv("TEST_MYSQL_DSN") == "" {
					t.Skip("existing isolated MySQL test database not configured; no service will be started")
				}
				driver = mysql.Open(os.Getenv("TEST_MYSQL_DSN"))
			case "postgres":
				if os.Getenv("TEST_POSTGRES_DSN") == "" {
					t.Skip("existing isolated PostgreSQL test database not configured; no service will be started")
				}
				driver = postgres.Open(os.Getenv("TEST_POSTGRES_DSN"))
			}
			db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "model_config_test_"}})
			require.NoError(t, err)
			pool, err := db.DB()
			require.NoError(t, err)
			oldDB, oldMemory, oldType := model.DB, common.MemoryCacheEnabled, common.MainDatabaseType()
			model.DB, common.MemoryCacheEnabled = db, false
			common.SetMainDatabaseType(common.DatabaseType(dialect))
			t.Cleanup(func() {
				common.MemoryCacheEnabled = false
				require.NoError(t, db.Migrator().DropTable(&model.ChannelDegradationSample{}, &model.Ability{}, &model.Channel{}, &model.Provider{}))
				model.DB = oldDB
				common.MemoryCacheEnabled = oldMemory
				common.SetMainDatabaseType(oldType)
				require.NoError(t, pool.Close())
			})
			require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Provider{}, &model.Ability{}, &model.ChannelDegradationSample{}))
			p := model.Provider{Name: "provider"}
			require.NoError(t, p.Save())
			channels := []model.Channel{
				{Name: ".14", Models: "quoted,other", Key: "private", Group: "default", ProviderID: p.ID, ModelConfigs: model.ChannelModelConfigs{"quoted": {PublicRatio: common.GetPointer(.14), UpstreamDiscount: common.GetPointer(.9)}}},
				{Name: ".13-.15", Models: "quoted", Key: "private", Group: "default", ProviderID: p.ID, ModelConfigs: model.ChannelModelConfigs{"quoted": {BillingExpression: `tier("x",p*3.25+c*15+cr*.75+cc*4.5+cc1h*7.5)`, UpstreamDiscount: common.GetPointer(.1)}}},
				{Name: ".15", Models: "quoted", Key: "private", Group: "default", ProviderID: p.ID, ModelConfigs: model.ChannelModelConfigs{"quoted": {PublicRatio: common.GetPointer(.15)}}},
				{Name: "excluded", Models: "quoted", Key: "private", Group: "default", ProviderID: p.ID, ModelConfigs: model.ChannelModelConfigs{"quoted": {BillingExpression: `tier("request",fixed(1))`}}},
			}
			importedSource := &model.ChannelModelPricingSource{Provider: "newapi", BaseURL: "https://prices.example", Group: "vip", Expression: `tier("base",p*3.5+c*14)`, SourceHash: "snapshot", FetchedAt: 100, AppliedRatio: common.GetPointer(.14)}
			imported := channels[0].ModelConfigs["quoted"]
			imported.PricingSource = importedSource
			channels[0].ModelConfigs["quoted"] = imported
			channels[0].Type = constant.ChannelTypeNewAPI
			channels[0].SetOtherSettings(dto.ChannelOtherSettings{UpstreamPricing: &dto.UpstreamPricingSource{Provider: "newapi", Group: "vip"}})
			for i := range channels {
				require.NoError(t, channels[i].Insert())
			}
			// Repeated startup migration must retain model overrides, zero and null.
			for range 2 {
				require.NoError(t, db.AutoMigrate(&model.Channel{}))
			}
			loaded, err := model.GetChannelById(channels[0].Id, true)
			require.NoError(t, err)
			assert.Equal(t, importedSource, loaded.ModelConfig("quoted").PricingSource)
			assert.Equal(t, "vip", loaded.GetOtherSettings().UpstreamPricing.Group)
			assert.Equal(t, .14, loaded.ModelConfig("quoted").ChargeRatio())
			assert.Equal(t, 1.0, loaded.ModelConfig("other").ChargeRatio())
			// Whole-interval multiplier filtering and worst-rate floor; discounts do not
			// change the user's explicitly chosen floor ranking.
			for _, cached := range []bool{false, true} {
				common.MemoryCacheEnabled = cached
				if cached {
					model.InitChannelCache()
				}
				got, err := model.GetFloorChannel("default", "quoted", []filterdto.ChannelFilter{{Kind: filterdto.FilterMultiplier, Multiplier: filterdto.MultiplierFilter{Operator: "<=", Value: .14}}, {Kind: filterdto.FilterAttemptedChannels, ExcludedChannelIDs: []int{channels[0].Id}}})
				require.NoError(t, err)
				assert.Nil(t, got, "range .13-.15 must fail <=.14")
				got, err = model.GetFloorChannel("default", "quoted", []filterdto.ChannelFilter{{Kind: filterdto.FilterMultiplier, Multiplier: filterdto.MultiplierFilter{Operator: "=", Value: .15}}})
				require.NoError(t, err)
				require.NotNil(t, got)
				assert.Equal(t, channels[2].Id, got.Id)
				var excluded []int
				for _, want := range channels[:3] {
					got, err := model.GetFloorChannel("default", "quoted", []filterdto.ChannelFilter{{Kind: filterdto.FilterAttemptedChannels, ExcludedChannelIDs: excluded}})
					require.NoError(t, err)
					require.NotNil(t, got)
					assert.Equal(t, want.Id, got.Id)
					excluded = append(excluded, got.Id)
				}
				got, err = model.GetFloorChannel("default", "quoted", []filterdto.ChannelFilter{{Kind: filterdto.FilterAttemptedChannels, ExcludedChannelIDs: excluded}})
				require.NoError(t, err)
				assert.Nil(t, got)
			}
			// Service-tier prices affect cached and database selection identically.
			dynamic := channels[1].ModelConfigs["quoted"]
			dynamic.BillingExpression = `tier("x",p*3.25+c*15+cr*.75+cc*4.5+cc1h*7.5) * (param("service_tier") == "fast" ? 5 : 1) * (param("service_tier") == "flex" ? .5 : 1)`
			channels[1].ModelConfigs["quoted"] = dynamic
			common.MemoryCacheEnabled = false
			require.NoError(t, channels[1].Update())
			for _, cached := range []bool{false, true} {
				common.MemoryCacheEnabled = cached
				if cached {
					model.InitChannelCache()
				}
				for _, tc := range []struct {
					tier string
					want int
				}{{"", channels[0].Id}, {"fast", channels[0].Id}, {"flex", channels[1].Id}} {
					ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
					body := `{"model":"quoted","service_tier":"` + tc.tier + `"}`
					ctx.Request = httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
					ctx.Request.Header.Set("Content-Type", "application/json")
					require.NoError(t, service.FreezeRoutingServiceTier(ctx))
					filters := service.GetChannelConstraints(ctx).Filters
					filters = append(filters, filterdto.ChannelFilter{Kind: filterdto.FilterMultiplier, Multiplier: filterdto.MultiplierFilter{Operator: "<=", Value: .2}})
					service.GetChannelConstraints(ctx).Filters = filters
					service.GetChannelConstraints(ctx).Routing = &filterdto.ModelRouting{Priority: "floor"}
					got, _, selectErr := service.SelectChannelForRequest(ctx, "quoted", &service.RetryParam{Ctx: ctx, TokenGroup: "default", ModelName: "quoted", Retry: common.GetPointer(0)})
					require.Nil(t, selectErr)
					require.NotNil(t, got)
					assert.Equal(t, tc.want, got.Id, "cached=%v tier=%s", cached, tc.tier)
					ok, _ := model.ChannelSatisfiesFilters(&channels[1], "quoted", filters)
					assert.Equal(t, tc.tier != "fast", ok)
					service.GetChannelConstraints(ctx).AddPin(filterdto.ChannelPin{ChannelId: channels[1].Id, Source: filterdto.PinSourceToken})
					_, _, selectErr = service.SelectChannelForRequest(ctx, "quoted", &service.RetryParam{Ctx: ctx, TokenGroup: "default", ModelName: "quoted"})
					if tc.tier == "fast" {
						require.NotNil(t, selectErr)
						assert.Equal(t, filterdto.FilterMultiplier, selectErr.FilterKind)
					} else {
						require.Nil(t, selectErr)
					}
					common.CleanupBodyStorage(ctx)
				}
			}
			common.MemoryCacheEnabled = false
			// Observations update one model, with another model retaining unknown state.
			sample := &model.ChannelDegradationSample{ID: "test-sample", Model: "quoted", State: "normal", StartedAt: 100000, CompletedAt: 100100, DurationMs: 100, TTFTMs: common.GetPointer(int64(0))}
			require.NoError(t, model.RecordChannelDegradation(context.Background(), loaded, sample, ""))
			assert.True(t, sample.SnapshotApplied)
			loaded, err = model.GetChannelById(loaded.Id, true)
			require.NoError(t, err)
			assert.Equal(t, common.GetPointer(true), loaded.ModelConfig("quoted").IsOnline)
			assert.Nil(t, loaded.ModelConfig("other").IsOnline)
			rows, total, err := model.ListPublicChannels(model.PublicCatalogQuery{Model: "quoted"})
			require.NoError(t, err)
			assert.EqualValues(t, 1, total)
			require.Len(t, rows, 1)
			assert.Equal(t, "quoted", rows[0].Model)
			assert.Equal(t, int64(0), *rows[0].TTFTMs)
			encoded, err := common.Marshal(rows)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), "upstream_discount")
			assert.NotContains(t, string(encoded), "private")
			assert.NotContains(t, string(encoded), "billing_expression")
			assert.NotContains(t, string(encoded), "pricing_source")
			assert.NotContains(t, string(encoded), "prices.example")
			// A settings update cannot delete model billing; explicit empty map clears it.
			loaded.SetOtherSettings(dto.ChannelOtherSettings{AllowSpeed: true})
			require.NoError(t, loaded.Update())
			assert.Equal(t, .14, loaded.ModelConfig("quoted").ChargeRatio())
			loaded.ModelConfigs = model.ChannelModelConfigs{}
			require.NoError(t, loaded.Update())
			assert.Empty(t, loaded.ModelConfigs)
		})
	}
}
