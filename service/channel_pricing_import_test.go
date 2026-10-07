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
	// The channel-8 upstream omits billing_expr and publishes this structure.
	body = `{"success":true,"group_ratio":{"vip":0.5},"data":[{"model_name":"structured-import","enable_groups":["vip"],"billing_mode":"tiered_expr","model_ratio":999,"expression_pricing":` + structuredImportFixture + `}]}`
	result, err = FetchNewAPIPrices(context.Background(), "https://prices.example", "vip", nil, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"structured-import"}, result.Models)
	require.Len(t, result.Items, 1)
	assert.Equal(t, "added", result.Items[0].Action)
	assert.False(t, result.Items[0].Schedulable, "a missing local system reference still prevents routing")
	imported := result.Configs["structured-import"]
	require.NotNil(t, imported.PricingSource)
	assert.Equal(t, imported.BillingExpression, imported.PricingSource.Expression)
	assert.Nil(t, imported.PublicRatio, "service rules cannot be replaced by the old model_ratio")
	for _, length := range []float64{272000, 272001} {
		for _, service := range []struct {
			name   string
			factor float64
		}{{"", 1}, {"default", 1}, {"fast", 2.5}, {"flex", .5}, {"priority", 2.5}, {"unknown", 1}} {
			request, err := common.Marshal(map[string]string{"service_tier": service.name})
			require.NoError(t, err)
			// These are normalized billable categories; Len independently selects the tier.
			cost, _, err := billingexpr.RunExprWithRequest(imported.BillingExpression, billingexpr.TokenParams{P: 100, C: 10, CR: 20, CC: 3, CC1h: 2, Len: length}, billingexpr.RequestInput{Body: request})
			require.NoError(t, err)
			base := 100*5.0 + 10*30 + 20*.5 + 3*5 + 2*5
			if length > 272000 {
				base = 100*10.0 + 10*45 + 20*1 + 3*10 + 2*10
			}
			assert.InDelta(t, base*.5*service.factor, cost, 1e-9, "len=%v service=%s", length, service.name)
		}
	}
	again, err := FetchNewAPIPrices(context.Background(), "https://prices.example", "vip", result.Models, result.Configs)
	require.NoError(t, err)
	assert.Equal(t, result.Configs, again.Configs)
	assert.Equal(t, "unchanged", again.Items[0].Action)

	body = `{"success":true,"group_ratio":{"vip":1},"data":[]}`
	result, err = FetchNewAPIPrices(context.Background(), "https://prices.example", "vip", []string{"keep"}, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"keep"}, result.Models)
	body = strings.Repeat("x", maxUpstreamPricingBytes+1)
	_, err = FetchNewAPIPrices(context.Background(), "https://prices.example", "vip", nil, nil)
	require.ErrorContains(t, err, "too large")
}

// Public expression_pricing returned for gpt-5.5 by channel 8's upstream.
const structuredImportFixture = `{"tiers":[{"min_input":0,"max_input":272000,"input":5,"output":30,"cache_read":0.5,"cache_write":5,"cache_write_1h":5},{"min_input":272001,"input":10,"output":45,"cache_read":1,"cache_write":10,"cache_write_1h":10}],"service_multipliers":{"fast":2.5,"flex":0.5,"priority":2.5}}`

