package relay

import (
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
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

func TestChannelTokenPricingValidation(t *testing.T) {
	for _, tc := range []struct {
		name, settings string
		valid          bool
	}{
		{"legacy", `{}`, true},
		{"independent expensive cache and free output", `{"model_token_prices":{"public-model":{"p":2,"c":0,"cr":9,"cc":3,"cc1h":7}}}`, true},
		{"missing output", `{"model_token_prices":{"m":{"p":2}}}`, false},
		{"null category", `{"model_token_prices":{"m":{"p":2,"c":4,"cr":null}}}`, false},
		{"negative", `{"model_token_prices":{"m":{"p":-1,"c":4}}}`, false},
		{"excessive", `{"model_token_prices":{"m":{"p":1000000001,"c":4}}}`, false},
		{"unknown category", `{"model_token_prices":{"m":{"p":2,"c":4,"unknown":1}}}`, false},
		{"empty model", `{"model_token_prices":{"":{"p":2,"c":4}}}`, false},
		{"null model", `{"model_token_prices":{"m":null}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			channel := &model.Channel{OtherSettings: tc.settings}
			err := channel.ValidateSettings()
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestChannelTokenPricingDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(filepath.Join(t.TempDir(), "channel-prices.db"))
			case "mysql":
				dsn := os.Getenv("TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("TEST_MYSQL_DSN required")
				}
				driver = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("TEST_POSTGRES_DSN required")
				}
				driver = postgres.Open(dsn)
			}
			db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "channel_token_pricing_test_"}})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			previousDB := model.DB
			model.DB = db
			t.Cleanup(func() { model.DB = previousDB })
			table := db.Session(&gorm.Session{})
			require.NoError(t, db.AutoMigrate(&model.Ability{}))
			t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable(&model.Ability{})) })
			t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable("channel_token_pricing_test_channels")) })
			var version string
			query := "SELECT version()"
			if dialect == "sqlite" {
				query = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(query).Scan(&version).Error)
			t.Logf("%s: %s", dialect, version)
			// No new schema: pre-feature settings and pricing use the same column.
			require.NoError(t, table.AutoMigrate(&model.Channel{}))
			legacy := model.Channel{Name: "legacy", Models: "public-model", Key: "private", OtherSettings: `{"allow_speed":true}`}
			require.NoError(t, table.Create(&legacy).Error)
			for range 2 {
				require.NoError(t, table.AutoMigrate(&model.Channel{}))
			}
			var loaded model.Channel
			require.NoError(t, table.First(&loaded, legacy.Id).Error)
			assert.Equal(t, legacy.OtherSettings, loaded.OtherSettings)
			assert.Equal(t, legacy.Key, loaded.Key)
			loaded.OtherSettings = `{"allow_speed":true,"model_token_prices":{"public-model":{"p":2,"c":0,"cr":9}}}`
			require.NoError(t, loaded.ValidateSettings())
			require.NoError(t, table.Save(&loaded).Error)
			for range 2 {
				require.NoError(t, table.AutoMigrate(&model.Channel{}))
				require.NoError(t, table.First(&loaded, legacy.Id).Error)
				prices, err := loaded.GetModelTokenPrices()
				require.NoError(t, err)
				expr, found, err := prices.Expression("public-model")
				require.NoError(t, err)
				assert.True(t, found)
				assert.Equal(t, `tier("channel", p * 2 + c * 0 + cr * 9 + cc * 2 + cc1h * 2)`, expr)
				// Discovery/adapter updates must not delete host pricing.
				settings := loaded.GetOtherSettings()
				settings.AllowSpeed = false
				loaded.SetOtherSettings(settings)
				require.NoError(t, table.Save(&loaded).Error)
			}
			// Separate channel/provider IDs never share pricing, even for one model.
			other := model.Channel{Name: "other", ProviderID: 2, Models: legacy.Models, OtherSettings: `{"model_token_prices":{"public-model":{"p":5,"c":8},"private-model":{"p":5,"c":8},"disabled-model":{"p":5,"c":8}}}`}
			require.NoError(t, table.Create(&other).Error)
			prices, err := other.GetModelTokenPrices()
			require.NoError(t, err)
			expr, _, err := prices.Expression("public-model")
			require.NoError(t, err)
			assert.Equal(t, `tier("channel", p * 5 + c * 8 + cc * 5 + cc1h * 5)`, expr)
			require.NoError(t, db.Create(&[]model.Ability{
				{Group: "default", Model: "public-model", ChannelId: loaded.Id, Enabled: true},
				{Group: "private", Model: "private-model", ChannelId: other.Id, Enabled: true},
				{Group: "default", Model: "disabled-model", ChannelId: other.Id, Enabled: false},
			}).Error)
			catalog, err := model.GetChannelPricedModels([]string{"default"})
			require.NoError(t, err)
			assert.Equal(t, map[string]bool{"public-model": true}, catalog)
			loaded.OtherSettings = `{"model_token_prices":{}}`
			require.NoError(t, table.Save(&loaded).Error)
			prices, err = loaded.GetModelTokenPrices()
			require.NoError(t, err)
			assert.Empty(t, prices)
		})
	}
}

// Only the external funding boundary is replaced; pricing and reservation paths
// are real. Reserve records a target, retains higher previous reservations, and
// can reject an increase before upstream submission.
type channelPricingFunds struct {
	reserved int
	deny     bool
}

func (f *channelPricingFunds) Settle(int) error         { return nil }
func (f *channelPricingFunds) Refund(*gin.Context)      {}
func (f *channelPricingFunds) NeedsRefund() bool        { return false }
func (f *channelPricingFunds) GetPreConsumedQuota() int { return f.reserved }
func (f *channelPricingFunds) Reserve(target int) error {
	if f.deny && target > f.reserved {
		return errors.New("insufficient funds")
	}
	f.reserved = max(f.reserved, target)
	return nil
}

func TestChannelTokenPricingRetryAndSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(k, v string) error { saved[k] = v; return nil }))
	t.Cleanup(func() { require.NoError(t, config.GlobalConfig.LoadFromDB(saved)) })
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode":         `{"public-model":"tiered_expr"}`,
		"billing_setting.billing_expr":         `{"public-model":"tier(\"system\", p * 1 + c * 2)"}`,
		"group_ratio_setting.group_ratio":      `{"default":2}`,
		"quota_setting.pre_consume_multiplier": "1",
	}))
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 1)
	ctx.Set("channel_public_ratio", 1.5)
	ctx.Set(model.ChannelPricingSettingsContextKey, `{"model_token_prices":{"public-model":{"p":2,"c":4,"cr":9,"cc":3,"cc1h":7},"upstream-id":{"p":99,"c":99}}}`)
	info := &relaycommon.RelayInfo{
		OriginModelName: "public-model", UserGroup: "default", UsingGroup: "default",
		BillingRequestInput: &billingexpr.RequestInput{},
		RelayCharge:         &hosttypes.RelayCharge{ServiceFeeEnabled: true, ServiceFeeRate: 0.1},
	}
	info.ChannelMeta = &relaycommon.ChannelMeta{UpstreamModelName: "upstream-id"}
	info.SetEstimatePromptTokens(1000)
	price, err := helper.ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})
	require.NoError(t, err)
	assert.Equal(t, 3300, price.QuotaToPreConsume) // 1000 * $2/1M * 500K * 2 * 1.5 * 1.1
	require.NotNil(t, info.TieredBillingSnapshot)
	assert.Contains(t, info.TieredBillingSnapshot.ExprString, "cr * 9")
	funds := &channelPricingFunds{reserved: 3300}
	info.Billing, info.FinalPreConsumedQuota = funds, 3300
	// Snapshot ignores changes to the same in-flight channel.
	ctx.Set(model.ChannelPricingSettingsContextKey, `{"model_token_prices":{"public-model":{"p":100,"c":100}}}`)
	require.Nil(t, PrepareSelectedChannelBilling(ctx, info))
	assert.Equal(t, 3300, funds.reserved)
	usage := &dto.Usage{PromptTokens: 1000, CompletionTokens: 100, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 200}}
	params := service.BuildTieredTokenParams(usage, false, billingexpr.UsedVarsByHash(info.TieredBillingSnapshot.ExprString, info.TieredBillingSnapshot.ExprHash))
	ok, quota, result := service.TryTieredSettle(info, params)
	assert.True(t, ok)
	require.NotNil(t, result)
	assert.Equal(t, 6270, quota) // (800*2 + 100*4 + 200*9) * .5 * 3.3
	// A more expensive second channel must increase the reservation.
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 2)
	ctx.Set(model.ChannelPricingSettingsContextKey, `{"model_token_prices":{"public-model":{"p":5,"c":8}}}`)
	require.Nil(t, PrepareSelectedChannelBilling(ctx, info))
	assert.Equal(t, 8250, funds.reserved)
	assert.Contains(t, info.TieredBillingSnapshot.ExprString, "p * 5")
	// An unconfigured next channel restores system pricing, never the previous
	// channel's custom price; cheaper estimates are refunded only at settlement.
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 3)
	ctx.Set(model.ChannelPricingSettingsContextKey, `{}`)
	require.Nil(t, PrepareSelectedChannelBilling(ctx, info))
	assert.False(t, info.ChannelTokenPricing)
	assert.Contains(t, info.TieredBillingSnapshot.ExprString, `tier("system"`)
	assert.Equal(t, 8250, funds.reserved)
	// Denied retry reservation terminates before the caller can send upstream.
	funds.deny = true
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 4)
	ctx.Set(model.ChannelPricingSettingsContextKey, `{"model_token_prices":{"public-model":{"p":20,"c":30}}}`)
	require.NotNil(t, PrepareSelectedChannelBilling(ctx, info))
	assert.Equal(t, 8250, funds.reserved)

	// Removing an override on a legacy-ratio channel must also clear the old
	// expression snapshot; otherwise settlement could keep the previous price.
	savedRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() { require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedRatios)) })
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(`{"public-model":3}`))
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"billing_setting.billing_mode": `{}`}))
	funds.deny = false
	common.SetContextKey(ctx, constant.ContextKeyChannelId, 5)
	ctx.Set(model.ChannelPricingSettingsContextKey, `{}`)
	require.Nil(t, PrepareSelectedChannelBilling(ctx, info))
	assert.Nil(t, info.TieredBillingSnapshot)
	assert.False(t, info.ChannelTokenPricing)
	assert.Equal(t, 3.0, info.PriceData.ModelRatio)
	assert.Equal(t, 9900, funds.reserved)
}

func TestChannelTokenPricingRejectsOversizedReservation(t *testing.T) {
	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(k, v string) error { saved[k] = v; return nil }))
	t.Cleanup(func() { require.NoError(t, config.GlobalConfig.LoadFromDB(saved)) })
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"group_ratio_setting.group_ratio":      `{"default":1}`,
		"quota_setting.pre_consume_multiplier": "1",
	}))
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/v1/chat/completions", nil)
	ctx.Set(model.ChannelPricingSettingsContextKey, `{"model_token_prices":{"m":{"p":1000000000,"c":0}}}`)
	info := &relaycommon.RelayInfo{OriginModelName: "m", UserGroup: "default", UsingGroup: "default", BillingRequestInput: &billingexpr.RequestInput{}}
	_, err := helper.ModelPriceHelper(ctx, info, 10000, &types.TokenCountMeta{})
	require.Error(t, err, "oversized prices must fail reservation, not wrap into free or negative quota")
	assert.Nil(t, info.TieredBillingSnapshot)
}

func TestChannelTokenPricingAllCategoriesAndClaudeNormalization(t *testing.T) {
	channel := &model.Channel{OtherSettings: `{"model_token_prices":{"m":{"p":0,"c":0,"cr":9,"cc":3,"cc1h":7,"img":11,"img_cr":13,"img_o":17,"ai":19,"ao":23}}}`}
	prices, err := channel.GetModelTokenPrices()
	require.NoError(t, err)
	expr, _, err := prices.Expression("m")
	require.NoError(t, err)
	cost, _, err := billingexpr.RunExprByHashWithRequest(expr, billingexpr.ExprHashString(expr), billingexpr.TokenParams{P: 10, C: 10, CR: 1, CC: 1, CC1h: 1, Img: 1, ImgCR: 1, ImgO: 1, AI: 1, AO: 1}, billingexpr.RequestInput{})
	require.NoError(t, err)
	assert.Equal(t, 102.0, cost)
	usage := &dto.Usage{PromptTokens: 1000, CompletionTokens: 100, UsageSemantic: "anthropic", ClaudeCacheCreation5mTokens: 20, ClaudeCacheCreation1hTokens: 30, PromptTokensDetails: dto.InputTokenDetails{CachedTokens: 200, CachedCreationTokens: 50}}
	params := service.BuildTieredTokenParams(usage, true, billingexpr.UsedVarsByHash(expr, billingexpr.ExprHashString(expr)))
	assert.Equal(t, 1000.0, params.P)
	cost, _, err = billingexpr.RunExprByHashWithRequest(expr, billingexpr.ExprHashString(expr), params, billingexpr.RequestInput{})
	require.NoError(t, err)
	assert.Equal(t, 2070.0, cost) // explicit free p/c, independent reads and TTL writes
	_, found, err := prices.Expression("upstream-alias")
	require.NoError(t, err)
	assert.False(t, found)

	// Optional cache writes use input price even though Anthropic reports them
	// outside input_tokens. Blank categories must not accidentally become free.
	prices["m"] = map[string]*float64{"p": common.GetPointer(2.0), "c": common.GetPointer(4.0)}
	expr, _, err = prices.Expression("m")
	require.NoError(t, err)
	params = service.BuildTieredTokenParams(usage, true, billingexpr.UsedVarsByHash(expr, billingexpr.ExprHashString(expr)))
	cost, _, err = billingexpr.RunExprByHashWithRequest(expr, billingexpr.ExprHashString(expr), params, billingexpr.RequestInput{})
	require.NoError(t, err)
	assert.Equal(t, 2900.0, cost) // (1000 + 200 + 20 + 30) * 2 + 100 * 4
}
