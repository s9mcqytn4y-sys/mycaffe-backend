package domain

import (
	"time"

	"github.com/google/uuid"
)

type PaymentStatus string

const (
	PaymentStatusUnpaid            PaymentStatus = "unpaid"
	PaymentStatusPending           PaymentStatus = "pending"
	PaymentStatusAuthorized        PaymentStatus = "authorized"
	PaymentStatusPartiallyPaid     PaymentStatus = "partially_paid"
	PaymentStatusPaid              PaymentStatus = "paid"
	PaymentStatusFailed            PaymentStatus = "failed"
	PaymentStatusCancelled         PaymentStatus = "cancelled"
	PaymentStatusExpired           PaymentStatus = "expired"
	PaymentStatusPartiallyRefunded PaymentStatus = "partially_refunded"
	PaymentStatusRefunded          PaymentStatus = "refunded"
	PaymentStatusChargeback        PaymentStatus = "chargeback"
)

// CanTransitionPaymentStatus enforces the payment attempt state machine so out-of-order
// webhooks (e.g. a delayed "pending" arriving after "settlement") never regress a settled payment.
func CanTransitionPaymentStatus(from, to PaymentStatus) bool {
	if from == to {
		return true
	}
	switch from {
	case PaymentStatusUnpaid, PaymentStatusPending:
		switch to {
		case PaymentStatusAuthorized, PaymentStatusPaid, PaymentStatusFailed, PaymentStatusCancelled, PaymentStatusExpired:
			return true
		}
	case PaymentStatusAuthorized:
		switch to {
		case PaymentStatusPaid, PaymentStatusFailed, PaymentStatusCancelled, PaymentStatusExpired:
			return true
		}
	case PaymentStatusPaid:
		switch to {
		case PaymentStatusPartiallyRefunded, PaymentStatusRefunded, PaymentStatusChargeback:
			return true
		}
	case PaymentStatusPartiallyRefunded:
		switch to {
		case PaymentStatusRefunded, PaymentStatusChargeback:
			return true
		}
	}
	return false
}

type PaymentMethod string

const (
	PaymentMethodQRIS         PaymentMethod = "qris"
	PaymentMethodStaticQRIS   PaymentMethod = "static_qris"
	PaymentMethodBankTransfer PaymentMethod = "bank_transfer"
	PaymentMethodGoPay        PaymentMethod = "gopay"
	PaymentMethodShopeePay    PaymentMethod = "shopeepay"
	PaymentMethodCard         PaymentMethod = "card"
	PaymentMethodCStore       PaymentMethod = "cstore"
	PaymentMethodCash         PaymentMethod = "cash"
	PaymentMethodEDC          PaymentMethod = "edc"
)

type SplitType string

const (
	SplitTypeFull         SplitType = "full"
	SplitTypeEqual        SplitType = "equal"
	SplitTypeByItem       SplitType = "by_item"
	SplitTypeCustomAmount SplitType = "custom_amount"
)

// PaymentSplit represents an individual bill portion under a parent Order.
type PaymentSplit struct {
	ID            uuid.UUID          `json:"id" db:"id"`
	StoreID       uuid.UUID          `json:"store_id" db:"store_id"`
	OrderID       uuid.UUID          `json:"order_id" db:"order_id"`
	SplitNumber   string             `json:"split_number" db:"split_number"`
	SplitType     SplitType          `json:"split_type" db:"split_type"`
	CustomerLabel string             `json:"customer_label,omitempty" db:"customer_label"`
	Amount        int64              `json:"amount" db:"amount"`
	PaidAmount    int64              `json:"paid_amount" db:"paid_amount"`
	Status        PaymentStatus      `json:"status" db:"status"`
	Items         []PaymentSplitItem `json:"items,omitempty"`
	CreatedAt     time.Time          `json:"created_at" db:"created_at"`
	UpdatedAt     time.Time          `json:"updated_at" db:"updated_at"`
}

// PaymentSplitItem links a specific quantity of an OrderItem to a PaymentSplit (for by_item splits).
type PaymentSplitItem struct {
	ID             uuid.UUID `json:"id" db:"id"`
	StoreID        uuid.UUID `json:"store_id" db:"store_id"`
	PaymentSplitID uuid.UUID `json:"payment_split_id" db:"payment_split_id"`
	OrderItemID    uuid.UUID `json:"order_item_id" db:"order_item_id"`
	Quantity       int       `json:"quantity" db:"quantity"`
	SubtotalAmount int64     `json:"subtotal_amount" db:"subtotal_amount"`
	CreatedAt      time.Time `json:"created_at" db:"created_at"`
}

