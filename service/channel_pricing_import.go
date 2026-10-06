package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	hostreasoning "github.com/QuantumNous/new-api/setting/reasoning"
)

const maxUpstreamPricingBytes = 10 << 20

// UpstreamPricingImport is a draft. Saving it uses the channel transaction and
// revision guard; fetching never changes routing, billing, or observations.
type UpstreamPricingImport struct {
	Models  []string                    `json:"models"`
	Configs model.ChannelModelConfigs   `json:"model_configs"`
	Items   []UpstreamPricingImportItem `json:"items"`
	Removed []string                    `json:"removed"`
}
type UpstreamPricingImportItem struct {
	Model       string `json:"model"`
	Action      string `json:"action"`
	Reason      string `json:"reason,omitempty"`
	Schedulable bool   `json:"schedulable"`
}
type upstreamPricingCatalog struct {
	Success    bool                   `json:"success"`
	Data       []upstreamPricingModel `json:"data"`
	GroupRatio map[string]float64     `json:"group_ratio"`
}
type upstreamPricingModel struct {
	Name                 string   `json:"model_name"`
	Groups               []string `json:"enable_groups"`
	QuotaType            int      `json:"quota_type"`
	ModelRatio           *float64 `json:"model_ratio"`
	ModelPrice           *float64 `json:"model_price"`
	CompletionRatio      *float64 `json:"completion_ratio"`
	CacheRatio           *float64 `json:"cache_ratio"`
	CreateCacheRatio     *float64 `json:"create_cache_ratio"`
	ImageRatio           *float64 `json:"image_ratio"`
	AudioRatio           *float64 `json:"audio_ratio"`
	AudioCompletionRatio *float64 `json:"audio_completion_ratio"`
	Mode                 string   `json:"billing_mode"`
	Expression           string   `json:"billing_expr"`
}

// FetchNewAPIPrices reads the public catalog without transmitting relay keys.
// A private/account-specific catalog requires a separate credential contract.
func FetchNewAPIPrices(ctx context.Context, baseURL, group string, currentModels []string, current model.ChannelModelConfigs) (*UpstreamPricingImport, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("invalid upstream pricing URL")
	}
	if strings.TrimSpace(group) != group || group == "" || group == "auto" || len(group) > 128 {
		return nil, errors.New("an exact, non-auto upstream group is required")
	}
	baseURL = parsed.String()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	raw, err := fetchPublicPricingJSON(ctx, baseURL+"/api/pricing")
	if err != nil {
		return nil, err
	}
	var catalog upstreamPricingCatalog
	if err = common.Unmarshal(raw, &catalog); err != nil || !catalog.Success || catalog.Data == nil {
		return nil, errors.New("invalid or unavailable upstream pricing catalog")
	}
	if len(catalog.Data) > 5000 {
		return nil, errors.New("upstream pricing catalog exceeds 5000 models")
	}
	factor, found := catalog.GroupRatio[group]
	if !found || !validImportPrice(factor) {
		return nil, errors.New("upstream group is unavailable in the public pricing catalog")
	}
	// The legacy ratio unit belongs to the upstream, never this installation.
	quotaUnit := 0.0
	for _, item := range catalog.Data {
		if (slices.Contains(item.Groups, group) || slices.Contains(item.Groups, "all")) && item.Mode != "tiered_expr" && item.QuotaType == 0 {
			statusRaw, statusErr := fetchPublicPricingJSON(ctx, baseURL+"/api/status")
			if statusErr != nil {
				return nil, errors.New("cannot determine upstream legacy pricing unit")
			}
			var status struct {
				Success bool `json:"success"`
				Data    struct {
					QuotaPerUnit float64 `json:"quota_per_unit"`
				} `json:"data"`
			}
			if common.Unmarshal(statusRaw, &status) != nil || !status.Success || !validImportPrice(status.Data.QuotaPerUnit) || status.Data.QuotaPerUnit == 0 {
				return nil, errors.New("invalid upstream legacy pricing unit")
			}
			quotaUnit = status.Data.QuotaPerUnit
			break
		}
	}
	return buildUpstreamPricingImport(baseURL, group, factor, quotaUnit, catalog.Data, currentModels, current, time.Now().Unix()), nil
}

