package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

// flagDuplicateSettlementTx reports whether the order was already paid by another attempt
// (e.g. cash taken while a QRIS charge stayed open) and, if so, emits payment.refund_required.
// priorStatus is the attempt status before it became paid; paid->paid (capture->settlement) is not a duplicate.
func (s *PaymentService) flagDuplicateSettlementTx(ctx context.Context, tx *sqlx.Tx, order *domain.Order, priorStatus domain.PaymentAttemptStatus) (bool, error) {
	if order.PaymentStatus != domain.OrderPaymentStatusPaid || priorStatus == domain.PaymentStatusPaid {
		return false, nil
	}
	return true, s.insertPaymentOutboxTx(ctx, tx, order, "payment.refund_required", map[string]any{
		"reason": "duplicate settlement: order already paid by another payment attempt",
	})
}

func (s *PaymentService) commitPaidOrderTx(ctx context.Context, tx *sqlx.Tx, order *domain.Order) error {
	if order == nil {
		return errors.New("order is required")
	}
	if order.PaymentStatus == domain.OrderPaymentStatusPaid &&
		(order.StockStatus == domain.OrderStockStatusCommitted || order.StockStatus == domain.OrderStockStatusNotTracked) {
		return nil
	}

	if order.Status == domain.OrderStatusCancelled && order.PaymentStatus != domain.OrderPaymentStatusExpired {
		order.PaymentStatus = domain.OrderPaymentStatusPaid
		order.PaidAmount = order.TotalAmount
		order.ExpiresAt = nil
		if err := s.orderRepo.UpdateOrderStatusAndStock(ctx, tx, order); err != nil {
			return err
		}
		return s.insertPaymentOutboxTx(ctx, tx, order, "payment.refund_required", map[string]any{
			"reason": "late settlement after manual cancellation",
		})
	}

	if order.StockStatus == domain.OrderStockStatusReleased {
		if _, err := tx.ExecContext(ctx, "SAVEPOINT payment_stock_commit"); err != nil {
			return fmt.Errorf("failed to create stock commit savepoint: %w", err)
		}
		products, options := computeOrderStockQuantities(order)
		var stockErr error
		for _, productID := range sortedUUIDKeysFromIntMap(products) {
			if err := s.orderRepo.DeductProductStock(ctx, tx, order.StoreID, productID, products[productID]); err != nil {
				stockErr = err
				break
			}
		}
		if stockErr == nil {
			for _, optionID := range sortedUUIDKeysFromIntMap(options) {
				if err := s.orderRepo.DeductModifierOptionStock(ctx, tx, order.StoreID, optionID, options[optionID]); err != nil {
					stockErr = err
					break
				}
			}
		}
		if errors.Is(stockErr, postgres.ErrStockUpdateFailed) {
			if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT payment_stock_commit"); err != nil {
				return fmt.Errorf("failed to roll back partial stock commit: %w", err)
			}
			if _, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT payment_stock_commit"); err != nil {
				return err
			}
			order.PaymentStatus = domain.OrderPaymentStatusPaid
			order.PaidAmount = order.TotalAmount
			order.Status = domain.OrderStatusCancelled
			order.StockStatus = domain.OrderStockStatusReleased
			order.ExpiresAt = nil
			if err := s.orderRepo.UpdateOrderStatusAndStock(ctx, tx, order); err != nil {
				return err
			}
			return s.insertPaymentOutboxTx(ctx, tx, order, "payment.late_settlement_stock_exhausted", map[string]any{
				"reason": "payment settled after stock reservation was released and stock is no longer available",
			})
		}
		if stockErr != nil {
			return fmt.Errorf("failed to recommit released stock: %w", stockErr)
		}
		if _, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT payment_stock_commit"); err != nil {
			return err
		}
		order.StockStatus = domain.OrderStockStatusCommitted
	} else if order.StockStatus == domain.OrderStockStatusReserved {
		order.StockStatus = domain.OrderStockStatusCommitted
	}

	// Open Tab lifecycle handling (R4 & P10/P12)
	if order.BillingMode == domain.BillingModeOpenTab {
		var unpaidCount int
		if err := tx.GetContext(ctx, &unpaidCount, `
			SELECT COUNT(*)
			FROM payment_splits
			WHERE store_id = $1 AND order_id = $2 AND status NOT IN ('paid', 'cancelled')
		`, order.StoreID, order.ID); err != nil {
			return err
		}
		var sumPaid int64
		if err := tx.GetContext(ctx, &sumPaid, `
			SELECT COALESCE(SUM(paid_amount), 0)
			FROM payment_splits
			WHERE store_id = $1 AND order_id = $2 AND status IN ('paid', 'partially_paid')
		`, order.StoreID, order.ID); err != nil {
			return err
		}
		order.PaidAmount = sumPaid
		if unpaidCount == 0 {
			order.PaymentStatus = domain.OrderPaymentStatusPaid
			order.Status = domain.OrderStatusCompleted
			order.ExpiresAt = nil
			if err := s.orderRepo.UpdateOrderStatusAndStock(ctx, tx, order); err != nil {
				return fmt.Errorf("failed to update paid tab order: %w", err)
			}
			if err := refreshTableSettledTx(ctx, tx, order.StoreID, order.TableSessionID, s.nowFunc().UTC()); err != nil {
				return err
			}
			return s.insertPaymentOutboxTx(ctx, tx, order, "order.paid", map[string]any{
				"paid_amount":  order.PaidAmount,
				"status":       order.Status,
				"stock_status": order.StockStatus,
			})
		}

		order.PaymentStatus = domain.OrderPaymentStatusPartiallyPaid
		order.ExpiresAt = nil
		if err := s.orderRepo.UpdateOrderStatusAndStock(ctx, tx, order); err != nil {
			return fmt.Errorf("failed to update partially paid tab order: %w", err)
		}
		return s.insertPaymentOutboxTx(ctx, tx, order, "payment.split_settled", map[string]any{
			"paid_amount":             order.PaidAmount,
			"remaining_unpaid_splits": unpaidCount,
		})
	}

	// A cancelled order revived by a late settlement is flagged on its KDS tickets (decision Q7).
	latePayment := order.Status == domain.OrderStatusCancelled
	if order.Status == domain.OrderStatusPendingPayment || order.Status == domain.OrderStatusCancelled {
		order.Status = domain.OrderStatusQueued
	}
	order.PaymentStatus = domain.OrderPaymentStatusPaid
	order.PaidAmount = order.TotalAmount
	order.ExpiresAt = nil
	if order.Status == domain.OrderStatusQueued {
		tickets, err := createKDSTicketsTx(ctx, tx, s.orderRepo, order, nil, latePayment, s.nowFunc().UTC())
		if err != nil {
			return err
		}
		if tickets == 0 {
			order.Status = domain.OrderStatusReady // nothing to prepare (ready-to-serve items only)
		}
	}
	if err := s.orderRepo.UpdateOrderStatusAndStock(ctx, tx, order); err != nil {
		return fmt.Errorf("failed to update paid order: %w", err)
	}
	if err := s.orderRepo.UpdatePaymentSplitsStatusByOrder(ctx, tx, order.StoreID, order.ID, domain.SplitBillStatusPaid, true); err != nil {
		return err
	}
	return s.insertPaymentOutboxTx(ctx, tx, order, "order.paid", map[string]any{
		"paid_amount":  order.PaidAmount,
		"status":       order.Status,
		"stock_status": order.StockStatus,
	})
}

