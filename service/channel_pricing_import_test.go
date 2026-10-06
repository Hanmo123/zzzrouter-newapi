package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChannelPricingImport(t *testing.T) {
	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(k, v string) error { saved[k] = v; return nil }))
	t.Cleanup(func() { require.NoError(t, config.GlobalConfig.LoadFromDB(saved)) })
	flat := `tier("base",p*2+c*8+cr*0.2)`
	tiered := `len <= 100 ? tier("short",p*2+c*8+cr*0.2) : tier("long",p*4+c*16+cr*0.4)`
	modes := map[string]string{}
	expressions := map[string]string{}
	for _, name := range []string{"flat", "tiers", "different-long", "different-threshold", "request", "free", "nonlinear", "manual"} {
		modes[name] = "tiered_expr"
		expressions[name] = flat
	}
	expressions["tiers"] = tiered
	expressions["different-long"] = tiered
	expressions["different-threshold"] = tiered
	modeJSON, err := common.Marshal(modes)
	require.NoError(t, err)
	exprJSON, err := common.Marshal(expressions)
	require.NoError(t, err)
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"billing_setting.billing_mode": string(modeJSON), "billing_setting.billing_expr": string(exprJSON)}))
	items := []upstreamPricingModel{
		{Name: "flat", Groups: []string{"vip"}, Mode: "tiered_expr", Expression: flat},
		{Name: "tiers", Groups: []string{"vip"}, Mode: "tiered_expr", Expression: tiered},
		{Name: "different-long", Groups: []string{"vip"}, Mode: "tiered_expr", Expression: `len <= 100 ? tier("short",p*2+c*8+cr*0.2) : tier("long",p*4+c*24+cr*0.4)`},
		{Name: "different-threshold", Groups: []string{"vip"}, Mode: "tiered_expr", Expression: `len <= 200 ? tier("short",p*2+c*8+cr*0.2) : tier("long",p*4+c*16+cr*0.4)`},
		{Name: "request", Groups: []string{"vip"}, Mode: "tiered_expr", Expression: `param("speed") == "fast" ? tier("fast",p*4+c*16) : tier("base",p*2+c*8)`},
		{Name: "free", Groups: []string{"vip"}, Mode: "tiered_expr", Expression: `tier("base",p*0+c*0+cr*0)`},
		{Name: "nonlinear", Groups: []string{"vip"}, Mode: "tiered_expr", Expression: `tier("base",p*p)`},
		{Name: "unknown-reference", Groups: []string{"vip"}, Mode: "tiered_expr", Expression: flat},
		{Name: "bad", Groups: []string{"vip"}, Mode: "tiered_expr", Expression: `tier("base",p*-2)`},
		{Name: "manual", Groups: []string{"vip"}, Mode: "tiered_expr", Expression: flat},
		{Name: "other-group", Groups: []string{"default"}, Mode: "tiered_expr", Expression: flat},
	}
	current := model.ChannelModelConfigs{
		"flat":   {UpstreamDiscount: common.GetPointer(.8), IsOnline: common.GetPointer(false), TTFTMs: common.GetPointer(int64(0))},
		"manual": {PublicRatio: common.GetPointer(.3)},
	}
	result := buildUpstreamPricingImport("https://upstream.example", "vip", .1, 500000, items, []string{"manual"}, current, 100)
	assert.InDelta(t, .1, *result.Configs["flat"].PublicRatio, 1e-12)
	assert.InDelta(t, .1, *result.Configs["tiers"].PublicRatio, 1e-12)
	assert.Equal(t, 0.0, *result.Configs["free"].PublicRatio)
	assert.Empty(t, result.Configs["tiers"].BillingExpression)
	assert.Equal(t, current["flat"].UpstreamDiscount, result.Configs["flat"].UpstreamDiscount)
	assert.Equal(t, current["flat"].IsOnline, result.Configs["flat"].IsOnline)
	assert.Equal(t, current["flat"].TTFTMs, result.Configs["flat"].TTFTMs)
	assert.Equal(t, current["manual"], result.Configs["manual"])
	assert.NotContains(t, result.Models, "bad")
	assert.NotContains(t, result.Models, "other-group")
	assert.Nil(t, result.Configs["different-long"].PublicRatio)
	cost, trace, err := billingexpr.RunExprWithRequest(result.Configs["different-long"].BillingExpression, billingexpr.TokenParams{P: 200, C: 100, Len: 200}, billingexpr.RequestInput{})
	require.NoError(t, err)
	assert.InDelta(t, 320, cost, 1e-9)
	assert.Equal(t, "long", trace.MatchedTier)
	require.NotEmpty(t, result.Configs["different-threshold"].BillingExpression)
	for _, name := range []string{"request", "nonlinear", "unknown-reference"} {
		assert.Nil(t, result.Configs[name].ModelPublicRatio(name))
		assert.NotEmpty(t, result.Configs[name].BillingExpression)
	}
	assert.Equal(t, .8, *current["flat"].UpstreamDiscount)
	assert.Nil(t, current["flat"].PricingSource, "preview must not mutate the input")
	again := buildUpstreamPricingImport("https://upstream.example", "vip", .1, 500000, items, result.Models, result.Configs, 200)
	assert.Equal(t, result.Configs, again.Configs, "unchanged synchronization is idempotent including timestamps")
	edited := again.Configs["tiers"]
	edited.PublicRatio = common.GetPointer(.7)
	again.Configs["tiers"] = edited
	changed := buildUpstreamPricingImport("https://upstream.example", "vip", .2, 500000, items, again.Models, again.Configs, 300)
	assert.Equal(t, .7, *changed.Configs["tiers"].PublicRatio, "manual edit must survive")
	assert.InDelta(t, .2, *changed.Configs["flat"].PublicRatio, 1e-12)
	removed := buildUpstreamPricingImport("https://upstream.example", "vip", .2, 500000, nil, changed.Models, changed.Configs, 400)
	assert.Contains(t, removed.Removed, "flat")
	assert.Equal(t, changed.Configs, removed.Configs)
	// Recompute against changed local references without changing the source price.
	expressions["flat"] = `tier("base",p*4+c*16+cr*0.4)`
	exprJSON, err = common.Marshal(expressions)
	require.NoError(t, err)
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{"billing_setting.billing_expr": string(exprJSON)}))
	repriced := buildUpstreamPricingImport("https://upstream.example", "vip", .2, 500000, items, changed.Models, changed.Configs, 500)
	assert.InDelta(t, .1, *repriced.Configs["flat"].PublicRatio, 1e-12)
}

