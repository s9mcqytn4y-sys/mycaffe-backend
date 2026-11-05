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

// Table timer states on the board (W6).
const (
	TimerNone    = "none"
	TimerOK      = "ok"
	TimerWarning = "warning"
	TimerExpired = "expired"
)

// TimerState derives the board state of a session timer at now.
func (s *TableSession) TimerState(now time.Time, warningMinutes int) string {
	switch {
	case s == nil || s.EndsAt == nil:
		return TimerNone
	case !now.Before(*s.EndsAt):
		return TimerExpired
	case !now.Before(s.EndsAt.Add(-time.Duration(warningMinutes) * time.Minute)):
		return TimerWarning
	}
	return TimerOK
}

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
	// Table timer (W6): ends_at is set when the session opens under a store time limit.
	EndsAt          *time.Time `json:"ends_at,omitempty" db:"ends_at"`
	WarnedAt        *time.Time `json:"warned_at,omitempty" db:"warned_at"`
	ExpiredAt       *time.Time `json:"expired_at,omitempty" db:"expired_at"`
	ExtendedMinutes int        `json:"extended_minutes" db:"extended_minutes"`
}

// TableBoardEntry is one row of the cashier's table board.
type TableBoardEntry struct {
	Table
	Status       string        `json:"status"`
	Session      *TableSession `json:"session,omitempty"`
	UnpaidOrders int           `json:"unpaid_orders"`
	UnpaidAmount int64         `json:"unpaid_amount"`
	// MergedIntoTableID is set when this table is merged into the session of another table (W5).
	MergedIntoTableID *uuid.UUID `json:"merged_into_table_id,omitempty"`
	// TimerState is none, ok, warning or expired, derived at read time from the session ends_at.
	TimerState string `json:"timer_state"`
}
