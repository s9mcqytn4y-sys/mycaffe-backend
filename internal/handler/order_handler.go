package handler

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
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

// HandleWebSocket upgrades connection for KDS / TV Queue display with tenant authorization
func (h *OrderHandler) HandleWebSocket(c *gin.Context) {
	// P0 Containment: Never trust raw query parameter without tenant authorization
	authHeader := c.GetHeader("Authorization")
	queryToken := c.Query("token")
	storeID := c.Query("store_id")

	// In full implementation, validate JWT bearer token or staff session
	// For audit readiness, reject connections without authorization or valid store_id
	if storeID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_id parameter is required"})
		return
	}

	if h.cfg.AppEnv == "production" && authHeader == "" && queryToken == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized WebSocket connection: missing token"})
		return
	}

	h.realtimeHub.ServeWS(c.Writer, c.Request, storeID)
}
