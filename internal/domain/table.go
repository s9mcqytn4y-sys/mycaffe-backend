package domain

import (
	"time"

	"github.com/google/uuid"
)

// Table is a dine-in table with a printed QR (decision QR1: opaque token, rotated by the owner).
type Table struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	StoreID     uuid.UUID  `json:"-" db:"store_id"`
	TableNumber string     `json:"table_number" db:"table_number"`
	Label       string     `json:"label" db:"label"`
	Section     string     `json:"section" db:"section"`
	Capacity    int        `json:"capacity" db:"capacity"`
	QRToken     string     `json:"qr_token,omitempty" db:"qr_token"`
	QRRotatedAt time.Time  `json:"qr_rotated_at" db:"qr_rotated_at"`
	IsActive    bool       `json:"is_active" db:"is_active"`
	ArchivedAt  *time.Time `json:"archived_at,omitempty" db:"archived_at"`
	CreatedAt   time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at" db:"updated_at"`
}

// Table occupancy is derived from table sessions (decision T1), never stored on the table.
const (
	TableStatusAvailable = "available"
	TableStatusOccupied  = "occupied"
	TableStatusSettled   = "settled" // all orders paid and served; auto-close countdown running
)

const (
	TableSessionOpen   = "open"
	TableSessionClosed = "closed"
)

type TableSession struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	StoreID        uuid.UUID  `json:"-" db:"store_id"`
	TableID        uuid.UUID  `json:"table_id" db:"table_id"`
	Status         string     `json:"status" db:"status"`
	OpenedAt       time.Time  `json:"opened_at" db:"opened_at"`
	OpenedVia      string     `json:"opened_via" db:"opened_via"`
	OpenedByUserID *uuid.UUID `json:"opened_by_user_id,omitempty" db:"opened_by_user_id"`
	SettledAt      *time.Time `json:"settled_at,omitempty" db:"settled_at"`
	ClosedAt       *time.Time `json:"closed_at,omitempty" db:"closed_at"`
	ClosedByUserID *uuid.UUID `json:"closed_by_user_id,omitempty" db:"closed_by_user_id"`
	CloseReason    *string    `json:"close_reason,omitempty" db:"close_reason"`
}

// TableBoardEntry is one row of the cashier's table board.
type TableBoardEntry struct {
	Table
	Status       string        `json:"status"`
	Session      *TableSession `json:"session,omitempty"`
	UnpaidOrders int           `json:"unpaid_orders"`
	UnpaidAmount int64         `json:"unpaid_amount"`
}
