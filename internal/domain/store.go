package domain

import (
	"time"

	"github.com/google/uuid"
)

type Store struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	Slug       string     `json:"slug" db:"slug"`
	Name       string     `json:"name" db:"name"`
	Address    string     `json:"address" db:"address"`
	Phone      string     `json:"phone" db:"phone"`
	Timezone   string     `json:"timezone" db:"timezone"`
	IsActive   bool       `json:"is_active" db:"is_active"`
	DeletedAt  *time.Time `json:"deleted_at,omitempty" db:"deleted_at"`
	TaxBps     int64      `json:"tax_bps" db:"tax_bps"`         // 1000 = 10.00% PB1
	ServiceBps int64      `json:"service_bps" db:"service_bps"` // 500 = 5.00%
	QrisMdrBps int64      `json:"qris_mdr_bps" db:"qris_mdr_bps"`
	Currency   string     `json:"currency" db:"currency"`
	CreatedAt  time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at" db:"updated_at"`
}

type UserRole string

const (
	RoleOwner   UserRole = "owner"
	RoleManager UserRole = "manager"
	RoleCashier UserRole = "cashier"
	RoleBarista UserRole = "barista"
	RoleKitchen UserRole = "kitchen"
)

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
	BasePrice     int64      `json:"base_price" db:"base_price"` // Integer Rupiah
	ImageURL      *string    `json:"image_url,omitempty" db:"image_url"`
	IsAvailable   bool       `json:"is_available" db:"is_available"`
	TrackStock    bool       `json:"track_stock" db:"track_stock"`
	StockQuantity int        `json:"stock_quantity" db:"stock_quantity"`
	CreatedAt     time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at" db:"updated_at"`
}

type StoreDailySequence struct {
	StoreID      uuid.UUID `json:"store_id" db:"store_id"`
	BusinessDate time.Time `json:"business_date" db:"business_date"`
	LastSeq      int       `json:"last_seq" db:"last_seq"`
	UpdatedAt    time.Time `json:"updated_at" db:"updated_at"`
}
