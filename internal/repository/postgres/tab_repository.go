package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
)

var (
	ErrRoundNotFound = errors.New("order round not found")
)

type TabRepository struct{}

func NewTabRepository() *TabRepository {
	return &TabRepository{}
}

// AcquireTabIdempotencyLock takes a transaction-scoped PostgreSQL advisory lock on (store_id, idempotency_key).
func (r *TabRepository) AcquireTabIdempotencyLock(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, idempotencyKey string) error {
	if tx == nil {
		return ErrNilTransaction
	}
	cleanKey := strings.TrimSpace(idempotencyKey)
	query := `SELECT pg_advisory_xact_lock(hashtextextended($1 || ':tab_idem:' || $2, 0))`
	_, err := tx.ExecContext(ctx, query, storeID.String(), cleanKey)
	return err
}

// AcquireRoundIdempotencyLock takes a transaction-scoped PostgreSQL advisory lock on (store_id, order_id, idempotency_key).
func (r *TabRepository) AcquireRoundIdempotencyLock(ctx context.Context, tx *sqlx.Tx, storeID, orderID uuid.UUID, idempotencyKey string) error {
	if tx == nil {
		return ErrNilTransaction
	}
	cleanKey := strings.TrimSpace(idempotencyKey)
	query := `SELECT pg_advisory_xact_lock(hashtextextended($1 || ':rnd_idem:' || $2 || ':' || $3, 0))`
	_, err := tx.ExecContext(ctx, query, storeID.String(), orderID.String(), cleanKey)
	return err
}

