package domain

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// PaymentAttemptStatus represents the lifecycle state of an individual payment attempt in `payments`.
type PaymentAttemptStatus string

// PaymentStatus is an alias for PaymentAttemptStatus for backward compatibility across callers.
type PaymentStatus = PaymentAttemptStatus

const (
	PaymentStatusUnpaid            PaymentAttemptStatus = "unpaid"
	PaymentStatusPending           PaymentAttemptStatus = "pending"
	PaymentStatusAuthorized        PaymentAttemptStatus = "authorized"
	PaymentStatusPartiallyPaid     PaymentAttemptStatus = "partially_paid"
	PaymentStatusPaid              PaymentAttemptStatus = "paid"
	PaymentStatusFailed            PaymentAttemptStatus = "failed"
	PaymentStatusCancelled         PaymentAttemptStatus = "cancelled"
	PaymentStatusExpired           PaymentAttemptStatus = "expired"
	PaymentStatusPartiallyRefunded PaymentAttemptStatus = "partially_refunded"
	PaymentStatusRefunded          PaymentAttemptStatus = "refunded"
	PaymentStatusChargeback        PaymentAttemptStatus = "chargeback"
	PaymentStatusPartialChargeback PaymentAttemptStatus = "partial_chargeback"
)

// OrderPaymentStatus represents the aggregate financial status on `orders.payment_status`.
type OrderPaymentStatus string

const (
	OrderPaymentStatusUnpaid            OrderPaymentStatus = "unpaid"
	OrderPaymentStatusPending           OrderPaymentStatus = "pending"
	OrderPaymentStatusAuthorized        OrderPaymentStatus = "authorized"
	OrderPaymentStatusPartiallyPaid     OrderPaymentStatus = "partially_paid"
	OrderPaymentStatusPaid              OrderPaymentStatus = "paid"
	OrderPaymentStatusFailed            OrderPaymentStatus = "failed"
	OrderPaymentStatusCancelled         OrderPaymentStatus = "cancelled"
	OrderPaymentStatusExpired           OrderPaymentStatus = "expired"
	OrderPaymentStatusPartiallyRefunded OrderPaymentStatus = "partially_refunded"
	OrderPaymentStatusRefunded          OrderPaymentStatus = "refunded"
	OrderPaymentStatusChargeback        OrderPaymentStatus = "chargeback"
	OrderPaymentStatusPartialChargeback OrderPaymentStatus = "partial_chargeback"
)

// SplitBillStatus represents the status of an individual split bill in `payment_splits.status`.
type SplitBillStatus string

const (
	SplitBillStatusUnpaid            SplitBillStatus = "unpaid"
	SplitBillStatusPending           SplitBillStatus = "pending"
	SplitBillStatusAuthorized        SplitBillStatus = "authorized"
	SplitBillStatusPartiallyPaid     SplitBillStatus = "partially_paid"
	SplitBillStatusPaid              SplitBillStatus = "paid"
	SplitBillStatusFailed            SplitBillStatus = "failed"
	SplitBillStatusCancelled         SplitBillStatus = "cancelled"
	SplitBillStatusExpired           SplitBillStatus = "expired"
	SplitBillStatusPartiallyRefunded SplitBillStatus = "partially_refunded"
	SplitBillStatusRefunded          SplitBillStatus = "refunded"
	SplitBillStatusChargeback        SplitBillStatus = "chargeback"
	SplitBillStatusPartialChargeback SplitBillStatus = "partial_chargeback"
)

