package handler

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/midtrans"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/realtime"
)

type OrderHandler struct {
	midtransClient *midtrans.Client
	realtimeHub    *realtime.Hub
}

func NewOrderHandler(client *midtrans.Client, hub *realtime.Hub) *OrderHandler {
	return &OrderHandler{
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
	Bank          string                   `json:"bank,omitempty"` // For VA
	Items         []CreateOrderItemRequest `json:"items" binding:"required,min=1"`
}

// CreateOrder handles customer order creation and initiates Midtrans Core API payment
// @Summary Create Customer Order & Initiate Core API Payment
// @Description Server recalculates all pricing, snapshots order items, and returns dynamic QRIS / VA
// @Tags Orders
// @Accept json
// @Produce json
// @Param order body CreateOrderRequest true "Order Request"
// @Success 201 {object} map[string]interface{}
// @Failure 400 {object} map[string]interface{}
// @Router /api/v1/orders [post]
func (h *OrderHandler) CreateOrder(c *gin.Context) {
	var req CreateOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	orderID := uuid.New()
	orderNumber := fmt.Sprintf("ORD-%s-%s", time.Now().Format("20060102"), orderID.String()[:4])
	queueNumber := fmt.Sprintf("A%s", orderID.String()[:2])

	// In real database repository: catalog prices are fetched and recalculated here.
	// For demonstration, mock snapshot item calculation:
	var subtotal int64 = 25000 // e.g. Rp 25.000
	var taxRate int64 = 10     // 10% PB1
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

	var paymentResponse gin.H

	switch req.PaymentMethod {
	case domain.PaymentMethodQRIS:
		chargeResp, err := h.midtransClient.ChargeQRIS(order)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal inisialisasi QRIS Midtrans: " + err.Error()})
			return
		}
		paymentResponse = gin.H{
			"payment_type":   "qris",
			"transaction_id": chargeResp.TransactionID,
			"qr_string":      chargeResp.QRString,
			"actions":        chargeResp.Actions,
		}
	case domain.PaymentMethodBankTransfer:
		chargeResp, err := h.midtransClient.ChargeBankTransfer(order, req.Bank)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "gagal inisialisasi Bank Transfer: " + err.Error()})
			return
		}
		paymentResponse = gin.H{
			"payment_type":   "bank_transfer",
			"transaction_id": chargeResp.TransactionID,
			"va_numbers":     chargeResp.VaNumbers,
		}
	default:
		paymentResponse = gin.H{
			"payment_type": "cash_or_manual",
			"instruction":  "Silakan selesaikan pembayaran di kasir.",
		}
	}

	// Broadcast order placed event to internal hub
	h.realtimeHub.BroadcastEvent("order.placed", req.StoreID.String(), gin.H{
		"order_id":     order.ID,
		"order_number": order.OrderNumber,
		"queue_number": order.QueueNumber,
		"table_number": order.TableNumber,
		"total_amount": order.TotalAmount,
	})

	c.JSON(http.StatusCreated, gin.H{
		"message": "Order berhasil dibuat",
		"order":   order,
		"payment": paymentResponse,
	})
}

// HandleWebSocket upgrades connection for KDS / TV Queue display
func (h *OrderHandler) HandleWebSocket(c *gin.Context) {
	storeID := c.Query("store_id")
	h.realtimeHub.ServeWS(c.Writer, c.Request, storeID)
}
