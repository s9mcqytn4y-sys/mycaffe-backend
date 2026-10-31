package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
)

var (
	ErrStoreNotFound      = errors.New("store not found or not accessible in tenant context")
	ErrStockUpdateFailed  = errors.New("insufficient stock during atomic deduction")
	ErrOrderNotFound      = errors.New("order not found")
	ErrNilTransaction     = errors.New("database transaction cannot be nil")
	ErrEmptyTenantContext = errors.New("tenant store_id cannot be nil")
)

type CatalogData struct {
	Store                 domain.Store
	Products              map[uuid.UUID]domain.Product
	ProductModifierGroups map[uuid.UUID][]domain.ProductModifierGroup
	ModifierGroups        map[uuid.UUID]domain.ModifierGroup
	ModifierOptions       map[uuid.UUID]domain.ModifierOption
	OptionsByGroup        map[uuid.UUID][]domain.ModifierOption
	OptionModifierGroups  map[uuid.UUID][]domain.OptionModifierGroup
}

type OrderRepository struct{}

func NewOrderRepository() *OrderRepository {
	return &OrderRepository{}
}

func (r *OrderRepository) GetStoreForOrder(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID) (*domain.Store, error) {
	if tx == nil {
		return nil, ErrNilTransaction
	}
	if storeID == uuid.Nil {
		return nil, ErrEmptyTenantContext
	}

	var store domain.Store
	query := `
		SELECT
			id, slug, name, COALESCE(address, '') AS address, COALESCE(phone, '') AS phone,
			timezone, is_active, order_prefix, queue_prefix, default_billing_mode, shift_mode,
			COALESCE(enabled_payment_methods::text, '[]') AS enabled_payment_methods,
			tax_bps, service_bps, qris_mdr_bps, currency, created_at, updated_at, deleted_at
		FROM stores
		WHERE id = $1
	`
	if err := tx.GetContext(ctx, &store, query, storeID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrStoreNotFound
		}
		return nil, fmt.Errorf("failed to query store: %w", err)
	}
	return &store, nil
}

func (r *OrderRepository) GetOrderByIdempotencyKey(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, idempotencyKey string) (*domain.Order, []domain.PaymentSplit, error) {
	if tx == nil {
		return nil, nil, ErrNilTransaction
	}
	if storeID == uuid.Nil {
		return nil, nil, ErrEmptyTenantContext
	}
	if idempotencyKey == "" {
		return nil, nil, ErrOrderNotFound
	}

	var order domain.Order
	orderQuery := `
		SELECT
			id, store_id, idempotency_key, request_hash, order_number, queue_number,
			table_number, customer_name, COALESCE(customer_phone, '') AS customer_phone,
			order_type, billing_mode, status, payment_status, stock_status,
			expires_at, created_by_user_id,
			subtotal_amount, discount_amount, tax_amount, service_charge_amount,
			total_amount, paid_amount, refunded_amount, COALESCE(notes, '') AS notes,
			created_at, updated_at
		FROM orders
		WHERE store_id = $1 AND idempotency_key = $2
	`
	if err := tx.GetContext(ctx, &order, orderQuery, storeID, idempotencyKey); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrOrderNotFound
		}
		return nil, nil, fmt.Errorf("failed to query order by idempotency_key: %w", err)
	}

	if err := r.populateOrderItemsAndOptions(ctx, tx, storeID, &order); err != nil {
		return nil, nil, err
	}

	splits, err := r.getOrderSplits(ctx, tx, storeID, order.ID)
	if err != nil {
		return nil, nil, err
	}

	return &order, splits, nil
}

