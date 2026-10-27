package handler

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
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

type CreateOrderItemRequest struct {
	ProductID uuid.UUID `json:"product_id" binding:"required"`
	Quantity  int       `json:"quantity" binding:"required,min=1"`
	Notes     string    `json:"notes"`
}

type CreateOrderRequest struct {
	StoreID       uuid.UUID                `json:"store_id" binding:"required"`
	TableNumber   string                   `json:"table_number"`
	CustomerName  string                   `json:"customer_name" binding:"required"`
	CustomerPhone string                   `json:"customer_phone"`
	OrderType     domain.OrderType         `json:"order_type" binding:"required"`
	PaymentMethod domain.PaymentMethod     `json:"payment_method" binding:"required"`
	Bank          string                   `json:"bank,omitempty"`
	Items         []CreateOrderItemRequest `json:"items" binding:"required,min=1"`
}

// CreateOrder handles customer order creation and initiates payment
// @Summary Create Customer Order & Initiate Core API Payment
// @Description Enforces server-side recalculation and fails-closed in production until database persistence repository is connected.
// @Tags Orders
// @Accept json
// @Produce json
// @Param order body CreateOrderRequest true "Order Request"
// @Success 201 {object} map[string]interface{}
// @Failure 400 {object} map[string]interface{}
// @Failure 503 {object} map[string]interface{}
// @Router /api/v1/orders [post]
func (h *OrderHandler) CreateOrder(c *gin.Context) {
	// P0 Containment: Production fails closed until database repository is connected
	if h.cfg.AppEnv == "production" {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "[P0 CONTAINMENT] Order persistence and verified pricing repository are currently being initialized. Live payment transactions are blocked.",
		})
		return
	}

	var req CreateOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	orderID := uuid.New()
	orderNumber := fmt.Sprintf("ORD-%s-%s", time.Now().Format("20060102"), orderID.String()[:8])
	queueNumber := fmt.Sprintf("A%s", orderID.String()[:3])

	// In development simulation:
	var subtotal int64 = 25000
	var taxRate int64 = 10
	var taxAmount int64 = (subtotal * taxRate) / 100
	var totalAmount int64 = subtotal + taxAmount

	order := &domain.Order{
		ID:                  orderID,
		StoreID:             req.StoreID,
		OrderNumber:         orderNumber,
		QueueNumber:         queueNumber,
		TableNumber:         req.TableNumber,
		CustomerName:        req.CustomerName,
		CustomerPhone:       req.CustomerPhone,
		OrderType:           req.OrderType,
		Status:              domain.OrderStatusPlaced,
		PaymentStatus:       domain.PaymentStatusPending,
		SubtotalAmount:      subtotal,
		DiscountAmount:      0,
		TaxAmount:           taxAmount,
		ServiceChargeAmount: 0,
		TotalAmount:         totalAmount,
		CreatedAt:           time.Now(),
		UpdatedAt:           time.Now(),
	}

	// P0 Containment: In development without DB persistence, do not call Midtrans Charge to avoid orphan charges
	c.JSON(http.StatusCreated, gin.H{
		"message":      "Order draft created (Simulation Mode - DB persistence pending)",
		"order":        order,
		"status":       "draft",
		"payment_mode": "mock_preview",
	})
}

// HandleWebSocket upgrades connection for KDS / TV Queue display with cryptographic token authorization
func (h *OrderHandler) HandleWebSocket(c *gin.Context) {
	// P0 Fail-Closed: Production disables unauthenticated/unvetted WebSocket endpoint
	if h.cfg.AppEnv == "production" {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error": "[P0 CONTAINMENT] Production WebSocket endpoint is currently disabled until staff authentication and session ticket validation are deployed in Phase 03.",
		})
		return
	}

	// Extract token from Authorization header or short-lived query ticket
	var tokenStr string
	authHeader := c.GetHeader("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		tokenStr = strings.TrimPrefix(authHeader, "Bearer ")
	} else {
		tokenStr = c.Query("token")
	}

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