func fetchPublicPricingJSON(ctx context.Context, endpoint string) ([]byte, error) {
	if err := ValidateSSRFProtectedFetchURL(endpoint); err != nil {
		return nil, errors.New("upstream pricing URL is blocked by fetch policy")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("invalid upstream pricing request")
	}
	request.Header.Set("Accept", "application/json")
	client := GetSSRFProtectedHTTPClient()
	if client == nil {
		return nil, errors.New("pricing HTTP client is unavailable")
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("upstream pricing request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream pricing returned HTTP %d; a public catalog is required", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxUpstreamPricingBytes+1))
	if err != nil {
		return nil, errors.New("cannot read upstream pricing response")
	}
	if len(body) > maxUpstreamPricingBytes {
		return nil, errors.New("upstream pricing response is too large")
	}
	return body, nil
}

func buildUpstreamPricingImport(baseURL, group string, factor, quotaUnit float64, items []upstreamPricingModel, currentModels []string, current model.ChannelModelConfigs, now int64) *UpstreamPricingImport {
	result := &UpstreamPricingImport{Models: slices.Clone(currentModels), Configs: maps.Clone(current), Items: []UpstreamPricingImportItem{}, Removed: []string{}}
	if result.Configs == nil {
		result.Configs = model.ChannelModelConfigs{}
	}
	available := map[string]bool{}
	counts := map[string]int{}
	for _, item := range items {
		if slices.Contains(item.Groups, group) || slices.Contains(item.Groups, "all") {
			counts[item.Name]++
		}
	}
	for _, item := range items {
		if !slices.Contains(item.Groups, group) && !slices.Contains(item.Groups, "all") {
			continue
		}
		if available[item.Name] {
			continue
		}
		available[item.Name] = true
		row := UpstreamPricingImportItem{Model: item.Name, Action: "unsupported"}
		expression, err := newAPIModelExpression(item, quotaUnit)
		if err == nil {
			expression, err = billingexpr.ScalePriceExpression(expression, factor)
		}
		candidate := model.ChannelModelConfig{BillingExpression: expression}
		if counts[item.Name] > 1 {
			err = errors.New("Duplicate upstream model prices")
		}
		if err == nil {
			err = (&model.Channel{ModelConfigs: model.ChannelModelConfigs{item.Name: candidate}}).ValidateModelConfigs()
		}
		if err != nil {
			row.Reason = err.Error()
			result.Items = append(result.Items, row)
			continue
		}
		if reference, ok := channelImportReference(item.Name); ok && candidate.ModelPublicRatio(item.Name) != nil {
			if ratio, proportional := billingexpr.ProportionalTokenPrice(expression, reference); proportional {
				candidate.BillingExpression = ""
				candidate.PublicRatio = &ratio
			}
		}
		old, exists := current[item.Name]
		source := old.PricingSource
		owned := source != nil && source.Provider == "newapi" && source.BaseURL == baseURL && source.Group == group && old.BillingExpression == source.AppliedExpression && equalImportedRatio(old.PublicRatio, source.AppliedRatio)
		if exists && !owned && (source != nil || old.BillingExpression != "" || old.PublicRatio != nil) {
			row.Action = "conflict"
			row.Reason = "Locally edited pricing was preserved"
			result.Items = append(result.Items, row)
			continue
		}
		row.Action = "added"
		if exists {
			row.Action = "updated"
		}
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(expression)))
		candidate.PricingSource = &model.ChannelModelPricingSource{Provider: "newapi", BaseURL: baseURL, Group: group, Expression: expression, SourceHash: hash, FetchedAt: now, AppliedExpression: candidate.BillingExpression, AppliedRatio: candidate.PublicRatio}
		if owned && source.SourceHash == hash && old.BillingExpression == candidate.BillingExpression && equalImportedRatio(old.PublicRatio, candidate.PublicRatio) {
			candidate.PricingSource = source
			row.Action = "unchanged"
		}
		candidate.UpstreamDiscount = old.UpstreamDiscount
		candidate.IsOnline, candidate.IsDegraded, candidate.TTFTMs, candidate.LastCheckedAt = old.IsOnline, old.IsDegraded, old.TTFTMs, old.LastCheckedAt
		row.Schedulable = candidate.ModelPublicRatio(item.Name) != nil
		if !row.Schedulable {
			row.Reason = "Imported expression has no comparable system price; routing is disabled"
		}
		result.Configs[item.Name] = candidate
		if !slices.Contains(result.Models, item.Name) {
			result.Models = append(result.Models, item.Name)
		}
		result.Items = append(result.Items, row)
	}
	for name, config := range current {
		source := config.PricingSource
		if source != nil && source.Provider == "newapi" && source.BaseURL == baseURL && source.Group == group && !available[name] {
			result.Removed = append(result.Removed, name)
		}
	}
	slices.Sort(result.Models)
	result.Models = slices.Compact(result.Models)
	slices.Sort(result.Removed)
	slices.SortFunc(result.Items, func(a, b UpstreamPricingImportItem) int { return strings.Compare(a.Model, b.Model) })
	return result
}
func equalImportedRatio(a, b *float64) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
func validImportPrice(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
func newAPIModelExpression(item upstreamPricingModel, quotaUnit float64) (string, error) {
	if item.Mode == "tiered_expr" {
		if len(item.Expression) > 16384 {
			return "", errors.New("Upstream billing expression is too long")
		}
		if strings.TrimSpace(item.Expression) == "" {
			return "", errors.New("Missing upstream billing expression")
		}
		return item.Expression, nil
	}
	if item.Mode != "" && item.Mode != "ratio" {
		return "", errors.New("Unsupported upstream billing mode")
	}
	if item.QuotaType == 1 {
		if item.ModelPrice == nil || !validImportPrice(*item.ModelPrice) {
			return "", errors.New("Invalid upstream request price")
		}
		return `tier("request", fixed(` + strconv.FormatFloat(*item.ModelPrice, 'g', -1, 64) + `))`, nil
	}
	if item.QuotaType != 0 || item.ModelRatio == nil || item.CompletionRatio == nil || quotaUnit <= 0 {
		return "", errors.New("Incomplete upstream token prices")
	}
	for _, value := range []*float64{item.ModelRatio, item.CompletionRatio, item.CacheRatio, item.CreateCacheRatio, item.ImageRatio, item.AudioRatio, item.AudioCompletionRatio} {
		if value != nil && !validImportPrice(*value) {
			return "", errors.New("Invalid upstream token price")
		}
	}
	// Legacy audio has endpoint-dependent semantics that the catalog cannot fully
	// describe. Native expressions remain lossless and are accepted above.
	if item.AudioRatio != nil || item.AudioCompletionRatio != nil {
		return "", errors.New("Legacy audio pricing requires an upstream expression")
	}
	input := *item.ModelRatio * 1_000_000 / quotaUnit
	prices := map[string]float64{"p": input, "c": input * *item.CompletionRatio, "cr": input}
	if item.CacheRatio != nil {
		prices["cr"] = input * *item.CacheRatio
	}
	if item.ImageRatio != nil && *item.ImageRatio != 1 {
		prices["img"] = input * *item.ImageRatio
	}
	claude := strings.Contains(strings.ToLower(item.Name), "claude")
	if item.CreateCacheRatio != nil || claude {
		ratio := 1.25
		if item.CreateCacheRatio != nil {
			ratio = *item.CreateCacheRatio
		}
		prices["cc"] = input * ratio
		if claude {
			prices["cc1h"] = prices["cc"] * 6 / 3.75
		}
	}
	return tokenPriceExpression(prices), nil
}
func tokenPriceExpression(prices map[string]float64) string {
	names := make([]string, 0, len(prices))
	for name := range prices {
		names = append(names, name)
	}
	slices.Sort(names)
	terms := make([]string, 0, len(names))
	for _, name := range names {
		terms = append(terms, name+" * "+strconv.FormatFloat(prices[name], 'g', -1, 64))
	}
	return `tier("base", ` + strings.Join(terms, " + ") + `)`
}
func channelImportReference(name string) (string, bool) {
	candidates := append([]string{name}, hostreasoning.CanonicalBillingModelNames(name)...)
	candidates = append(candidates, hostreasoning.BaseModelName(name))
	for _, candidate := range slices.Compact(candidates) {
		if billing_setting.GetBillingMode(candidate) == billing_setting.BillingModeTieredExpr {
			return billing_setting.GetBillingExpr(candidate)
		}
		if _, fixed := ratio_setting.GetModelPrice(candidate, false); fixed {
			return "", false
		}
		if _, found, _ := ratio_setting.GetModelRatio(candidate); !found {
			continue
		}
		if operation_setting.GetGeminiInputAudioPricePerMillionTokens(candidate) > 0 {
			return "", false
		}
		prices, ok := model.SystemTokenPrices(candidate)
		if !ok {
			return "", false
		}
		if _, audio := prices["ai"]; audio {
			return "", false
		}
		return tokenPriceExpression(prices), true
	}
	return "", false
}