func (r *OrderRepository) GetOrderByIDForUpdate(ctx context.Context, tx *sqlx.Tx, storeID, orderID uuid.UUID) (*domain.Order, []domain.PaymentSplit, error) {
	if tx == nil {
		return nil, nil, ErrNilTransaction
	}
	if storeID == uuid.Nil {
		return nil, nil, ErrEmptyTenantContext
	}
	if orderID == uuid.Nil {
		return nil, nil, ErrOrderNotFound
	}

	var order domain.Order
	orderQuery := `
		SELECT
			id, store_id, idempotency_key, request_hash, order_number, queue_number,
			table_number, customer_name, COALESCE(customer_phone, '') AS customer_phone,
			order_type, billing_mode, status, payment_status, stock_status,
			expires_at, created_by_user_id,
			subtotal_amount, discount_amount, tax_amount, service_charge_amount,
			total_amount, paid_amount, refunded_amount, COALESCE(notes, '') AS notes,
			created_at, updated_at
		FROM orders
		WHERE store_id = $1 AND id = $2
		FOR UPDATE
	`
	if err := tx.GetContext(ctx, &order, orderQuery, storeID, orderID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrOrderNotFound
		}
		return nil, nil, fmt.Errorf("failed to lock and query order by id: %w", err)
	}

	if err := r.populateOrderItemsAndOptions(ctx, tx, storeID, &order); err != nil {
		return nil, nil, err
	}

	splits, err := r.getOrderSplits(ctx, tx, storeID, order.ID)
	if err != nil {
		return nil, nil, err
	}

	return &order, splits, nil
}

func (r *OrderRepository) populateOrderItemsAndOptions(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, order *domain.Order) error {
	itemsQuery := `
		SELECT
			id, store_id, order_id, product_id, config_hash, snapshot_name,
			unit_base_price, modifier_total_price, snapshot_price, quantity,
			subtotal_amount, COALESCE(selected_options::text, '[]') AS selected_options,
			COALESCE(notes, '') AS notes, created_at
		FROM order_items
		WHERE store_id = $1 AND order_id = $2
		ORDER BY created_at ASC, id ASC
	`
	var items []domain.OrderItem
	if err := tx.SelectContext(ctx, &items, itemsQuery, storeID, order.ID); err != nil {
		return fmt.Errorf("failed to query order items: %w", err)
	}

	if len(items) > 0 {
		optionsQuery := `
			SELECT
				oio.id, oio.store_id, oio.order_item_id, oio.parent_order_item_option_id,
				oio.modifier_group_id, oio.modifier_option_id, oio.parent_option_id,
				oio.snapshot_group_name, oio.snapshot_option_name, oio.snapshot_price_delta,
				oio.quantity, oio.created_at
			FROM order_item_options oio
			INNER JOIN order_items oi ON oi.store_id = oio.store_id AND oi.id = oio.order_item_id
			WHERE oio.store_id = $1 AND oi.order_id = $2
			ORDER BY oio.parent_order_item_option_id NULLS FIRST, oio.created_at ASC, oio.id ASC
		`
		var options []domain.OrderItemOption
		if err := tx.SelectContext(ctx, &options, optionsQuery, storeID, order.ID); err != nil {
			return fmt.Errorf("failed to query order item options: %w", err)
		}

		optsByItem := make(map[uuid.UUID][]domain.OrderItemOption, len(items))
		for _, opt := range options {
			optsByItem[opt.OrderItemID] = append(optsByItem[opt.OrderItemID], opt)
		}
		for i := range items {
			items[i].Options = optsByItem[items[i].ID]
		}
	}
	order.Items = items
	return nil
}

func (r *OrderRepository) getOrderSplits(ctx context.Context, tx *sqlx.Tx, storeID, orderID uuid.UUID) ([]domain.PaymentSplit, error) {
	splitsQuery := `
		SELECT
			id, store_id, order_id, split_number, label, split_mode,
			subtotal_amount, tax_amount, service_charge_amount, total_amount,
			paid_amount, status, created_at, updated_at
		FROM payment_splits
		WHERE store_id = $1 AND order_id = $2
		ORDER BY split_number ASC
	`
	var splits []domain.PaymentSplit
	if err := tx.SelectContext(ctx, &splits, splitsQuery, storeID, orderID); err != nil {
		return nil, fmt.Errorf("failed to query payment splits: %w", err)
	}
	return splits, nil
}

func (r *OrderRepository) VerifyActiveStaffActor(ctx context.Context, tx *sqlx.Tx, storeID, userID uuid.UUID) (bool, error) {
	if tx == nil {
		return false, ErrNilTransaction
	}
	if storeID == uuid.Nil || userID == uuid.Nil {
		return false, nil
	}

	var exists bool
	query := `
		SELECT EXISTS (
			SELECT 1
			FROM users
			WHERE store_id = $1
			  AND id = $2
			  AND is_active = true
			  AND role IN ('owner', 'cashier')
		)
	`
	if err := tx.GetContext(ctx, &exists, query, storeID, userID); err != nil {
		return false, fmt.Errorf("failed to verify active staff actor: %w", err)
	}
	return exists, nil
}

