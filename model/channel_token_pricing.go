package model

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// ChannelTokenPrices are customer billing base prices in USD per million tokens,
// keyed by the channel's client-facing model name, never the mapped upstream ID.
// A record replaces system pricing. p/c are required; omitted categories remain
// in input/output, rather than inheriting unrelated system cache multipliers.
type ChannelTokenPrices map[string]map[string]*float64

const ChannelPricingSettingsContextKey = "channel_pricing_settings"

var channelTokenPriceVariables = []string{"p", "c", "cr", "cc", "cc1h", "img", "img_cr", "img_o", "ai", "ao"}

func (prices ChannelTokenPrices) Expression(modelName string) (string, bool, error) {
	values, found := prices[modelName]
	if !found {
		return "", false, nil
	}
	if values["p"] == nil || values["c"] == nil {
		return "", true, fmt.Errorf("model_token_prices[%s] requires input (p) and output (c) prices", modelName)
	}
	terms := make([]string, 0, len(values))
	for _, variable := range channelTokenPriceVariables {
		price, exists := values[variable]
		if !exists {
			continue
		}
		// Explicit zero is valid. Bound coefficients before expression evaluation;
		// quota conversion additionally bounds request reservations and settlement.
		if price == nil || math.IsNaN(*price) || math.IsInf(*price, 0) || *price < 0 || *price > 1_000_000_000 {
			return "", true, fmt.Errorf("model_token_prices[%s].%s must be between 0 and 1000000000 USD/1M tokens", modelName, variable)
		}
		terms = append(terms, variable+" * "+strconv.FormatFloat(*price, 'f', -1, 64))
	}
	if len(terms) != len(values) {
		return "", true, fmt.Errorf("model_token_prices[%s] contains an unknown token category", modelName)
	}
	// Anthropic reports cache writes outside prompt_tokens. The shared engine
	// only folds omitted cache reads into p, so keep explicit write terms even
	// when the administrator leaves them blank, at the base input price.
	for _, variable := range []string{"cc", "cc1h"} {
		if _, configured := values[variable]; !configured {
			terms = append(terms, variable+" * "+strconv.FormatFloat(*values["p"], 'f', -1, 64))
		}
	}
	return `tier("channel", ` + strings.Join(terms, " + ") + `)`, true, nil
}

func (channel *Channel) GetModelTokenPrices() (ChannelTokenPrices, error) {
	var settings struct {
		Prices ChannelTokenPrices `json:"model_token_prices"`
	}
	if channel.OtherSettings != "" {
		if err := common.UnmarshalJsonStr(channel.OtherSettings, &settings); err != nil {
			return nil, fmt.Errorf("invalid channel token pricing: %w", err)
		}
	}
	return settings.Prices, nil
}

// GetChannelPricedModels exposes only model names, not channel settings, to the
// authenticated model catalog. Batch queries avoid per-model database reads.
func GetChannelPricedModels(groups []string) (map[string]bool, error) {
	configured := make(map[string]bool)
	if len(groups) == 0 {
		return configured, nil
	}
	var abilities []Ability
	if err := DB.Select("model", "channel_id").Where(map[string]any{"group": groups, "enabled": true}).Find(&abilities).Error; err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(abilities))
	seenChannels := make(map[int]struct{})
	for _, ability := range abilities {
		if _, seen := seenChannels[ability.ChannelId]; seen {
			continue
		}
		seenChannels[ability.ChannelId] = struct{}{}
		ids = append(ids, ability.ChannelId)
	}
	var channels []Channel
	if len(ids) > 0 {
		if err := DB.Select("id", "settings").Where("id IN ? AND status = ?", ids, common.ChannelStatusEnabled).Find(&channels).Error; err != nil {
			return nil, err
		}
	}
	byChannel := make(map[int]ChannelTokenPrices, len(channels))
	for _, channel := range channels {
		prices, err := channel.GetModelTokenPrices()
		if err != nil {
			return nil, err
		}
		byChannel[channel.Id] = prices
	}
	for _, ability := range abilities {
		_, found, err := byChannel[ability.ChannelId].Expression(ability.Model)
		if found && err == nil {
			configured[ability.Model] = true
		}
	}
	return configured, nil
}

func (channel *Channel) ValidateModelTokenPrices() error {
	prices, err := channel.GetModelTokenPrices()
	if err != nil {
		return err
	}
	for name := range prices {
		if name == "" || strings.TrimSpace(name) != name {
			return fmt.Errorf("model_token_prices requires a non-empty, exact client-facing model name")
		}
		if _, _, err := prices.Expression(name); err != nil {
			return err
		}
	}
	return nil
}
