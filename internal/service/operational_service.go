package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

var (
	ErrOrderAlreadyCancelled    = errors.New("order is already cancelled")
	ErrOrderHasPayments         = errors.New("order has settled or pending payments and cannot be voided; use refund instead")
	ErrOrderNotRefundable       = errors.New("order or payment is not in refundable state")
	ErrPaymentAlreadyRefunded   = errors.New("payment has already been refunded")
	ErrOwnerPINRequired         = errors.New("action amount exceeds store threshold; owner PIN is required")
	ErrInvalidStaffPIN          = errors.New("invalid staff PIN")
	ErrInsufficientCashInDrawer = errors.New("insufficient cash in active drawer for cash refund")
	ErrRefundMethodNotSupported = errors.New("only cash payments can be refunded at the POS until a non-cash refund policy is approved")
	ErrProductNotTracked        = errors.New("product is not configured for inventory tracking")
)

type OperationalService struct {
	db          *sqlx.DB
	orderRepo   *postgres.OrderRepository
	paymentRepo *postgres.PaymentRepository
	shiftRepo   *postgres.ShiftRepository
	settings    *postgres.SettingsRepository
	pins        *pinGuard
	nowFunc     func() time.Time
}

func NewOperationalService(
	db *sqlx.DB,
	orderRepo *postgres.OrderRepository,
	paymentRepo *postgres.PaymentRepository,
	shiftRepo *postgres.ShiftRepository,
	authRepo *postgres.AuthRepository,
	settings any,
) *OperationalService {
	if orderRepo == nil {
		orderRepo = postgres.NewOrderRepository()
	}
	if paymentRepo == nil {
		paymentRepo = postgres.NewPaymentRepository()
	}
	if shiftRepo == nil {
		shiftRepo = postgres.NewShiftRepository()
	}
	if authRepo == nil {
		authRepo = postgres.NewAuthRepository()
	}
	settingsRepo, ok := settings.(*postgres.SettingsRepository)
	if !ok || settingsRepo == nil {
		settingsRepo = postgres.NewSettingsRepository()
	}
	s := &OperationalService{
		db:          db,
		orderRepo:   orderRepo,
		paymentRepo: paymentRepo,
		shiftRepo:   shiftRepo,
		settings:    settingsRepo,
		nowFunc:     time.Now,
	}
	s.pins = newPINGuard(db, authRepo, func() time.Time { return s.nowFunc() })
	return s
}

// authorizeAmount enforces decision O1: the actor's own PIN up to void_refund_owner_threshold, an
// owner PIN above it. It returns the approving owner's id when an owner PIN was used.
func (s *OperationalService) authorizeAmount(ctx context.Context, tx *sqlx.Tx, actor *AuthenticatedStaff, amount int64, pin string, ownerPIN *string) (*uuid.UUID, error) {
	settings, err := s.settings.GetStoreSettings(ctx, tx, actor.Staff.Store.ID)
	if err != nil {
		return nil, err
	}
	threshold := settings.VoidRefundOwnerThreshold
	if threshold <= 0 {
		threshold = 100000 // default Rp 100.000
	}
	pin = strings.TrimSpace(pin)
	if amount > threshold {
		owner := ""
		if ownerPIN != nil {
			owner = strings.TrimSpace(*ownerPIN)
		}
		if owner == "" && actor.Staff.Role == domain.RoleOwner {
			owner = pin
		}
		if owner == "" {
			return nil, ErrOwnerPINRequired
		}
		ownerID, err := s.pins.verifyOwner(ctx, tx, actor, owner)
		if err != nil {
			return nil, err
		}
		return &ownerID, nil
	}
	if pin == "" {
		return nil, &ValidationError{Fields: map[string]string{"pin": "PIN staf wajib diisi"}}
	}
	return nil, s.pins.verifyStaff(ctx, tx, actor, pin)
}

type VoidOrderInput struct {
	Reason   string  `json:"reason"`
	PIN      string  `json:"pin"`
	OwnerPIN *string `json:"owner_pin"`
}

type VoidOrderResult struct {
	Order   *domain.Order `json:"order"`
	Message string        `json:"message"`
}

