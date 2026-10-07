package controller

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// PreviewChannelPricingImport returns a merged draft. No credentials are read or
// forwarded, and model membership changes only when the channel draft is saved.
func PreviewChannelPricingImport(c *gin.Context) {
	var request struct {
		Type             int                       `json:"type"`
		BaseURL          string                    `json:"base_url"`
		Group            string                    `json:"group"`
		Models           string                    `json:"models"`
		Configs          model.ChannelModelConfigs `json:"model_configs"`
		ManualMultiplier *float64                  `json:"manual_multiplier,omitempty"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 10<<20)
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid pricing import request"})
		return
	}
	if request.Type != constant.ChannelTypeNewAPI && request.Type != constant.ChannelTypeSub2API {
		common.ApiErrorMsg(c, "This channel type does not support pricing imports")
		return
	}
	if len(request.Configs) > 5000 || len(request.BaseURL) > 2048 || len(request.Models) > 1_280_000 {
		common.ApiErrorMsg(c, "Pricing import is too large")
		return
	}
	if err := (&model.Channel{ModelConfigs: request.Configs}).ValidateModelConfigs(); err != nil {
		common.ApiError(c, err)
		return
	}
	models := []string{}
	for name := range strings.SplitSeq(request.Models, ",") {
		if name = strings.TrimSpace(name); name != "" {
			models = append(models, name)
		}
	}
	var result *service.UpstreamPricingImport
	var err error
	if request.Type == constant.ChannelTypeSub2API {
		result, err = service.FetchSub2APIPrices(c.Request.Context(), request.BaseURL, request.Group, models, request.Configs, request.ManualMultiplier)
	} else {
		result, err = service.FetchNewAPIPrices(c.Request.Context(), request.BaseURL, request.Group, models, request.Configs)
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}
