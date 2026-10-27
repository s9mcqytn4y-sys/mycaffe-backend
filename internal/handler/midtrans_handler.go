package handler

import (
	"encoding/json"
	"io"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/midtrans"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/realtime"
)

const MaxWebhookBodyBytes = 64 * 1024 // 64 KB maximum payload size

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
	TransactionStatus string `json:"transaction_status" binding:"required"`
	TransactionID     string `json:"transaction_id"`
	StatusMessage     string `json:"status_message"`
	StatusCode        string `json:"status_code" binding:"required"`
	SignatureKey      string `json:"signature_key" binding:"required"`
	PaymentType       string `json:"payment_type"`
	OrderID           string `json:"order_id" binding:"required"`
	GrossAmount       string `json:"gross_amount" binding:"required"`
	FraudStatus       string `json:"fraud_status"`
	Currency          string `json:"currency"`
}

// HandleWebhook processes incoming Midtrans payment notifications
// @Summary Midtrans Webhook Callback
// @Description Secure webhook endpoint for Midtrans Core API. Fails-closed with 503 until durable DB persistence repository is active.
// @Tags Payments
// @Accept json
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Failure 400 {object} map[string]interface{}
// @Failure 401 {object} map[string]interface{}
// @Failure 413 {object} map[string]interface{}
// @Failure 503 {object} map[string]interface{}
// @Router /api/v1/payments/midtrans/webhook [post]
func (h *MidtransHandler) HandleWebhook(c *gin.Context) {
	// 1. Strict maximum body size limit to prevent resource exhaustion / DoS
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, MaxWebhookBodyBytes)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "payload too large or unreadable"})
		return
	}

	var notif MidtransNotificationPayload
	if err := json.Unmarshal(body, &notif); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json payload"})
		return
	}

	// 2. Validate mandatory notification fields
	if notif.OrderID == "" || notif.StatusCode == "" || notif.GrossAmount == "" || notif.SignatureKey == "" || notif.TransactionStatus == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing required notification fields"})
		return
	}

	// 3. Validate Order ID format (must be valid UUID)
	orderUUID, err := uuid.Parse(notif.OrderID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "malformed order_id: must be a valid UUID"})
		return
	}

	// 4. Verify Midtrans signature: SHA512(order_id + status_code + gross_amount + server_key)
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

	// 5. P0 Fail-Closed / Durable Storage Enforcement:
	// The database repository is not yet connected to execute ACID transaction updates on payments & orders.
	// Returning HTTP 200 would falsely acknowledge receipt and lose state permanently.
	// We return HTTP 503 Service Unavailable with a Retry-After header so Midtrans gateway safely retries delivery.
	log.Printf("[WEBHOOK AUDIT] Valid signature for order %s, status: %s. Durable DB storage uncommitted.", orderUUID, notif.TransactionStatus)

	c.Header("Retry-After", "60")
	c.JSON(http.StatusServiceUnavailable, gin.H{
		"status":  "deferred",
		"message": "Signature verified, but durable payment storage transaction is uncommitted. Returning 503 to trigger gateway retry.",
	})
}