func (r *OrderRepository) LockAndLoadCatalogForOrder(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, productIDs []uuid.UUID) (*CatalogData, error) {
	store, err := r.GetStoreForOrder(ctx, tx, storeID)
	if err != nil {
		return nil, err
	}

	uniqueProdIDs := deduplicateAndSortUUIDs(productIDs)
	data := &CatalogData{
		Store:                 *store,
		Products:              make(map[uuid.UUID]domain.Product, len(uniqueProdIDs)),
		ProductModifierGroups: make(map[uuid.UUID][]domain.ProductModifierGroup),
		ModifierGroups:        make(map[uuid.UUID]domain.ModifierGroup),
		ModifierOptions:       make(map[uuid.UUID]domain.ModifierOption),
		OptionsByGroup:        make(map[uuid.UUID][]domain.ModifierOption),
		OptionModifierGroups:  make(map[uuid.UUID][]domain.OptionModifierGroup),
	}

	if len(uniqueProdIDs) == 0 {
		return data, nil
	}

	// Lock requested products in deterministic UUID order to prevent deadlocks across concurrent checkouts.
	prodQuery, prodArgs, err := sqlx.In(`
		SELECT
			id, store_id, category_id, name, COALESCE(description, '') AS description,
			base_price, COALESCE(image_url, '') AS image_url, is_available,
			track_stock, stock_quantity, created_at, updated_at
		FROM products
		WHERE store_id = ? AND id IN (?)
		ORDER BY id ASC
		FOR UPDATE
	`, storeID, uniqueProdIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to build products query: %w", err)
	}
	prodQuery = tx.Rebind(prodQuery)

	var products []domain.Product
	if err := tx.SelectContext(ctx, &products, prodQuery, prodArgs...); err != nil {
		return nil, fmt.Errorf("failed to lock and query products: %w", err)
	}
	for _, p := range products {
		data.Products[p.ID] = p
	}

	// Load product_modifier_groups for requested products.
	pmgQuery, pmgArgs, err := sqlx.In(`
		SELECT
			store_id, product_id, group_id, display_order, override_required, created_at
		FROM product_modifier_groups
		WHERE store_id = ? AND product_id IN (?)
		ORDER BY product_id ASC, display_order ASC, group_id ASC
	`, storeID, uniqueProdIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to build product_modifier_groups query: %w", err)
	}
	pmgQuery = tx.Rebind(pmgQuery)

	var pmgs []domain.ProductModifierGroup
	if err := tx.SelectContext(ctx, &pmgs, pmgQuery, pmgArgs...); err != nil {
		return nil, fmt.Errorf("failed to query product_modifier_groups: %w", err)
	}

	groupIDSet := make(map[uuid.UUID]struct{})
	for _, pmg := range pmgs {
		data.ProductModifierGroups[pmg.ProductID] = append(data.ProductModifierGroups[pmg.ProductID], pmg)
		groupIDSet[pmg.GroupID] = struct{}{}
	}

	if len(groupIDSet) == 0 {
		return data, nil
	}

	parentGroupIDs := mapKeysSorted(groupIDSet)

	// Load parent modifier groups.
	if err := r.loadModifierGroups(ctx, tx, storeID, parentGroupIDs, data); err != nil {
		return nil, err
	}

	// Load and lock parent modifier options.
	parentOptionIDs, err := r.lockAndLoadModifierOptionsByGroups(ctx, tx, storeID, parentGroupIDs, data)
	if err != nil {
		return nil, err
	}

	if len(parentOptionIDs) == 0 {
		return data, nil
	}

	// Load 1-level nested sub-addon mappings (option_modifier_groups) for parent options.
	omgQuery, omgArgs, err := sqlx.In(`
		SELECT
			store_id, parent_option_id, child_group_id, display_order, created_at
		FROM option_modifier_groups
		WHERE store_id = ? AND parent_option_id IN (?)
		ORDER BY parent_option_id ASC, display_order ASC, child_group_id ASC
	`, storeID, parentOptionIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to build option_modifier_groups query: %w", err)
	}
	omgQuery = tx.Rebind(omgQuery)

	var omgs []domain.OptionModifierGroup
	if err := tx.SelectContext(ctx, &omgs, omgQuery, omgArgs...); err != nil {
		return nil, fmt.Errorf("failed to query option_modifier_groups: %w", err)
	}

	childGroupSet := make(map[uuid.UUID]struct{})
	for _, omg := range omgs {
		data.OptionModifierGroups[omg.ParentOptionID] = append(data.OptionModifierGroups[omg.ParentOptionID], omg)
		if _, alreadyLoaded := data.ModifierGroups[omg.ChildGroupID]; !alreadyLoaded {
			childGroupSet[omg.ChildGroupID] = struct{}{}
		}
	}

	if len(childGroupSet) > 0 {
		childGroupIDs := mapKeysSorted(childGroupSet)
		if err := r.loadModifierGroups(ctx, tx, storeID, childGroupIDs, data); err != nil {
			return nil, err
		}
		if _, err := r.lockAndLoadModifierOptionsByGroups(ctx, tx, storeID, childGroupIDs, data); err != nil {
			return nil, err
		}
	}

	return data, nil
}

