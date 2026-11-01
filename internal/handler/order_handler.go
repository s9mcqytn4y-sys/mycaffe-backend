package handler

import (
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/auth"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/midtrans"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/realtime"
)

type OrderHandler struct {
	cfg            *config.Config
	midtransClient *midtrans.Client
	realtimeHub    *realtime.Hub
}

func NewOrderHandler(cfg *config.Config, client *midtrans.Client, hub *realtime.Hub) *OrderHandler {
	return &OrderHandler{
		cfg:            cfg,
		midtransClient: client,
		realtimeHub:    hub,
	}
}

// HandleWebSocket upgrades connection for KDS / TV Queue display with cryptographic token authorization.
func (h *OrderHandler) HandleWebSocket(c *gin.Context) {
	// P0 Fail-Closed: Staging and production disable unvetted WebSocket endpoint until Phase 03 session ticket validation.
	if h.cfg.AppEnv == "production" || h.cfg.AppEnv == "staging" {
		reqID := uuid.NewString()
		log.Printf("[WS CONTAINMENT] request_id=%s env=%s ip=%s blocked WebSocket upgrade: session ticket validation not yet active", reqID, h.cfg.AppEnv, c.ClientIP())
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":      "Layanan pembaruan status langsung sedang dalam pemeliharaan.",
			"code":       "REALTIME_TEMPORARILY_UNAVAILABLE",
			"request_id": reqID,
		})
		return
	}

	// Development only: Authorization header. Access tokens are never accepted in the query
	// string (they would land in proxy logs, ADR-003 F3); Phase 03 adds a one-time WS ticket.
	tokenStr := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")

	if tokenStr == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized: missing authentication token or ticket"})
		return
	}

	claims, err := auth.ValidateToken(tokenStr, h.cfg.JWTSecret)
	if err != nil {
		log.Printf("[AUTH] WebSocket token validation failed for IP %s: %v", c.ClientIP(), err)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized: invalid or expired token"})
		return
	}

	// Multi-tenant check: if client also provided store_id query param, it MUST match claims.StoreID
	clientQueryStoreID := c.Query("store_id")
	if clientQueryStoreID != "" && !strings.EqualFold(clientQueryStoreID, claims.StoreID) {
		c.JSON(http.StatusForbidden, gin.H{"error": "Forbidden: store_id mismatch with authenticated token claims"})
		return
	}

	// Serve WebSocket with authenticated tenant StoreID derived exclusively from trusted claims
	h.realtimeHub.ServeWS(c.Writer, c.Request, claims.StoreID)
}
