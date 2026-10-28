package midtrans

import (
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/midtrans/midtrans-go"
	"github.com/midtrans/midtrans-go/coreapi"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
)

type Client struct {
	coreClient coreapi.Client
	serverKey  string
}

func NewClient(cfg *config.Config) *Client {
	var env midtrans.EnvironmentType = midtrans.Sandbox
	if cfg.MidtransIsProduction {
		env = midtrans.Production
	}

	c := coreapi.Client{}
	c.New(cfg.MidtransServerKey, env)

	return &Client{
		coreClient: c,
		serverKey:  cfg.MidtransServerKey,
	}
}

// ChargeQRIS initiates a dynamic QRIS payment using Midtrans Core API.
// Uses stable order.OrderNumber as ProviderOrderID.
func (c *Client) ChargeQRIS(order *domain.Order) (*coreapi.ChargeResponse, error) {
	providerOrderID := order.OrderNumber
	if providerOrderID == "" {
		providerOrderID = order.ID.String()
	}

	req := &coreapi.ChargeReq{
		PaymentType: coreapi.PaymentTypeQris,
		TransactionDetails: midtrans.TransactionDetails{
			OrderID:  providerOrderID,
			GrossAmt: order.TotalAmount,
		},
		Qris: &coreapi.QrisDetails{
			Acquirer: "gopay",
		},
	}

	resp, midtransErr := c.coreClient.ChargeTransaction(req)
	if midtransErr != nil {
		return nil, fmt.Errorf("midtrans charge qris error: %s (code: %d)", midtransErr.Message, midtransErr.StatusCode)
	}

	return resp, nil
}

// ChargeBankTransfer initiates a VA bank transfer using Midtrans Core API.
// Uses stable order.OrderNumber as ProviderOrderID.
func (c *Client) ChargeBankTransfer(order *domain.Order, bank string) (*coreapi.ChargeResponse, error) {
	var midtransBank midtrans.Bank
	switch strings.ToLower(bank) {
	case "bca":
		midtransBank = midtrans.BankBca
	case "bni":
		midtransBank = midtrans.BankBni
	case "bri":
		midtransBank = midtrans.BankBri
	case "mandiri":
		midtransBank = midtrans.BankMandiri
	case "permata":
		midtransBank = midtrans.BankPermata
	default:
		return nil, errors.New("unsupported bank transfer: " + bank)
	}

	providerOrderID := order.OrderNumber
	if providerOrderID == "" {
		providerOrderID = order.ID.String()
	}

	req := &coreapi.ChargeReq{
		PaymentType: coreapi.PaymentTypeBankTransfer,
		TransactionDetails: midtrans.TransactionDetails{
			OrderID:  providerOrderID,
			GrossAmt: order.TotalAmount,
		},
		BankTransfer: &coreapi.BankTransferDetails{
			Bank: midtransBank,
		},
	}

	resp, midtransErr := c.coreClient.ChargeTransaction(req)
	if midtransErr != nil {
		return nil, fmt.Errorf("midtrans charge va error: %s (code: %d)", midtransErr.Message, midtransErr.StatusCode)
	}

	return resp, nil
}

// VerifySignature verifies Midtrans notification signature key using the raw gross_amount string.
// Formula: SHA512(order_id + status_code + gross_amount + server_key)
func (c *Client) VerifySignature(orderID, statusCode, grossAmount, incomingSignature string) bool {
	raw := orderID + statusCode + grossAmount + c.serverKey
	hasher := sha512.New()
	hasher.Write([]byte(raw))
	expectedSignature := hex.EncodeToString(hasher.Sum(nil))

	return strings.EqualFold(expectedSignature, incomingSignature)
}

// ParseGrossAmountIDR parses a Midtrans gross_amount string (e.g. "58905.00" or "58905")
// into exact integer Indonesian Rupiah (int64) without using floating-point arithmetic.
// Rejects non-zero fractional cents, negative/zero values, and malformed strings.
func ParseGrossAmountIDR(grossAmount string) (int64, error) {
	trimmed := strings.TrimSpace(grossAmount)
	if trimmed == "" {
		return 0, errors.New("gross_amount cannot be empty")
	}

	parts := strings.Split(trimmed, ".")
	if len(parts) > 2 {
		return 0, fmt.Errorf("invalid gross_amount format: %q", grossAmount)
	}

	intPart := parts[0]
	if intPart == "" {
		return 0, fmt.Errorf("missing integer part in gross_amount: %q", grossAmount)
	}

	if len(parts) == 2 {
		fracPart := parts[1]
		if fracPart == "" {
			return 0, fmt.Errorf("empty fractional part in gross_amount: %q", grossAmount)
		}
		for i := 0; i < len(fracPart); i++ {
			if fracPart[i] != '0' {
				return 0, fmt.Errorf("non-zero fractional IDR cents are not allowed in gross_amount: %q", grossAmount)
			}
		}
	}

	val, err := strconv.ParseInt(intPart, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid integer in gross_amount %q: %w", grossAmount, err)
	}
	if val <= 0 {
		return 0, fmt.Errorf("gross_amount must be strictly positive, got %d", val)
	}

	return val, nil
}

// MapMidtransStatus maps Midtrans transaction_status and fraud_status to internal domain.PaymentStatus.
// Returns an explicit error for unknown or unmapped statuses instead of silently defaulting to pending.
func MapMidtransStatus(transactionStatus, fraudStatus string) (domain.PaymentStatus, error) {
	txStatus := strings.ToLower(strings.TrimSpace(transactionStatus))
	fStatus := strings.ToLower(strings.TrimSpace(fraudStatus))

	switch txStatus {
	case "authorize":
		return domain.PaymentStatusAuthorized, nil
	case "capture":
		switch fStatus {
		case "challenge":
			return domain.PaymentStatusPending, nil
		case "accept":
			return domain.PaymentStatusPaid, nil
		case "deny":
			return domain.PaymentStatusFailed, nil
		default:
			return "", fmt.Errorf("unsupported fraud_status %q for capture transaction", fraudStatus)
		}
	case "settlement":
		if fStatus == "deny" {
			return domain.PaymentStatusFailed, nil
		}
		return domain.PaymentStatusPaid, nil
	case "pending":
		return domain.PaymentStatusPending, nil
	case "deny", "failure":
		return domain.PaymentStatusFailed, nil
	case "cancel":
		return domain.PaymentStatusCancelled, nil
	case "expire":
		return domain.PaymentStatusExpired, nil
	case "partial_refund":
		return domain.PaymentStatusPartiallyRefunded, nil
	case "refund":
		return domain.PaymentStatusRefunded, nil
	case "chargeback", "partial_chargeback":
		return domain.PaymentStatusChargeback, nil
	default:
		return "", fmt.Errorf("unknown midtrans transaction_status: %q", transactionStatus)
	}
}