func (r *OrderRepository) loadModifierGroups(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, groupIDs []uuid.UUID, data *CatalogData) error {
	mgQuery, mgArgs, err := sqlx.In(`
		SELECT
			id, store_id, name, selection_type, is_required, min_select, max_select,
			display_order, is_active, created_at, updated_at
		FROM modifier_groups
		WHERE store_id = ? AND id IN (?)
		ORDER BY display_order ASC, id ASC
	`, storeID, groupIDs)
	if err != nil {
		return fmt.Errorf("failed to build modifier_groups query: %w", err)
	}
	mgQuery = tx.Rebind(mgQuery)

	var groups []domain.ModifierGroup
	if err := tx.SelectContext(ctx, &groups, mgQuery, mgArgs...); err != nil {
		return fmt.Errorf("failed to query modifier_groups: %w", err)
	}
	for _, g := range groups {
		data.ModifierGroups[g.ID] = g
	}
	return nil
}

func (r *OrderRepository) lockAndLoadModifierOptionsByGroups(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, groupIDs []uuid.UUID, data *CatalogData) ([]uuid.UUID, error) {
	moQuery, moArgs, err := sqlx.In(`
		SELECT
			id, store_id, group_id, name, price_delta, is_available,
			track_stock, stock_quantity, max_quantity, display_order,
			created_at, updated_at
		FROM modifier_options
		WHERE store_id = ? AND group_id IN (?)
		ORDER BY id ASC
		FOR UPDATE
	`, storeID, groupIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to build modifier_options query: %w", err)
	}
	moQuery = tx.Rebind(moQuery)

	var options []domain.ModifierOption
	if err := tx.SelectContext(ctx, &options, moQuery, moArgs...); err != nil {
		return nil, fmt.Errorf("failed to lock and query modifier_options: %w", err)
	}

	optionIDs := make([]uuid.UUID, 0, len(options))
	for _, opt := range options {
		data.ModifierOptions[opt.ID] = opt
		data.OptionsByGroup[opt.GroupID] = append(data.OptionsByGroup[opt.GroupID], opt)
		optionIDs = append(optionIDs, opt.ID)
	}
	return deduplicateAndSortUUIDs(optionIDs), nil
}

func (r *OrderRepository) DeductProductStock(ctx context.Context, tx *sqlx.Tx, storeID, productID uuid.UUID, quantity int) error {
	res, err := tx.ExecContext(ctx, `
		UPDATE products
		SET stock_quantity = stock_quantity - $3
		WHERE store_id = $1 AND id = $2 AND track_stock = true AND stock_quantity >= $3
	`, storeID, productID, quantity)
	if err != nil {
		return fmt.Errorf("failed to deduct product stock: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to inspect affected rows on product stock deduction: %w", err)
	}
	if rows != 1 {
		return ErrStockUpdateFailed
	}
	return nil
}

func (r *OrderRepository) DeductModifierOptionStock(ctx context.Context, tx *sqlx.Tx, storeID, optionID uuid.UUID, quantity int) error {
	res, err := tx.ExecContext(ctx, `
		UPDATE modifier_options
		SET stock_quantity = stock_quantity - $3
		WHERE store_id = $1 AND id = $2 AND track_stock = true AND stock_quantity >= $3
	`, storeID, optionID, quantity)
	if err != nil {
		return fmt.Errorf("failed to deduct modifier option stock: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to inspect affected rows on modifier option stock deduction: %w", err)
	}
	if rows != 1 {
		return ErrStockUpdateFailed
	}
	return nil
}

