package controller

import (
	"errors"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func ListProviders(c *gin.Context) {
	page := common.GetPageQuery(c)
	providers, total, err := model.ListProviders(c.Query("keyword"), page.GetStartIdx(), page.GetPageSize())
	if err != nil {
		common.ApiError(c, err)
		return
	}
	page.SetTotal(int(total))
	page.SetItems(providers)
	common.ApiSuccess(c, page)
}

func SaveProvider(c *gin.Context) {
	// Accept only mutable metadata; IDs, timestamps and deletion state cannot
	// be injected through the request body.
	var request struct {
		Name                 string `json:"name"`
		Description          string `json:"description"`
		SkipDegradationCheck *bool  `json:"skip_degradation_check"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil {
		common.ApiError(c, err)
		return
	}
	provider := model.Provider{Name: request.Name, Description: request.Description}
	if request.SkipDegradationCheck != nil {
		provider.SkipDegradationCheck = *request.SkipDegradationCheck
	}
	if c.Request.Method == "PUT" {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil || id <= 0 {
			common.ApiError(c, errors.New("invalid provider ID"))
			return
		}
		provider.ID = id
	}
	if err := provider.Save(request.SkipDegradationCheck); err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAudit(c, "provider.save", map[string]any{"provider_id": provider.ID, "name": provider.Name, "skip_degradation_check": provider.SkipDegradationCheck})
	common.ApiSuccess(c, provider)
}

func DeleteProvider(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiError(c, errors.New("invalid provider ID"))
		return
	}
	if err := model.DeleteProvider(id); err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAudit(c, "provider.delete", map[string]any{"provider_id": id})
	common.ApiSuccess(c, nil)
}

// channelProviderFilter deliberately rejects malformed filters instead of
// silently returning all providers' channels.
func channelProviderFilter(c *gin.Context) (int, error) {
	value, present := c.GetQuery("provider_id")
	if !present {
		return 0, nil
	}
	id, err := strconv.Atoi(value)
	if err != nil || id <= 0 {
		return 0, errors.New("provider_id must be a positive integer")
	}
	return id, nil
}
