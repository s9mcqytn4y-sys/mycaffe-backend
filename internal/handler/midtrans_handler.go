package handler

import (
	"encoding/json"
	"io"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/midtrans"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/realtime"
)

type MidtransHandler struct {
	midtransClient *midtrans.Client
	realtimeHub    *realtime.Hub
}

func NewMidtransHandler(client *midtrans.Client, hub *realtime.Hub) *MidtransHandler {
	return &MidtransHandler{
		midtransClient: client,
		realtimeHub:    hub,
	}
}

type MidtransNotificationPayload struct {
	TransactionTime   string `json:"transaction_time"`
	TransactionStatus string `json:"transaction_status"`
	TransactionID     string `json:"transaction_id"`
	StatusMessage     string `json:"status_message"`
	StatusCode        string `json:"status_code"`
	SignatureKey      string `json:"signature_key"`
	PaymentType       string `json:"payment_type"`
	OrderID           string `json:"order_id"`
	GrossAmount       string `json:"gross_amount"`
	FraudStatus       string `json:"fraud_status"`
	Currency          string `json:"currency"`
}

// HandleWebhook processes incoming Midtrans payment notifications
// @Summary Midtrans Webhook Callback
// @Description Secure, idempotent webhook callback endpoint for Midtrans Core API
// @Tags Payments
// @Accept json
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} map[string]interface{}
// @Failure 401 {object} map[string]interface{}
// @Router /api/v1/payments/midtrans/webhook [post]
func (h *MidtransHandler) HandleWebhook(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unable to read payload"})
		return
	}

	var notif MidtransNotificationPayload
	if err := json.Unmarshal(body, &notif); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json payload"})
		return
	}

	// 1. Verify Midtrans signature: SHA512(order_id + status_code + gross_amount + server_key)
	isValid := h.midtransClient.VerifySignature(
		notif.OrderID,
		notif.StatusCode,
		notif.GrossAmount,
		notif.SignatureKey,
	)

	if !isValid {
		log.Printf("[SECURITY] Invalid Midtrans signature received for order: %s", notif.OrderID)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid signature key"})
		return
	}

	// 2. Validate Order UUID
	orderUUID, err := uuid.Parse(notif.OrderID)
	if err != nil {
		log.Printf("[WARN] Non-UUID order_id from Midtrans: %s", notif.OrderID)
	}

	// 3. Map status
	paymentStatus := midtrans.MapMidtransStatus(notif.TransactionStatus, notif.FraudStatus)
	log.Printf("[PAYMENT] Order %s status updated: %s (payment_status: %s)", notif.OrderID, notif.TransactionStatus, paymentStatus)

	// 4. Broadcast realtime event to KDS / TV display if paid
	if paymentStatus == domain.PaymentStatusPaid {
		h.realtimeHub.BroadcastEvent("payment.paid", "", gin.H{
			"order_id":       orderUUID,
			"transaction_id": notif.TransactionID,
			"amount":         notif.GrossAmount,
			"payment_type":   notif.PaymentType,
		})

		h.realtimeHub.BroadcastEvent("kds.order_queued", "", gin.H{
			"order_id": orderUUID,
			"status":   domain.OrderStatusQueued,
		})
	}

	// 5. Always return 200 OK to acknowledge receipt to Midtrans
	c.JSON(http.StatusOK, gin.H{
		"status":  "success",
		"message": "notification processed",
	})
}
