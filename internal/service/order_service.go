package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

type CreateOrderResult struct {
	Order              domain.Order          `json:"order"`
	Splits             []domain.PaymentSplit `json:"splits"`
	OutboxEventID      uuid.UUID             `json:"outbox_event_id,omitempty"`
	IsIdempotentReplay bool                  `json:"is_idempotent_replay"`
}

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

func (s *OrderService) CreateOrder(ctx context.Context, trustedStoreID uuid.UUID, input OrderCreationInput) (*CreateOrderResult, error) {
	if trustedStoreID == uuid.Nil {
		return nil, postgres.ErrEmptyTenantContext
	}
	if s.db == nil {
		return nil, errors.New("database connection pool is not initialized")
	}

	input.CustomerName = strings.TrimSpace(input.CustomerName)
	input.CustomerPhone = strings.TrimSpace(input.CustomerPhone)
	input.TableNumber = strings.TrimSpace(input.TableNumber)
	input.Notes = strings.TrimSpace(input.Notes)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)

	if input.CustomerName == "" {
		return nil, fmt.Errorf("%w: customer_name is required", ErrInvalidOrderInput)
	}
	if input.IdempotencyKey == "" {
		return nil, fmt.Errorf("%w: idempotency_key is required", ErrInvalidOrderInput)
	}
	if len(input.IdempotencyKey) > 100 {
		return nil, fmt.Errorf("%w: idempotency_key exceeds 100 characters", ErrInvalidOrderInput)
	}
	if len(input.Items) == 0 {
		return nil, fmt.Errorf("%w: order must contain at least one item", ErrInvalidOrderInput)
	}

	res, err := s.executeCreateOrderTx(ctx, trustedStoreID, input)
	if err != nil {
		// Handle concurrent race on identical idempotency_key (PostgreSQL 23505 on uq_orders_store_idempotency_key).
		var pgErr *pgconn.PgError
		if input.IdempotencyKey != "" && errors.As(err, &pgErr) && pgErr.Code == "23505" && strings.Contains(pgErr.ConstraintName, "idempotency") {
			return s.executeCreateOrderTx(ctx, trustedStoreID, input)
		}
		return nil, err
	}
	return res, nil
}

