package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
)

// Sub2API's model plaza exposes prices in USD per token. The local billing
// expression language uses USD per million tokens, so conversion happens here
// before the selected group's rate multiplier is applied by the import merge.
type sub2APIModelPlaza struct {
	ID     int                 `json:"id"`
	Name   string              `json:"name"`
	Models []sub2APIPlazaModel `json:"models"`
	Rate   *float64            `json:"rate_multiplier"`
}

type sub2APIPlazaModel struct {
	Name    string               `json:"name"`
	Pricing *sub2APIPlazaPricing `json:"pricing"`
}

type sub2APIPlazaPricing struct {
	InputPrice                 *float64               `json:"input_price"`
	OutputPrice                *float64               `json:"output_price"`
	CacheWritePrice            *float64               `json:"cache_write_price"`
	CacheWrite1hPrice          *float64               `json:"cache_write_1h_price"`
	CacheReadPrice             *float64               `json:"cache_read_price"`
	ImageInputPrice            *float64               `json:"image_input_price"`
	ImageOutputPrice           *float64               `json:"image_output_price"`
	PerRequestPrice            *float64               `json:"per_request_price"`
	ReasoningEffortMultipliers map[string]float64     `json:"reasoning_effort_multipliers"`
	TimePricing                json.RawMessage        `json:"time_pricing"`
	Intervals                  []sub2APIPriceInterval `json:"intervals"`
}

type sub2APIPriceInterval struct {
	MinTokens         int64    `json:"min_tokens"`
	MaxTokens         *int64   `json:"max_tokens"`
	InputPrice        *float64 `json:"input_price"`
	OutputPrice       *float64 `json:"output_price"`
	CacheWritePrice   *float64 `json:"cache_write_price"`
	CacheWrite1hPrice *float64 `json:"cache_write_1h_price"`
	CacheReadPrice    *float64 `json:"cache_read_price"`
	ImageInputPrice   *float64 `json:"image_input_price"`
	ImageOutputPrice  *float64 `json:"image_output_price"`
	PerRequestPrice   *float64 `json:"per_request_price"`
}

type sub2APIModelPlazaResponse struct {
	Code int `json:"code"`
	Data struct {
		Groups []sub2APIModelPlaza `json:"groups"`
	} `json:"data"`
}

func FetchSub2APIPrices(ctx context.Context, baseURL, group string, currentModels []string, current model.ChannelModelConfigs, manualFactor *float64) (*UpstreamPricingImport, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("invalid upstream pricing URL")
	}
	if strings.TrimSpace(group) != group || group == "" || group == "auto" || len(group) > 128 {
		return nil, errors.New("an exact, non-auto upstream group is required")
	}
	baseURL = parsed.String()
	if manualFactor != nil {
		if !validImportPrice(*manualFactor) || *manualFactor > 1e9 {
			return nil, errors.New("invalid manual Sub2API group multiplier")
		}
		return buildSub2APIManualPricingImport(baseURL, group, *manualFactor, currentModels, current, time.Now().Unix()), nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	raw, err := fetchPublicPricingJSON(ctx, baseURL+"/api/v1/model-plaza")
	if err != nil {
		return nil, err
	}
	groups, err := decodeSub2APIModelPlaza(raw)
	if err != nil {
		return nil, err
	}
	selected, found := sub2APIGroup(groups, group)
	if !found {
		return nil, errors.New("Sub2API group is unavailable in the public model plaza")
	}
	factor, err := sub2APIGroupRate(selected)
	if err != nil {
		return nil, err
	}
	if len(selected.Models) > 5000 {
		return nil, errors.New("Sub2API group exceeds 5000 models")
	}
	entries := make([]upstreamExpressionItem, 0, len(selected.Models))
	for _, item := range selected.Models {
		if strings.TrimSpace(item.Name) == "" {
			entries = append(entries, upstreamExpressionItem{err: errors.New("Sub2API model has no name")})
			continue
		}
		expression, expressionErr := newSub2APIModelExpression(item.Pricing)
		entries = append(entries, upstreamExpressionItem{name: item.Name, expression: expression, err: expressionErr})
	}
	return buildExpressionPricingImport("sub2api", baseURL, group, factor, entries, currentModels, current, time.Now().Unix()), nil
}

