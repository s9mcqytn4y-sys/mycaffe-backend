package midtrans

import (
	"context"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/midtrans/midtrans-go"
	"github.com/midtrans/midtrans-go/coreapi"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/config"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
)

type Client struct {
	serverKey              string
	env                    midtrans.EnvironmentType
	defaultNotificationURL string
}

func NewClient(cfg *config.Config) *Client {
	c := NewClientWithKey(cfg.MidtransServerKey, cfg.MidtransIsProduction)
	if cfg != nil {
		base := strings.TrimRight(strings.TrimSpace(cfg.AppPublicURL), "/")
		if strings.HasPrefix(strings.ToLower(base), "https://") {
			c.defaultNotificationURL = base + "/api/v1/payments/midtrans/webhook"
		}
	}
	return c
}

// NewClientWithKey constructs an isolated Midtrans Core API client for either Platform Billing
// (Lane 1) or a tenant's decrypted BYOK Midtrans credentials (Lane 2).
func NewClientWithKey(serverKey string, isProduction bool) *Client {
	var env midtrans.EnvironmentType = midtrans.Sandbox
	if isProduction {
		env = midtrans.Production
	}

	return &Client{
		serverKey: strings.TrimSpace(serverKey),
		env:       env,
	}
}

// WithDefaultNotificationURL returns a shallow copy of the Client configured to send
// X-Override-Notification on every ChargeCoreAPI call unless overridden per request.
func (c *Client) WithDefaultNotificationURL(notificationURL string) *Client {
	if c == nil {
		return nil
	}
	cp := *c
	cp.defaultNotificationURL = strings.TrimSpace(notificationURL)
	return &cp
}

// IsConfigured reports whether a non-empty server key is loaded.
func (c *Client) IsConfigured() bool {
	return c != nil && strings.TrimSpace(c.serverKey) != ""
}

// IsProduction reports whether the client targets the live Midtrans Production endpoint.
func (c *Client) IsProduction() bool {
	return c != nil && c.env == midtrans.Production
}

// ChargeRequest represents one immutable Midtrans payment attempt.
type ChargeRequest struct {
	OrderID         string
	Amount          int64
	Method          domain.PaymentMethod
	Bank            string
	IdempotencyKey  string // UUID v4 (36 characters), stable for this payment attempt.
	CallbackURL     string
	NotificationURL string // Optional X-Override-Notification webhook URL.
}

// newCoreAPIClient creates a per-call SDK client so mutable SDK options are never shared
// between concurrent payment attempts. SDK v1.3.8 has an 80s default timeout; use a
// bounded client timeout because its ConfigOptions.SetContext currently does not replace
// the request returned by http.Request.WithContext.
func (c *Client) newCoreAPIClient() coreapi.Client {
	client := coreapi.Client{}
	client.New(c.serverKey, c.env)
	client.HttpClient = &midtrans.HttpClientImplementation{
		HttpClient: &http.Client{Timeout: 12 * time.Second},
		// Errors only: the SDK's sandbox default logs raw request/response bodies (QR strings, merchant id).
		Logger: &midtrans.LoggerImplementation{LogLevel: midtrans.LogError},
	}
	return client
}