// CanTransitionPaymentStatus enforces the payment attempt state machine so out-of-order
// webhooks (e.g. a delayed "pending" arriving after "settlement") never regress a settled payment.
func CanTransitionPaymentStatus(from, to PaymentAttemptStatus) bool {
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
		case PaymentStatusPartiallyRefunded, PaymentStatusRefunded, PaymentStatusPartialChargeback, PaymentStatusChargeback:
			return true
		}
	case PaymentStatusPartiallyRefunded:
		switch to {
		case PaymentStatusRefunded, PaymentStatusPartialChargeback, PaymentStatusChargeback:
			return true
		}
	case PaymentStatusPartialChargeback:
		switch to {
		case PaymentStatusChargeback, PaymentStatusRefunded:
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

type SplitMode string

const (
	SplitModeFull         SplitMode = "full"
	SplitModeEqual        SplitMode = "equal"
	SplitModeByItem       SplitMode = "by_item"
	SplitModeCustomAmount SplitMode = "custom_amount"
)

// PaymentSplit represents an individual bill portion under a parent Order (`payment_splits`).
type PaymentSplit struct {
	ID                  uuid.UUID          `json:"id" db:"id"`
	StoreID             uuid.UUID          `json:"store_id" db:"store_id"`
	OrderID             uuid.UUID          `json:"order_id" db:"order_id"`
	SplitNumber         int                `json:"split_number" db:"split_number"`
	Label               string             `json:"label,omitempty" db:"label"`
	SplitMode           SplitMode          `json:"split_mode" db:"split_mode"`
	SubtotalAmount      int64              `json:"subtotal_amount" db:"subtotal_amount"`
	TaxAmount           int64              `json:"tax_amount" db:"tax_amount"`
	ServiceChargeAmount int64              `json:"service_charge_amount" db:"service_charge_amount"`
	TotalAmount         int64              `json:"total_amount" db:"total_amount"`
	PaidAmount          int64              `json:"paid_amount" db:"paid_amount"`
	Status              SplitBillStatus    `json:"status" db:"status"`
	Items               []PaymentSplitItem `json:"items,omitempty"`
	CreatedAt           time.Time          `json:"created_at" db:"created_at"`
	UpdatedAt           time.Time          `json:"updated_at" db:"updated_at"`
}

// PaymentSplitItem links a specific quantity of an OrderItem to a PaymentSplit (`payment_split_items`).
type PaymentSplitItem struct {
	ID              uuid.UUID `json:"id" db:"id"`
	StoreID         uuid.UUID `json:"store_id" db:"store_id"`
	SplitID         uuid.UUID `json:"split_id" db:"split_id"`
	OrderItemID     uuid.UUID `json:"order_item_id" db:"order_item_id"`
	Quantity        int       `json:"quantity" db:"quantity"`
	AllocatedAmount int64     `json:"allocated_amount" db:"allocated_amount"`
	CreatedAt       time.Time `json:"created_at" db:"created_at"`
}

// ValidateSplitAllocations enforces mathematical and item-quantity invariants across split bills:
// 1. Every split must have TotalAmount > 0, PaidAmount >= 0, and PaidAmount <= TotalAmount.
// 2. The sum of all splits' TotalAmount must equal orderTotal exactly (no lost or invented Rupiah).
// 3. For item allocations, no OrderItem can be allocated beyond its purchased Quantity.
func ValidateSplitAllocations(orderTotal int64, orderItems []OrderItem, splits []PaymentSplit) error {
	if orderTotal <= 0 {
		return errors.New("order total must be strictly positive")
	}
	if len(splits) == 0 {
		return errors.New("at least one payment split is required")
	}

	itemMaxQty := make(map[uuid.UUID]int, len(orderItems))
	for _, item := range orderItems {
		itemMaxQty[item.ID] = item.Quantity
	}
	allocatedQty := make(map[uuid.UUID]int, len(orderItems))

	var sumSplitTotals int64
	seenSplitNumbers := make(map[int]bool, len(splits))

	for _, sp := range splits {
		if sp.SplitNumber <= 0 {
			return fmt.Errorf("split_number must be positive, got %d", sp.SplitNumber)
		}
		if seenSplitNumbers[sp.SplitNumber] {
			return fmt.Errorf("duplicate split_number %d in order splits", sp.SplitNumber)
		}
		seenSplitNumbers[sp.SplitNumber] = true

		if sp.TotalAmount <= 0 {
			return fmt.Errorf("split #%d total_amount must be strictly positive, got %d", sp.SplitNumber, sp.TotalAmount)
		}
		if sp.PaidAmount < 0 || sp.PaidAmount > sp.TotalAmount {
			return fmt.Errorf("split #%d paid_amount (%d) out of bounds [0, %d]", sp.SplitNumber, sp.PaidAmount, sp.TotalAmount)
		}
		if sp.SubtotalAmount+sp.TaxAmount+sp.ServiceChargeAmount != sp.TotalAmount {
			return fmt.Errorf("split #%d components (%d + %d + %d) do not equal total_amount (%d)",
				sp.SplitNumber, sp.SubtotalAmount, sp.TaxAmount, sp.ServiceChargeAmount, sp.TotalAmount)
		}

		sumSplitTotals += sp.TotalAmount

		var itemAllocSum int64
		for _, item := range sp.Items {
			if item.Quantity <= 0 {
				return fmt.Errorf("split #%d item quantity must be positive", sp.SplitNumber)
			}
			if item.AllocatedAmount < 0 {
				return fmt.Errorf("split #%d item allocated_amount cannot be negative", sp.SplitNumber)
			}
			maxQ, exists := itemMaxQty[item.OrderItemID]
			if !exists {
				return fmt.Errorf("split #%d references unknown order_item_id %s", sp.SplitNumber, item.OrderItemID)
			}
			allocatedQty[item.OrderItemID] += item.Quantity
			if allocatedQty[item.OrderItemID] > maxQ {
				return fmt.Errorf("order_item_id %s over-allocated across splits: allocated %d > purchased %d",
					item.OrderItemID, allocatedQty[item.OrderItemID], maxQ)
			}
			itemAllocSum += item.AllocatedAmount
		}
		if sp.SplitMode == SplitModeByItem && len(sp.Items) > 0 && itemAllocSum != sp.SubtotalAmount {
			return fmt.Errorf("split #%d by_item allocated sum (%d) does not match split subtotal_amount (%d)",
				sp.SplitNumber, itemAllocSum, sp.SubtotalAmount)
		}
	}

	if sumSplitTotals != orderTotal {
		return fmt.Errorf("sum of split total_amounts (%d) does not equal order total_amount (%d)", sumSplitTotals, orderTotal)
	}

	return nil
}

// Payment represents a tenant-scoped payment transaction attempt (`payments`).
type Payment struct {
	ID                uuid.UUID            `json:"id" db:"id"`
	StoreID           uuid.UUID            `json:"store_id" db:"store_id"`
	OrderID           uuid.UUID            `json:"order_id" db:"order_id"`
	SplitID           *uuid.UUID           `json:"split_id,omitempty" db:"split_id"`
	AttemptNo         int                  `json:"attempt_no" db:"attempt_no"`
	IdempotencyKey    string               `json:"idempotency_key" db:"idempotency_key"`
	Provider          string               `json:"provider" db:"provider"`
	ProviderOrderID   string               `json:"provider_order_id" db:"provider_order_id"` // Format: {order_number}-S{split}-A{attempt}
	Amount            int64                `json:"amount" db:"amount"`                       // Integer Rupiah
	RefundedAmount    int64                `json:"refunded_amount" db:"refunded_amount"`     // Integer Rupiah
	Method            PaymentMethod        `json:"method" db:"method"`
	Status            PaymentAttemptStatus `json:"status" db:"status"`
	RawProviderStatus string               `json:"raw_provider_status,omitempty" db:"raw_provider_status"`
	FraudStatus       string               `json:"fraud_status,omitempty" db:"fraud_status"`
	TransactionID     string               `json:"transaction_id,omitempty" db:"transaction_id"` // Midtrans transaction_id
	PaymentType       string               `json:"payment_type,omitempty" db:"payment_type"`     // qris, bank_transfer, gopay, shopeepay, card, cstore
	Acquirer          string               `json:"acquirer,omitempty" db:"acquirer"`
	QRString          string               `json:"qr_string,omitempty" db:"qr_string"` // For dynamic QRIS
	DeeplinkURL       string               `json:"deeplink_url,omitempty" db:"deeplink_url"`
	VANumber          string               `json:"va_number,omitempty" db:"va_number"` // For Virtual Account
	Bank              string               `json:"bank,omitempty" db:"bank"`
	BillerCode        string               `json:"biller_code,omitempty" db:"biller_code"` // For Mandiri Bill
	BillKey           string               `json:"bill_key,omitempty" db:"bill_key"`       // For Mandiri Bill
	PaymentCode       string               `json:"payment_code,omitempty" db:"payment_code"`
	ReconciliationRef string               `json:"reconciliation_ref,omitempty" db:"reconciliation_ref"` // Mandatory for static_qris / edc
	VerifiedByUserID  *uuid.UUID           `json:"verified_by_user_id,omitempty" db:"verified_by_user_id"`
	FailureCode       string               `json:"failure_code,omitempty" db:"failure_code"`
	FailureReason     string               `json:"failure_reason,omitempty" db:"failure_reason"`
	PaidAt            *time.Time           `json:"paid_at,omitempty" db:"paid_at"`
	ExpiresAt         *time.Time           `json:"expires_at,omitempty" db:"expires_at"`
	CreatedAt         time.Time            `json:"created_at" db:"created_at"`
	UpdatedAt         time.Time            `json:"updated_at" db:"updated_at"`
}

// PaymentNotificationLog records raw incoming webhooks for audit and idempotency (`payment_notification_logs`).
type PaymentNotificationLog struct {
	ID                uuid.UUID  `json:"id" db:"id"`
	StoreID           uuid.UUID  `json:"store_id" db:"store_id"`
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
