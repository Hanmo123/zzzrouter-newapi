package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAPIOnlyDoesNotServeOrRedirectFrontend(t *testing.T) {
	t.Setenv("FRONTEND_BASE_URL", "https://frontend.example")
	previousMaster := common.IsMasterNode
	t.Cleanup(func() { common.IsMasterNode = previousMaster })
	for _, master := range []bool{true, false} {
		common.IsMasterNode = master
		engine := gin.New()
		SetRouter(engine, WebAssets{APIOnly: true, IndexPage: []byte("must not be served")})
		for _, path := range []string{"/", "/channels", "/static/js/missing.js", "/api/missing", "/v1/missing"} {
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			require.Equal(t, http.StatusNotFound, response.Code, path)
			assert.Empty(t, response.Header().Get("Location"))
			assert.Contains(t, response.Header().Get("Content-Type"), "application/json")
			assert.Contains(t, response.Header().Get("Cache-Control"), "no-store")
			assert.NotContains(t, response.Body.String(), "must not be served")
		}
	}
}

func TestAPIOnlyPreservesRegisteredAndDynamicRoutes(t *testing.T) {
	engine := gin.New()
	engine.GET("/api/example", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"success": true}) })
	SetWebRouter(engine, WebAssets{APIOnly: true}, func(c *gin.Context) {
		if c.Request.URL.Path == "/dynamic-endpoint" {
			c.JSON(http.StatusAccepted, gin.H{"success": true})
			c.Abort()
			return
		}
		c.Next()
	})
	for path, code := range map[string]int{"/api/example": http.StatusOK, "/dynamic-endpoint": http.StatusAccepted} {
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, code, response.Code)
		assert.JSONEq(t, `{"success":true}`, response.Body.String())
	}
}
