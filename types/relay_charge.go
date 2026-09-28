package types

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

// RelayCharge is a request/task billing snapshot. Upstream cost is deliberately
// absent. Only the successful attempt's public channel multiplier is charged.
type RelayCharge struct {
	BaseGroupRatio     float64 `json:"base_group_ratio"`
	ChannelPublicRatio float64 `json:"channel_public_ratio"`
	ServiceFeeEnabled  bool    `json:"service_fee_enabled"`
	ServiceFeeRate     float64 `json:"service_fee_rate"`
}

func (p RelayCharge) EffectiveGroupRatio() float64 {
	ratio := p.BaseGroupRatio * p.ChannelPublicRatio
	if p.ServiceFeeEnabled {
		ratio *= 1 + p.ServiceFeeRate
	}
	return ratio
}

// ServiceFeeQuota attributes the charged integer quota, not a second charge.
// Base + fee always equals the final charge, including sub-quota rounding.
func (p RelayCharge) ServiceFeeQuota(total int) int {
	if total <= 0 || !p.ServiceFeeEnabled || p.ServiceFeeRate <= 0 {
		return 0
	}
	base := common.QuotaFromDecimal(decimal.NewFromInt(int64(total)).Div(decimal.NewFromInt(1).Add(decimal.NewFromFloat(p.ServiceFeeRate))))
	return max(0, total-base)
}
