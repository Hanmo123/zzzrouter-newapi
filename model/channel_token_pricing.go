package model

import "github.com/QuantumNous/new-api/common"

// GetChannelPricedModels exposes only model names to the authenticated catalog.
// Batch queries avoid per-model database reads; incomparable expressions never
// make an otherwise unpriced model available.
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
	seen := make(map[int]bool)
	for _, ability := range abilities {
		if !seen[ability.ChannelId] {
			seen[ability.ChannelId] = true
			ids = append(ids, ability.ChannelId)
		}
	}
	var channels []Channel
	if len(ids) > 0 {
		if err := DB.Select("id", "model_configs").Where("id IN ? AND status = ?", ids, common.ChannelStatusEnabled).Find(&channels).Error; err != nil {
			return nil, err
		}
	}
	byChannel := make(map[int]*Channel, len(channels))
	for i := range channels {
		byChannel[channels[i].Id] = &channels[i]
	}
	for _, ability := range abilities {
		ch := byChannel[ability.ChannelId]
		if ch == nil {
			continue
		}
		cfg := ch.ModelConfig(ability.Model)
		if cfg.BillingExpression != "" && cfg.ModelPublicRatio(ability.Model) != nil {
			configured[ability.Model] = true
		}
	}
	return configured, nil
}
