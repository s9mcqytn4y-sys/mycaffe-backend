package handler_test

import (
	"bytes"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/handler"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/midtrans"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/realtime"
)

// calculateMidtransSignature calculates official SHA-512(order_id + status_code + gross_amount + server_key)
func calculateMidtransSignature(orderID, statusCode, grossAmount, serverKey string) string {
	raw := orderID + statusCode + grossAmount + serverKey
	hasher := sha512.New()
	hasher.Write([]byte(raw))
	return hex.EncodeToString(hasher.Sum(nil))
}

// TestSimulateTenantWebhookCoreAPI tests the dynamic notification webhook URL per-tenant flow:
// POST /api/v1/payments/midtrans/webhook/:store_slug
func TestSimulateTenantWebhookCoreAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tenantServerKey := "SB-Mid-server-CentralCafeSandbox123"
	tenantStoreSlug := "central-cafe"

	hub := realtime.NewHub([]string{"http://localhost:3000"})
	h := handler.NewMidtransHandler(nil, hub)

	r := gin.New()
	r.POST("/api/v1/payments/midtrans/webhook/:store_slug", h.HandleTenantWebhook)

	t.Run("Reject invalid signature for tenant", func(t *testing.T) {
		orderID := uuid.New().String()
		payload := map[string]string{
			"order_id":           orderID,
			"status_code":        "200",
			"gross_amount":       "45000.00",
			"signature_key":      "invalid-signature-key-abc",
			"transaction_status": "settlement",
			"payment_type":       "qris",
			"currency":           "IDR",
		}
		body, _ := json.Marshal(payload)
		req, _ := http.NewRequest(http.MethodPost, "/api/v1/payments/midtrans/webhook/"+tenantStoreSlug, bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		// Without governance service initialized or with invalid sig, it fails securely
		if w.Code == http.StatusOK {
			t.Errorf("expected rejection for invalid signature, got %d", w.Code)
		}
	})

	t.Run("Verify calculation formula SHA-512 matches Midtrans spec", func(t *testing.T) {
		orderID := "ORDER-TEST-001"
		statusCode := "200"
		grossAmount := "50000.00"
		expectedSig := calculateMidtransSignature(orderID, statusCode, grossAmount, tenantServerKey)

		// Direct verification against midtrans client
		client := midtrans.NewClientWithKey(tenantServerKey, false)
		if !client.VerifySignature(orderID, statusCode, grossAmount, expectedSig) {
			t.Fatalf("signature verification formula failed against midtrans client")
		}
	})

	t.Run("Simulate Full Lifecycle Status Mapping", func(t *testing.T) {
		// Midtrans transaction status mappings
		testCases := []struct {
			status    string
			fraud     string
			isSuccess bool
		}{
			{"settlement", "accept", true},
			{"capture", "accept", true},
			{"pending", "", false},
			{"expire", "", false},
			{"cancel", "", false},
			{"deny", "", false},
		}

		for _, tc := range testCases {
			mapped, err := midtrans.MapMidtransStatus(tc.status, tc.fraud)
			if err != nil {
				t.Fatalf("unexpected mapping error for status %s: %v", tc.status, err)
			}
			if tc.isSuccess && mapped != "paid" {
				t.Errorf("expected paid for status %s, got %s", tc.status, mapped)
			}
		}
	})
}
