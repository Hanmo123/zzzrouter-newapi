package model

import (
	"crypto/sha256"
	"database/sql/driver"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	hostreasoning "github.com/QuantumNous/new-api/setting/reasoning"
)

// ChannelModelConfig belongs to one channel and one public model. Observations are
// nullable; billing settings never use a channel-wide fallback.
type ChannelModelConfig struct {
	PricingSource     *ChannelModelPricingSource `json:"pricing_source,omitempty"`
	UpstreamDiscount  *float64                   `json:"upstream_discount,omitempty"`
	PublicRatio       *float64                   `json:"public_ratio,omitempty"`
	BillingExpression string                     `json:"billing_expression,omitempty"`
	IsDegraded        *bool                      `json:"is_degraded"`
	IsOnline          *bool                      `json:"is_online"`
	TTFTMs            *int64                     `json:"ttft_ms"`
	LastCheckedAt     *int64                     `json:"last_checked_at"`
}

// ChannelModelPricingSource is a public-price snapshot, never a credential.
// Applied fields let future synchronization distinguish manual edits from imports.
type ChannelModelPricingSource struct {
	Provider          string   `json:"provider"`
	BaseURL           string   `json:"base_url"`
	Group             string   `json:"group"`
	Expression        string   `json:"expression"`
	SourceHash        string   `json:"source_hash"`
	FetchedAt         int64    `json:"fetched_at"`
	AppliedExpression string   `json:"applied_expression,omitempty"`
	AppliedRatio      *float64 `json:"applied_ratio,omitempty"`
}

type ChannelModelConfigs map[string]ChannelModelConfig

func (configs ChannelModelConfigs) Revision() string {
	data, _ := common.Marshal(configs)
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func (configs ChannelModelConfigs) Value() (driver.Value, error) {
	if configs == nil {
		return nil, nil
	}
	data, err := common.Marshal(configs)
	return string(data), err
}
func (configs *ChannelModelConfigs) Scan(value any) error {
	*configs = nil
	if value == nil {
		return nil
	}
	var data []byte
	switch raw := value.(type) {
	case []byte:
		data = raw
	case string:
		data = []byte(raw)
	default:
		return errors.New("invalid channel model configuration storage")
	}
	if len(data) == 0 {
		return nil
	}
	return common.Unmarshal(data, configs)
}

func (config ChannelModelConfig) Discount() float64 {
	if config.UpstreamDiscount == nil {
		return 1
	}
	return *config.UpstreamDiscount
}
func (config ChannelModelConfig) ChargeRatio() float64 {
	if config.BillingExpression != "" || config.PublicRatio == nil {
		return 1
	}
	return *config.PublicRatio
}
func (config ChannelModelConfig) Validate() error {
	if source := config.PricingSource; source != nil {
		if (source.Provider != "newapi" && source.Provider != "sub2api") || len(source.BaseURL) > 2048 || len(source.Group) > 128 || len(source.Expression) > 16384 || len(source.AppliedExpression) > 16384 || source.FetchedAt < 0 {
			return errors.New("invalid model pricing source")
		}
		if source.AppliedRatio != nil && (*source.AppliedRatio < 0 || math.IsNaN(*source.AppliedRatio) || math.IsInf(*source.AppliedRatio, 0)) {
			return errors.New("invalid imported ratio")
		}
	}
	if value := config.Discount(); math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
		return errors.New("upstream_discount must be between 0 and 1")
	}
	if config.BillingExpression != "" && config.PublicRatio != nil {
		return errors.New("public_ratio is calculated automatically when billing_expression is set")
	}
	if config.PublicRatio != nil && (*config.PublicRatio < 0 || math.IsNaN(*config.PublicRatio) || math.IsInf(*config.PublicRatio, 0)) {
		return errors.New("public_ratio must be finite and non-negative")
	}
	if config.TTFTMs != nil && *config.TTFTMs < 0 {
		return errors.New("ttft_ms must be non-negative")
	}
	if config.LastCheckedAt != nil && (*config.LastCheckedAt < 0 || *config.LastCheckedAt > 253402300799) {
		return errors.New("last_checked_at must be Unix seconds")
	}
	if config.BillingExpression != "" {
		if len(config.BillingExpression) > 16384 {
			return errors.New("billing expression is too long")
		}
		return billing_setting.SmokeTestExpr(config.BillingExpression)
	}
	return nil
}