func TestStructuredNewAPIPriceValidation(t *testing.T) {
	for _, tc := range []struct{ name, source string }{
		{"empty tiers", `{"tiers":[]}`},
		{"null", `null`},
		{"unknown rule", strings.Replace(structuredImportFixture, `"tiers":`, `"discount":0.5,"tiers":`, 1)},
		{"unknown price", strings.Replace(structuredImportFixture, `"input":5`, `"image":5,"input":5`, 1)},
		{"gap", strings.Replace(structuredImportFixture, `"min_input":272001`, `"min_input":272002`, 1)},
		{"overlap", strings.Replace(structuredImportFixture, `"min_input":272001`, `"min_input":272000`, 1)},
		{"fractional boundary", strings.Replace(structuredImportFixture, `"max_input":272000`, `"max_input":272000.5`, 1)},
		{"missing first boundary", strings.Replace(structuredImportFixture, `"min_input":0,`, ``, 1)},
		{"bounded last tier", strings.Replace(structuredImportFixture, `"min_input":272001`, `"min_input":272001,"max_input":500000`, 1)},
		{"null cache write", strings.Replace(structuredImportFixture, `"cache_write":5`, `"cache_write":null`, 1)},
		{"null 1h cache write", strings.Replace(structuredImportFixture, `"cache_write_1h":5`, `"cache_write_1h":null`, 1)},
		{"negative cache write", strings.Replace(structuredImportFixture, `"cache_write":5`, `"cache_write":-1`, 1)},
		{"negative 1h cache write", strings.Replace(structuredImportFixture, `"cache_write_1h":5`, `"cache_write_1h":-1`, 1)},
		{"missing input", strings.Replace(structuredImportFixture, `"input":5,`, ``, 1)},
		{"missing cache price", strings.Replace(structuredImportFixture, `"cache_read":0.5,`, ``, 1)},
		{"negative price", strings.Replace(structuredImportFixture, `"input":5`, `"input":-5`, 1)},
		{"null price", strings.Replace(structuredImportFixture, `"input":5`, `"input":null`, 1)},
		{"unknown service", strings.Replace(structuredImportFixture, `"fast":`, `"new-rule":`, 1)},
		{"null services", strings.Replace(structuredImportFixture, `{"fast":2.5,"flex":0.5,"priority":2.5}`, `null`, 1)},
		{"negative service", strings.Replace(structuredImportFixture, `"fast":2.5`, `"fast":-2.5`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var item upstreamPricingModel
			raw := `{"billing_mode":"tiered_expr","model_ratio":1,"expression_pricing":` + tc.source + `}`
			require.NoError(t, common.Unmarshal([]byte(raw), &item))
			_, err := newAPIModelExpression(item, 500000)
			require.Error(t, err, "invalid expression metadata must never fall back to legacy ratios")
			item.Expression = `tier("native", p*2+c*8)`
			expr, err := newAPIModelExpression(item, 500000)
			require.NoError(t, err)
			assert.Equal(t, item.Expression, expr, "the complete native expression takes priority")
		})
	}
	for _, tc := range []struct {
		name, fields   string
		write, write1h float64
	}{
		{"1h inherits write", `,"cache_write":3`, 3, 3},
		{"both inherit input", ``, 2, 2},
		{"explicit 1h keeps price", `,"cache_write_1h":7`, 2, 7},
		{"zero write inherited", `,"cache_write":0`, 0, 0},
		{"zero 1h preserved", `,"cache_write":3,"cache_write_1h":0`, 3, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expr, err := newAPIStructuredPriceExpression([]byte(`{"tiers":[{"min_input":0,"input":2,"output":8,"cache_read":0.2` + tc.fields + `}]}`))
			require.NoError(t, err)
			prices, ok := billingexpr.BaseTokenPrices(expr)
			require.True(t, ok)
			assert.Equal(t, tc.write, prices["cc"])
			assert.Equal(t, tc.write1h, prices["cc1h"])
			require.True(t, billingexpr.UsedVars(expr)["cc"])
			require.True(t, billingexpr.UsedVars(expr)["cc1h"])
			cost, _, err := billingexpr.RunExprWithRequest(expr, billingexpr.TokenParams{CC: 100, CC1h: 50}, billingexpr.RequestInput{})
			require.NoError(t, err)
			assert.Equal(t, tc.write*100+tc.write1h*50, cost)
		})
	}
	// Resolve each tier independently; never inherit a price from a previous tier.
	inherited := strings.ReplaceAll(strings.ReplaceAll(structuredImportFixture, `,"cache_write_1h":5`, ``), `,"cache_write_1h":10`, ``)
	inherited = strings.Replace(inherited, `,"cache_write":10`, ``, 1)
	inherited = strings.Replace(inherited, `"input":10`, `"input":20`, 1)
	tierExpr, err := newAPIStructuredPriceExpression([]byte(inherited))
	require.NoError(t, err)
	for _, tc := range []struct{ length, price float64 }{{272000, 5}, {272001, 20}} {
		cost, _, err := billingexpr.RunExprWithRequest(tierExpr, billingexpr.TokenParams{CC: 100, CC1h: 50, Len: tc.length}, billingexpr.RequestInput{})
		require.NoError(t, err)
		assert.Equal(t, 150*tc.price, cost)
	}
	// Explicit zero prices remain distinct from missing fields; no service rules
	// permits the existing complete-function proportionality check.
	expr, err := newAPIStructuredPriceExpression([]byte(`{"tiers":[{"min_input":0,"max_input":null,"input":2,"output":8,"cache_read":0,"cache_write":0,"cache_write_1h":0}]}`))
	require.NoError(t, err)
	prices, ok := billingexpr.BaseTokenPrices(expr)
	require.True(t, ok)
	assert.Contains(t, prices, "cr")
	assert.Zero(t, prices["cr"])
	ratio, ok := billingexpr.ProportionalTokenPrice(expr, `tier("base", p*4+c*16+cr*0+cc*0+cc1h*0)`)
	require.True(t, ok)
	assert.Equal(t, .5, ratio)
	// Zero is also a valid service price, not an omitted multiplier.
	expr, err = newAPIStructuredPriceExpression([]byte(strings.Replace(structuredImportFixture, `"fast":2.5`, `"fast":0`, 1)))
	require.NoError(t, err)
	cost, _, err := billingexpr.RunExprWithRequest(expr, billingexpr.TokenParams{P: 100, C: 10, Len: 100}, billingexpr.RequestInput{Body: []byte(`{"service_tier":"fast"}`)})
	require.NoError(t, err)
	assert.Zero(t, cost)
}
