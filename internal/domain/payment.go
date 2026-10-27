package domain

import (
	"time"

	"github.com/google/uuid"
)

type PaymentStatus string

const (
	PaymentStatusPending           PaymentStatus = "pending"
	PaymentStatusPaid              PaymentStatus = "paid"
	PaymentStatusFailed            PaymentStatus = "failed"
	PaymentStatusExpired           PaymentStatus = "expired"
	PaymentStatusPartiallyRefunded PaymentStatus = "partially_refunded"
	PaymentStatusRefunded          PaymentStatus = "refunded"
)

type PaymentMethod string

const (
	PaymentMethodQRIS         PaymentMethod = "qris"
	PaymentMethodBankTransfer PaymentMethod = "bank_transfer"
	PaymentMethodCash         PaymentMethod = "cash"
)

// Payment represents a tenant-scoped payment transaction
type Payment struct {
	ID              uuid.UUID     `json:"id" db:"id"`
	StoreID         uuid.UUID     `json:"store_id" db:"store_id"`
	OrderID         uuid.UUID     `json:"order_id" db:"order_id"`
	ProviderOrderID string        `json:"provider_order_id" db:"provider_order_id"` // Stable Midtrans order_id
	Amount          int64         `json:"amount" db:"amount"`                       // Integer Rupiah
	Method          PaymentMethod `json:"method" db:"method"`
	Status          PaymentStatus `json:"status" db:"status"`
	TransactionID   string        `json:"transaction_id,omitempty" db:"transaction_id"` // Midtrans transaction_id
	PaymentType     string        `json:"payment_type,omitempty" db:"payment_type"`     // qris, bank_transfer, etc.
	QRString        string        `json:"qr_string,omitempty" db:"qr_string"`           // For dynamic QRIS
	VANumber        string        `json:"va_number,omitempty" db:"va_number"`           // For Virtual Account
	Bank            string        `json:"bank,omitempty" db:"bank"`
	PaidAt          *time.Time    `json:"paid_at,omitempty" db:"paid_at"`
	CreatedAt       time.Time     `json:"created_at" db:"created_at"`
	UpdatedAt       time.Time     `json:"updated_at" db:"updated_at"`
}

// PaymentNotificationLog records raw incoming webhooks for audit and idempotency
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