func (channel *Channel) ModelConfig(name string) ChannelModelConfig {
	if config, exists := channel.ModelConfigs[name]; exists {
		return config
	}
	for _, candidate := range []string{hostreasoning.BaseModelName(name), ratio_setting.RoutingMatchModelName(name)} {
		if config, exists := channel.ModelConfigs[candidate]; exists {
			return config
		}
	}
	return ChannelModelConfig{}
}

func (channel *Channel) ValidateModelConfigs() error {
	for name, config := range channel.ModelConfigs {
		if name == "" || strings.TrimSpace(name) != name || len(name) > 255 || strings.Contains(name, ",") {
			return errors.New("model_configs requires exact public model names")
		}
		if err := config.Validate(); err != nil {
			return fmt.Errorf("model %s: %w", name, err)
		}
	}
	return nil
}

type ModelRatioRange struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// ModelPublicRatio is a current quotation, never a multiplier for expression
// billing. A nil result excludes the channel-model from dispatch.
func (config ChannelModelConfig) ModelPublicRatio(modelName string) *ModelRatioRange {
	return config.ModelServiceTierRatio(modelName, "")
}

// ModelServiceTierRatio compares channel and system prices for the same request
// service tier, at the base context length. It never changes settlement.
func (config ChannelModelConfig) ModelServiceTierRatio(modelName, serviceTier string) *ModelRatioRange {
	if config.BillingExpression == "" {
		ratio := config.ChargeRatio()
		if ratio < 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
			return nil
		}
		return &ModelRatioRange{Min: ratio, Max: ratio}
	}
	prices, ok := billingexpr.TokenPricesForServiceTier(config.BillingExpression, serviceTier)
	if !ok {
		return nil
	}
	baseline, ok := systemTokenPricesForServiceTier(modelName, serviceTier)
	if !ok {
		return nil
	}
	// Compare only categories explicitly present in either price contract, plus
	// input/output. Omitted subcategories follow the input/output unit price.
	categories := map[string]bool{"p": true, "c": true}
	for key := range prices {
		categories[key] = true
	}
	for key := range baseline {
		categories[key] = true
	}
	result := &ModelRatioRange{Min: math.Inf(1)}
	for key := range categories {
		anthropic := strings.Contains(strings.ToLower(modelName), "claude")
		quoted := effectiveTokenPrice(prices, key, anthropic)
		reference := effectiveTokenPrice(baseline, key, anthropic)
		// 0/0 has no informative ratio; a paid category without a reference cannot
		// be compared. Never silently omit that category and claim eligibility.
		if reference == 0 && quoted == 0 {
			continue
		}
		if reference <= 0 || quoted < 0 {
			return nil
		}
		ratio := quoted / reference
		if math.IsNaN(ratio) || math.IsInf(ratio, 0) {
			return nil
		}
		result.Min, result.Max = min(result.Min, ratio), max(result.Max, ratio)
	}
	if math.IsInf(result.Min, 0) {
		return nil
	}
	return result
}

func effectiveTokenPrice(prices map[string]float64, variable string, anthropic bool) float64 {
	if price, exists := prices[variable]; exists {
		return price
	}
	if variable == "cc1h" {
		// Missing one-hour cache-write pricing inherits the ordinary cache-write
		// price first, including an explicit zero; only a missing cc falls back
		// to the input price below.
		if price, exists := prices["cc"]; exists {
			return price
		}
	}
	if anthropic && (variable == "cc" || variable == "cc1h") {
		return 0
	}
	if variable == "img_cr" {
		// Without an explicit image-cache category, cached image tokens remain
		// within both image and cache totals in the existing token normalizer.
		return effectiveTokenPrice(prices, "cr", false) + effectiveTokenPrice(prices, "img", false) - prices["p"]
	}
	return prices[tokenPriceParent(variable)]
}

