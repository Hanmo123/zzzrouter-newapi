package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchSub2APIPrices(t *testing.T) {
	oldHTTP, oldProtected := httpClient, ssrfProtectedHTTPClient
	fetch := system_setting.GetFetchSetting()
	oldFetch := *fetch
	fetch.EnableSSRFProtection = false
	t.Cleanup(func() { httpClient, ssrfProtectedHTTPClient = oldHTTP, oldProtected; *fetch = oldFetch })
	body := `{"code":0,"message":"success","data":{"groups":[{"id":12,"name":"vip","rate_multiplier":1.5,"models":[{"name":"tiered","pricing":{"input_price":0.000003,"output_price":0.000015,"cache_write_price":0.00000375,"cache_write_1h_price":0.000006,"cache_read_price":0.0000003,"intervals":[{"min_tokens":0,"max_tokens":200000,"input_price":0.000003,"output_price":0.000015,"cache_write_price":0.00000375,"cache_write_1h_price":0.000006,"cache_read_price":0.0000003},{"min_tokens":200000,"max_tokens":null,"input_price":0.000006,"output_price":0.0000225,"cache_write_price":0.0000075,"cache_write_1h_price":0.000012,"cache_read_price":0.0000006}] }},{"name":"time-priced","pricing":{"input_price":0.000003,"output_price":0.000015,"cache_write_price":0.00000375,"cache_write_1h_price":0.000006,"cache_read_price":0.0000003,"time_pricing":{"timezone":"UTC"}}},{"name":"request-priced","pricing":{"per_request_price":0.01}}]},{"id":13,"name":"other","rate_multiplier":1,"models":[]}]}}`
	client := &http.Client{Transport: importPricingTransport(func(request *http.Request) (*http.Response, error) {
		assert.Empty(t, request.Header.Get("Authorization"))
		assert.Equal(t, "/api/v1/model-plaza", request.URL.Path)
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	httpClient, ssrfProtectedHTTPClient = client, client
	result, err := FetchSub2APIPrices(context.Background(), "https://sub2api.example/", "12", nil, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"tiered"}, result.Models)
	require.Len(t, result.Items, 3)
	assert.Equal(t, "sub2api", result.Configs["tiered"].PricingSource.Provider)
	assert.Equal(t, "12", result.Configs["tiered"].PricingSource.Group)
	expression := result.Configs["tiered"].PricingSource.Expression
	assert.Contains(t, expression, "len <= 200000")
	cost, _, err := billingexpr.RunExprWithRequest(expression, billingexpr.TokenParams{P: 100, C: 10, CR: 20, CC: 3, CC1h: 2, Len: 200001}, billingexpr.RequestInput{})
	require.NoError(t, err)
	assert.InDelta(t, (100*6+10*22.5+20*.6+3*7.5+2*12)*1.5, cost, 1e-9)
	for _, item := range result.Items {
		switch item.Model {
		case "time-priced", "request-priced":
			assert.Equal(t, "unsupported", item.Action)
		case "tiered":
			assert.Equal(t, "added", item.Action)
		}
	}
	_, err = FetchSub2APIPrices(context.Background(), "https://sub2api.example", "missing", nil, nil)
	assert.ErrorContains(t, err, "group is unavailable")
}
