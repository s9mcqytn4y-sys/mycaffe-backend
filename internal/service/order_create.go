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
	if err := ValidateItemInputs(input.Items); err != nil {
		return nil, err
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

		if normalizedInput.Guest != nil {
			// Session validity, pause, open shift and unpaid caps are re-checked under row locks (QR4, QR5).
			if err := normalizedInput.Guest.Guard(ctx, tx); err != nil {
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

		actualFingerprint := ComputeQuoteFingerprint(catalogData.Store, priced)
		totalMismatch := normalizedInput.ExpectedTotal != nil && *normalizedInput.ExpectedTotal != priced.TotalAmount
		fingerprintMismatch := normalizedInput.ExpectedQuoteFingerprint != "" && normalizedInput.ExpectedQuoteFingerprint != actualFingerprint

		if totalMismatch || fingerprintMismatch {
			var expTotal int64
			if normalizedInput.ExpectedTotal != nil {
				expTotal = *normalizedInput.ExpectedTotal
			}
			return &QuoteChangedError{
				ExpectedTotal:       expTotal,
				ActualTotal:         priced.TotalAmount,
				ExpectedFingerprint: normalizedInput.ExpectedQuoteFingerprint,
				ActualFingerprint:   actualFingerprint,
				Quote:               buildQuoteResult(catalogData.Store, priced),
			}
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

		orderNumber, queueNumber, err := AllocateOrderIdentity(ctx, tx, s.repo, store, trustedStoreID, s.nowFunc())
		if err != nil {
			return err
		}

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

		if g := normalizedInput.Guest; g != nil {
			order.TableID, order.TableSessionID, order.GuestSessionID = &g.TableID, &g.TableSessionID, &g.GuestSessionID
			order.TrackingTokenHash = g.TrackingTokenHash
		}

		if err := s.repo.InsertOrder(ctx, tx, &order); err != nil {
			return fmt.Errorf("failed to insert order: %w", err)
		}

		persistedItems, err := InsertPricedItems(ctx, tx, s.repo, trustedStoreID, order.ID, nil, priced.Items)
		if err != nil {
			return err
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