func (s *PaymentService) applyNonPaidStatusTx(
	ctx context.Context,
	tx *sqlx.Tx,
	order *domain.Order,
	status domain.PaymentAttemptStatus,
) error {
	if order == nil || order.PaymentStatus == domain.OrderPaymentStatusPaid {
		return nil
	}
	order.PaymentStatus = orderPaymentStatusFromAttempt(status)

	deadlinePassed := order.ExpiresAt != nil && !s.nowFunc().Before(*order.ExpiresAt)
	if deadlinePassed {
		if order.StockStatus == domain.OrderStockStatusReserved {
			if err := releaseOrderStockTx(ctx, tx, s.orderRepo, order); err != nil {
				return err
			}
		}
		order.Status = domain.OrderStatusCancelled
		order.PaymentStatus = domain.OrderPaymentStatusExpired
		order.ExpiresAt = nil
		if err := s.orderRepo.UpdatePaymentSplitsStatusByOrder(ctx, tx, order.StoreID, order.ID, domain.SplitBillStatusCancelled, false); err != nil {
			return err
		}
		if err := s.insertPaymentOutboxTx(ctx, tx, order, "order.expired", map[string]any{
			"reason": "payment reservation deadline elapsed",
		}); err != nil {
			return err
		}
		return s.orderRepo.UpdateOrderStatusAndStock(ctx, tx, order)
	}
	return s.orderRepo.UpdateOrderStatusAndStock(ctx, tx, order)
}

