package handler

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

type HealthHandler struct {
	appName string
	appEnv  string
}

func NewHealthHandler(appName, appEnv string) *HealthHandler {
	return &HealthHandler{
		appName: appName,
		appEnv:  appEnv,
	}
}

// Check returns server health status
// @Summary System Health Check
// @Description Returns current status and uptime of the backend service
// @Tags System
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Router /api/v1/health [get]
func (h *HealthHandler) Check(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":    "healthy",
		"app":       h.appName,
		"env":       h.appEnv,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}
