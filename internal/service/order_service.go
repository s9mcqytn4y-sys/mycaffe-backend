package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

type OrderServiceOption func(*OrderService)

func WithClock(nowFunc func() time.Time) OrderServiceOption {
	return func(s *OrderService) {
		if nowFunc != nil {
			s.nowFunc = nowFunc
		}
	}
}

func WithBeforeOutboxHook(hook func(ctx context.Context, tx *sqlx.Tx) error) OrderServiceOption {
	return func(s *OrderService) {
		s.beforeOutboxHook = hook
	}
}

type OrderService struct {
	db               *sqlx.DB
	repo             *postgres.OrderRepository
	nowFunc          func() time.Time
	beforeOutboxHook func(ctx context.Context, tx *sqlx.Tx) error
}

func NewOrderService(db *sqlx.DB, repo *postgres.OrderRepository, opts ...OrderServiceOption) *OrderService {
	if repo == nil {
		repo = postgres.NewOrderRepository()
	}
	svc := &OrderService{
		db:      db,
		repo:    repo,
		nowFunc: time.Now,
	}
	for _, opt := range opts {
		opt(svc)
	}
	return svc
}

// CancelOrExpireUnpaidOrder idempotently cancels an unpaid order (due to payment expiry or manual cancellation)
// and restores any reserved product and modifier option stock back to the catalog exactly once.
func (s *OrderService) CancelOrExpireUnpaidOrder(
	ctx context.Context,
	trustedStoreID uuid.UUID,
	orderID uuid.UUID,
	targetPaymentStatus domain.OrderPaymentStatus,
	reason string,
) (*domain.Order, error) {
	if trustedStoreID == uuid.Nil {
		return nil, postgres.ErrEmptyTenantContext
	}
	if orderID == uuid.Nil {
		return nil, postgres.ErrOrderNotFound
	}
	if targetPaymentStatus != domain.OrderPaymentStatusExpired && targetPaymentStatus != domain.OrderPaymentStatusFailed && targetPaymentStatus != domain.OrderPaymentStatusUnpaid {
		targetPaymentStatus = domain.OrderPaymentStatusExpired
	}

	var updatedOrder *domain.Order
	err := database.WithTenantTx(ctx, s.db, trustedStoreID, func(tx *sqlx.Tx) error {
		order, _, err := s.repo.GetOrderByIDForUpdate(ctx, tx, trustedStoreID, orderID)
		if err != nil {
			return err
		}

		// Guard: Paid or partially paid orders cannot be expired/cancelled as unpaid.
		if order.PaymentStatus == domain.OrderPaymentStatusPaid || order.PaymentStatus == domain.OrderPaymentStatusPartiallyPaid {
			return ErrOrderAlreadyPaid
		}

		// Idempotent replay: if already cancelled and stock is already released (or not_tracked), return immediately.
		if order.Status == domain.OrderStatusCancelled && (order.StockStatus == domain.OrderStockStatusReleased || order.StockStatus == domain.OrderStockStatusNotTracked) {
			updatedOrder = order
			return nil
		}

		// Restore stock only if currently reserved.
		if order.StockStatus == domain.OrderStockStatusReserved {
			if err := releaseOrderStockTx(ctx, tx, s.repo, order); err != nil {
				return err
			}
		}

		order.Status = domain.OrderStatusCancelled
		order.PaymentStatus = targetPaymentStatus

		if err := s.repo.UpdateOrderStatusAndStock(ctx, tx, order); err != nil {
			return fmt.Errorf("failed to update cancelled order status: %w", err)
		}

		if err := s.repo.UpdatePaymentSplitsStatusByOrder(ctx, tx, trustedStoreID, order.ID, domain.SplitBillStatusCancelled, false); err != nil {
			return err
		}

		eventType := "order.expired"
		if targetPaymentStatus != domain.OrderPaymentStatusExpired {
			eventType = "order.cancelled"
		}
		payloadBytes, err := json.Marshal(map[string]any{
			"order_id":       order.ID.String(),
			"store_id":       order.StoreID.String(),
			"order_number":   order.OrderNumber,
			"status":         order.Status,
			"payment_status": order.PaymentStatus,
			"stock_status":   order.StockStatus,
			"reason":         strings.TrimSpace(reason),
		})
		if err != nil {
			return err
		}
		outboxEvent := domain.OutboxEvent{
			ID:            uuid.New(),
			StoreID:       trustedStoreID,
			EventType:     eventType,
			AggregateType: "order",
			AggregateID:   order.ID.String(),
			Payload:       string(payloadBytes),
		}
		if err := s.repo.InsertOutboxEvent(ctx, tx, &outboxEvent); err != nil {
			return err
		}

		updatedOrder = order
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updatedOrder, nil
}

// ConfirmOrderPaymentAndCommitStock transitions an order to paid + queued and commits its stock reservation.
// If the order had already expired and released its stock before a late settlement notification arrived,
// it attempts to atomically re-deduct stock or returns ErrLatePaymentStockExhausted so the caller can flag for refund/review.
func (s *OrderService) ConfirmOrderPaymentAndCommitStock(
	ctx context.Context,
	trustedStoreID uuid.UUID,
	orderID uuid.UUID,
) (*domain.Order, error) {
	if trustedStoreID == uuid.Nil {
		return nil, postgres.ErrEmptyTenantContext
	}
	if orderID == uuid.Nil {
		return nil, postgres.ErrOrderNotFound
	}

	var updatedOrder *domain.Order
	err := database.WithTenantTx(ctx, s.db, trustedStoreID, func(tx *sqlx.Tx) error {
		order, _, err := s.repo.GetOrderByIDForUpdate(ctx, tx, trustedStoreID, orderID)
		if err != nil {
			return err
		}

		// Idempotent replay: already paid and committed.
		if order.PaymentStatus == domain.OrderPaymentStatusPaid &&
			(order.StockStatus == domain.OrderStockStatusCommitted || order.StockStatus == domain.OrderStockStatusNotTracked) {
			updatedOrder = order
			return nil
		}

		// If stock was previously released due to expiry before late settlement arrived, attempt atomic re-deduction.
		if order.StockStatus == domain.OrderStockStatusReleased {
			prodDeducts, optDeducts := computeOrderStockQuantities(order)
			for _, prodID := range sortedUUIDKeysFromIntMap(prodDeducts) {
				if err := s.repo.DeductProductStock(ctx, tx, trustedStoreID, prodID, prodDeducts[prodID]); err != nil {
					if errors.Is(err, postgres.ErrStockUpdateFailed) {
						return ErrLatePaymentStockExhausted
					}
					return err
				}
			}
			for _, optID := range sortedUUIDKeysFromIntMap(optDeducts) {
				if err := s.repo.DeductModifierOptionStock(ctx, tx, trustedStoreID, optID, optDeducts[optID]); err != nil {
					if errors.Is(err, postgres.ErrStockUpdateFailed) {
						return ErrLatePaymentStockExhausted
					}
					return err
				}
			}
			order.StockStatus = domain.OrderStockStatusCommitted
		} else if order.StockStatus == domain.OrderStockStatusReserved {
			order.StockStatus = domain.OrderStockStatusCommitted
		}

		if order.Status == domain.OrderStatusPendingPayment || order.Status == domain.OrderStatusCancelled {
			order.Status = domain.OrderStatusQueued
		}
		order.PaymentStatus = domain.OrderPaymentStatusPaid
		order.PaidAmount = order.TotalAmount
		order.ExpiresAt = nil

		if err := s.repo.UpdateOrderStatusAndStock(ctx, tx, order); err != nil {
			return fmt.Errorf("failed to update paid order status: %w", err)
		}

		if err := s.repo.UpdatePaymentSplitsStatusByOrder(ctx, tx, trustedStoreID, order.ID, domain.SplitBillStatusPaid, true); err != nil {
			return err
		}

		payloadBytes, err := json.Marshal(map[string]any{
			"order_id":       order.ID.String(),
			"store_id":       order.StoreID.String(),
			"order_number":   order.OrderNumber,
			"queue_number":   order.QueueNumber,
			"status":         order.Status,
			"payment_status": order.PaymentStatus,
			"stock_status":   order.StockStatus,
			"paid_amount":    order.PaidAmount,
		})
		if err != nil {
			return err
		}
		outboxEvent := domain.OutboxEvent{
			ID:            uuid.New(),
			StoreID:       trustedStoreID,
			EventType:     "order.paid",
			AggregateType: "order",
			AggregateID:   order.ID.String(),
			Payload:       string(payloadBytes),
		}
		if err := s.repo.InsertOutboxEvent(ctx, tx, &outboxEvent); err != nil {
			return err
		}

		updatedOrder = order
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updatedOrder, nil
}

func computeOrderStockQuantities(order *domain.Order) (map[uuid.UUID]int, map[uuid.UUID]int) {
	return computeItemStockQuantities(order, nil)
}

// computeItemStockQuantities sums product and modifier-option stock for the order's lines; when
// lineQty is non-nil only those order_item ids count, with the given quantity (by-item split refunds).
func computeItemStockQuantities(order *domain.Order, lineQty map[uuid.UUID]int) (map[uuid.UUID]int, map[uuid.UUID]int) {
	prodQuantities := make(map[uuid.UUID]int)
	optQuantities := make(map[uuid.UUID]int)
	if order == nil {
		return prodQuantities, optQuantities
	}
	for _, item := range order.Items {
		qty := item.Quantity
		if lineQty != nil {
			qty = min(lineQty[item.ID], item.Quantity)
		}
		if qty <= 0 {
			continue
		}
		if item.ProductID != nil && *item.ProductID != uuid.Nil {
			prodQuantities[*item.ProductID] += qty
		}
		for _, opt := range item.Options {
			if opt.ModifierOptionID != nil && *opt.ModifierOptionID != uuid.Nil {
				// Note: opt.Quantity on persisted order_item_options already stores the effective per-item option quantity
				// (for parent options: modReq.Quantity; for child sub-addons: parentOptionQuantity * subReq.Quantity).
				optQuantities[*opt.ModifierOptionID] += opt.Quantity * qty
			}
		}
	}
	return prodQuantities, optQuantities
}

// restoreStockTx adds the given product and modifier-option quantities back to the catalog.
func restoreStockTx(ctx context.Context, tx *sqlx.Tx, repo *postgres.OrderRepository, storeID uuid.UUID, products, options map[uuid.UUID]int) error {
	for _, productID := range sortedUUIDKeysFromIntMap(products) {
		if err := repo.RestoreProductStock(ctx, tx, storeID, productID, products[productID]); err != nil {
			return err
		}
	}
	for _, optionID := range sortedUUIDKeysFromIntMap(options) {
		if err := repo.RestoreModifierOptionStock(ctx, tx, storeID, optionID, options[optionID]); err != nil {
			return err
		}
	}
	return nil
}

// releaseOrderStockTx returns all stock the order still holds (reserved for unpaid orders and tab
// rounds, committed once paid) and marks it released, so a second call or a later cancel is a no-op.
// The caller persists order.StockStatus.
func releaseOrderStockTx(ctx context.Context, tx *sqlx.Tx, repo *postgres.OrderRepository, order *domain.Order) error {
	if order.StockStatus != domain.OrderStockStatusReserved && order.StockStatus != domain.OrderStockStatusCommitted {
		return nil
	}
	products, options := computeOrderStockQuantities(order)
	if err := restoreStockTx(ctx, tx, repo, order.StoreID, products, options); err != nil {
		return err
	}
	order.StockStatus = domain.OrderStockStatusReleased
	return nil
}

func resolveStoreBusinessDates(now time.Time, tzName string) (string, string) {
	tz := strings.TrimSpace(tzName)
	if tz == "" {
		tz = "Asia/Jakarta"
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		// Fallback to UTC+7 fixed zone if tzdata is missing on minimal host.
		loc = time.FixedZone("WIB", 7*3600)
	}
	localNow := now.In(loc)
	return localNow.Format("2006-01-02"), localNow.Format("20060102")
}

func sortedUUIDKeysFromIntMap(m map[uuid.UUID]int) []uuid.UUID {
	keys := make([]uuid.UUID, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j].String() < keys[i].String() {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}