// AcquireTabLabelLock takes a transaction-scoped PostgreSQL advisory lock on the normalized label.
func (r *TabRepository) AcquireTabLabelLock(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, normalizedLabel string) error {
	if tx == nil {
		return ErrNilTransaction
	}
	cleanLabel := strings.ToLower(strings.TrimSpace(normalizedLabel))
	query := `SELECT pg_advisory_xact_lock(hashtextextended($1 || ':' || $2, 0))`
	_, err := tx.ExecContext(ctx, query, storeID.String(), cleanLabel)
	return err
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

// GetRoundByIdempotencyKey retrieves an existing round by idempotency key.
func (r *TabRepository) GetRoundByIdempotencyKey(ctx context.Context, tx *sqlx.Tx, storeID, orderID uuid.UUID, idempotencyKey string) (*domain.OrderRound, error) {
	if tx == nil {
		return nil, ErrNilTransaction
	}
	var round domain.OrderRound
	query := `
		SELECT id, store_id, order_id, round_number, status, idempotency_key, request_hash,
		       submitted_by_user_id, submitted_at, notes, created_at, updated_at
		FROM order_rounds
		WHERE store_id = $1 AND order_id = $2 AND idempotency_key = $3
		LIMIT 1
	`
	err := tx.GetContext(ctx, &round, query, storeID, orderID, idempotencyKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &round, nil
}

// InsertOrderRound inserts a new round into order_rounds.
func (r *TabRepository) InsertOrderRound(ctx context.Context, tx *sqlx.Tx, round *domain.OrderRound) error {
	if tx == nil {
		return ErrNilTransaction
	}
	query := `
		INSERT INTO order_rounds (
			id, store_id, order_id, round_number, status,
			idempotency_key, request_hash,
			submitted_by_user_id, submitted_at, notes
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7,
			$8, $9, $10
		)
		RETURNING created_at, updated_at
	`
	return tx.QueryRowxContext(
		ctx, query,
		round.ID, round.StoreID, round.OrderID, round.RoundNumber, round.Status,
		round.IdempotencyKey, round.RequestHash,
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
		SELECT id, store_id, order_id, round_number, status, idempotency_key, request_hash,
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

// GetTabOrderByIDReadOnly retrieves the tab order without taking a row lock (read-only for GET /tabs/:id).
func (r *TabRepository) GetTabOrderByIDReadOnly(ctx context.Context, tx *sqlx.Tx, storeID, orderID uuid.UUID) (*domain.Order, error) {
	if tx == nil {
		return nil, ErrNilTransaction
	}
	var order domain.Order
	query := `
		SELECT id, store_id, idempotency_key, request_hash, order_number, queue_number,
		       table_number, customer_name, customer_phone, order_type, billing_mode,
		       status, payment_status, stock_status, expires_at, created_by_user_id,
		       shift_id, table_id, subtotal_amount, discount_amount, tax_amount,
		       service_charge_amount, total_amount, paid_amount, refunded_amount,
		       notes, created_at, updated_at
		FROM orders
		WHERE store_id = $1 AND id = $2
	`
	err := tx.GetContext(ctx, &order, query, storeID, orderID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrOrderNotFound
	}
	if err != nil {
		return nil, err
	}
	return &order, nil
}

// GetOrderItemsWithOptionsByOrder retrieves all items and options for an order in 2 batch queries (eliminating N+1).
func (r *TabRepository) GetOrderItemsWithOptionsByOrder(ctx context.Context, tx *sqlx.Tx, storeID, orderID uuid.UUID) ([]domain.OrderItem, error) {
	if tx == nil {
		return nil, ErrNilTransaction
	}

	var items []domain.OrderItem
	itemQuery := `
		SELECT id, store_id, order_id, product_id, variant_id, snapshot_variant_name,
		       snapshot_sku, config_hash, snapshot_name, unit_base_price, modifier_total_price,
		       snapshot_price, quantity, subtotal_amount, selected_options, notes, round_id, created_at
		FROM order_items
		WHERE store_id = $1 AND order_id = $2
		ORDER BY created_at ASC
	`
	if err := tx.SelectContext(ctx, &items, itemQuery, storeID, orderID); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return items, nil
	}

	itemIDs := make([]uuid.UUID, len(items))
	itemIndexMap := make(map[uuid.UUID]int, len(items))
	for i, it := range items {
		itemIDs[i] = it.ID
		itemIndexMap[it.ID] = i
	}

	optQuery, args, err := sqlx.In(`
		SELECT id, store_id, order_item_id, parent_order_item_option_id,
		       modifier_group_id, modifier_option_id, parent_option_id,
		       snapshot_group_name, snapshot_option_name, snapshot_price_delta,
		       quantity, created_at
		FROM order_item_options
		WHERE store_id = ? AND order_item_id IN (?)
		ORDER BY created_at ASC
	`, storeID, itemIDs)
	if err != nil {
		return nil, err
	}
	optQuery = tx.Rebind(optQuery)

	var allOpts []domain.OrderItemOption
	if err := tx.SelectContext(ctx, &allOpts, optQuery, args...); err != nil {
		return nil, err
	}

	for _, opt := range allOpts {
		if idx, ok := itemIndexMap[opt.OrderItemID]; ok {
			items[idx].Options = append(items[idx].Options, opt)
		}
	}

	return items, nil
}

// CreatePaymentSplitsTx inserts payment splits and their item allocations inside a transaction.
func (r *TabRepository) CreatePaymentSplitsTx(
	ctx context.Context,
	tx *sqlx.Tx,
	splits []domain.PaymentSplit,
	splitItems []domain.PaymentSplitItem,
) error {
	if tx == nil {
		return ErrNilTransaction
	}
	if len(splits) == 0 {
		return nil
	}

	splitQuery := `
		INSERT INTO payment_splits (
			id, store_id, order_id, split_number, label, split_mode,
			subtotal_amount, tax_amount, service_charge_amount, total_amount, paid_amount, status
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11, $12
		)
	`
	for _, sp := range splits {
		_, err := tx.ExecContext(
			ctx, splitQuery,
			sp.ID, sp.StoreID, sp.OrderID, sp.SplitNumber, sp.Label, sp.SplitMode,
			sp.SubtotalAmount, sp.TaxAmount, sp.ServiceChargeAmount, sp.TotalAmount, sp.PaidAmount, sp.Status,
		)
		if err != nil {
			return err
		}
	}

	if len(splitItems) > 0 {
		itemQuery := `
			INSERT INTO payment_split_items (
				id, store_id, split_id, order_item_id, quantity, allocated_amount
			) VALUES (
				$1, $2, $3, $4, $5, $6
			)
		`
		for _, it := range splitItems {
			_, err := tx.ExecContext(
				ctx, itemQuery,
				it.ID, it.StoreID, it.SplitID, it.OrderItemID, it.Quantity, it.AllocatedAmount,
			)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// GetPaymentSplitsByOrderID retrieves all splits for an order, along with allocated items if any.
func (r *TabRepository) GetPaymentSplitsByOrderID(
	ctx context.Context,
	tx *sqlx.Tx,
	storeID, orderID uuid.UUID,
) ([]domain.PaymentSplit, error) {
	if tx == nil {
		return nil, ErrNilTransaction
	}

	var splits []domain.PaymentSplit
	query := `
		SELECT id, store_id, order_id, split_number, label, split_mode,
		       subtotal_amount, tax_amount, service_charge_amount, total_amount,
		       paid_amount, status, created_at, updated_at
		FROM payment_splits
		WHERE store_id = $1 AND order_id = $2
		ORDER BY split_number ASC
	`
	if err := tx.SelectContext(ctx, &splits, query, storeID, orderID); err != nil {
		return nil, err
	}
	if len(splits) == 0 {
		return splits, nil
	}

	splitIDs := make([]uuid.UUID, len(splits))
	splitIndexMap := make(map[uuid.UUID]int, len(splits))
	for i, sp := range splits {
		splitIDs[i] = sp.ID
		splitIndexMap[sp.ID] = i
	}

	itemQuery, args, err := sqlx.In(`
		SELECT id, store_id, split_id, order_item_id, quantity, allocated_amount, created_at
		FROM payment_split_items
		WHERE store_id = ? AND split_id IN (?)
		ORDER BY created_at ASC
	`, storeID, splitIDs)
	if err != nil {
		return nil, err
	}
	itemQuery = tx.Rebind(itemQuery)

	var items []domain.PaymentSplitItem
	if err := tx.SelectContext(ctx, &items, itemQuery, args...); err != nil {
		return nil, err
	}

	for _, it := range items {
		if idx, ok := splitIndexMap[it.SplitID]; ok {
			splits[idx].Items = append(splits[idx].Items, it)
		}
	}

	return splits, nil
}

// GetPaymentSplitByIDForUpdate locks and retrieves a single split row.
func (r *TabRepository) GetPaymentSplitByIDForUpdate(
	ctx context.Context,
	tx *sqlx.Tx,
	storeID, splitID uuid.UUID,
) (*domain.PaymentSplit, error) {
	if tx == nil {
		return nil, ErrNilTransaction
	}

	var sp domain.PaymentSplit
	query := `
		SELECT id, store_id, order_id, split_number, label, split_mode,
		       subtotal_amount, tax_amount, service_charge_amount, total_amount,
		       paid_amount, status, created_at, updated_at
		FROM payment_splits
		WHERE store_id = $1 AND id = $2
		FOR UPDATE
	`
	err := tx.GetContext(ctx, &sp, query, storeID, splitID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sp, nil
}

// CancelPaymentSplitsTx marks all non-cancelled splits of an order as cancelled.
func (r *TabRepository) CancelPaymentSplitsTx(
	ctx context.Context,
	tx *sqlx.Tx,
	storeID, orderID uuid.UUID,
) error {
	if tx == nil {
		return ErrNilTransaction
	}
	query := `
		UPDATE payment_splits
		SET status = 'cancelled', updated_at = NOW()
		WHERE store_id = $1 AND order_id = $2 AND status != 'cancelled'
	`
	_, err := tx.ExecContext(ctx, query, storeID, orderID)
	return err
}

// UpdatePaymentSplitStatusTx updates paid_amount and status of a single split.
func (r *TabRepository) UpdatePaymentSplitStatusTx(
	ctx context.Context,
	tx *sqlx.Tx,
	storeID, splitID uuid.UUID,
	status domain.SplitBillStatus,
	paidAmount int64,
) error {
	if tx == nil {
		return ErrNilTransaction
	}
	query := `
		UPDATE payment_splits
		SET status = $1, paid_amount = $2, updated_at = NOW()
		WHERE store_id = $3 AND id = $4
	`
	_, err := tx.ExecContext(ctx, query, status, paidAmount, storeID, splitID)
	return err
}

// CountUnpaidSplitsTx returns the number of active (non-cancelled, non-paid) splits for an order.
func (r *TabRepository) CountUnpaidSplitsTx(
	ctx context.Context,
	tx *sqlx.Tx,
	storeID, orderID uuid.UUID,
) (int, error) {
	if tx == nil {
		return 0, ErrNilTransaction
	}
	var count int
	query := `
		SELECT COUNT(*)
		FROM payment_splits
		WHERE store_id = $1 AND order_id = $2 AND status NOT IN ('paid', 'cancelled')
	`
	if err := tx.GetContext(ctx, &count, query, storeID, orderID); err != nil {
		return 0, err
	}
	return count, nil
}

// HasPendingPaymentForSplitTx checks if there is any pending or authorized payment attempt for a given split.
func (r *TabRepository) HasPendingPaymentForSplitTx(
	ctx context.Context,
	tx *sqlx.Tx,
	storeID, splitID uuid.UUID,
) (bool, error) {
	if tx == nil {
		return false, ErrNilTransaction
	}
	var count int
	query := `
		SELECT COUNT(*)
		FROM payments
		WHERE store_id = $1 AND split_id = $2 AND status IN ('pending', 'authorized')
	`
	if err := tx.GetContext(ctx, &count, query, storeID, splitID); err != nil {
		return false, err
	}
	return count > 0, nil
}

// UpdateOrderStatusAndPaymentStatusTx updates order status and payment status in an active transaction.
func (r *TabRepository) UpdateOrderStatusAndPaymentStatusTx(
	ctx context.Context,
	tx *sqlx.Tx,
	storeID, orderID uuid.UUID,
	status domain.OrderStatus,
	paymentStatus domain.OrderPaymentStatus,
) error {
	if tx == nil {
		return ErrNilTransaction
	}
	query := `
		UPDATE orders
		SET status = $1, payment_status = $2, updated_at = NOW()
		WHERE store_id = $3 AND id = $4
	`
	_, err := tx.ExecContext(ctx, query, status, paymentStatus, storeID, orderID)
	return err
}
