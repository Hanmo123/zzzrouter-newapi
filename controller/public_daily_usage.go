package controller

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func parseDailyUsageDays(c *gin.Context, defaultDays int) (int, error) {
	parameters, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		return 0, errors.New("invalid query string")
	}
	days := defaultDays
	for key, values := range parameters {
		if key != "days" || len(values) != 1 || values[0] == "" {
			return 0, errors.New("only one non-empty days parameter is supported")
		}
		days, err = strconv.Atoi(values[0])
		if err != nil {
			return 0, errors.New("invalid days")
		}
	}
	if days != 7 && days != 30 && days != 90 && days != 365 {
		return 0, errors.New("days must be 7, 30, 90 or 365")
	}
	return days, nil
}

func GetPublicDailyUsage(c *gin.Context) {
	days, err := parseDailyUsageDays(c, 365)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	data, err := model.ListPublicDailyUsage(c.Request.Context(), days, false, time.Now())
	if err != nil {
		common.SysError("could not read public daily usage: " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "daily usage is temporarily unavailable"})
		return
	}
	common.ApiSuccess(c, data)
}

func GetManagedDailyUsage(c *gin.Context) {
	days, err := parseDailyUsageDays(c, 30)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	data, err := model.ListPublicDailyUsage(c.Request.Context(), days, false, time.Now())
	if err != nil {
		common.SysError("could not read managed daily usage: " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "daily usage is temporarily unavailable"})
		return
	}
	common.ApiSuccess(c, data)
}

func SetManagedDailyUsage(c *gin.Context) {
	if c.Request.URL.RawQuery != "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "query parameters are not supported"})
		return
	}
	var request struct {
		Items []model.DailyUsageInput `json:"items"`
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64*1024)
	body, readErr := io.ReadAll(c.Request.Body)
	if readErr != nil || common.Unmarshal(body, &request) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "provide dates and integer token counts"})
		return
	}
	err := model.SetPublicDailyUsage(c.Request.Context(), request.Items, c.GetInt("id"), time.Now())
	if errors.Is(err, model.ErrInvalidDailyUsage) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	if err != nil {
		common.SysError("could not save daily usage: " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "daily usage could not be saved"})
		return
	}
	common.ApiSuccess(c, gin.H{"updated": len(request.Items)})
}

func DeleteManagedDailyUsage(c *gin.Context) {
	if c.Request.URL.RawQuery != "" {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "query parameters are not supported"})
		return
	}
	err := model.DeletePublicDailyUsage(c.Request.Context(), c.Param("day"), time.Now())
	switch {
	case errors.Is(err, model.ErrInvalidDailyUsage):
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "daily usage record not found"})
	case err != nil:
		common.SysError("could not delete daily usage: " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "daily usage could not be deleted"})
	default:
		common.ApiSuccess(c, nil)
	}
}
