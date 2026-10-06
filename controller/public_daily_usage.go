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

func parseDailyUsageQuery(c *gin.Context, defaultDays int, managed bool) (int, time.Time, error) {
	parameters, err := url.ParseQuery(c.Request.URL.RawQuery)
	end := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
	if err != nil {
		return 0, end, errors.New("invalid query string")
	}
	days := defaultDays
	for key, values := range parameters {
		if len(values) != 1 || values[0] == "" {
			return 0, end, errors.New("query parameters must be non-empty and unique")
		}
		switch {
		case key == "days":
			days, err = strconv.Atoi(values[0])
			if err != nil {
				return 0, end, errors.New("invalid days")
			}
		case key == "to" && managed:
			if err := model.ValidateDailyUsageDate(values[0]); err != nil {
				return 0, end, err
			}
			end, err = time.Parse("2006-01-02", values[0])
			if err != nil {
				return 0, end, err
			}
		default:
			return 0, end, errors.New("unsupported query parameter")
		}
	}
	if days != 7 && days != 30 && days != 90 && days != 365 {
		return 0, end, errors.New("days must be 7, 30, 90 or 365")
	}
	return days, end, nil
}

func GetPublicDailyUsage(c *gin.Context) {
	days, _, err := parseDailyUsageQuery(c, 365, false)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	data, err := model.ListPublicConsumptionDailyUsage(c.Request.Context(), days, time.Now())
	if err != nil {
		common.SysError("could not read public daily usage: " + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "daily usage is temporarily unavailable"})
		return
	}
	common.ApiSuccess(c, data)
}

func GetManagedDailyUsage(c *gin.Context) {
	days, end, err := parseDailyUsageQuery(c, 30, true)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	data, err := model.ListDailyUsageEnding(c.Request.Context(), days, end)
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
	err := model.DeletePublicDailyUsage(c.Request.Context(), c.Param("day"))
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
