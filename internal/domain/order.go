package domain

import (
	"time"

	"github.com/google/uuid"
)

type OrderStatus string

const (
	OrderStatusPlaced    OrderStatus = "placed"    // Baru checkout
	OrderStatusQueued    OrderStatus = "queued"    // Terkonfirmasi masuk antrean KDS
	OrderStatusPreparing OrderStatus = "preparing" // Sedang dimasak/dibuat
	OrderStatusReady     OrderStatus = "ready"     // Siap diambil / diantar
	OrderStatusCompleted OrderStatus = "completed" // Selesai
	OrderStatusCancelled OrderStatus = "cancelled" // Dibatalkan
)

type OrderType string

const (
	OrderTypeDineIn   OrderType = "dine_in"
	OrderTypeTakeaway OrderType = "takeaway"
)

type Order struct {
	ID            uuid.UUID     `json:"id" db:"id"`
	StoreID       uuid.UUID     `json:"store_id" db:"store_id"`
	OrderNumber   string        `json:"order_number" db:"order_number"` // Display: e.g. ORD-20261027-001
	QueueNumber   string        `json:"queue_number" db:"queue_number"` // Display: e.g. A01
	TableNumber   string        `json:"table_number" db:"table_number"`
	CustomerName  string        `json:"customer_name" db:"customer_name"`
	CustomerPhone string        `json:"customer_phone" db:"customer_phone"`
	OrderType     OrderType     `json:"order_type" db:"order_type"`
	Status        OrderStatus   `json:"status" db:"status"`
	PaymentStatus PaymentStatus `json:"payment_status" db:"payment_status"`

	// Financials in Integer Rupiah (no float!)
	SubtotalAmount      int64 `json:"subtotal_amount" db:"subtotal_amount"`
	DiscountAmount      int64 `json:"discount_amount" db:"discount_amount"`
	TaxAmount           int64 `json:"tax_amount" db:"tax_amount"` // PB1 / Pajak Restoran
	ServiceChargeAmount int64 `json:"service_charge_amount" db:"service_charge_amount"`
	TotalAmount         int64 `json:"total_amount" db:"total_amount"`

	Notes     string      `json:"notes,omitempty" db:"notes"`
	Items     []OrderItem `json:"items,omitempty"`
	CreatedAt time.Time   `json:"created_at" db:"created_at"`
	UpdatedAt time.Time   `json:"updated_at" db:"updated_at"`
}

type OrderItem struct {
	ID              uuid.UUID `json:"id" db:"id"`
	OrderID         uuid.UUID `json:"order_id" db:"order_id"`
	ProductID       uuid.UUID `json:"product_id" db:"product_id"`
	SnapshotName    string    `json:"snapshot_name" db:"snapshot_name"`   // Historical snapshot of product name
	SnapshotPrice   int64     `json:"snapshot_price" db:"snapshot_price"` // Historical unit price in IDR
	Quantity        int       `json:"quantity" db:"quantity"`
	SubtotalAmount  int64     `json:"subtotal_amount" db:"subtotal_amount"`   // Quantity * SnapshotPrice + Modifiers
	SelectedOptions string    `json:"selected_options" db:"selected_options"` // JSON snapshot of chosen modifiers
	Notes           string    `json:"notes,omitempty" db:"notes"`
	CreatedAt       time.Time `json:"created_at" db:"created_at"`
}
