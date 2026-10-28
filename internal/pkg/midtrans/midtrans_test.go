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

	orderID := "ORD-20261028-0001-S1-A1"
	statusCode := "200"
	grossAmount := "58905.00"

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

func TestParseGrossAmountIDR(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    int64
		wantErr bool
	}{
		{"standard midtrans decimal .00", "58905.00", 58905, false},
		{"plain integer string", "58905", 58905, false},
		{"single zero decimal .0", "125000.0", 125000, false},
		{"non-zero cents rejected", "58905.50", 0, true},
		{"zero amount rejected", "0.00", 0, true},
		{"negative amount rejected", "-10000.00", 0, true},
		{"empty string rejected", "", 0, true},
		{"malformed decimal rejected", "58905.00.00", 0, true},
		{"non-numeric rejected", "abc.00", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := midtrans.ParseGrossAmountIDR(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseGrossAmountIDR(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("ParseGrossAmountIDR(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestMapMidtransStatus(t *testing.T) {
	tests := []struct {
		name              string
		transactionStatus string
		fraudStatus       string
		expected          domain.PaymentStatus
		wantErr           bool
	}{
		{"authorize is authorized", "authorize", "accept", domain.PaymentStatusAuthorized, false},
		{"settlement is paid", "settlement", "", domain.PaymentStatusPaid, false},
		{"capture accept is paid", "capture", "accept", domain.PaymentStatusPaid, false},
		{"capture challenge is pending", "capture", "challenge", domain.PaymentStatusPending, false},
		{"capture deny is failed", "capture", "deny", domain.PaymentStatusFailed, false},
		{"capture unknown fraud_status errors", "capture", "", "", true},
		{"pending is pending", "pending", "", domain.PaymentStatusPending, false},
		{"cancel is cancelled", "cancel", "", domain.PaymentStatusCancelled, false},
		{"deny is failed", "deny", "", domain.PaymentStatusFailed, false},
		{"failure is failed (never pending)", "failure", "", domain.PaymentStatusFailed, false},
		{"expire is expired", "expire", "", domain.PaymentStatusExpired, false},
		{"partial_refund is partially_refunded", "partial_refund", "", domain.PaymentStatusPartiallyRefunded, false},
		{"refund is refunded", "refund", "", domain.PaymentStatusRefunded, false},
		{"chargeback is chargeback", "chargeback", "", domain.PaymentStatusChargeback, false},
		{"partial_chargeback is chargeback", "partial_chargeback", "", domain.PaymentStatusChargeback, false},
		{"unknown status returns error", "unrecognized_state", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := midtrans.MapMidtransStatus(tt.transactionStatus, tt.fraudStatus)
			if (err != nil) != tt.wantErr {
				t.Fatalf("MapMidtransStatus(%q, %q) error = %v, wantErr %v", tt.transactionStatus, tt.fraudStatus, err, tt.wantErr)
			}
			if got != tt.expected {
				t.Errorf("MapMidtransStatus(%q, %q) = %v; want %v", tt.transactionStatus, tt.fraudStatus, got, tt.expected)
			}
		})
	}
}

func TestCanTransitionPaymentStatus_NoRegression(t *testing.T) {
	if !domain.CanTransitionPaymentStatus(domain.PaymentStatusPending, domain.PaymentStatusPaid) {
		t.Error("expected pending -> paid to be allowed")
	}
	if domain.CanTransitionPaymentStatus(domain.PaymentStatusPaid, domain.PaymentStatusPending) {
		t.Error("expected paid -> pending out-of-order regression to be blocked")
	}
	if domain.CanTransitionPaymentStatus(domain.PaymentStatusPaid, domain.PaymentStatusExpired) {
		t.Error("expected paid -> expired regression to be blocked")
	}
	if !domain.CanTransitionPaymentStatus(domain.PaymentStatusPaid, domain.PaymentStatusRefunded) {
		t.Error("expected paid -> refunded to be allowed")
	}
}