func buildSub2APIManualPricingImport(baseURL, group string, factor float64, currentModels []string, current model.ChannelModelConfigs, now int64) *UpstreamPricingImport {
	entries := make([]upstreamExpressionItem, 0, len(currentModels))
	for _, name := range currentModels {
		expression, ok := channelImportReference(name)
		if !ok {
			entries = append(entries, upstreamExpressionItem{name: name, err: errors.New("No comparable system price for manual Sub2API multiplier")})
			continue
		}
		entries = append(entries, upstreamExpressionItem{name: name, expression: expression})
	}
	return buildExpressionPricingImport("sub2api", baseURL, group, factor, entries, currentModels, current, now)
}
func sub2APIGroup(groups []sub2APIModelPlaza, requested string) (sub2APIModelPlaza, bool) {
	requestedID, idErr := strconv.Atoi(requested)
	var selected sub2APIModelPlaza
	found := false
	for _, group := range groups {
		matches := group.Name == requested || idErr == nil && group.ID == requestedID
		if !matches {
			continue
		}
		if found {
			return sub2APIModelPlaza{}, false
		}
		selected, found = group, true
	}
	return selected, found
}
func newSub2APIModelExpression(pricing *sub2APIPlazaPricing) (string, error) {
	if pricing == nil {
		return "", errors.New("Missing Sub2API model pricing")
	}
	if pricing.PerRequestPrice != nil || (len(pricing.Intervals) > 0 && slices.ContainsFunc(pricing.Intervals, func(interval sub2APIPriceInterval) bool { return interval.PerRequestPrice != nil })) {
		return "", errors.New("Sub2API per-request pricing is not representable as token pricing")
	}
	if len(pricing.TimePricing) != 0 && string(pricing.TimePricing) != "null" && string(pricing.TimePricing) != "{}" {
		return "", errors.New("Sub2API time pricing is not representable in a static import")
	}

	intervals := pricing.Intervals
	if len(intervals) == 0 {
		intervals = []sub2APIPriceInterval{{
			InputPrice: pricing.InputPrice, OutputPrice: pricing.OutputPrice,
			CacheWritePrice: pricing.CacheWritePrice, CacheWrite1hPrice: pricing.CacheWrite1hPrice,
			CacheReadPrice: pricing.CacheReadPrice, ImageInputPrice: pricing.ImageInputPrice,
			ImageOutputPrice: pricing.ImageOutputPrice,
		}}
	}
	var expression strings.Builder
	for index, interval := range intervals {
		if index == 0 {
			if interval.MinTokens != 0 {
				return "", errors.New("Sub2API pricing intervals must start at zero")
			}
		} else {
			previous := intervals[index-1].MaxTokens
			if previous == nil || interval.MinTokens != *previous || interval.MinTokens < 0 {
				return "", errors.New("Sub2API pricing intervals must be contiguous")
			}
		}
		if index == len(intervals)-1 {
			if interval.MaxTokens != nil {
				return "", errors.New("The final Sub2API pricing interval must have no upper bound")
			}
		} else if interval.MaxTokens == nil || *interval.MaxTokens < interval.MinTokens || *interval.MaxTokens > (1<<53)-2 {
			return "", errors.New("Invalid Sub2API pricing interval boundary")
		} else {
			fmt.Fprintf(&expression, "len <= %d ? ", *interval.MaxTokens)
		}
		prices, err := sub2APITokenPrices(interval)
		if err != nil {
			return "", err
		}
		expression.WriteString(strings.Replace(tokenPriceExpression(prices), `"base"`, fmt.Sprintf(`"tier_%d"`, index+1), 1))
		if index < len(intervals)-1 {
			expression.WriteString(" : ")
		}
	}
	result := "v1:(" + expression.String() + ")"
	names := make([]string, 0, len(pricing.ReasoningEffortMultipliers))
	for name, multiplier := range pricing.ReasoningEffortMultipliers {
		if strings.TrimSpace(name) == "" || !validImportPrice(multiplier) {
			return "", errors.New("Invalid Sub2API reasoning effort multiplier")
		}
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		multiplier := pricing.ReasoningEffortMultipliers[name]
		if multiplier != 1 {
			result += fmt.Sprintf(` * (param("reasoning_effort") == %q ? %s : 1)`, name, strconv.FormatFloat(multiplier, 'g', -1, 64))
		}
	}
	if len(result) > 16384 {
		return "", errors.New("Sub2API billing expression is too long")
	}
	if _, err := billingexpr.CompileFromCache(result); err != nil {
		return "", err
	}
	return result, nil
}

func sub2APITokenPrices(interval sub2APIPriceInterval) (map[string]float64, error) {
	fields := []struct {
		name  string
		value *float64
		unit  string
	}{
		{"input_price", interval.InputPrice, "p"},
		{"output_price", interval.OutputPrice, "c"},
		{"cache_write_price", interval.CacheWritePrice, "cc"},
		{"cache_write_1h_price", interval.CacheWrite1hPrice, "cc1h"},
		{"cache_read_price", interval.CacheReadPrice, "cr"},
	}
	prices := make(map[string]float64, len(fields)+2)
	for _, field := range fields {
		if field.value == nil || !validImportPrice(*field.value) {
			return nil, fmt.Errorf("Missing or invalid Sub2API token price: %s", field.name)
		}
		value := *field.value * 1_000_000
		if !validImportPrice(value) {
			return nil, fmt.Errorf("Invalid Sub2API token price: %s", field.name)
		}
		prices[field.unit] = value
	}
	for _, image := range []struct {
		name  string
		value *float64
		unit  string
	}{{"image_input_price", interval.ImageInputPrice, "img"}, {"image_output_price", interval.ImageOutputPrice, "img_o"}} {
		if image.value != nil {
			if !validImportPrice(*image.value) || !validImportPrice(*image.value*1_000_000) {
				return nil, fmt.Errorf("Invalid Sub2API token price: %s", image.name)
			}
			prices[image.unit] = *image.value * 1_000_000
		}
	}
	return prices, nil
}

func decodeSub2APIModelPlaza(raw []byte) ([]sub2APIModelPlaza, error) {
	var response sub2APIModelPlazaResponse
	if err := common.Unmarshal(raw, &response); err != nil || response.Code != 0 || response.Data.Groups == nil {
		return nil, errors.New("Invalid or unavailable Sub2API model plaza")
	}
	return response.Data.Groups, nil
}

func sub2APIGroupRate(group sub2APIModelPlaza) (float64, error) {
	if group.Rate == nil || !validImportPrice(*group.Rate) {
		return 0, errors.New("Sub2API group has no valid rate multiplier")
	}
	return *group.Rate, nil
}
