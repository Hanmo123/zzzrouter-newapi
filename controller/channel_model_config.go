package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
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
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"public_ratio": ratio, "schedulable": ratio != nil}})
}
