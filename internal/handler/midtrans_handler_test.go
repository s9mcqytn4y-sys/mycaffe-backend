package handler_test

import (
	"bytes"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/handler"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/midtrans"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/realtime"
)

func setupTestWebhookRouter(serverKey string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	hub := realtime.NewHub([]string{"http://localhost:3000"})
	go hub.Run()

	cfg := &config.Config{
		MidtransServerKey:    serverKey,
		MidtransIsProduction: false,
	}
	client := midtrans.NewClient(cfg)
	webhookHandler := handler.NewMidtransHandler(client, hub)

	r.POST("/api/v1/payments/midtrans/webhook", webhookHandler.HandleWebhook)
	return r
}

func computeSignature(orderID, statusCode, grossAmount, serverKey string) string {
	raw := orderID + statusCode + grossAmount + serverKey
	hasher := sha512.New()
	hasher.Write([]byte(raw))
	return hex.EncodeToString(hasher.Sum(nil))
}

func TestMidtransWebhookEdgeCases(t *testing.T) {
	serverKey := "SB-Mid-server-test-webhook-key-123"
	r := setupTestWebhookRouter(serverKey)

	// 1. Oversized body (>64 KB) must fail with 413 Payload Too Large
	hugePayload := strings.Repeat("A", 70*1024)
	reqHuge, _ := http.NewRequest(http.MethodPost, "/api/v1/payments/midtrans/webhook", strings.NewReader(hugePayload))
	reqHuge.Header.Set("Content-Type", "application/json")
	wHuge := httptest.NewRecorder()
	r.ServeHTTP(wHuge, reqHuge)
	if wHuge.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413 Request Entity Too Large for oversized payload, got %d", wHuge.Code)
	}

	// 2. Malformed JSON must fail with 400 Bad Request
	reqBadJSON, _ := http.NewRequest(http.MethodPost, "/api/v1/payments/midtrans/webhook", strings.NewReader("{invalid-json"))
	reqBadJSON.Header.Set("Content-Type", "application/json")
	wBadJSON := httptest.NewRecorder()
	r.ServeHTTP(wBadJSON, reqBadJSON)
	if wBadJSON.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for malformed json, got %d", wBadJSON.Code)
	}

	// 3. Invalid order_id length (< 3 chars) must fail with 400 Bad Request
	payloadInvalidOrderID := map[string]string{
		"order_id":           "x",
		"status_code":        "200",
		"gross_amount":       "10000.00",
		"signature_key":      "fake-sig",
		"transaction_status": "settlement",
	}
	bodyInvalidOrderID, _ := json.Marshal(payloadInvalidOrderID)
	reqInvalidOrderID, _ := http.NewRequest(http.MethodPost, "/api/v1/payments/midtrans/webhook", bytes.NewBuffer(bodyInvalidOrderID))
	reqInvalidOrderID.Header.Set("Content-Type", "application/json")
	wInvalidOrderID := httptest.NewRecorder()
	r.ServeHTTP(wInvalidOrderID, reqInvalidOrderID)
	if wInvalidOrderID.Code != http.StatusBadRequest {
		t.Errorf("expected 400 Bad Request for short order_id, got %d", wInvalidOrderID.Code)
	}

	// 4. Invalid signature must fail with 401 Unauthorized
	validOrderID := uuid.New().String()
	payloadInvalidSig := map[string]string{
		"order_id":           validOrderID,
		"status_code":        "200",
		"gross_amount":       "10000.00",
		"signature_key":      "invalid-computed-signature-hash",
		"transaction_status": "settlement",
	}
	bodyInvalidSig, _ := json.Marshal(payloadInvalidSig)
	reqInvalidSig, _ := http.NewRequest(http.MethodPost, "/api/v1/payments/midtrans/webhook", bytes.NewBuffer(bodyInvalidSig))
	reqInvalidSig.Header.Set("Content-Type", "application/json")
	wInvalidSig := httptest.NewRecorder()
	r.ServeHTTP(wInvalidSig, reqInvalidSig)
	if wInvalidSig.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for invalid signature, got %d", wInvalidSig.Code)
	}

	// 5. Valid signature but no durable database persistence yet must return 503 with Retry-After header
	statusCode := "200"
	grossAmount := "25000.00"
	validSig := computeSignature(validOrderID, statusCode, grossAmount, serverKey)

	payloadValid := map[string]string{
		"order_id":           validOrderID,
		"status_code":        statusCode,
		"gross_amount":       grossAmount,
		"signature_key":      validSig,
		"transaction_status": "settlement",
	}
	bodyValid, _ := json.Marshal(payloadValid)
	reqValid, _ := http.NewRequest(http.MethodPost, "/api/v1/payments/midtrans/webhook", bytes.NewBuffer(bodyValid))
	reqValid.Header.Set("Content-Type", "application/json")
	wValid := httptest.NewRecorder()
	r.ServeHTTP(wValid, reqValid)

	if wValid.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 Service Unavailable when durable DB persistence is uncommitted, got %d", wValid.Code)
	}
	if wValid.Header().Get("Retry-After") != "60" {
		t.Errorf("expected Retry-After header to be '60', got %q", wValid.Header().Get("Retry-After"))
	}
}
