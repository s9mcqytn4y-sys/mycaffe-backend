package midtrans

import (
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
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

// ChargeQRIS initiates a dynamic QRIS payment using Midtrans Core API
func (c *Client) ChargeQRIS(order *domain.Order) (*coreapi.ChargeResponse, error) {
	req := &coreapi.ChargeReq{
		PaymentType: coreapi.PaymentTypeQris,
		TransactionDetails: midtrans.TransactionDetails{
			OrderID:  order.ID.String(),
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

// ChargeBankTransfer initiates a VA bank transfer using Midtrans Core API
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

	req := &coreapi.ChargeReq{
		PaymentType: coreapi.PaymentTypeBankTransfer,
		TransactionDetails: midtrans.TransactionDetails{
			OrderID:  order.ID.String(),
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

// VerifySignature verifies Midtrans notification signature key
// Formula: SHA512(order_id + status_code + gross_amount + server_key)
func (c *Client) VerifySignature(orderID, statusCode, grossAmount, incomingSignature string) bool {
	raw := orderID + statusCode + grossAmount + c.serverKey
	hasher := sha512.New()
	hasher.Write([]byte(raw))
	expectedSignature := hex.EncodeToString(hasher.Sum(nil))

	return strings.EqualFold(expectedSignature, incomingSignature)
}

// MapMidtransStatus maps Midtrans transaction status to internal domain.PaymentStatus
func MapMidtransStatus(transactionStatus, fraudStatus string) domain.PaymentStatus {
	switch transactionStatus {
	case "capture":
		if fraudStatus == "challenge" {
			return domain.PaymentStatusPending
		} else if fraudStatus == "accept" {
			return domain.PaymentStatusPaid
		}
		return domain.PaymentStatusPending
	case "settlement":
		return domain.PaymentStatusPaid
	case "pending":
		return domain.PaymentStatusPending
	case "deny", "cancel", "expire":
		return domain.PaymentStatusFailed
	case "refund", "partial_refund":
		return domain.PaymentStatusRefunded
	default:
		return domain.PaymentStatusPending
	}
}
