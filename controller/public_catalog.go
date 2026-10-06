package controller

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting"
	"github.com/gin-gonic/gin"
)

// parsePublicCatalogQuery rejects ambiguous/unknown inputs instead of silently
// dropping a filter. In particular, clients cannot request a private projection
// or sort by upstream pricing. Administrative query helpers are not reused.
func parsePublicCatalogQuery(c *gin.Context, channelList, modelOptions bool) (model.PublicCatalogQuery, error) {
	query := model.PublicCatalogQuery{}
	parameters, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return query, errors.New("invalid query string")
	}
	for key, values := range parameters {
		if len(values) != 1 || values[0] == "" {
			return query, errors.New("query parameters must have a single non-empty value")
		}
		value := values[0]
		switch key {
		case "p", "page_size", "provider_id":
			if key == "provider_id" && !channelList {
				return query, errors.New("provider_id is only supported for channel lists")
			}
			number, err := strconv.Atoi(value)
			if err != nil || number <= 0 {
				return query, errors.New("p, page_size and provider_id must be positive integers")
			}
			switch key {
			case "p":
				query.Page = number
			case "page_size":
				query.PageSize = number
			case "provider_id":
				query.ProviderID = number
			}
		case "model":
			query.Model = value
		case "search":
			if !modelOptions {
				return query, errors.New("search is only supported for model options")
			}
			query.Search = value
		case "is_online":
			query.IsOnline = value
		case "is_degraded":
			query.IsDegraded = value
		case "sort_by", "sort_order":
			if !channelList {
				return query, errors.New("sorting is only supported for channel lists")
			}
			if key == "sort_by" {
				query.SortBy = value
			} else {
				query.SortOrder = value
			}
		default:
			return query, errors.New("unsupported query parameter")
		}
	}
	if id := c.Param("id"); id != "" {
		providerID, err := strconv.Atoi(id)
		if err != nil || providerID <= 0 {
			return query, errors.New("provider ID must be a positive integer")
		}
		if query.ProviderID != 0 && query.ProviderID != providerID {
			return query, errors.New("provider_id must match the path ID")
		}
		query.ProviderID = providerID
	}
	if err := query.Normalize(); err != nil {
		return query, err
	}
	return query, nil
}

func ListPublicModels(c *gin.Context) {
	if _, set := c.GetQuery("model"); set {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "model filtering is only supported for channel and provider lists"})
		return
	}
	if _, set := c.GetQuery("sort_by"); set {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "sorting is not supported for model options"})
		return
	}
	if _, set := c.GetQuery("sort_order"); set {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "sorting is not supported for model options"})
		return
	}
	query, err := parsePublicCatalogQuery(c, true, true)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	items, total, err := model.ListPublicModels(query)
	if err != nil {
		common.SysError("failed to list public models: " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "public catalog is temporarily unavailable"})
		return
	}
	board := setting.DefaultHomepageBoardConfig()
	common.ApiSuccess(c, &struct {
		*common.PageInfo
		DefaultModel   string   `json:"default_model"`
		HomepageModels []string `json:"homepage_models"`
	}{
		PageInfo:       &common.PageInfo{Page: query.Page, PageSize: query.PageSize, Total: int(total), Items: items},
		DefaultModel:   board.DefaultModel,
		HomepageModels: board.Models,
	})
}

func GetPublicPlatformStatistics(c *gin.Context) {
	if c.Request.URL.RawQuery != "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "query parameters are not supported"})
		return
	}
	data, err := model.GetPublicPlatformStatistics(c.Request.Context(), time.Now())
	if err != nil {
		common.SysError("could not read public platform statistics: " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "platform statistics are temporarily unavailable"})
		return
	}
	common.ApiSuccess(c, data)
}

func GetPublicProviderHistory(c *gin.Context) {
	query := model.PublicHistoryQuery{}
	var err error
	query.ProviderID, err = strconv.Atoi(c.Param("id"))
	if err != nil || query.ProviderID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid provider ID"})
		return
	}
	parameters, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid query string"})
		return
	}
	for key, values := range parameters {
		if len(values) != 1 || values[0] == "" {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "query parameters must have one non-empty value"})
			return
		}
		switch key {
		case "days", "channel_id":
			value, parseErr := strconv.Atoi(values[0])
			if parseErr != nil || value <= 0 {
				c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "invalid days or channel ID"})
				return
			}
			if key == "days" {
				query.Days = value
			} else {
				query.ChannelID = value
			}
		case "model":
			query.Model = values[0]
		default:
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "unsupported query parameter"})
			return
		}
	}
	if err := query.Normalize(); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	history, err := model.ListPublicProviderHistory(c.Request.Context(), query, time.Now(), model.NewRelationalEvaluationHistoryStore(model.DB))
	if err != nil {
		common.SysError("failed to read public evaluation history: " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "evaluation history is temporarily unavailable"})
		return
	}
	common.ApiSuccess(c, history)
}

func ListPublicProviders(c *gin.Context) {
	query, err := parsePublicCatalogQuery(c, false, false)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	items, total, err := model.ListPublicProviders(query)
	if err != nil {
		common.SysError("failed to list public providers: " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "public catalog is temporarily unavailable"})
		return
	}
	common.ApiSuccess(c, &common.PageInfo{Page: query.Page, PageSize: query.PageSize, Total: int(total), Items: items})
}

// ListPublicChannels serves both the complete public list and a provider's
// filtered list. Missing/deleted providers naturally produce an empty page.
func ListPublicChannels(c *gin.Context) {
	query, err := parsePublicCatalogQuery(c, true, false)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	items, total, err := model.ListPublicChannels(query)
	if err != nil {
		common.SysError("failed to list public channels: " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "public catalog is temporarily unavailable"})
		return
	}
	common.ApiSuccess(c, &common.PageInfo{Page: query.Page, PageSize: query.PageSize, Total: int(total), Items: items})
}
