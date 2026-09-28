package controller

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func GetChannelDegradationExclusion(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiError(c, errors.New("invalid channel ID"))
		return
	}
	var row model.Channel
	if err := model.DB.Select("id", "skip_degradation_check").First(&row, id).Error; err != nil {
		common.ApiError(c, errors.New("channel not found"))
		return
	}
	common.ApiSuccess(c, gin.H{"skip_degradation_check": row.SkipDegradationCheck})
}

func SetChannelDegradationExclusion(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiError(c, errors.New("invalid channel ID"))
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 2048)
	var body struct {
		Skip *bool `json:"skip_degradation_check"`
	}
	if err := common.DecodeJson(c.Request.Body, &body); err != nil || body.Skip == nil {
		common.ApiError(c, errors.New("skip_degradation_check must be a boolean"))
		return
	}
	if err := model.SetChannelDegradationExcluded(id, *body.Skip); err != nil {
		common.ApiError(c, errors.New("could not update channel degradation policy"))
		return
	}
	recordManageAudit(c, "channel.degradation_exclusion", map[string]any{"channel_id": id, "skip_degradation_check": *body.Skip})
	common.ApiSuccess(c, gin.H{"skip_degradation_check": *body.Skip})
}