// ChargeCoreAPI creates a single Midtrans charge for a persisted payment attempt.
// OrderID and IdempotencyKey must be stable across retries of the same attempt.
func (c *Client) ChargeCoreAPI(ctx context.Context, input ChargeRequest) (*coreapi.ChargeResponse, error) {
	if c == nil || strings.TrimSpace(c.serverKey) == "" {
		return nil, errors.New("midtrans client is not configured")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(input.OrderID) == "" || len(input.OrderID) > 100 {
		return nil, errors.New("provider order ID must contain 1 to 100 characters")
	}
	if input.Amount <= 0 {
		return nil, errors.New("payment amount must be positive")
	}
	if _, err := uuid.Parse(input.IdempotencyKey); err != nil {
		return nil, errors.New("Midtrans payment idempotency key must be a UUID")
	}

	req := &coreapi.ChargeReq{
		TransactionDetails: midtrans.TransactionDetails{
			OrderID:  input.OrderID,
			GrossAmt: input.Amount,
		},
	}
	switch input.Method {
	case domain.PaymentMethodQRIS:
		req.PaymentType = coreapi.PaymentTypeQris
		req.Qris = &coreapi.QrisDetails{Acquirer: "gopay"}
	case domain.PaymentMethodGoPay:
		req.PaymentType = coreapi.PaymentTypeGopay
		req.Gopay = &coreapi.GopayDetails{EnableCallback: input.CallbackURL != "", CallbackUrl: input.CallbackURL}
	case domain.PaymentMethodShopeePay:
		req.PaymentType = coreapi.PaymentTypeShopeepay
		req.ShopeePay = &coreapi.ShopeePayDetails{CallbackUrl: input.CallbackURL}
	case domain.PaymentMethodBankTransfer:
		var bank midtrans.Bank
		switch strings.ToLower(strings.TrimSpace(input.Bank)) {
		case "bca":
			bank = midtrans.BankBca
		case "bni":
			bank = midtrans.BankBni
		case "bri":
			bank = midtrans.BankBri
		case "mandiri":
			bank = midtrans.BankMandiri
		case "permata":
			bank = midtrans.BankPermata
		default:
			return nil, fmt.Errorf("unsupported bank transfer: %s", input.Bank)
		}
		req.PaymentType = coreapi.PaymentTypeBankTransfer
		req.BankTransfer = &coreapi.BankTransferDetails{Bank: bank}
	default:
		return nil, fmt.Errorf("unsupported Midtrans payment method: %s", input.Method)
	}

	client := c.newCoreAPIClient()
	client.Options.SetPaymentIdempotencyKey(input.IdempotencyKey)
	notifURL := strings.TrimSpace(input.NotificationURL)
	if notifURL == "" {
		notifURL = strings.TrimSpace(c.defaultNotificationURL)
	}
	if notifURL != "" {
		client.Options.SetPaymentOverrideNotification(notifURL)
	}
	resp, midtransErr := client.ChargeTransaction(req)
	if midtransErr != nil {
		return nil, fmt.Errorf("midtrans charge failed (status %d): %s", midtransErr.StatusCode, midtransErr.Message)
	}
	return resp, nil
}

// CheckTransaction retrieves the provider's current transaction state for webhook
// reconciliation and ambiguous charge retries.
func (c *Client) CheckTransaction(ctx context.Context, providerOrderID string) (*coreapi.TransactionStatusResponse, error) {
	if c == nil || strings.TrimSpace(c.serverKey) == "" {
		return nil, errors.New("midtrans client is not configured")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(providerOrderID) == "" {
		return nil, errors.New("provider order ID is required")
	}
	client := c.newCoreAPIClient()
	resp, midtransErr := client.CheckTransaction(providerOrderID)
	if midtransErr != nil {
		return nil, fmt.Errorf("midtrans status lookup failed (status %d): %s", midtransErr.StatusCode, midtransErr.Message)
	}
	return resp, nil
}

// DirectRefund calls the Direct Refund API (RF1-a). refundKey makes retries idempotent for 7 days.
// A 2xx answer is returned as is: 200 means the bank approved the request, 202 means it was denied.
func (c *Client) DirectRefund(ctx context.Context, providerOrderID, refundKey string, amount int64, reason string) (*coreapi.RefundResponse, error) {
	if c == nil || strings.TrimSpace(c.serverKey) == "" {
		return nil, errors.New("midtrans client is not configured")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(reason) > 255 {
		reason = reason[:255]
	}
	resp, midtransErr := c.newCoreAPIClient().DirectRefundTransaction(providerOrderID, &coreapi.RefundReq{RefundKey: refundKey, Amount: amount, Reason: reason})
	if midtransErr != nil {
		if resp != nil && resp.StatusCode != "" {
			return resp, nil // a business answer (406, 412, 414) in a non-2xx envelope
		}
		return nil, fmt.Errorf("midtrans direct refund failed (status %d): %s", midtransErr.StatusCode, midtransErr.Message)
	}
	return resp, nil
}

// ChargeQRIS is a compatibility wrapper. New payment flows must pass payments.id as
// OrderID and IdempotencyKey through ChargeCoreAPI, not an order display number.
func (c *Client) ChargeQRIS(order *domain.Order) (*coreapi.ChargeResponse, error) {
	if order == nil || order.ID == uuid.Nil {
		return nil, errors.New("order ID is required")
	}
	return c.ChargeCoreAPI(context.Background(), ChargeRequest{
		OrderID: order.ID.String(), Amount: order.TotalAmount,
		Method: domain.PaymentMethodQRIS, IdempotencyKey: order.ID.String(),
	})
}

// ChargeBankTransfer is a compatibility wrapper. New payment flows must use a
// payment-attempt UUID via ChargeCoreAPI.
func (c *Client) ChargeBankTransfer(order *domain.Order, bank string) (*coreapi.ChargeResponse, error) {
	if order == nil || order.ID == uuid.Nil {
		return nil, errors.New("order ID is required")
	}
	return c.ChargeCoreAPI(context.Background(), ChargeRequest{
		OrderID: order.ID.String(), Amount: order.TotalAmount,
		Method: domain.PaymentMethodBankTransfer, Bank: bank, IdempotencyKey: order.ID.String(),
	})
}

// VerifySignature verifies Midtrans notification signature key using the raw gross_amount string.
// Formula: SHA512(order_id + status_code + gross_amount + server_key)
func (c *Client) VerifySignature(orderID, statusCode, grossAmount, incomingSignature string) bool {
	raw := orderID + statusCode + grossAmount + c.serverKey
	hasher := sha512.New()
	hasher.Write([]byte(raw))
	expectedSignature := hex.EncodeToString(hasher.Sum(nil))

	providedSignature, err := hex.DecodeString(strings.TrimSpace(incomingSignature))
	if err != nil || len(providedSignature) != sha512.Size {
		return false
	}
	expectedBytes, err := hex.DecodeString(expectedSignature)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(expectedBytes, providedSignature) == 1
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
		switch fStatus {
		case "", "accept":
			return domain.PaymentStatusPaid, nil
		case "deny":
			return domain.PaymentStatusFailed, nil
		default:
			return "", fmt.Errorf("unsupported fraud_status %q for settlement transaction", fraudStatus)
		}
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
	case "partial_chargeback":
		return domain.PaymentStatusPartialChargeback, nil
	case "chargeback":
		return domain.PaymentStatusChargeback, nil
	default:
		return "", fmt.Errorf("unknown midtrans transaction_status: %q", transactionStatus)
	}
}