// VoidOrder cancels an order that has no money attached (no paid, refunded, pending or authorized
// payment on the order or its splits), returns the stock it still holds exactly once, cancels its
// rounds and emits order.cancelled. Authority follows decision O1.
func (s *OperationalService) VoidOrder(
	ctx context.Context,
	actor *AuthenticatedStaff,
	orderID uuid.UUID,
	in VoidOrderInput,
) (*VoidOrderResult, error) {
	if actor == nil {
		return nil, ErrInvalidSession
	}
	if !actor.Can(rbac.OrderVoid) {
		return nil, ErrForbidden
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return nil, &ValidationError{Fields: map[string]string{"reason": "alasan pembatalan (void) wajib diisi"}}
	}

	storeID := actor.Staff.Store.ID
	var result *VoidOrderResult
	err := s.pins.withPINTx(ctx, storeID, func(tx *sqlx.Tx) error {
		order, _, err := s.orderRepo.GetOrderByIDForUpdate(ctx, tx, storeID, orderID)
		if err != nil {
			return err
		}
		if order.Status == domain.OrderStatusCancelled {
			return ErrOrderAlreadyCancelled
		}
		switch order.PaymentStatus {
		case domain.OrderPaymentStatusUnpaid, domain.OrderPaymentStatusFailed, domain.OrderPaymentStatusExpired:
		default:
			return ErrOrderHasPayments
		}
		var moneyAttached bool
		if err := tx.GetContext(ctx, &moneyAttached, `
			SELECT EXISTS (
				SELECT 1 FROM payments
				WHERE store_id = $1 AND order_id = $2
				  AND status IN ('pending', 'authorized', 'paid', 'partially_refunded', 'refunded')
			)
		`, storeID, order.ID); err != nil {
			return fmt.Errorf("failed to check order payments: %w", err)
		}
		if moneyAttached {
			return ErrOrderHasPayments
		}

		ownerOverrideID, err := s.authorizeAmount(ctx, tx, actor, order.TotalAmount, in.PIN, in.OwnerPIN)
		if err != nil {
			return err
		}

		now := s.nowFunc().UTC()
		previousStock := order.StockStatus
		wasted, err := releaseUnstartedStockTx(ctx, tx, s.orderRepo, order)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE kds_tickets SET status = 'cancelled', cancelled_at = $3, updated_at = $3
			WHERE store_id = $1 AND order_id = $2 AND status IN ('queued', 'preparing', 'ready')
		`, storeID, order.ID, now); err != nil {
			return fmt.Errorf("failed to cancel kds tickets: %w", err)
		}
		order.Status = domain.OrderStatusCancelled
		order.ExpiresAt = nil
		if err := s.orderRepo.UpdateOrderStatusAndStock(ctx, tx, order); err != nil {
			return fmt.Errorf("failed to cancel order: %w", err)
		}
		if err := s.orderRepo.UpdatePaymentSplitsStatusByOrder(ctx, tx, storeID, order.ID, domain.SplitBillStatusCancelled, false); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE order_rounds SET status = 'cancelled', updated_at = $3
			WHERE store_id = $1 AND order_id = $2 AND status <> 'cancelled'
		`, storeID, order.ID, now); err != nil {
			return fmt.Errorf("failed to cancel order rounds: %w", err)
		}

		payload, err := json.Marshal(map[string]any{
			"order_id":       order.ID.String(),
			"store_id":       storeID.String(),
			"order_number":   order.OrderNumber,
			"status":         order.Status,
			"payment_status": order.PaymentStatus,
			"stock_status":   order.StockStatus,
			"reason":         reason,
			"voided":         true,
		})
		if err != nil {
			return err
		}
		if err := s.orderRepo.InsertOutboxEvent(ctx, tx, &domain.OutboxEvent{
			ID:            uuid.New(),
			StoreID:       storeID,
			EventType:     "order.cancelled",
			AggregateType: "order",
			AggregateID:   order.ID.String(),
			Payload:       string(payload),
		}); err != nil {
			return err
		}

		actorID := actor.Staff.UserID
		auditPayload, _ := json.Marshal(map[string]any{
			"order_id":          order.ID,
			"total_amount":      order.TotalAmount,
			"owner_override_by": ownerOverrideID,
			"stock_before":      previousStock,
			"stock_after":       order.StockStatus,
			"wasted_lines":      wasted,
		})
		if err := postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{
			StoreID:     storeID,
			ActorID:     &actorID,
			ActorName:   actor.Staff.FullName,
			Action:      "VOID_ORDER",
			Reason:      reason,
			ReferenceID: order.OrderNumber,
			Metadata:    string(auditPayload),
			CreatedAt:   now,
		}); err != nil {
			return fmt.Errorf("failed to record void audit log: %w", err)
		}

		result = &VoidOrderResult{
			Order:   order,
			Message: "Pesanan berhasil dibatalkan (void) dan stok telah dikembalikan.",
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

type RefundPaymentInput struct {
	Reason   string  `json:"reason"`
	PIN      string  `json:"pin"`
	OwnerPIN *string `json:"owner_pin"`
	// RestoreStock must be sent explicitly: true returns the goods to stock, false records them as waste.
	RestoreStock *bool `json:"restore_stock"`
}

type RefundPaymentResult struct {
	Payment       *domain.Payment `json:"payment"`
	Order         *domain.Order   `json:"order"`
	OpenDrawer    bool            `json:"open_drawer"`
	StockRestored bool            `json:"stock_restored"`
	Message       string          `json:"message"`
}

// RefundPayment fully refunds one settled cash payment (a whole order or one split) from the actor's
// open drawer. Stock comes back only when asked: the split's own lines for a by-item split, otherwise
// the whole order once, when the refund makes the order fully refunded.
func (s *OperationalService) RefundPayment(
	ctx context.Context,
	actor *AuthenticatedStaff,
	orderID uuid.UUID,
	paymentID uuid.UUID,
	in RefundPaymentInput,
) (*RefundPaymentResult, error) {
	if actor == nil {
		return nil, ErrInvalidSession
	}
	if !actor.Can(rbac.PaymentRefund) {
		return nil, ErrForbidden
	}
	reason := strings.TrimSpace(in.Reason)
	fields := map[string]string{}
	if reason == "" {
		fields["reason"] = "alasan pengembalian dana (refund) wajib diisi"
	}
	if in.RestoreStock == nil {
		fields["restore_stock"] = "wajib diisi: true untuk mengembalikan stok, false bila barang dibuang (waste)"
	}
	if len(fields) > 0 {
		return nil, &ValidationError{Fields: fields}
	}

	storeID := actor.Staff.Store.ID
	var result *RefundPaymentResult
	err := s.pins.withPINTx(ctx, storeID, func(tx *sqlx.Tx) error {
		order, _, err := s.orderRepo.GetOrderByIDForUpdate(ctx, tx, storeID, orderID)
		if err != nil {
			return err
		}
		payment, err := s.paymentRepo.GetPaymentByIDForUpdate(ctx, tx, storeID, paymentID)
		if err != nil {
			return err
		}
		if payment.OrderID != order.ID {
			return postgres.ErrPaymentNotFound
		}
		if payment.Status == domain.PaymentStatusRefunded {
			return ErrPaymentAlreadyRefunded
		}
		// Refunds start once the whole bill is settled; a half-paid tab is still being collected.
		if payment.Status != domain.PaymentStatusPaid ||
			(order.PaymentStatus != domain.OrderPaymentStatusPaid && order.PaymentStatus != domain.OrderPaymentStatusPartiallyRefunded) {
			return ErrOrderNotRefundable
		}
		if payment.Method != domain.PaymentMethodCash {
			return ErrRefundMethodNotSupported
		}

		ownerOverrideID, err := s.authorizeAmount(ctx, tx, actor, payment.Amount, in.PIN, in.OwnerPIN)
		if err != nil {
			return err
		}
		now := s.nowFunc().UTC()

		activeShift, err := s.shiftRepo.GetActiveShiftForStaff(ctx, tx, storeID, actor.Staff.UserID, nil)
		if err != nil || activeShift == nil {
			return ErrNoOpenShift
		}
		summary, err := s.shiftRepo.AggregateCashTotalsByShift(ctx, tx, storeID, activeShift.ID)
		if err != nil {
			return err
		}
		if summary.ExpectedCash(activeShift.OpeningFloat) < payment.Amount {
			return ErrInsufficientCashInDrawer
		}
		if err := s.shiftRepo.InsertCashMovement(ctx, tx, &domain.CashMovement{
			ID:           uuid.New(),
			StoreID:      storeID,
			ShiftID:      activeShift.ID,
			MovementType: domain.CashMovementRefund,
			Amount:       payment.Amount,
			ActorID:      actor.Staff.UserID,
			Reason:       &reason,
			OrderID:      &order.ID,
			PaymentID:    &payment.ID,
			CreatedAt:    now,
		}); err != nil {
			return fmt.Errorf("failed to record cash movement for refund: %w", err)
		}

		if _, err := tx.ExecContext(ctx, `
			UPDATE payments SET status = $3, refunded_amount = amount, updated_at = $4
			WHERE store_id = $1 AND id = $2
		`, storeID, payment.ID, domain.PaymentStatusRefunded, now); err != nil {
			return fmt.Errorf("failed to update payment to refunded: %w", err)
		}
		payment.Status = domain.PaymentStatusRefunded
		payment.RefundedAmount = payment.Amount
		payment.UpdatedAt = now

		splitMode := domain.SplitModeFull
		if payment.SplitID != nil {
			// paid_amount stays gross (like orders.paid_amount); settlement recomputes the order's paid
			// amount from it, and the refunded value lives on the payment and orders.refunded_amount.
			if err := tx.GetContext(ctx, &splitMode, `
				UPDATE payment_splits SET status = 'refunded', updated_at = $3
				WHERE store_id = $1 AND id = $2
				RETURNING split_mode
			`, storeID, *payment.SplitID, now); err != nil {
				return fmt.Errorf("failed to mark split refunded: %w", err)
			}
		}

		order.RefundedAmount += payment.Amount
		fullyRefunded := order.RefundedAmount >= order.PaidAmount
		order.PaymentStatus = domain.OrderPaymentStatusPartiallyRefunded
		if fullyRefunded {
			order.PaymentStatus = domain.OrderPaymentStatusRefunded
		}

		stockRestored := false
		if *in.RestoreStock {
			switch {
			case splitMode == domain.SplitModeByItem:
				var lines []struct {
					OrderItemID uuid.UUID `db:"order_item_id"`
					Quantity    int       `db:"quantity"`
				}
				if err := tx.SelectContext(ctx, &lines, `
					SELECT order_item_id, quantity FROM payment_split_items
					WHERE store_id = $1 AND split_id = $2
				`, storeID, *payment.SplitID); err != nil {
					return fmt.Errorf("failed to load split items for restock: %w", err)
				}
				lineQty := make(map[uuid.UUID]int, len(lines))
				for _, l := range lines {
					lineQty[l.OrderItemID] += l.Quantity
				}
				products, options := computeItemStockQuantities(order, lineQty)
				if err := restoreStockTx(ctx, tx, s.orderRepo, storeID, products, options); err != nil {
					return err
				}
				stockRestored = len(lines) > 0
			case fullyRefunded:
				before := order.StockStatus
				if err := releaseOrderStockTx(ctx, tx, s.orderRepo, order); err != nil {
					return err
				}
				stockRestored = order.StockStatus != before
			}
		}

		if err := tx.QueryRowxContext(ctx, `
			UPDATE orders SET payment_status = $3, refunded_amount = $4, stock_status = $5, updated_at = $6
			WHERE store_id = $1 AND id = $2
			RETURNING updated_at
		`, storeID, order.ID, order.PaymentStatus, order.RefundedAmount, order.StockStatus, now).Scan(&order.UpdatedAt); err != nil {
			return fmt.Errorf("failed to update refunded order: %w", err)
		}

		actorID := actor.Staff.UserID
		auditPayload, _ := json.Marshal(map[string]any{
			"payment_id":        payment.ID,
			"order_id":          order.ID,
			"split_id":          payment.SplitID,
			"split_mode":        splitMode,
			"amount":            payment.Amount,
			"method":            payment.Method,
			"restore_stock":     *in.RestoreStock,
			"stock_restored":    stockRestored,
			"owner_override_by": ownerOverrideID,
		})
		if err := postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{
			StoreID:     storeID,
			ActorID:     &actorID,
			ActorName:   actor.Staff.FullName,
			Action:      "REFUND",
			Reason:      reason,
			ReferenceID: payment.ID.String(),
			Metadata:    string(auditPayload),
			CreatedAt:   now,
		}); err != nil {
			return fmt.Errorf("failed to record refund audit log: %w", err)
		}

		result = &RefundPaymentResult{
			Payment:       payment,
			Order:         order,
			OpenDrawer:    true,
			StockRestored: stockRestored,
			Message:       "Pengembalian dana (refund) tunai berhasil diproses.",
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

type StockOverrideInput struct {
	NewQuantity int    `json:"new_quantity"`
	Reason      string `json:"reason"`
	OwnerPIN    string `json:"owner_pin"`
	// EnableTracking must be true to start tracking a product that is not tracked yet.
	EnableTracking bool `json:"enable_tracking"`
}

type StockOverrideResult struct {
	ProductID        uuid.UUID `json:"product_id"`
	ProductName      string    `json:"product_name"`
	PreviousQuantity int       `json:"previous_quantity"`
	NewQuantity      int       `json:"new_quantity"`
	Delta            int       `json:"delta"`
	Message          string    `json:"message"`
}

// OverrideStock manually adjusts product physical inventory, requiring Owner PIN authorization
// and recording an auditable entry in audit_logs (Decision O4).
func (s *OperationalService) OverrideStock(
	ctx context.Context,
	actor *AuthenticatedStaff,
	productID uuid.UUID,
	in StockOverrideInput,
) (*StockOverrideResult, error) {
	if actor == nil {
		return nil, ErrInvalidSession
	}
	if !actor.Can(rbac.StockOverride) {
		return nil, ErrForbidden
	}
	if in.NewQuantity < 0 {
		return nil, &ValidationError{Fields: map[string]string{"new_quantity": "jumlah stok tidak boleh negatif"}}
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return nil, &ValidationError{Fields: map[string]string{"reason": "alasan penyesuaian stok (opname/override) wajib diisi"}}
	}
	ownerPIN := strings.TrimSpace(in.OwnerPIN)
	if ownerPIN == "" {
		return nil, &ValidationError{Fields: map[string]string{"owner_pin": "PIN Owner wajib diisi untuk penyesuaian stok"}}
	}

	var result *StockOverrideResult
	err := s.pins.withPINTx(ctx, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID

		// Verify Owner PIN unconditionally (Decision O4)
		verifiedOwnerID, err := s.pins.verifyOwner(ctx, tx, actor, ownerPIN)
		if err != nil {
			return err
		}

		var product struct {
			ID            uuid.UUID `db:"id"`
			Name          string    `db:"name"`
			StockQuantity int       `db:"stock_quantity"`
			TrackStock    bool      `db:"track_stock"`
		}
		query := `
			SELECT id, name, stock_quantity, track_stock
			FROM products
			WHERE store_id = $1 AND id = $2
			FOR UPDATE
		`
		if err := tx.GetContext(ctx, &product, query, storeID, productID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrProductNotFound
			}
			return fmt.Errorf("failed to load product for stock override: %w", err)
		}
		if !product.TrackStock && !in.EnableTracking {
			return ErrProductNotTracked
		}

		now := s.nowFunc().UTC()
		prevQty := product.StockQuantity
		delta := in.NewQuantity - prevQty

		if _, err := tx.ExecContext(ctx, `
			UPDATE products
			SET stock_quantity = $1, track_stock = true, updated_at = $2
			WHERE store_id = $3 AND id = $4
		`, in.NewQuantity, now, storeID, productID); err != nil {
			return fmt.Errorf("failed to update product stock: %w", err)
		}

		// Audit Log
		actorID := actor.Staff.UserID
		auditPayload, _ := json.Marshal(map[string]any{
			"product_id":        product.ID,
			"product_name":      product.Name,
			"previous_quantity": prevQty,
			"new_quantity":      in.NewQuantity,
			"delta":             delta,
			"owner_verified_by": verifiedOwnerID,
			"tracking_enabled":  !product.TrackStock,
		})
		if err := postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{
			StoreID:     storeID,
			ActorID:     &actorID,
			ActorName:   actor.Staff.FullName,
			Action:      "STOCK_OVERRIDE",
			Reason:      reason,
			ReferenceID: product.ID.String(),
			Metadata:    string(auditPayload),
			CreatedAt:   now,
		}); err != nil {
			return fmt.Errorf("failed to record stock override audit log: %w", err)
		}

		result = &StockOverrideResult{
			ProductID:        product.ID,
			ProductName:      product.Name,
			PreviousQuantity: prevQty,
			NewQuantity:      in.NewQuantity,
			Delta:            delta,
			Message:          "Penyesuaian stok berhasil disimpan.",
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return result, nil
}
