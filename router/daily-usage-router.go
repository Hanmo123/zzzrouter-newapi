package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-gonic/gin"
)

// Registered beneath the existing RootAuth-protected /api/option group.
func registerDailyUsageManagementRoutes(options *gin.RouterGroup) {
	usage := options.Group("/daily_usage", middleware.DisableCache())
	usage.GET("", controller.GetManagedDailyUsage)
	usage.PUT("", controller.SetManagedDailyUsage)
	usage.DELETE("/:day", controller.DeleteManagedDailyUsage)
}