func (r *OrderRepository) RestoreProductStock(ctx context.Context, tx *sqlx.Tx, storeID, productID uuid.UUID, quantity int) error {
	if quantity <= 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE products
		SET stock_quantity = stock_quantity + $3
		WHERE store_id = $1 AND id = $2 AND track_stock = true
	`, storeID, productID, quantity)
	if err != nil {
		return fmt.Errorf("failed to restore product stock: %w", err)
	}
	return nil
}

func (r *OrderRepository) RestoreModifierOptionStock(ctx context.Context, tx *sqlx.Tx, storeID, optionID uuid.UUID, quantity int) error {
	if quantity <= 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE modifier_options
		SET stock_quantity = stock_quantity + $3
		WHERE store_id = $1 AND id = $2 AND track_stock = true
	`, storeID, optionID, quantity)
	if err != nil {
		return fmt.Errorf("failed to restore modifier option stock: %w", err)
	}
	return nil
}

func (r *OrderRepository) AllocateDailySequence(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, businessDate string) (int, error) {
	var seq int
	query := `
		INSERT INTO store_daily_sequences (store_id, business_date, last_seq, updated_at)
		VALUES ($1, $2::date, 1, clock_timestamp())
		ON CONFLICT (store_id, business_date)
		DO UPDATE SET
			last_seq = store_daily_sequences.last_seq + 1,
			updated_at = clock_timestamp()
		RETURNING last_seq
	`
	if err := tx.GetContext(ctx, &seq, query, storeID, businessDate); err != nil {
		return 0, fmt.Errorf("failed to allocate daily sequence: %w", err)
	}
	return seq, nil
}

func (r *OrderRepository) InsertOrder(ctx context.Context, tx *sqlx.Tx, order *domain.Order) error {
	if order.StockStatus == "" {
		order.StockStatus = domain.OrderStockStatusReserved
	}
	query := `
		INSERT INTO orders (
			id, store_id, idempotency_key, request_hash, order_number, queue_number,
			table_number, customer_name, customer_phone, order_type, billing_mode,
			status, payment_status, stock_status, expires_at, created_by_user_id,
			subtotal_amount, discount_amount, tax_amount,
			service_charge_amount, total_amount, paid_amount, refunded_amount, notes
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11,
			$12, $13, $14, $15, $16,
			$17, $18, $19,
			$20, $21, $22, $23, $24
		)
		RETURNING created_at, updated_at
	`
	return tx.QueryRowxContext(
		ctx, query,
		order.ID, order.StoreID, order.IdempotencyKey, order.RequestHash, order.OrderNumber, order.QueueNumber,
		order.TableNumber, order.CustomerName, order.CustomerPhone, order.OrderType, order.BillingMode,
		order.Status, order.PaymentStatus, order.StockStatus, order.ExpiresAt, order.CreatedByUserID,
		order.SubtotalAmount, order.DiscountAmount, order.TaxAmount,
		order.ServiceChargeAmount, order.TotalAmount, order.PaidAmount, order.RefundedAmount, order.Notes,
	).Scan(&order.CreatedAt, &order.UpdatedAt)
}

func (r *OrderRepository) UpdateOrderStatusAndStock(ctx context.Context, tx *sqlx.Tx, order *domain.Order) error {
	query := `
		UPDATE orders
		SET status = $3,
		    payment_status = $4,
		    stock_status = $5,
		    paid_amount = $6,
		    expires_at = $7
		WHERE store_id = $1 AND id = $2
		RETURNING updated_at
	`
	return tx.QueryRowxContext(
		ctx, query,
		order.StoreID, order.ID, order.Status, order.PaymentStatus, order.StockStatus, order.PaidAmount, order.ExpiresAt,
	).Scan(&order.UpdatedAt)
}

