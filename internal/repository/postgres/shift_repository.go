package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
)

var (
	ErrDrawerNotFound = errors.New("cash drawer not found")
	ErrShiftNotFound  = errors.New("shift not found")
	ErrNoOpenShift    = errors.New("no open shift found")
)

type ShiftRepository struct{}

func NewShiftRepository() *ShiftRepository {
	return &ShiftRepository{}
}

// ---------- Cash Drawers ----------

func (r *ShiftRepository) InsertCashDrawer(ctx context.Context, tx *sqlx.Tx, d *domain.CashDrawer) error {
	query := `
		INSERT INTO cash_drawers (id, store_id, name, is_active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`
	_, err := tx.ExecContext(ctx, query, d.ID, d.StoreID, d.Name, d.IsActive, d.CreatedAt, d.UpdatedAt)
	return err
}

func (r *ShiftRepository) GetCashDrawer(ctx context.Context, tx *sqlx.Tx, storeID, id uuid.UUID) (*domain.CashDrawer, error) {
	var d domain.CashDrawer
	query := `
		SELECT id, store_id, name, is_active, created_at, updated_at
		FROM cash_drawers
		WHERE store_id = $1 AND id = $2
	`
	err := tx.GetContext(ctx, &d, query, storeID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDrawerNotFound
	}
	return &d, err
}

func (r *ShiftRepository) ListCashDrawers(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID) ([]domain.CashDrawer, error) {
	var ds []domain.CashDrawer
	query := `
		SELECT id, store_id, name, is_active, created_at, updated_at
		FROM cash_drawers
		WHERE store_id = $1
		ORDER BY created_at ASC
	`
	err := tx.SelectContext(ctx, &ds, query, storeID)
	return ds, err
}

func (r *ShiftRepository) UpdateCashDrawer(ctx context.Context, tx *sqlx.Tx, d *domain.CashDrawer) error {
	query := `
		UPDATE cash_drawers
		SET name = $1, is_active = $2, updated_at = $3
		WHERE store_id = $4 AND id = $5
	`
	res, err := tx.ExecContext(ctx, query, d.Name, d.IsActive, d.UpdatedAt, d.StoreID, d.ID)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrDrawerNotFound
	}
	return nil
}

func (r *ShiftRepository) GetDefaultCashDrawer(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID) (*domain.CashDrawer, error) {
	var d domain.CashDrawer
	query := `
		SELECT id, store_id, name, is_active, created_at, updated_at
		FROM cash_drawers
		WHERE store_id = $1 AND is_active = true
		ORDER BY created_at ASC
		LIMIT 1
	`
	err := tx.GetContext(ctx, &d, query, storeID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrDrawerNotFound
	}
	return &d, err
}

// ---------- Shifts ----------

func (r *ShiftRepository) InsertShift(ctx context.Context, tx *sqlx.Tx, s *domain.Shift) error {
	query := `
		INSERT INTO shifts (
			id, store_id, drawer_id, opened_by, opening_float, opened_at,
			status, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9
		)
	`
	_, err := tx.ExecContext(ctx, query,
		s.ID, s.StoreID, s.DrawerID, s.OpenedBy, s.OpeningFloat, s.OpenedAt,
		s.Status, s.CreatedAt, s.UpdatedAt,
	)
	return err
}

func (r *ShiftRepository) GetShiftForUpdate(ctx context.Context, tx *sqlx.Tx, storeID, id uuid.UUID) (*domain.Shift, error) {
	var s domain.Shift
	query := `
		SELECT id, store_id, drawer_id, opened_by, opening_float, opened_at,
		       closed_by, closed_at, status, expected_cash, counted_cash, variance,
		       variance_reason, owner_override_by, notes, created_at, updated_at
		FROM shifts
		WHERE store_id = $1 AND id = $2
		FOR UPDATE
	`
	err := tx.GetContext(ctx, &s, query, storeID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrShiftNotFound
	}
	return &s, err
}

func (r *ShiftRepository) GetShiftByID(ctx context.Context, tx *sqlx.Tx, storeID, id uuid.UUID) (*domain.Shift, error) {
	var s domain.Shift
	query := `
		SELECT id, store_id, drawer_id, opened_by, opening_float, opened_at,
		       closed_by, closed_at, status, expected_cash, counted_cash, variance,
		       variance_reason, owner_override_by, notes, created_at, updated_at
		FROM shifts
		WHERE store_id = $1 AND id = $2
	`
	err := tx.GetContext(ctx, &s, query, storeID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrShiftNotFound
	}
	return &s, err
}

