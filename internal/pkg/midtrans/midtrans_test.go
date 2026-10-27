package midtrans_test

import (
	"crypto/sha512"
	"encoding/hex"
	"testing"

	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/midtrans"
)

func TestVerifySignature(t *testing.T) {
	serverKey := "SB-Mid-server-test-123"
	cfg := &config.Config{
		MidtransServerKey:    serverKey,
		MidtransIsProduction: false,
	}
	client := midtrans.NewClient(cfg)

	orderID := "test-order-001"
	statusCode := "200"
	grossAmount := "50000.00"

	// Correct signature calculation
	raw := orderID + statusCode + grossAmount + serverKey
	hasher := sha512.New()
	hasher.Write([]byte(raw))
	validSignature := hex.EncodeToString(hasher.Sum(nil))

	if !client.VerifySignature(orderID, statusCode, grossAmount, validSignature) {
		t.Errorf("expected valid signature to pass verification")
	}

	invalidSignature := "invalid_signature_hash_xyz"
	if client.VerifySignature(orderID, statusCode, grossAmount, invalidSignature) {
		t.Errorf("expected invalid signature to fail verification")
	}
}

func TestMapMidtransStatus(t *testing.T) {
	tests := []struct {
		name              string
		transactionStatus string
		fraudStatus       string
		expected          domain.PaymentStatus
	}{
		{"settlement is paid", "settlement", "", domain.PaymentStatusPaid},
		{"capture accept is paid", "capture", "accept", domain.PaymentStatusPaid},
		{"capture challenge is pending", "capture", "challenge", domain.PaymentStatusPending},
		{"pending is pending", "pending", "", domain.PaymentStatusPending},
		{"cancel is failed", "cancel", "", domain.PaymentStatusFailed},
		{"expire is failed", "expire", "", domain.PaymentStatusFailed},
		{"refund is refunded", "refund", "", domain.PaymentStatusRefunded},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := midtrans.MapMidtransStatus(tt.transactionStatus, tt.fraudStatus)
			if got != tt.expected {
				t.Errorf("MapMidtransStatus(%q, %q) = %v; want %v", tt.transactionStatus, tt.fraudStatus, got, tt.expected)
			}
		})
	}
}
