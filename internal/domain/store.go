package domain

import (
	"time"

	"github.com/google/uuid"
)

type Store struct {
	ID                    uuid.UUID   `json:"id" db:"id"`
	Slug                  string      `json:"slug" db:"slug"`
	Name                  string      `json:"name" db:"name"`
	Address               string      `json:"address" db:"address"`
	Phone                 string      `json:"phone" db:"phone"`
	Timezone              string      `json:"timezone" db:"timezone"`
	IsActive              bool        `json:"is_active" db:"is_active"`
	DeletedAt             *time.Time  `json:"deleted_at,omitempty" db:"deleted_at"`
	OrderPrefix           string      `json:"order_prefix" db:"order_prefix"`
	QueuePrefix           string      `json:"queue_prefix" db:"queue_prefix"`
	DefaultBillingMode    BillingMode `json:"default_billing_mode" db:"default_billing_mode"`
	ShiftMode             string      `json:"shift_mode" db:"shift_mode"`
	EnabledPaymentMethods string      `json:"enabled_payment_methods" db:"enabled_payment_methods"`
	TaxBps                int64       `json:"tax_bps" db:"tax_bps"`
	ServiceBps            int64       `json:"service_bps" db:"service_bps"`
	QrisMdrBps            int64       `json:"qris_mdr_bps" db:"qris_mdr_bps"`
	Currency              string      `json:"currency" db:"currency"`
	CreatedAt             time.Time   `json:"created_at" db:"created_at"`
	UpdatedAt             time.Time   `json:"updated_at" db:"updated_at"`
}

type UserRole string

const (
	RoleOwner   UserRole = "owner"
	RoleCashier UserRole = "cashier"
	RoleBarista UserRole = "barista"
	RoleKitchen UserRole = "kitchen"
)

func ValidUserRoles() []UserRole {
	return []UserRole{RoleOwner, RoleCashier, RoleBarista, RoleKitchen}
}

type User struct {
	ID        uuid.UUID `json:"id" db:"id"`
	StoreID   uuid.UUID `json:"store_id" db:"store_id"`
	Email     string    `json:"email" db:"email"`
	FullName  string    `json:"full_name" db:"full_name"`
	Role      UserRole  `json:"role" db:"role"`
	PinHash   string    `json:"-" db:"pin_hash"`
	IsActive  bool      `json:"is_active" db:"is_active"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

type Category struct {
	ID           uuid.UUID `json:"id" db:"id"`
	StoreID      uuid.UUID `json:"store_id" db:"store_id"`
	Name         string    `json:"name" db:"name"`
	DisplayOrder int       `json:"display_order" db:"display_order"`
	IsActive     bool      `json:"is_active" db:"is_active"`
	CreatedAt    time.Time `json:"created_at" db:"created_at"`
	UpdatedAt    time.Time `json:"updated_at" db:"updated_at"`
}

type Product struct {
	ID            uuid.UUID  `json:"id" db:"id"`
	StoreID       uuid.UUID  `json:"store_id" db:"store_id"`
	CategoryID    *uuid.UUID `json:"category_id,omitempty" db:"category_id"`
	Name          string     `json:"name" db:"name"`
	Description   string     `json:"description" db:"description"`
	BasePrice     int64      `json:"base_price" db:"base_price"`
	ImageURL      *string    `json:"image_url,omitempty" db:"image_url"`
	IsAvailable   bool       `json:"is_available" db:"is_available"`
	TrackStock    bool       `json:"track_stock" db:"track_stock"`
	StockQuantity int        `json:"stock_quantity" db:"stock_quantity"`
	CreatedAt     time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at" db:"updated_at"`
}

type ProductModifierGroup struct {
	StoreID          uuid.UUID `json:"store_id" db:"store_id"`
	ProductID        uuid.UUID `json:"product_id" db:"product_id"`
	GroupID          uuid.UUID `json:"group_id" db:"group_id"`
	DisplayOrder     int       `json:"display_order" db:"display_order"`
	OverrideRequired *bool     `json:"override_required,omitempty" db:"override_required"`
	CreatedAt        time.Time `json:"created_at" db:"created_at"`
}

type OptionModifierGroup struct {
	StoreID        uuid.UUID `json:"store_id" db:"store_id"`
	ParentOptionID uuid.UUID `json:"parent_option_id" db:"parent_option_id"`
	ChildGroupID   uuid.UUID `json:"child_group_id" db:"child_group_id"`
	DisplayOrder   int       `json:"display_order" db:"display_order"`
	CreatedAt      time.Time `json:"created_at" db:"created_at"`
}

type StoreDailySequence struct {
	StoreID      uuid.UUID `json:"store_id" db:"store_id"`
	BusinessDate time.Time `json:"business_date" db:"business_date"`
	LastSeq      int       `json:"last_seq" db:"last_seq"`
	UpdatedAt    time.Time `json:"updated_at" db:"updated_at"`
}

type OutboxEvent struct {
	ID            uuid.UUID  `json:"id" db:"id"`
	StoreID       uuid.UUID  `json:"store_id" db:"store_id"`
	EventType     string     `json:"event_type" db:"event_type"`
	AggregateType string     `json:"aggregate_type" db:"aggregate_type"`
	AggregateID   string     `json:"aggregate_id" db:"aggregate_id"`
	Payload       string     `json:"payload" db:"payload"`
	CreatedAt     time.Time  `json:"created_at" db:"created_at"`
	ProcessedAt   *time.Time `json:"processed_at,omitempty" db:"processed_at"`
	RetryCount    int        `json:"retry_count" db:"retry_count"`
	LastError     *string    `json:"last_error,omitempty" db:"last_error"`
}

type AuditLog struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	StoreID     uuid.UUID  `json:"store_id" db:"store_id"`
	ActorID     *uuid.UUID `json:"actor_id,omitempty" db:"actor_id"`
	ActorName   string     `json:"actor_name" db:"actor_name"`
	Action      string     `json:"action" db:"action"`
	Reason      string     `json:"reason" db:"reason"`
	ReferenceID string     `json:"reference_id,omitempty" db:"reference_id"`
	Metadata    string     `json:"metadata" db:"metadata"`
	CreatedAt   time.Time  `json:"created_at" db:"created_at"`
}