func (r *ShiftRepository) GetOpenShiftByDrawer(ctx context.Context, tx *sqlx.Tx, storeID, drawerID uuid.UUID) (*domain.Shift, error) {
	var s domain.Shift
	query := `
		SELECT id, store_id, drawer_id, opened_by, opening_float, opened_at,
		       closed_by, closed_at, status, expected_cash, counted_cash, variance,
		       variance_reason, owner_override_by, notes, created_at, updated_at
		FROM shifts
		WHERE store_id = $1 AND drawer_id = $2 AND status = 'open'
		LIMIT 1
	`
	err := tx.GetContext(ctx, &s, query, storeID, drawerID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoOpenShift
	}
	return &s, err
}

// GetActiveShiftForStaff resolves an active ('open') shift for a cashier or default drawer.
// If drawerID is specified, it strictly retrieves the open shift on that drawer.
// If drawerID is nil, it first looks for any open shift opened by the user,
// and falls back to an open shift on the store's default drawer.
func (r *ShiftRepository) GetActiveShiftForStaff(ctx context.Context, tx *sqlx.Tx, storeID, userID uuid.UUID, drawerID *uuid.UUID) (*domain.Shift, error) {
	if drawerID != nil && *drawerID != uuid.Nil {
		return r.GetOpenShiftByDrawer(ctx, tx, storeID, *drawerID)
	}

	// 1. Look for an open shift explicitly opened by this user
	var s domain.Shift
	queryByUser := `
		SELECT id, store_id, drawer_id, opened_by, opening_float, opened_at,
		       closed_by, closed_at, status, expected_cash, counted_cash, variance,
		       variance_reason, owner_override_by, notes, created_at, updated_at
		FROM shifts
		WHERE store_id = $1 AND opened_by = $2 AND status = 'open'
		ORDER BY opened_at DESC
		LIMIT 1
	`
	err := tx.GetContext(ctx, &s, queryByUser, storeID, userID)
	if err == nil {
		return &s, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	// 2. Fallback: check if the default drawer has an open shift
	defaultDrawer, errDefault := r.GetDefaultCashDrawer(ctx, tx, storeID)
	if errDefault == nil && defaultDrawer != nil {
		sDefault, errShift := r.GetOpenShiftByDrawer(ctx, tx, storeID, defaultDrawer.ID)
		if errShift == nil {
			return sDefault, nil
		}
	}

	return nil, ErrNoOpenShift
}

func (r *ShiftRepository) UpdateShift(ctx context.Context, tx *sqlx.Tx, s *domain.Shift) error {
	query := `
		UPDATE shifts
		SET closed_by = $1, closed_at = $2, status = $3,
		    expected_cash = $4, counted_cash = $5, variance = $6,
		    variance_reason = $7, owner_override_by = $8, notes = $9,
		    updated_at = $10
		WHERE store_id = $11 AND id = $12
	`
	res, err := tx.ExecContext(ctx, query,
		s.ClosedBy, s.ClosedAt, s.Status,
		s.ExpectedCash, s.CountedCash, s.Variance,
		s.VarianceReason, s.OwnerOverrideBy, s.Notes,
		s.UpdatedAt, s.StoreID, s.ID,
	)
	if err != nil {
		return err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return ErrShiftNotFound
	}
	return nil
}

// ---------- Cash Movements ----------

func (r *ShiftRepository) InsertCashMovement(ctx context.Context, tx *sqlx.Tx, m *domain.CashMovement) error {
	query := `
		INSERT INTO cash_movements (
			id, store_id, shift_id, movement_type, amount,
			actor_id, reason, order_id, payment_id, created_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10
		)
	`
	_, err := tx.ExecContext(ctx, query,
		m.ID, m.StoreID, m.ShiftID, m.MovementType, m.Amount,
		m.ActorID, m.Reason, m.OrderID, m.PaymentID, m.CreatedAt,
	)
	return err
}

func (r *ShiftRepository) ListCashMovementsByShift(ctx context.Context, tx *sqlx.Tx, storeID, shiftID uuid.UUID) ([]domain.CashMovement, error) {
	var ms []domain.CashMovement
	query := `
		SELECT id, store_id, shift_id, movement_type, amount,
		       actor_id, reason, order_id, payment_id, created_at
		FROM cash_movements
		WHERE store_id = $1 AND shift_id = $2
		ORDER BY created_at ASC
	`
	err := tx.SelectContext(ctx, &ms, query, storeID, shiftID)
	return ms, err
}

func (r *ShiftRepository) AggregateCashTotalsByShift(ctx context.Context, tx *sqlx.Tx, storeID, shiftID uuid.UUID) (*domain.ShiftCashSummary, error) {
	summary := &domain.ShiftCashSummary{}

	query := `
		SELECT movement_type, COALESCE(SUM(amount), 0) AS total_amount, COUNT(*) AS count
		FROM cash_movements
		WHERE store_id = $1 AND shift_id = $2
		GROUP BY movement_type
	`
	type row struct {
		MovementType string `db:"movement_type"`
		TotalAmount  int64  `db:"total_amount"`
		Count        int    `db:"count"`
	}
	var rows []row
	if err := tx.SelectContext(ctx, &rows, query, storeID, shiftID); err != nil {
		return nil, err
	}

	for _, ro := range rows {
		summary.TotalMovements += ro.Count
		switch domain.CashMovementType(ro.MovementType) {
		case domain.CashMovementOpeningFloat:
			summary.OpeningFloat = ro.TotalAmount
		case domain.CashMovementSale:
			summary.CashSales = ro.TotalAmount
		case domain.CashMovementRefund:
			summary.CashRefunds = ro.TotalAmount
			summary.RefundCount = ro.Count
		case domain.CashMovementPaidOut:
			summary.PaidOuts = ro.TotalAmount
		case domain.CashMovementCashIn:
			summary.CashIns = ro.TotalAmount
		case domain.CashMovementTip:
			summary.Tips = ro.TotalAmount
		case domain.CashMovementStaffMeal:
			summary.StaffMeals = ro.TotalAmount
		case domain.CashMovementNoSale:
			summary.NoSaleCount = ro.Count
		}
	}

	return summary, nil
}

func (r *ShiftRepository) GetUserFullName(ctx context.Context, tx *sqlx.Tx, storeID, userID uuid.UUID) (string, error) {
	var name string
	query := `
		SELECT full_name
		FROM users
		WHERE store_id = $1 AND id = $2
	`
	err := tx.GetContext(ctx, &name, query, storeID, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Sprintf("User %s", userID.String()[:8]), nil
	}
	return name, err
}

// GetUnsettledOpenTabs returns all tabs currently open or pending payment for the store.
func (r *ShiftRepository) GetUnsettledOpenTabs(
	ctx context.Context,
	tx *sqlx.Tx,
	storeID uuid.UUID,
	currentBusinessDate time.Time,
) ([]domain.UnsettledTabSummary, error) {
	query := `
		SELECT order_number,
		       COALESCE(NULLIF(table_number, ''), NULLIF(customer_name, ''), 'Tab') AS label,
		       status,
		       total_amount,
		       created_at
		FROM orders
		WHERE store_id = $1
		  AND billing_mode = 'open_tab'
		  AND status IN ('open', 'pending_payment')
		ORDER BY created_at ASC
	`
	type row struct {
		OrderNumber string    `db:"order_number"`
		Label       string    `db:"label"`
		Status      string    `db:"status"`
		TotalAmount int64     `db:"total_amount"`
		CreatedAt   time.Time `db:"created_at"`
	}
	var rows []row
	if err := tx.SelectContext(ctx, &rows, query, storeID); err != nil {
		return nil, err
	}

	result := make([]domain.UnsettledTabSummary, len(rows))
	cutoffDate := time.Date(currentBusinessDate.Year(), currentBusinessDate.Month(), currentBusinessDate.Day(), 0, 0, 0, 0, currentBusinessDate.Location())

	for i, ro := range rows {
		result[i] = domain.UnsettledTabSummary{
			OrderNumber:           ro.OrderNumber,
			Label:                 ro.Label,
			Status:                ro.Status,
			TotalAmount:           ro.TotalAmount,
			CreatedAt:             ro.CreatedAt,
			IsOverdueBusinessDate: ro.CreatedAt.Before(cutoffDate),
		}
	}
	return result, nil
}
