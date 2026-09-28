package model

import (
	"math"
	"math/rand/v2"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
)

// GetFloorChannel keeps the existing group/model/path eligibility boundary and
// replaces only priority ordering. Private cost never appears in an error.
func GetFloorChannel(group, modelName string, filters []dto.ChannelFilter) (*Channel, error) {
	var candidates []*Channel
	names := []string{modelName}
	if normalized := ratio_setting.RoutingMatchModelName(modelName); normalized != modelName {
		names = append(names, normalized)
	}
	if common.MemoryCacheEnabled {
		channelSyncLock.RLock()
		defer channelSyncLock.RUnlock()
		for _, name := range names {
			ids, _ := filterCandidateIDs(group2model2channels[group][name], modelName, filters)
			for _, id := range ids {
				if ch := channelsIDM[id]; ch != nil && ch.Status == common.ChannelStatusEnabled {
					candidates = append(candidates, ch)
				}
			}
			if len(candidates) > 0 {
				break
			}
		}
	} else {
		for _, name := range names {
			ids := DB.Model(&Ability{}).Select("channel_id").Where(commonGroupCol+" = ? AND model = ? AND enabled = ?", group, name, true)
			var channels []*Channel
			if err := DB.Where("id IN (?) AND status = ?", ids, common.ChannelStatusEnabled).Find(&channels).Error; err != nil {
				return nil, err
			}
			for _, ch := range channels {
				if ok, _ := ChannelSatisfiesFilters(ch, modelName, filters); ok {
					candidates = append(candidates, ch)
				}
			}
			if len(candidates) > 0 {
				break
			}
		}
	}
	return chooseFloorChannel(candidates), nil
}

func chooseFloorChannel(candidates []*Channel) *Channel {
	floor := math.Inf(1)
	var tier []*Channel
	for _, ch := range candidates {
		if ch.UpstreamRatio == nil {
			continue
		}
		cost := *ch.UpstreamRatio
		if cost <= 0 || math.IsNaN(cost) || math.IsInf(cost, 0) || cost > floor {
			continue
		}
		if cost < floor {
			floor = cost
			tier = nil
		}
		tier = append(tier, ch)
	}
	if len(tier) == 0 {
		return nil
	}
	// Equal-cost channels retain weighted choice. Zero weights are excluded when
	// any positive weight exists; an all-zero tier retains uniform fallback.
	total := 0.0
	for _, ch := range tier {
		total += float64(ch.GetWeight())
	}
	if total == 0 {
		return tier[rand.IntN(len(tier))]
	}
	target := rand.Float64() * total
	for _, ch := range tier {
		target -= float64(ch.GetWeight())
		if target < 0 {
			return ch
		}
	}
	return tier[len(tier)-1]
}
