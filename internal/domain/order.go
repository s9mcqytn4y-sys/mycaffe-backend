package domain

import (
	"time"

	"github.com/google/uuid"
)

type OrderStatus string

const (
	OrderStatusPendingPayment OrderStatus = "pending_payment"
	OrderStatusQueued         OrderStatus = "queued"
	OrderStatusPreparing      OrderStatus = "preparing"
	OrderStatusReady          OrderStatus = "ready"
	OrderStatusServed         OrderStatus = "served"
	OrderStatusCompleted      OrderStatus = "completed"
	OrderStatusCancelled      OrderStatus = "cancelled"
)

func ValidOrderStatuses() []OrderStatus {
	return []OrderStatus{
		OrderStatusPendingPayment,
		OrderStatusQueued,
		OrderStatusPreparing,
		OrderStatusReady,
		OrderStatusServed,
		OrderStatusCompleted,
		OrderStatusCancelled,
	}
}

func CanTransitionOrderStatus(from, to OrderStatus) bool {
	if from == to {
		return true
	}
	switch from {
	case OrderStatusPendingPayment:
		return to == OrderStatusQueued || to == OrderStatusCancelled
	case OrderStatusQueued:
		return to == OrderStatusPreparing || to == OrderStatusCancelled
	case OrderStatusPreparing:
		return to == OrderStatusReady
	case OrderStatusReady:
		return to == OrderStatusServed || to == OrderStatusCompleted
	case OrderStatusServed:
		return to == OrderStatusCompleted
	}
	return false
}

type OrderType string

const (
	OrderTypeDineIn   OrderType = "dine_in"
	OrderTypeTakeaway OrderType = "takeaway"
)

func ValidOrderTypes() []OrderType {
	return []OrderType{OrderTypeDineIn, OrderTypeTakeaway}
}

type BillingMode string

const (
	BillingModePayFirst  BillingMode = "pay_first"
	BillingModeOpenTab   BillingMode = "open_tab"
	BillingModeSplitBill BillingMode = "split_bill"
)

func ValidBillingModes() []BillingMode {
	return []BillingMode{BillingModePayFirst, BillingModeOpenTab, BillingModeSplitBill}
}

type OrderStockStatus string

const (
	OrderStockStatusNotTracked OrderStockStatus = "not_tracked"
	OrderStockStatusReserved   OrderStockStatus = "reserved"
	OrderStockStatusCommitted  OrderStockStatus = "committed"
	OrderStockStatusReleased   OrderStockStatus = "released"
)

func ValidOrderStockStatuses() []OrderStockStatus {
	return []OrderStockStatus{
		OrderStockStatusNotTracked,
		OrderStockStatusReserved,
		OrderStockStatusCommitted,
		OrderStockStatusReleased,
	}
}

type Order struct {
	ID              uuid.UUID          `json:"id" db:"id"`
	StoreID         uuid.UUID          `json:"store_id" db:"store_id"`
	IdempotencyKey  string             `json:"idempotency_key,omitempty" db:"idempotency_key"`
	RequestHash     string             `json:"request_hash,omitempty" db:"request_hash"`
	OrderNumber     string             `json:"order_number" db:"order_number"`
	QueueNumber     string             `json:"queue_number" db:"queue_number"`
	TableNumber     string             `json:"table_number" db:"table_number"`
	CustomerName    string             `json:"customer_name" db:"customer_name"`
	CustomerPhone   string             `json:"customer_phone" db:"customer_phone"`
	OrderType       OrderType          `json:"order_type" db:"order_type"`
	BillingMode     BillingMode        `json:"billing_mode" db:"billing_mode"`
	Status          OrderStatus        `json:"status" db:"status"`
	PaymentStatus   OrderPaymentStatus `json:"payment_status" db:"payment_status"`
	StockStatus     OrderStockStatus   `json:"stock_status" db:"stock_status"`
	ExpiresAt       *time.Time         `json:"expires_at,omitempty" db:"expires_at"`
	CreatedByUserID *uuid.UUID         `json:"created_by_user_id,omitempty" db:"created_by_user_id"`

	SubtotalAmount      int64 `json:"subtotal_amount" db:"subtotal_amount"`
	DiscountAmount      int64 `json:"discount_amount" db:"discount_amount"`
	TaxAmount           int64 `json:"tax_amount" db:"tax_amount"`
	ServiceChargeAmount int64 `json:"service_charge_amount" db:"service_charge_amount"`
	TotalAmount         int64 `json:"total_amount" db:"total_amount"`
	PaidAmount          int64 `json:"paid_amount" db:"paid_amount"`
	RefundedAmount      int64 `json:"refunded_amount" db:"refunded_amount"`

	Notes     string      `json:"notes,omitempty" db:"notes"`
	Items     []OrderItem `json:"items,omitempty"`
	CreatedAt time.Time   `json:"created_at" db:"created_at"`
	UpdatedAt time.Time   `json:"updated_at" db:"updated_at"`
}