func orderPaymentStatusFromAttempt(status domain.PaymentAttemptStatus) domain.OrderPaymentStatus {
	switch status {
	case domain.PaymentStatusPending, domain.PaymentStatusAuthorized:
		return domain.OrderPaymentStatusPending
	case domain.PaymentStatusFailed:
		return domain.OrderPaymentStatusFailed
	case domain.PaymentStatusCancelled:
		return domain.OrderPaymentStatusCancelled
	case domain.PaymentStatusExpired:
		return domain.OrderPaymentStatusExpired
	case domain.PaymentStatusPaid:
		return domain.OrderPaymentStatusPaid
	default:
		return domain.OrderPaymentStatusPending
	}
}

func (s *PaymentService) insertPaymentOutboxTx(
	ctx context.Context,
	tx *sqlx.Tx,
	order *domain.Order,
	eventType string,
	extra map[string]any,
) error {
	payload := map[string]any{
		"order_id":       order.ID.String(),
		"store_id":       order.StoreID.String(),
		"order_number":   order.OrderNumber,
		"queue_number":   order.QueueNumber,
		"status":         order.Status,
		"payment_status": order.PaymentStatus,
		"stock_status":   order.StockStatus,
		"paid_amount":    order.PaidAmount,
	}
	for key, value := range extra {
		payload[key] = value
	}
	bytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	event := domain.OutboxEvent{
		ID:            uuid.New(),
		StoreID:       order.StoreID,
		EventType:     eventType,
		AggregateType: "order",
		AggregateID:   order.ID.String(),
		Payload:       string(bytes),
	}
	return s.orderRepo.InsertOutboxEvent(ctx, tx, &event)
}

// recordPartialPaymentTx marks an order partially paid after a partial direct tender (V12). The order
// stops expiring (money is attached) and is sent to the kitchen only once fully paid.
func (s *PaymentService) recordPartialPaymentTx(ctx context.Context, tx *sqlx.Tx, order *domain.Order) error {
	if err := tx.GetContext(ctx, &order.PaidAmount, `
		SELECT COALESCE(SUM(paid_amount), 0) FROM payment_splits WHERE store_id = $1 AND order_id = $2`, order.StoreID, order.ID); err != nil {
		return err
	}
	order.PaymentStatus = domain.OrderPaymentStatusPartiallyPaid
	order.ExpiresAt = nil
	if err := s.orderRepo.UpdateOrderStatusAndStock(ctx, tx, order); err != nil {
		return fmt.Errorf("failed to update partially paid order: %w", err)
	}
	return s.insertPaymentOutboxTx(ctx, tx, order, "payment.partially_paid", map[string]any{
		"paid_amount": order.PaidAmount, "remaining": order.TotalAmount - order.PaidAmount,
	})
}

// roundToNearest rounds a cash amount to the nearest step, halves up (V11). step 0 disables rounding.
func roundToNearest(amount, step int64) int64 {
	if step <= 0 {
		return amount
	}
	return (amount + step/2) / step * step
}
