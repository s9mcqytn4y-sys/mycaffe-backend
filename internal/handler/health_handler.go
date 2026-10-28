package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
)

type HealthHandler struct {
	appName string
	appEnv  string
	db      *sqlx.DB
}

func NewHealthHandler(appName, appEnv string, db ...*sqlx.DB) *HealthHandler {
	var dbConn *sqlx.DB
	if len(db) > 0 {
		dbConn = db[0]
	}
	return &HealthHandler{
		appName: appName,
		appEnv:  appEnv,
		db:      dbConn,
	}
}

// Liveness returns 200 OK if the HTTP process is responsive (/health/live).
func (h *HealthHandler) Liveness(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":    "alive",
		"app":       h.appName,
		"env":       h.appEnv,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}

// Check returns server readiness status including PostgreSQL connectivity when configured (/health, /health/ready, /api/v1/health).
// @Summary System Readiness & Health Check
// @Description Returns current status and database readiness of the backend service
// @Tags System
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Failure 503 {object} map[string]interface{}
// @Router /api/v1/health [get]
func (h *HealthHandler) Check(c *gin.Context) {
	dbStatus := "not_configured"
	if h.db != nil {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if err := h.db.PingContext(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status":    "unhealthy",
				"app":       h.appName,
				"env":       h.appEnv,
				"database":  "unreachable",
				"timestamp": time.Now().UTC().Format(time.RFC3339),
			})
			return
		}
		dbStatus = "connected"
	} else if h.appEnv == "production" || h.appEnv == "staging" {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status":    "unhealthy",
			"app":       h.appName,
			"env":       h.appEnv,
			"database":  "missing_connection",
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":    "healthy",
		"app":       h.appName,
		"env":       h.appEnv,
		"database":  dbStatus,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}
