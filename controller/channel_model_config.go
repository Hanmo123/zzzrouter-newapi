package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/gin-gonic/gin"
	"net/http"
)

// PreviewChannelModelConfig is read-only and shares the routing quotation.
// Uncomparable (but valid) expressions may be saved, but cannot be dispatched.
func PreviewChannelModelConfig(c *gin.Context) {
	var request struct {
		Model  string                   `json:"model"`
		Config model.ChannelModelConfig `json:"config"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiError(c, err)
		return
	}
	channel := model.Channel{ModelConfigs: model.ChannelModelConfigs{request.Model: request.Config}}
	if err := channel.ValidateModelConfigs(); err != nil {
		common.ApiError(c, err)
		return
	}
	ratio := request.Config.ModelPublicRatio(request.Model)
	upstreamPrices := map[string]float64(nil)
	upstreamExpression := ""
	if source := request.Config.PricingSource; source != nil {
		upstreamExpression = source.Expression
		upstreamPrices, _ = billingexpr.BaseTokenPrices(source.Expression)
		upstreamPrices = model.CompleteTokenPrices(upstreamPrices)
	}
	systemPrices, _ := model.SystemTokenPrices(request.Model)
	systemPrices = model.CompleteTokenPrices(systemPrices)
	channelPrices := map[string]float64(nil)
	if request.Config.BillingExpression != "" {
		channelPrices, _ = billingexpr.BaseTokenPrices(request.Config.BillingExpression)
		channelPrices = model.CompleteTokenPrices(channelPrices)
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{
		"public_ratio":        ratio,
		"schedulable":         ratio != nil,
		"system_prices":       systemPrices,
		"upstream_prices":     upstreamPrices,
		"channel_prices":      channelPrices,
		"system_expression":   model.SystemBillingExpression(request.Model),
		"upstream_expression": upstreamExpression,
		"channel_expression":  request.Config.BillingExpression,
		"upstream_group": func() string {
			if request.Config.PricingSource != nil {
				return request.Config.PricingSource.Group
			}
			return ""
		}(),
	}})
}