func (r *OrderRepository) UpdatePaymentSplitsStatusByOrder(ctx context.Context, tx *sqlx.Tx, storeID, orderID uuid.UUID, status domain.SplitBillStatus, setPaidFull bool) error {
	var query string
	if setPaidFull {
		query = `
			UPDATE payment_splits
			SET status = $3,
			    paid_amount = total_amount
			WHERE store_id = $1 AND order_id = $2
		`
	} else {
		query = `
			UPDATE payment_splits
			SET status = $3
			WHERE store_id = $1 AND order_id = $2
		`
	}
	_, err := tx.ExecContext(ctx, query, storeID, orderID, status)
	if err != nil {
		return fmt.Errorf("failed to update payment splits status: %w", err)
	}
	return nil
}

func (r *OrderRepository) InsertOrderItem(ctx context.Context, tx *sqlx.Tx, item *domain.OrderItem) error {
	selectedOptsJSON := item.SelectedOptions
	if selectedOptsJSON == "" {
		selectedOptsJSON = "[]"
	}
	query := `
		INSERT INTO order_items (
			id, store_id, order_id, product_id, config_hash, snapshot_name,
			unit_base_price, modifier_total_price, snapshot_price, quantity,
			subtotal_amount, selected_options, notes
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10,
			$11, $12::jsonb, $13
		)
		RETURNING created_at
	`
	return tx.QueryRowxContext(
		ctx, query,
		item.ID, item.StoreID, item.OrderID, item.ProductID, item.ConfigHash, item.SnapshotName,
		item.UnitBasePrice, item.ModifierTotalPrice, item.SnapshotPrice, item.Quantity,
		item.SubtotalAmount, selectedOptsJSON, item.Notes,
	).Scan(&item.CreatedAt)
}

func (r *OrderRepository) InsertOrderItemOption(ctx context.Context, tx *sqlx.Tx, opt *domain.OrderItemOption) error {
	query := `
		INSERT INTO order_item_options (
			id, store_id, order_item_id, parent_order_item_option_id,
			modifier_group_id, modifier_option_id, parent_option_id,
			snapshot_group_name, snapshot_option_name, snapshot_price_delta, quantity
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7,
			$8, $9, $10, $11
		)
		RETURNING created_at
	`
	return tx.QueryRowxContext(
		ctx, query,
		opt.ID, opt.StoreID, opt.OrderItemID, opt.ParentOrderItemOptionID,
		opt.ModifierGroupID, opt.ModifierOptionID, opt.ParentOptionID,
		opt.SnapshotGroupName, opt.SnapshotOptionName, opt.SnapshotPriceDelta, opt.Quantity,
	).Scan(&opt.CreatedAt)
}

func (r *OrderRepository) InsertPaymentSplit(ctx context.Context, tx *sqlx.Tx, split *domain.PaymentSplit) error {
	query := `
		INSERT INTO payment_splits (
			id, store_id, order_id, split_number, label, split_mode,
			subtotal_amount, tax_amount, service_charge_amount, total_amount,
			paid_amount, status
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10,
			$11, $12
		)
		RETURNING created_at, updated_at
	`
	return tx.QueryRowxContext(
		ctx, query,
		split.ID, split.StoreID, split.OrderID, split.SplitNumber, split.Label, split.SplitMode,
		split.SubtotalAmount, split.TaxAmount, split.ServiceChargeAmount, split.TotalAmount,
		split.PaidAmount, split.Status,
	).Scan(&split.CreatedAt, &split.UpdatedAt)
}

func (r *OrderRepository) InsertOutboxEvent(ctx context.Context, tx *sqlx.Tx, event *domain.OutboxEvent) error {
	query := `
		INSERT INTO outbox_events (
			id, store_id, event_type, aggregate_type, aggregate_id, payload
		) VALUES (
			$1, $2, $3, $4, $5, $6::jsonb
		)
		RETURNING created_at
	`
	return tx.QueryRowxContext(
		ctx, query,
		event.ID, event.StoreID, event.EventType, event.AggregateType, event.AggregateID, event.Payload,
	).Scan(&event.CreatedAt)
}

func deduplicateAndSortUUIDs(ids []uuid.UUID) []uuid.UUID {
	set := make(map[uuid.UUID]struct{}, len(ids))
	for _, id := range ids {
		if id != uuid.Nil {
			set[id] = struct{}{}
		}
	}
	return mapKeysSorted(set)
}

func mapKeysSorted(set map[uuid.UUID]struct{}) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].String() < out[j].String()
	})
	return out
}