type OrderItem struct {
	ID                 uuid.UUID         `json:"id" db:"id"`
	StoreID            uuid.UUID         `json:"store_id" db:"store_id"`
	OrderID            uuid.UUID         `json:"order_id" db:"order_id"`
	ProductID          *uuid.UUID        `json:"product_id,omitempty" db:"product_id"`
	VariantID          *uuid.UUID        `json:"variant_id,omitempty" db:"variant_id"`
	SnapshotVariant    string            `json:"snapshot_variant_name,omitempty" db:"snapshot_variant_name"`
	SnapshotSKU        string            `json:"snapshot_sku,omitempty" db:"snapshot_sku"`
	ConfigHash         string            `json:"config_hash" db:"config_hash"`
	SnapshotName       string            `json:"snapshot_name" db:"snapshot_name"`
	UnitBasePrice      int64             `json:"unit_base_price" db:"unit_base_price"`
	ModifierTotalPrice int64             `json:"modifier_total_price" db:"modifier_total_price"`
	SnapshotPrice      int64             `json:"snapshot_price" db:"snapshot_price"`
	Quantity           int               `json:"quantity" db:"quantity"`
	SubtotalAmount     int64             `json:"subtotal_amount" db:"subtotal_amount"`
	SelectedOptions    string            `json:"selected_options" db:"selected_options"`
	Options            []OrderItemOption `json:"options,omitempty"`
	Notes              string            `json:"notes,omitempty" db:"notes"`
	CreatedAt          time.Time         `json:"created_at" db:"created_at"`
}

type OrderItemOption struct {
	ID                      uuid.UUID  `json:"id" db:"id"`
	StoreID                 uuid.UUID  `json:"store_id" db:"store_id"`
	OrderItemID             uuid.UUID  `json:"order_item_id" db:"order_item_id"`
	ParentOrderItemOptionID *uuid.UUID `json:"parent_order_item_option_id,omitempty" db:"parent_order_item_option_id"`
	ModifierGroupID         *uuid.UUID `json:"modifier_group_id,omitempty" db:"modifier_group_id"`
	ModifierOptionID        *uuid.UUID `json:"modifier_option_id,omitempty" db:"modifier_option_id"`
	ParentOptionID          *uuid.UUID `json:"parent_option_id,omitempty" db:"parent_option_id"`
	SnapshotGroupName       string     `json:"snapshot_group_name" db:"snapshot_group_name"`
	SnapshotOptionName      string     `json:"snapshot_option_name" db:"snapshot_option_name"`
	SnapshotPriceDelta      int64      `json:"snapshot_price_delta" db:"snapshot_price_delta"`
	Quantity                int        `json:"quantity" db:"quantity"`
	CreatedAt               time.Time  `json:"created_at" db:"created_at"`
}

type ModifierGroup struct {
	ID            uuid.UUID        `json:"id" db:"id"`
	StoreID       uuid.UUID        `json:"store_id" db:"store_id"`
	Name          string           `json:"name" db:"name"`
	SelectionType string           `json:"selection_type" db:"selection_type"`
	IsRequired    bool             `json:"is_required" db:"is_required"`
	MinSelect     int              `json:"min_select" db:"min_select"`
	MaxSelect     int              `json:"max_select" db:"max_select"`
	DisplayOrder  int              `json:"display_order" db:"display_order"`
	IsActive      bool             `json:"is_active" db:"is_active"`
	Options       []ModifierOption `json:"options,omitempty"`
	CreatedAt     time.Time        `json:"created_at" db:"created_at"`
	UpdatedAt     time.Time        `json:"updated_at" db:"updated_at"`
}

type ModifierOption struct {
	ID            uuid.UUID `json:"id" db:"id"`
	StoreID       uuid.UUID `json:"store_id" db:"store_id"`
	GroupID       uuid.UUID `json:"group_id" db:"group_id"`
	Name          string    `json:"name" db:"name"`
	PriceDelta    int64     `json:"price_delta" db:"price_delta"`
	IsAvailable   bool      `json:"is_available" db:"is_available"`
	TrackStock    bool      `json:"track_stock" db:"track_stock"`
	StockQuantity int       `json:"stock_quantity" db:"stock_quantity"`
	MaxQuantity   int       `json:"max_quantity" db:"max_quantity"`
	DisplayOrder  int       `json:"display_order" db:"display_order"`
	CreatedAt     time.Time `json:"created_at" db:"created_at"`
	UpdatedAt     time.Time `json:"updated_at" db:"updated_at"`
}
