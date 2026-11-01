package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
)

var (
	ErrRoundNotFound = errors.New("order round not found")
)

type TabRepository struct{}

func NewTabRepository() *TabRepository {
	return &TabRepository{}
}

// FindActiveTabByLabel looks for an open tab (orders with status = 'open' and billing_mode = 'open_tab')
// matching the normalized table_number or customer_name.
func (r *TabRepository) FindActiveTabByLabel(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, normalizedLabel string) (*domain.Order, error) {
	if tx == nil {
		return nil, ErrNilTransaction
	}
	cleanLabel := strings.ToLower(strings.TrimSpace(normalizedLabel))
	if cleanLabel == "" {
		return nil, nil
	}

	var ord domain.Order
	query := `
		SELECT id, store_id, idempotency_key, request_hash, order_number, queue_number,
		       table_number, customer_name, customer_phone, order_type, billing_mode,
		       status, payment_status, stock_status, expires_at, created_by_user_id,
		       shift_id, table_id, subtotal_amount, discount_amount, tax_amount,
		       service_charge_amount, total_amount, paid_amount, refunded_amount,
		       notes, created_at, updated_at
		FROM orders
		WHERE store_id = $1
		  AND status = 'open'
		  AND billing_mode = 'open_tab'
		  AND (LOWER(TRIM(table_number)) = $2 OR LOWER(TRIM(customer_name)) = $2)
		ORDER BY created_at DESC
		LIMIT 1
	`
	err := tx.GetContext(ctx, &ord, query, storeID, cleanLabel)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ord, nil
}

// InsertOrderRound inserts a new round into order_rounds.
func (r *TabRepository) InsertOrderRound(ctx context.Context, tx *sqlx.Tx, round *domain.OrderRound) error {
	if tx == nil {
		return ErrNilTransaction
	}
	query := `
		INSERT INTO order_rounds (
			id, store_id, order_id, round_number, status,
			submitted_by_user_id, submitted_at, notes
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8
		)
		RETURNING created_at, updated_at
	`
	return tx.QueryRowxContext(
		ctx, query,
		round.ID, round.StoreID, round.OrderID, round.RoundNumber, round.Status,
		round.SubmittedByUserID, round.SubmittedAt, round.Notes,
	).Scan(&round.CreatedAt, &round.UpdatedAt)
}

// GetNextRoundNumber returns the next incremental round_number for an order.
func (r *TabRepository) GetNextRoundNumber(ctx context.Context, tx *sqlx.Tx, storeID, orderID uuid.UUID) (int, error) {
	if tx == nil {
		return 0, ErrNilTransaction
	}
	var maxRound sql.NullInt64
	query := `
		SELECT MAX(round_number)
		FROM order_rounds
		WHERE store_id = $1 AND order_id = $2
	`
	if err := tx.GetContext(ctx, &maxRound, query, storeID, orderID); err != nil {
		return 0, err
	}
	if !maxRound.Valid {
		return 1, nil
	}
	return int(maxRound.Int64) + 1, nil
}

// ListRoundsByOrder returns all rounds for a given order, ordered by round_number ASC.
func (r *TabRepository) ListRoundsByOrder(ctx context.Context, tx *sqlx.Tx, storeID, orderID uuid.UUID) ([]domain.OrderRound, error) {
	if tx == nil {
		return nil, ErrNilTransaction
	}
	var rounds []domain.OrderRound
	query := `
		SELECT id, store_id, order_id, round_number, status,
		       submitted_by_user_id, submitted_at, notes, created_at, updated_at
		FROM order_rounds
		WHERE store_id = $1 AND order_id = $2
		ORDER BY round_number ASC
	`
	if err := tx.SelectContext(ctx, &rounds, query, storeID, orderID); err != nil {
		return nil, err
	}
	return rounds, nil
}

// UpdateOrderTotals updates the monetary amounts on the parent order after round additions.
func (r *TabRepository) UpdateOrderTotals(ctx context.Context, tx *sqlx.Tx, order *domain.Order) error {
	if tx == nil {
		return ErrNilTransaction
	}
	query := `
		UPDATE orders
		SET subtotal_amount = $3,
		    tax_amount = $4,
		    service_charge_amount = $5,
		    total_amount = $6,
		    updated_at = NOW()
		WHERE store_id = $1 AND id = $2
		RETURNING updated_at
	`
	return tx.QueryRowxContext(
		ctx, query,
		order.StoreID, order.ID,
		order.SubtotalAmount, order.TaxAmount, order.ServiceChargeAmount, order.TotalAmount,
	).Scan(&order.UpdatedAt)
}