func tokenPriceParent(variable string) string {
	if variable == "c" || variable == "ao" || variable == "img_o" {
		return "c"
	}
	return "p"
}

func CompleteTokenPrices(prices map[string]float64) map[string]float64 {
	result := maps.Clone(prices)
	if _, exists := result["cc1h"]; !exists {
		if price, exists := result["cc"]; exists {
			result["cc1h"] = price
		} else if price, exists := result["p"]; exists {
			result["cc1h"] = price
		}
	}
	return result
}

// SystemTokenPrices uses the same model identity candidates and administrator
// overrides as relay pricing. It reads current settings, so changing system
// prices immediately changes quotations without rewriting channel expressions.
// SystemBillingExpression returns the configured native system expression when
// one exists. Legacy ratio pricing is represented by SystemTokenPrices instead.
func SystemBillingExpression(name string) string {
	candidates := append([]string{name}, hostreasoning.CanonicalBillingModelNames(name)...)
	candidates = append(candidates, hostreasoning.BaseModelName(name))
	for _, candidate := range slices.Compact(candidates) {
		if billing_setting.GetBillingMode(candidate) == billing_setting.BillingModeTieredExpr {
			if expression, found := billing_setting.GetBillingExpr(candidate); found {
				return expression
			}
		}
	}
	return ""
}

func SystemTokenPrices(name string) (map[string]float64, bool) {
	return systemTokenPricesForServiceTier(name, "")
}

func systemTokenPricesForServiceTier(name, serviceTier string) (map[string]float64, bool) {
	candidates := append([]string{name}, hostreasoning.CanonicalBillingModelNames(name)...)
	candidates = append(candidates, hostreasoning.BaseModelName(name))
	for _, candidate := range slices.Compact(candidates) {
		if billing_setting.GetBillingMode(candidate) == billing_setting.BillingModeTieredExpr {
			source, found := billing_setting.GetBillingExpr(candidate)
			if !found {
				return nil, false
			}
			return billingexpr.TokenPricesForServiceTier(source, serviceTier)
		}
		if _, fixed := ratio_setting.GetModelPrice(candidate, false); fixed {
			return nil, false
		}
		input, found, _ := ratio_setting.GetModelRatio(candidate)
		if !found {
			continue
		}
		input *= 1_000_000 / common.QuotaPerUnit
		cache, _ := ratio_setting.GetCacheRatio(candidate)
		write, hasWrite := ratio_setting.GetCreateCacheRatio(candidate)
		prices := map[string]float64{"p": input, "c": input * ratio_setting.GetCompletionRatio(candidate), "cr": input * cache}
		if hasWrite || strings.Contains(strings.ToLower(candidate), "claude") {
			prices["cc"] = input * write
			if strings.Contains(strings.ToLower(candidate), "claude") {
				prices["cc1h"] = input * write * 6 / 3.75
			}
		}
		image, imageConfigured := ratio_setting.GetImageRatio(candidate)
		if imageConfigured && image != 1 {
			prices["img"] = input * image
		}
		if ratio_setting.ContainsAudioRatio(candidate) || ratio_setting.ContainsAudioCompletionRatio(candidate) {
			prices["ai"] = input * ratio_setting.GetAudioRatio(candidate)
			prices["ao"] = prices["ai"] * ratio_setting.GetAudioCompletionRatio(candidate)
		}
		return prices, true
	}
	return nil, false
}

func (channel *Channel) ModelPricingRatios() map[string]*ModelRatioRange {
	result := make(map[string]*ModelRatioRange)
	for _, name := range channel.GetModels() {
		result[name] = channel.ModelConfig(name).ModelPublicRatio(name)
	}
	return result
}

func (channel *Channel) ResetModelObservations() {
	channel.ModelConfigs = maps.Clone(channel.ModelConfigs)
	for name, config := range channel.ModelConfigs {
		config.IsOnline, config.IsDegraded, config.TTFTMs, config.LastCheckedAt = nil, nil, nil, nil
		channel.ModelConfigs[name] = config
	}
}
