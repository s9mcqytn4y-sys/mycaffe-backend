package service

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
)

// ValidateItemInputs enforces service-level limits on item inputs:
// - Max 100 items per request
// - Quantity 1..99 per item
// - Notes max 140 runes per item
func ValidateItemInputs(items []OrderItemInput) error {
	if len(items) == 0 {
		return fmt.Errorf("%w: items list cannot be empty", ErrInvalidOrderInput)
	}
	if len(items) > 100 {
		return fmt.Errorf("%w: items count (%d) exceeds limit of 100", ErrInvalidOrderInput, len(items))
	}
	for i, it := range items {
		if it.Quantity < 1 || it.Quantity > 99 {
			return fmt.Errorf("%w: item[%d] quantity must be between 1 and 99, got %d", ErrInvalidOrderInput, i, it.Quantity)
		}
		if utf8.RuneCountInString(it.Notes) > 140 {
			return fmt.Errorf("%w: item[%d] notes exceed 140 characters", ErrInvalidOrderInput, i)
		}
	}
	return nil
}

// AllocateOrderIdentity generates the atomic daily sequence, order_number, and queue_number.
func AllocateOrderIdentity(
	ctx context.Context,
	tx *sqlx.Tx,
	repo *postgres.OrderRepository,
	store *domain.Store,
	storeID uuid.UUID,
	now time.Time,
) (orderNum string, queueNum string, err error) {
	businessDateISO, compactDate := resolveStoreBusinessDates(now, store.Timezone)
	seq, err := repo.AllocateDailySequence(ctx, tx, storeID, businessDateISO)
	if err != nil {
		return "", "", fmt.Errorf("failed to allocate daily sequence: %w", err)
	}

	orderPrefix := strings.TrimSpace(store.OrderPrefix)
	if orderPrefix == "" {
		orderPrefix = "ORD"
	}
	queuePrefix := strings.TrimSpace(store.QueuePrefix)
	if queuePrefix == "" {
		queuePrefix = "A"
	}

	orderNum = fmt.Sprintf("%s-%s-%04d", orderPrefix, compactDate, seq)
	queueNum = fmt.Sprintf("%s%03d", queuePrefix, seq)
	return orderNum, queueNum, nil
}

// InsertPricedItems persists priced order items and their 1-level nested options.
// If roundID is provided, it associates items to that round.
func InsertPricedItems(
	ctx context.Context,
	tx *sqlx.Tx,
	repo *postgres.OrderRepository,
	storeID uuid.UUID,
	orderID uuid.UUID,
	roundID *uuid.UUID,
	pricedItems []PricedOrderItem,
) ([]domain.OrderItem, error) {
	persistedItems := make([]domain.OrderItem, 0, len(pricedItems))

	for _, pricedItem := range pricedItems {
		itemRow := pricedItem.Item
		itemRow.ID = uuid.New()
		itemRow.StoreID = storeID
		itemRow.OrderID = orderID
		itemRow.RoundID = roundID

		if err := repo.InsertOrderItem(ctx, tx, &itemRow); err != nil {
			return nil, fmt.Errorf("failed to insert order item: %w", err)
		}

		persistedOpts := make([]domain.OrderItemOption, 0, len(pricedItem.Options))

		for _, pricedOpt := range pricedItem.Options {
			parentOptRow := pricedOpt.Option
			parentOptRow.ID = uuid.New()
			parentOptRow.StoreID = storeID
			parentOptRow.OrderItemID = itemRow.ID
			parentOptRow.ParentOrderItemOptionID = nil

			if err := repo.InsertOrderItemOption(ctx, tx, &parentOptRow); err != nil {
				return nil, fmt.Errorf("failed to insert parent order item option: %w", err)
			}
			persistedOpts = append(persistedOpts, parentOptRow)

			for _, childOpt := range pricedOpt.Children {
				childOptRow := childOpt
				childOptRow.ID = uuid.New()
				childOptRow.StoreID = storeID
				childOptRow.OrderItemID = itemRow.ID
				parentRowIDCopy := parentOptRow.ID
				childOptRow.ParentOrderItemOptionID = &parentRowIDCopy

				if err := repo.InsertOrderItemOption(ctx, tx, &childOptRow); err != nil {
					return nil, fmt.Errorf("failed to insert child order item option: %w", err)
				}
				persistedOpts = append(persistedOpts, childOptRow)
			}
		}

		itemRow.Options = persistedOpts
		persistedItems = append(persistedItems, itemRow)
	}

	return persistedItems, nil
}

// RecalculateOrderTotals computes the total subtotal across all active items of an order,
// and computes tax and service charge once over the cumulative net subtotal using CalculateRoundHalfUpBPS.
//
// NOTE (Iteration 4d): When order item void/refund capabilities are introduced in Iteration 4d,
// items with void status must be explicitly excluded from cumulativeSubtotal (e.g., WHERE round_id IS NOT NULL AND status != 'void').
func RecalculateOrderTotals(
	ctx context.Context,
	tx *sqlx.Tx,
	store *domain.Store,
	order *domain.Order,
) error {
	var cumulativeSubtotal int64
	query := `
		SELECT COALESCE(SUM(subtotal_amount), 0)
		FROM order_items
		WHERE store_id = $1 AND order_id = $2
	`
	if err := tx.GetContext(ctx, &cumulativeSubtotal, query, order.StoreID, order.ID); err != nil {
		return fmt.Errorf("failed to sum order items subtotal: %w", err)
	}

	netSubtotal := cumulativeSubtotal - order.DiscountAmount
	if netSubtotal < 0 {
		netSubtotal = 0
	}

	serviceCharge, err := CalculateRoundHalfUpBPS(netSubtotal, store.ServiceBps)
	if err != nil {
		return fmt.Errorf("failed to calculate service charge: %w", err)
	}

	taxableBase := netSubtotal + serviceCharge
	taxAmount, err := CalculateRoundHalfUpBPS(taxableBase, store.TaxBps)
	if err != nil {
		return fmt.Errorf("failed to calculate tax: %w", err)
	}

	totalAmount := netSubtotal + serviceCharge + taxAmount

	order.SubtotalAmount = cumulativeSubtotal
	order.ServiceChargeAmount = serviceCharge
	order.TaxAmount = taxAmount
	order.TotalAmount = totalAmount

	updateQuery := `
		UPDATE orders
		SET subtotal_amount = $3,
		    service_charge_amount = $4,
		    tax_amount = $5,
		    total_amount = $6,
		    updated_at = NOW()
		WHERE store_id = $1 AND id = $2
		RETURNING updated_at
	`
	return tx.QueryRowxContext(
		ctx, updateQuery,
		order.StoreID, order.ID,
		order.SubtotalAmount, order.ServiceChargeAmount, order.TaxAmount, order.TotalAmount,
	).Scan(&order.UpdatedAt)
}