func (s *OrderService) executeCreateOrderTx(ctx context.Context, trustedStoreID uuid.UUID, input OrderCreationInput) (*CreateOrderResult, error) {
	var result *CreateOrderResult

	err := database.WithTenantTx(ctx, s.db, trustedStoreID, func(tx *sqlx.Tx) error {
		store, err := s.repo.GetStoreForOrder(ctx, tx, trustedStoreID)
		if err != nil {
			if errors.Is(err, postgres.ErrStoreNotFound) {
				return fmt.Errorf("%w: %v", ErrCrossTenantAccess, err)
			}
			return err
		}
		if !store.IsActive || store.DeletedAt != nil {
			return ErrStoreInactive
		}

		normalizedInput := input
		if normalizedInput.OrderType == "" {
			normalizedInput.OrderType = domain.OrderTypeDineIn
		}
		if normalizedInput.BillingMode == "" {
			if store.DefaultBillingMode != "" {
				normalizedInput.BillingMode = store.DefaultBillingMode
			} else {
				normalizedInput.BillingMode = domain.BillingModePayFirst
			}
		}

		if normalizedInput.BillingMode == domain.BillingModeSplitBill {
			return fmt.Errorf("%w: split_bill is not supported directly on CreateOrder; use pay_first or open_tab", ErrUnsupportedBillingMode)
		}

		canonicalRequestHash := ComputeInputRequestHash(trustedStoreID, normalizedInput.BillingMode, normalizedInput)

		if normalizedInput.IdempotencyKey != "" {
			existingOrder, existingSplits, err := s.repo.GetOrderByIdempotencyKey(ctx, tx, trustedStoreID, normalizedInput.IdempotencyKey)
			if err == nil && existingOrder != nil {
				if existingOrder.RequestHash == "" {
					return fmt.Errorf("%w: idempotency_key %q collides with a legacy order lacking request_hash", ErrIdempotencyConflict, normalizedInput.IdempotencyKey)
				}
				if existingOrder.RequestHash != canonicalRequestHash {
					return fmt.Errorf("%w: idempotency_key %q was already used with a different order payload", ErrIdempotencyConflict, normalizedInput.IdempotencyKey)
				}
				result = &CreateOrderResult{
					Order:              *existingOrder,
					Splits:             existingSplits,
					IsIdempotentReplay: true,
				}
				return nil
			} else if err != nil && !errors.Is(err, postgres.ErrOrderNotFound) {
				return err
			}
		}

		if normalizedInput.BillingMode == domain.BillingModeOpenTab {
			if normalizedInput.ActorUserID == nil || *normalizedInput.ActorUserID == uuid.Nil {
				return fmt.Errorf("%w: open_tab requires an authenticated staff ActorUserID", ErrUnauthorizedBillingMode)
			}
			isStaff, err := s.repo.VerifyActiveStaffActor(ctx, tx, trustedStoreID, *normalizedInput.ActorUserID)
			if err != nil {
				return err
			}
			if !isStaff {
				return fmt.Errorf("%w: actor %s is not an active owner or cashier in store", ErrUnauthorizedBillingMode, normalizedInput.ActorUserID)
			}
		}

		productIDs := make([]uuid.UUID, 0, len(normalizedInput.Items))
		for _, item := range normalizedInput.Items {
			productIDs = append(productIDs, item.ProductID)
		}

		catalogData, err := s.repo.LockAndLoadCatalogForOrder(ctx, tx, trustedStoreID, productIDs)
		if err != nil {
			return err
		}

		snapshot := CatalogSnapshot{
			Products:          catalogData.Products,
			ModifierGroups:    catalogData.ModifierGroups,
			ModifierOptions:   catalogData.ModifierOptions,
			ProductGroups:     catalogData.ProductModifierGroups,
			OptionChildGroups: catalogData.OptionModifierGroups,
			Variants:          catalogData.Variants,
		}

		priced, err := ValidateAndPriceOrder(catalogData.Store, normalizedInput, snapshot)
		if err != nil {
			return err
		}

		// Deduct product stock in deterministic sorted UUID order.
		for _, prodID := range sortedUUIDKeysFromIntMap(priced.ProductStockDeducts) {
			qty := priced.ProductStockDeducts[prodID]
			if err := s.repo.DeductProductStock(ctx, tx, trustedStoreID, prodID, qty); err != nil {
				if errors.Is(err, postgres.ErrStockUpdateFailed) {
					return ErrInsufficientStock
				}
				return err
			}
		}

		// Deduct modifier option stock in deterministic sorted UUID order.
		for _, optID := range sortedUUIDKeysFromIntMap(priced.OptionStockDeducts) {
			qty := priced.OptionStockDeducts[optID]
			if err := s.repo.DeductModifierOptionStock(ctx, tx, trustedStoreID, optID, qty); err != nil {
				if errors.Is(err, postgres.ErrStockUpdateFailed) {
					return ErrInsufficientStock
				}
				return err
			}
		}

		businessDateISO, compactDate := resolveStoreBusinessDates(s.nowFunc(), store.Timezone)
		seq, err := s.repo.AllocateDailySequence(ctx, tx, trustedStoreID, businessDateISO)
		if err != nil {
			return err
		}

		orderPrefix := strings.TrimSpace(store.OrderPrefix)
		if orderPrefix == "" {
			orderPrefix = "ORD"
		}
		queuePrefix := strings.TrimSpace(store.QueuePrefix)
		if queuePrefix == "" {
			queuePrefix = "A"
		}

		orderNumber := fmt.Sprintf("%s-%s-%04d", orderPrefix, compactDate, seq)
		queueNumber := fmt.Sprintf("%s%03d", queuePrefix, seq)

		initialOrderStatus := domain.OrderStatusPendingPayment
		var expiresAt *time.Time
		if normalizedInput.BillingMode == domain.BillingModeOpenTab {
			initialOrderStatus = domain.OrderStatusQueued
		} else {
			exp := s.nowFunc().UTC().Add(15 * time.Minute)
			expiresAt = &exp
		}

		initialStockStatus := domain.OrderStockStatusNotTracked
		if len(priced.ProductStockDeducts) > 0 || len(priced.OptionStockDeducts) > 0 {
			initialStockStatus = domain.OrderStockStatusReserved
			if normalizedInput.BillingMode == domain.BillingModeOpenTab {
				initialStockStatus = domain.OrderStockStatusCommitted
			}
		}

		var createdByUserID *uuid.UUID
		if normalizedInput.ActorUserID != nil && *normalizedInput.ActorUserID != uuid.Nil {
			actorCopy := *normalizedInput.ActorUserID
			createdByUserID = &actorCopy
		}

		order := domain.Order{
			ID:                  uuid.New(),
			StoreID:             trustedStoreID,
			IdempotencyKey:      normalizedInput.IdempotencyKey,
			RequestHash:         priced.RequestHash,
			OrderNumber:         orderNumber,
			QueueNumber:         queueNumber,
			TableNumber:         normalizedInput.TableNumber,
			CustomerName:        normalizedInput.CustomerName,
			CustomerPhone:       normalizedInput.CustomerPhone,
			OrderType:           normalizedInput.OrderType,
			BillingMode:         normalizedInput.BillingMode,
			Status:              initialOrderStatus,
			PaymentStatus:       domain.OrderPaymentStatusUnpaid,
			StockStatus:         initialStockStatus,
			ExpiresAt:           expiresAt,
			CreatedByUserID:     createdByUserID,
			SubtotalAmount:      priced.SubtotalAmount,
			DiscountAmount:      priced.DiscountAmount,
			TaxAmount:           priced.TaxAmount,
			ServiceChargeAmount: priced.ServiceChargeAmount,
			TotalAmount:         priced.TotalAmount,
			PaidAmount:          0,
			RefundedAmount:      0,
			Notes:               normalizedInput.Notes,
		}

		if err := s.repo.InsertOrder(ctx, tx, &order); err != nil {
			return fmt.Errorf("failed to insert order: %w", err)
		}

		persistedItems := make([]domain.OrderItem, 0, len(priced.Items))
		for _, pricedItem := range priced.Items {
			itemRow := pricedItem.Item
			itemRow.ID = uuid.New()
			itemRow.StoreID = trustedStoreID
			itemRow.OrderID = order.ID

			if err := s.repo.InsertOrderItem(ctx, tx, &itemRow); err != nil {
				return fmt.Errorf("failed to insert order item: %w", err)
			}

			persistedOpts := make([]domain.OrderItemOption, 0, len(pricedItem.Options))

			for _, pricedOpt := range pricedItem.Options {
				parentOptRow := pricedOpt.Option
				parentOptRow.ID = uuid.New()
				parentOptRow.StoreID = trustedStoreID
				parentOptRow.OrderItemID = itemRow.ID
				parentOptRow.ParentOrderItemOptionID = nil

				if err := s.repo.InsertOrderItemOption(ctx, tx, &parentOptRow); err != nil {
					return fmt.Errorf("failed to insert parent order item option: %w", err)
				}
				persistedOpts = append(persistedOpts, parentOptRow)

				for _, childOpt := range pricedOpt.Children {
					childOptRow := childOpt
					childOptRow.ID = uuid.New()
					childOptRow.StoreID = trustedStoreID
					childOptRow.OrderItemID = itemRow.ID
					parentRowIDCopy := parentOptRow.ID
					childOptRow.ParentOrderItemOptionID = &parentRowIDCopy

					if err := s.repo.InsertOrderItemOption(ctx, tx, &childOptRow); err != nil {
						return fmt.Errorf("failed to insert child order item option: %w", err)
					}
					persistedOpts = append(persistedOpts, childOptRow)
				}
			}

			itemRow.Options = persistedOpts
			persistedItems = append(persistedItems, itemRow)
		}
		order.Items = persistedItems

		initialSplit := domain.PaymentSplit{
			ID:                  uuid.New(),
			StoreID:             trustedStoreID,
			OrderID:             order.ID,
			SplitNumber:         1,
			Label:               "Main Bill",
			SplitMode:           domain.SplitModeFull,
			SubtotalAmount:      order.SubtotalAmount - order.DiscountAmount,
			TaxAmount:           order.TaxAmount,
			ServiceChargeAmount: order.ServiceChargeAmount,
			TotalAmount:         order.TotalAmount,
			PaidAmount:          0,
			Status:              domain.SplitBillStatusUnpaid,
		}
		if err := s.repo.InsertPaymentSplit(ctx, tx, &initialSplit); err != nil {
			return fmt.Errorf("failed to insert initial payment split: %w", err)
		}

		if s.beforeOutboxHook != nil {
			if err := s.beforeOutboxHook(ctx, tx); err != nil {
				return fmt.Errorf("beforeOutboxHook aborted transaction: %w", err)
			}
		}

		outboxPayloadBytes, err := json.Marshal(map[string]any{
			"order_id":       order.ID.String(),
			"store_id":       order.StoreID.String(),
			"order_number":   order.OrderNumber,
			"queue_number":   order.QueueNumber,
			"table_number":   order.TableNumber,
			"order_type":     order.OrderType,
			"billing_mode":   order.BillingMode,
			"status":         order.Status,
			"payment_status": order.PaymentStatus,
			"total_amount":   order.TotalAmount,
		})
		if err != nil {
			return fmt.Errorf("failed to marshal outbox payload: %w", err)
		}

		outboxEvent := domain.OutboxEvent{
			ID:            uuid.New(),
			StoreID:       trustedStoreID,
			EventType:     "order.created",
			AggregateType: "order",
			AggregateID:   order.ID.String(),
			Payload:       string(outboxPayloadBytes),
		}
		if err := s.repo.InsertOutboxEvent(ctx, tx, &outboxEvent); err != nil {
			return fmt.Errorf("failed to insert outbox event: %w", err)
		}

		result = &CreateOrderResult{
			Order:              order,
			Splits:             []domain.PaymentSplit{initialSplit},
			OutboxEventID:      outboxEvent.ID,
			IsIdempotentReplay: false,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
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
			prodRestores, optRestores := computeOrderStockQuantities(order)

			for _, prodID := range sortedUUIDKeysFromIntMap(prodRestores) {
				if err := s.repo.RestoreProductStock(ctx, tx, trustedStoreID, prodID, prodRestores[prodID]); err != nil {
					return err
				}
			}
			for _, optID := range sortedUUIDKeysFromIntMap(optRestores) {
				if err := s.repo.RestoreModifierOptionStock(ctx, tx, trustedStoreID, optID, optRestores[optID]); err != nil {
					return err
				}
			}
			order.StockStatus = domain.OrderStockStatusReleased
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
	prodQuantities := make(map[uuid.UUID]int)
	optQuantities := make(map[uuid.UUID]int)
	if order == nil {
		return prodQuantities, optQuantities
	}
	for _, item := range order.Items {
		if item.ProductID != nil && *item.ProductID != uuid.Nil {
			prodQuantities[*item.ProductID] += item.Quantity
		}
		for _, opt := range item.Options {
			if opt.ModifierOptionID != nil && *opt.ModifierOptionID != uuid.Nil {
				// Note: opt.Quantity on persisted order_item_options already stores the effective per-item option quantity
				// (for parent options: modReq.Quantity; for child sub-addons: parentOptionQuantity * subReq.Quantity).
				optQuantities[*opt.ModifierOptionID] += opt.Quantity * item.Quantity
			}
		}
	}
	return prodQuantities, optQuantities
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
