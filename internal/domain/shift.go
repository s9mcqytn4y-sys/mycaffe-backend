package domain

import (
	"time"

	"github.com/google/uuid"
)

type ShiftStatus string

const (
	ShiftStatusOpen        ShiftStatus = "open"
	ShiftStatusClosed      ShiftStatus = "closed"
	ShiftStatusForceClosed ShiftStatus = "force_closed"
)

type CashMovementType string

const (
	CashMovementOpeningFloat CashMovementType = "opening_float"
	CashMovementSale         CashMovementType = "sale"
	CashMovementRefund       CashMovementType = "refund"
	CashMovementPaidOut      CashMovementType = "paid_out"
	CashMovementCashIn       CashMovementType = "cash_in"
	CashMovementNoSale       CashMovementType = "no_sale"
	CashMovementTip          CashMovementType = "tip"
	CashMovementStaffMeal    CashMovementType = "staff_meal"
)

type CashDrawer struct {
	ID        uuid.UUID `json:"id" db:"id"`
	StoreID   uuid.UUID `json:"store_id" db:"store_id"`
	Name      string    `json:"name" db:"name"`
	IsActive  bool      `json:"is_active" db:"is_active"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`
}

type Shift struct {
	ID              uuid.UUID   `json:"id" db:"id"`
	StoreID         uuid.UUID   `json:"store_id" db:"store_id"`
	DrawerID        uuid.UUID   `json:"drawer_id" db:"drawer_id"`
	OpenedBy        uuid.UUID   `json:"opened_by" db:"opened_by"`
	OpeningFloat    int64       `json:"opening_float" db:"opening_float"`
	OpenedAt        time.Time   `json:"opened_at" db:"opened_at"`
	ClosedBy        *uuid.UUID  `json:"closed_by,omitempty" db:"closed_by"`
	ClosedAt        *time.Time  `json:"closed_at,omitempty" db:"closed_at"`
	Status          ShiftStatus `json:"status" db:"status"`
	ExpectedCash    *int64      `json:"expected_cash,omitempty" db:"expected_cash"`
	CountedCash     *int64      `json:"counted_cash,omitempty" db:"counted_cash"`
	Variance        *int64      `json:"variance,omitempty" db:"variance"`
	VarianceReason  *string     `json:"variance_reason,omitempty" db:"variance_reason"`
	OwnerOverrideBy *uuid.UUID  `json:"owner_override_by,omitempty" db:"owner_override_by"`
	Notes           *string     `json:"notes,omitempty" db:"notes"`
	CreatedAt       time.Time   `json:"created_at" db:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at" db:"updated_at"`
}

type CashMovement struct {
	ID           uuid.UUID        `json:"id" db:"id"`
	StoreID      uuid.UUID        `json:"store_id" db:"store_id"`
	ShiftID      uuid.UUID        `json:"shift_id" db:"shift_id"`
	MovementType CashMovementType `json:"movement_type" db:"movement_type"`
	Amount       int64            `json:"amount" db:"amount"`
	ActorID      uuid.UUID        `json:"actor_id" db:"actor_id"`
	Reason       *string          `json:"reason,omitempty" db:"reason"`
	OrderID      *uuid.UUID       `json:"order_id,omitempty" db:"order_id"`
	PaymentID    *uuid.UUID       `json:"payment_id,omitempty" db:"payment_id"`
	CreatedAt    time.Time        `json:"created_at" db:"created_at"`
}

type ShiftCashSummary struct {
	OpeningFloat   int64 `json:"opening_float"`
	CashSales      int64 `json:"cash_sales"`
	CashRefunds    int64 `json:"cash_refunds"`
	RefundCount    int   `json:"refund_count"`
	PaidOuts       int64 `json:"paid_outs"`
	CashIns        int64 `json:"cash_ins"`
	Tips           int64 `json:"tips"`
	StaffMeals     int64 `json:"staff_meals"`
	NoSaleCount    int   `json:"no_sale_count"`
	TotalMovements int   `json:"total_movements"`
}

// ExpectedCash calculates current total physical cash present in the drawer.
func (s *ShiftCashSummary) ExpectedCash(openingFloat int64) int64 {
	return openingFloat + s.CashSales + s.Tips + s.CashIns - s.CashRefunds - s.PaidOuts - s.StaffMeals
}

type UnsettledTabSummary struct {
	OrderNumber           string    `json:"order_number"`
	Label                 string    `json:"label"`
	Status                string    `json:"status"`
	TotalAmount           int64     `json:"total_amount"`
	CreatedAt             time.Time `json:"created_at"`
	IsOverdueBusinessDate bool      `json:"is_overdue_business_date"`
}

type XReport struct {
	ShiftID                      uuid.UUID             `json:"shift_id"`
	StoreID                      uuid.UUID             `json:"store_id"`
	DrawerID                     uuid.UUID             `json:"drawer_id"`
	DrawerName                   string                `json:"drawer_name"`
	OpenedBy                     uuid.UUID             `json:"opened_by"`
	OpenedByName                 string                `json:"opened_by_name"`
	OpenedAt                     time.Time             `json:"opened_at"`
	GeneratedAt                  time.Time             `json:"generated_at"`
	Status                       ShiftStatus           `json:"status"`
	Summary                      ShiftCashSummary      `json:"summary"`
	ExpectedCash                 *int64                `json:"expected_cash,omitempty"` // disamarkan (nil) jika dibaca kasir saat shift open
	ExpectedRedacted             bool                  `json:"expected_redacted"`
	UnsettledOpenTabsCount       int                   `json:"unsettled_open_tabs_count"`
	UnsettledOpenTabsTotalAmount int64                 `json:"unsettled_open_tabs_total_amount"`
	UnsettledOpenTabs            []UnsettledTabSummary `json:"unsettled_open_tabs,omitempty"`
}

type ZReport struct {
	ShiftID                      uuid.UUID             `json:"shift_id"`
	StoreID                      uuid.UUID             `json:"store_id"`
	DrawerID                     uuid.UUID             `json:"drawer_id"`
	DrawerName                   string                `json:"drawer_name"`
	OpenedBy                     uuid.UUID             `json:"opened_by"`
	OpenedByName                 string                `json:"opened_by_name"`
	OpenedAt                     time.Time             `json:"opened_at"`
	ClosedBy                     *uuid.UUID            `json:"closed_by,omitempty"`
	ClosedByName                 *string               `json:"closed_by_name,omitempty"`
	ClosedAt                     *time.Time            `json:"closed_at,omitempty"`
	Status                       ShiftStatus           `json:"status"`
	Summary                      ShiftCashSummary      `json:"summary"`
	ExpectedCash                 int64                 `json:"expected_cash"`
	CountedCash                  int64                 `json:"counted_cash"`
	Variance                     int64                 `json:"variance"`
	VarianceReason               *string               `json:"variance_reason,omitempty"`
	OwnerOverrideBy              *uuid.UUID            `json:"owner_override_by,omitempty"`
	OverrideByName               *string               `json:"override_by_name,omitempty"`
	Notes                        *string               `json:"notes,omitempty"`
	UnsettledOpenTabsCount       int                   `json:"unsettled_open_tabs_count"`
	UnsettledOpenTabsTotalAmount int64                 `json:"unsettled_open_tabs_total_amount"`
	UnsettledOpenTabs            []UnsettledTabSummary `json:"unsettled_open_tabs,omitempty"`
}
