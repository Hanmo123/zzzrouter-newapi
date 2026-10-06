package types

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

// RelayCharge freezes the selected model's charging factors and private cost.
// Only the successful attempt is settled; public logs omit procurement fields.
type RelayCharge struct {
	BaseGroupRatio     float64  `json:"base_group_ratio"`
	ChannelPublicRatio float64  `json:"channel_public_ratio"`
	ServiceFeeEnabled  bool     `json:"service_fee_enabled"`
	ServiceFeeRate     float64  `json:"service_fee_rate"`
	UpstreamDiscount   *float64 `json:"upstream_discount,omitempty"`
	ModelExpression    string   `json:"model_expression,omitempty"`
	// Quota before service fee/group, including manual public ratio once.
	BaseQuota *float64 `json:"base_quota,omitempty"`
}

func (p RelayCharge) EffectiveGroupRatio() float64 {
	ratio := p.ChannelPublicRatio
	if p.ServiceFeeEnabled {
		ratio *= 1 + p.ServiceFeeRate
	}
	return ratio * p.BaseGroupRatio
}

// CaptureBaseQuota records cost independently of the user's group, including a
// zero group. Callers supply an unrounded amount before all charging factors.
func (p *RelayCharge) CaptureBaseQuota(base float64) {
	if p == nil {
		return
	}
	value := base * p.ChannelPublicRatio
	p.BaseQuota = &value
}

func (p RelayCharge) CostQuota() (int, *common.QuotaClamp) {
	if p.BaseQuota == nil || p.UpstreamDiscount == nil {
		return 0, nil
	}
	return common.QuotaRoundChecked(*p.BaseQuota * *p.UpstreamDiscount)
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