// Payment represents a tenant-scoped payment transaction attempt.
type Payment struct {
	ID              uuid.UUID     `json:"id" db:"id"`
	StoreID         uuid.UUID     `json:"store_id" db:"store_id"`
	OrderID         uuid.UUID     `json:"order_id" db:"order_id"`
	PaymentSplitID  *uuid.UUID    `json:"payment_split_id,omitempty" db:"payment_split_id"`
	AttemptNumber   int           `json:"attempt_number" db:"attempt_number"`
	IdempotencyKey  string        `json:"idempotency_key" db:"idempotency_key"`
	ProviderOrderID string        `json:"provider_order_id" db:"provider_order_id"` // Format: {order_number}-S{split}-A{attempt}
	Amount          int64         `json:"amount" db:"amount"`                       // Integer Rupiah
	RefundedAmount  int64         `json:"refunded_amount" db:"refunded_amount"`     // Integer Rupiah
	Method          PaymentMethod `json:"method" db:"method"`
	Status          PaymentStatus `json:"status" db:"status"`
	TransactionID   string        `json:"transaction_id,omitempty" db:"transaction_id"` // Midtrans transaction_id
	PaymentType     string        `json:"payment_type,omitempty" db:"payment_type"`     // qris, bank_transfer, gopay, shopeepay, card, cstore
	Acquirer        string        `json:"acquirer,omitempty" db:"acquirer"`
	FraudStatus     string        `json:"fraud_status,omitempty" db:"fraud_status"`
	QRString        string        `json:"qr_string,omitempty" db:"qr_string"` // For dynamic QRIS
	DeeplinkURL     string        `json:"deeplink_url,omitempty" db:"deeplink_url"`
	VANumber        string        `json:"va_number,omitempty" db:"va_number"` // For Virtual Account
	Bank            string        `json:"bank,omitempty" db:"bank"`
	BillerCode      string        `json:"biller_code,omitempty" db:"biller_code"` // For Mandiri Bill
	BillKey         string        `json:"bill_key,omitempty" db:"bill_key"`       // For Mandiri Bill
	PaymentCode     string        `json:"payment_code,omitempty" db:"payment_code"`
	ReferenceNumber string        `json:"reference_number,omitempty" db:"reference_number"` // Mandatory for static_qris / edc
	VerifiedByUser  *uuid.UUID    `json:"verified_by_user_id,omitempty" db:"verified_by_user_id"`
	FailureCode     string        `json:"failure_code,omitempty" db:"failure_code"`
	FailureReason   string        `json:"failure_reason,omitempty" db:"failure_reason"`
	ExpiresAt       *time.Time    `json:"expires_at,omitempty" db:"expires_at"`
	PaidAt          *time.Time    `json:"paid_at,omitempty" db:"paid_at"`
	CreatedAt       time.Time     `json:"created_at" db:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at" db:"updated_at"`
}

// PaymentNotificationLog records raw incoming webhooks for audit and idempotency.
type PaymentNotificationLog struct {
	ID                uuid.UUID  `json:"id" db:"id"`
	StoreID           *uuid.UUID `json:"store_id,omitempty" db:"store_id"`
	OrderID           *uuid.UUID `json:"order_id,omitempty" db:"order_id"`
	Provider          string     `json:"provider" db:"provider"` // midtrans
	TransactionID     string     `json:"transaction_id" db:"transaction_id"`
	OrderNumber       string     `json:"order_number" db:"order_number"`
	StatusCode        string     `json:"status_code" db:"status_code"`
	TransactionStatus string     `json:"transaction_status" db:"transaction_status"`
	GrossAmount       int64      `json:"gross_amount" db:"gross_amount"`
	RawPayload        string     `json:"raw_payload" db:"raw_payload"`
	IsProcessed       bool       `json:"is_processed" db:"is_processed"`
	ProcessedAt       *time.Time `json:"processed_at,omitempty" db:"processed_at"`
	ErrorMessage      *string    `json:"error_message,omitempty" db:"error_message"`
	CreatedAt         time.Time  `json:"created_at" db:"created_at"`
}
