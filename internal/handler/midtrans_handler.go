package handler

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/midtrans"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/realtime"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

const MaxWebhookBodyBytes = 64 * 1024 // 64 KB maximum payload size

type MidtransHandler struct {
	midtransClient    *midtrans.Client
	realtimeHub       *realtime.Hub
	paymentService    *service.PaymentService
	governanceService *service.GovernanceService
}

func NewMidtransHandler(client *midtrans.Client, hub *realtime.Hub) *MidtransHandler {
	return &MidtransHandler{
		midtransClient: client,
		realtimeHub:    hub,
	}
}

func (h *MidtransHandler) SetPaymentService(paymentService *service.PaymentService) {
	h.paymentService = paymentService
}

func (h *MidtransHandler) SetGovernanceService(governanceService *service.GovernanceService) {
	h.governanceService = governanceService
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

// HandleWebhook processes incoming Midtrans payment notifications.
// @Summary Midtrans Webhook Callback
// @Description Secure webhook endpoint for Midtrans Core API. Verifies SHA-512 signature, validates IDR amount/status, and durably reconciles the payment ledger and order state.
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
	if notif.OrderID == "" || notif.StatusCode == "" || notif.GrossAmount == "" ||
		notif.SignatureKey == "" || notif.TransactionStatus == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing required notification fields"})
		return
	}
	if h.midtransClient == nil {
		c.Header("Retry-After", "60")
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "deferred", "message": "payment gateway verifier is not ready"})
		return
	}

	trimmedOrderID := strings.TrimSpace(notif.OrderID)
	if len(trimmedOrderID) < 3 || len(trimmedOrderID) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid order_id length"})
		return
	}

	// 2. Verify SHA-512(order_id + status_code + gross_amount + server_key).
	if !h.midtransClient.VerifySignature(notif.OrderID, notif.StatusCode, notif.GrossAmount, notif.SignatureKey) {
		log.Printf("[SECURITY] Invalid Midtrans signature received for provider order ID: %s", trimmedOrderID)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid signature key"})
		return
	}

	h.processVerifiedNotification(c, body, notif, trimmedOrderID)
}

// HandleTenantWebhook processes Midtrans Core API notifications signed with a specific tenant's
// BYOK Server Key (POST /api/v1/payments/midtrans/webhook/:store_slug).
func (h *MidtransHandler) HandleTenantWebhook(c *gin.Context) {
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
	if notif.OrderID == "" || notif.StatusCode == "" || notif.GrossAmount == "" ||
		notif.SignatureKey == "" || notif.TransactionStatus == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing required notification fields"})
		return
	}
	trimmedOrderID := strings.TrimSpace(notif.OrderID)
	if len(trimmedOrderID) < 3 || len(trimmedOrderID) > 100 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid order_id length"})
		return
	}
	if h.governanceService == nil {
		c.Header("Retry-After", "60")
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "deferred", "message": "tenant gateway verifier is not ready"})
		return
	}

	storeSlug := strings.ToLower(strings.TrimSpace(c.Param("store_slug")))
	if storeSlug == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "store_slug is required"})
		return
	}

	verified, err := h.governanceService.VerifyTenantWebhookSignature(
		c.Request.Context(),
		storeSlug,
		notif.OrderID,
		notif.StatusCode,
		notif.GrossAmount,
		notif.SignatureKey,
	)
	if err != nil {
		if errors.Is(err, service.ErrGovernanceNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "tenant BYOK payment webhook is not enabled"})
			return
		}
		log.Printf("[WEBHOOK ERROR] store_slug=%s provider_order_id=%s verify_error=%v", storeSlug, trimmedOrderID, err)
		c.Header("Retry-After", "60")
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "deferred", "message": "tenant gateway verifier temporarily unavailable"})
		return
	}
	if !verified {
		log.Printf("[SECURITY] Invalid tenant BYOK Midtrans signature store_slug=%s provider_order_id=%s", storeSlug, trimmedOrderID)
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid signature key"})
		return
	}

	h.processVerifiedNotification(c, body, notif, trimmedOrderID)
}

func (h *MidtransHandler) processVerifiedNotification(c *gin.Context, body []byte, notif MidtransNotificationPayload, trimmedOrderID string) {

	if !strings.EqualFold(strings.TrimSpace(notif.Currency), "IDR") {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported currency: only IDR is accepted"})
		return
	}
	grossAmountIDR, err := midtrans.ParseGrossAmountIDR(notif.GrossAmount)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid gross_amount format"})
		return
	}
	mappedStatus, err := midtrans.MapMidtransStatus(notif.TransactionStatus, notif.FraudStatus)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "unsupported transaction_status or fraud_status"})
		return
	}
	if mappedStatus == "paid" && notif.StatusCode != "200" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "paid notification must have status_code 200"})
		return
	}

	// Only require durable service availability after the payload has passed
	// signature, currency, amount, and status validation. Invalid requests should
	// retain their 4xx response even when the database-backed service is not wired.
	if h.paymentService == nil {
		c.Header("Retry-After", "60")
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "deferred", "message": "payment webhook service is not ready"})
		return
	}

	// Persist only a sanitized payload. signature_key is not needed after verification.
	var sanitized map[string]json.RawMessage
	if err := json.Unmarshal(body, &sanitized); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json payload"})
		return
	}
	delete(sanitized, "signature_key")
	sanitizedBytes, err := json.Marshal(sanitized)
	if err != nil {
		c.Header("Retry-After", "60")
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "failed to prepare notification audit payload"})
		return
	}

	result, err := h.paymentService.ProcessMidtransWebhook(c.Request.Context(), service.MidtransWebhookInput{
		OrderID:           notif.OrderID,
		TransactionID:     notif.TransactionID,
		StatusCode:        notif.StatusCode,
		TransactionStatus: notif.TransactionStatus,
		GrossAmount:       notif.GrossAmount,
		FraudStatus:       notif.FraudStatus,
		PaymentType:       notif.PaymentType,
		Currency:          notif.Currency,
	}, string(sanitizedBytes))
	if err != nil {
		if errors.Is(err, service.ErrInvalidMidtransWebhook) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid payment notification"})
			return
		}
		if errors.Is(err, postgres.ErrPaymentNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "payment attempt not found"})
			return
		}
		log.Printf("[WEBHOOK ERROR] provider_order_id=%s amount=%d error=%v", trimmedOrderID, grossAmountIDR, err)
		c.Header("Retry-After", "60")
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "deferred", "message": "payment notification could not be committed; provider retry requested"})
		return
	}

	if result.Anomaly {
		log.Printf("[WEBHOOK ANOMALY] provider_order_id=%s amount=%d status=%s; durable event stored for reconciliation", trimmedOrderID, grossAmountIDR, result.Status)
	}
	c.JSON(http.StatusOK, gin.H{
		"status":    "ok",
		"duplicate": result.Duplicate,
		"ignored":   result.Ignored,
		"anomaly":   result.Anomaly,
	})

}