func TestImportedExpressionsPreserveFullSettlement(t *testing.T) {
	for _, source := range []string{
		`v1:len <= 100 ? tier("a",p*2+c*8) : tier("b",p*4+c*16+cr*0.4)`,
		`tier("base",p*2+c*8) * (param("speed") == "fast" ? 6 : 1)`,
		`len <= 100 ? tier("request",fixed(0.02)) : tier("token",p*3+c*9)`,
		`tier("image",fixed(0.1)) * image_count`,
	} {
		t.Run(source, func(t *testing.T) {
			scaled, err := billingexpr.ScalePriceExpression(source, .125)
			require.NoError(t, err)
			for _, length := range []float64{50, 200} {
				params := billingexpr.TokenParams{P: length, C: 20, CR: 10, Len: length}
				request := billingexpr.RequestInput{Body: []byte(`{"speed":"fast"}`)}
				before, _, err := billingexpr.RunExprWithRequest(source, params, request)
				require.NoError(t, err)
				after, _, err := billingexpr.RunExprWithRequest(scaled, params, request)
				require.NoError(t, err)
				assert.InDelta(t, before*.125, after, 1e-9)
			}
		})
	}
	// A later-only variable must not be mistaken for an omitted variable in a
	// reference expression: their normalization differs in the first tier.
	_, ok := billingexpr.ProportionalTokenPrice(`len<100 ? tier("a",p) : tier("b",p+cr)`, `len<100 ? tier("a",p) : tier("b",p)`)
	assert.False(t, ok)
}

type importPricingTransport func(*http.Request) (*http.Response, error)

func (fn importPricingTransport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }
func TestFetchNewAPIGroupCatalog(t *testing.T) {
	oldHTTP, oldProtected := httpClient, ssrfProtectedHTTPClient
	fetch := system_setting.GetFetchSetting()
	oldFetch := *fetch
	fetch.EnableSSRFProtection = false
	t.Cleanup(func() { httpClient, ssrfProtectedHTTPClient = oldHTTP, oldProtected; *fetch = oldFetch })
	body := `{"success":true,"group_ratio":{"vip":0.2},"data":[{"model_name":"claude-import","enable_groups":["vip"],"quota_type":0,"model_ratio":1,"completion_ratio":4,"cache_ratio":0,"create_cache_ratio":1.25},{"model_name":"else","enable_groups":["default"],"quota_type":0}]}`
	status := `{"success":true,"data":{"quota_per_unit":1000000}}`
	code := 200
	client := &http.Client{Transport: importPricingTransport(func(request *http.Request) (*http.Response, error) {
		assert.Empty(t, request.Header.Get("Authorization"))
		assert.Equal(t, http.MethodGet, request.Method)
		_, deadline := request.Context().Deadline()
		assert.True(t, deadline)
		result := body
		if request.URL.Path == "/api/status" {
			result = status
		}
		return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(result))}, nil
	})}
	httpClient, ssrfProtectedHTTPClient = client, client
	result, err := FetchNewAPIPrices(context.Background(), "https://prices.example/", "vip", nil, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"claude-import"}, result.Models)
	prices, ok := billingexpr.BaseTokenPrices(result.Configs["claude-import"].PricingSource.Expression)
	require.True(t, ok)
	assert.InDelta(t, .2, prices["p"], 1e-12)
	assert.InDelta(t, .8, prices["c"], 1e-12)
	assert.Zero(t, prices["cr"])
	assert.InDelta(t, .4, prices["cc1h"], 1e-12)
	_, err = FetchNewAPIPrices(context.Background(), "https://prices.example", "missing", nil, nil)
	require.ErrorContains(t, err, "group is unavailable")
	_, err = FetchNewAPIPrices(context.Background(), "https://prices.example", "auto", nil, nil)
	require.Error(t, err)
	_, err = FetchNewAPIPrices(context.Background(), "https://secret@prices.example", "vip", nil, nil)
	require.Error(t, err)
	code = 403
	_, err = FetchNewAPIPrices(context.Background(), "https://prices.example", "vip", nil, nil)
	require.ErrorContains(t, err, "HTTP 403")
	code = 200
	body = `{"success":true,"group_ratio":{"vip":1},"data":[]}`
	result, err = FetchNewAPIPrices(context.Background(), "https://prices.example", "vip", []string{"keep"}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"keep"}, result.Models)
	body = strings.Repeat("x", maxUpstreamPricingBytes+1)
	_, err = FetchNewAPIPrices(context.Background(), "https://prices.example", "vip", nil, nil)
	require.ErrorContains(t, err, "too large")
}
