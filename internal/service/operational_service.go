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
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/auth"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

var (
	ErrOrderAlreadyCancelled    = errors.New("order is already cancelled")
	ErrOrderNotRefundable       = errors.New("order or payment is not in refundable state")
	ErrPaymentAlreadyRefunded   = errors.New("payment has already been refunded")
	ErrOwnerPINRequired         = errors.New("action amount exceeds store threshold; owner PIN is required")
	ErrInvalidStaffPIN          = errors.New("invalid staff PIN")
	ErrInsufficientCashInDrawer = errors.New("insufficient cash in active drawer for cash refund")
	ErrNegativeStockQuantity    = errors.New("stock quantity cannot be negative")
	ErrEmptyReason              = errors.New("reason is required for operational actions")
)

type OperationalService struct {
	db          *sqlx.DB
	orderRepo   *postgres.OrderRepository
	paymentRepo *postgres.PaymentRepository
	shiftRepo   *postgres.ShiftRepository
	authRepo    *postgres.AuthRepository
	settings    *postgres.SettingsRepository
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
	return &OperationalService{
		db:          db,
		orderRepo:   orderRepo,
		paymentRepo: paymentRepo,
		shiftRepo:   shiftRepo,
		authRepo:    authRepo,
		settings:    settingsRepo,
		nowFunc:     time.Now,
	}
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

// VoidOrder cancels an unpaid order, releases reserved stock, audits the action,
// and enforces cashier PIN vs owner PIN based on void_refund_owner_threshold (Decision O1).
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

	var result *VoidOrderResult
	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID

		order, _, err := s.orderRepo.GetOrderByIDForUpdate(ctx, tx, storeID, orderID)
		if err != nil {
			return err
		}

		if order.Status == domain.OrderStatusCancelled {
			return ErrOrderAlreadyCancelled
		}
		if order.PaymentStatus == domain.OrderPaymentStatusPaid {
			return ErrOrderAlreadyPaid
		}

		// Retrieve store threshold
		settings, err := s.settings.GetStoreSettings(ctx, tx, storeID)
		if err != nil {
			return err
		}
		threshold := settings.VoidRefundOwnerThreshold
		if threshold <= 0 {
			threshold = 100000 // default Rp 100.000
		}

		var ownerOverrideID *uuid.UUID
		if order.TotalAmount > threshold {
			// Owner PIN required
			ownerPIN := ""
			if in.OwnerPIN != nil {
				ownerPIN = strings.TrimSpace(*in.OwnerPIN)
			}
			if ownerPIN == "" && actor.Staff.Role == domain.RoleOwner {
				ownerPIN = strings.TrimSpace(in.PIN)
			}
			if ownerPIN == "" {
				return ErrOwnerPINRequired
			}
			verifiedOwnerID, err := s.verifyOwnerPIN(ctx, tx, storeID, ownerPIN)
			if err != nil {
				return err
			}
			ownerOverrideID = &verifiedOwnerID
		} else {
			// Cashier PIN required
			if strings.TrimSpace(in.PIN) == "" {
				return &ValidationError{Fields: map[string]string{"pin": "PIN staf wajib diisi"}}
			}
			if err := s.verifyStaffPIN(ctx, tx, storeID, actor.Staff.UserID, strings.TrimSpace(in.PIN)); err != nil {
				return err
			}
		}

		now := s.nowFunc().UTC()

		// 1. Update order status to cancelled
		if _, err := tx.ExecContext(ctx, `
			UPDATE orders
			SET status = $1, updated_at = $2
			WHERE store_id = $3 AND id = $4
		`, domain.OrderStatusCancelled, now, storeID, order.ID); err != nil {
			return fmt.Errorf("failed to cancel order: %w", err)
		}
		order.Status = domain.OrderStatusCancelled
		order.UpdatedAt = now

		// 2. If tab, cancel any active splits
		_, _ = tx.ExecContext(ctx, `
			UPDATE payment_splits
			SET status = 'cancelled', updated_at = $1
			WHERE store_id = $2 AND order_id = $3 AND status != 'cancelled'
		`, now, storeID, order.ID)

		// 3. 100% Stock Restoration for tracked products
		var items []struct {
			ProductID uuid.UUID `db:"product_id"`
			Quantity  int       `db:"quantity"`
		}
		if err := tx.SelectContext(ctx, &items, `
			SELECT product_id, quantity
			FROM order_items
			WHERE store_id = $1 AND order_id = $2
		`, storeID, order.ID); err != nil {
			return fmt.Errorf("failed to fetch order items for restock: %w", err)
		}
		for _, it := range items {
			if err := s.orderRepo.RestoreProductStock(ctx, tx, storeID, it.ProductID, it.Quantity); err != nil {
				return fmt.Errorf("failed to restore stock for product %s: %w", it.ProductID, err)
			}
		}

		// 4. Audit Log
		actorID := actor.Staff.UserID
		auditPayload, _ := json.Marshal(map[string]any{
			"order_id":          order.ID,
			"total_amount":      order.TotalAmount,
			"owner_override_by": ownerOverrideID,
			"items_restocked":   len(items),
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
	Reason       string  `json:"reason"`
	PIN          string  `json:"pin"`
	OwnerPIN     *string `json:"owner_pin"`
	RestoreStock bool    `json:"restore_stock"` // default true
}

type RefundPaymentResult struct {
	Payment    *domain.Payment `json:"payment"`
	Order      *domain.Order   `json:"order"`
	OpenDrawer bool            `json:"open_drawer"`
	Message    string          `json:"message"`
}

// RefundPayment processes a full refund for a settled payment or split bill,
// validates active drawer cash balance for cash refunds, updates payment/order statuses,
// optionally restores inventory, and records audit and cash movement entries.
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
	if reason == "" {
		return nil, &ValidationError{Fields: map[string]string{"reason": "alasan pengembalian dana (refund) wajib diisi"}}
	}

	var result *RefundPaymentResult
	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID

		order, _, err := s.orderRepo.GetOrderByIDForUpdate(ctx, tx, storeID, orderID)
		if err != nil {
			return err
		}

		payment, err := s.paymentRepo.GetPaymentByIDForUpdate(ctx, tx, storeID, paymentID)
		if err != nil {
			return err
		}
		if payment.OrderID != order.ID {
			return errors.New("payment does not belong to specified order")
		}
		if payment.Status == domain.PaymentStatusRefunded {
			return ErrPaymentAlreadyRefunded
		}
		if payment.Status != domain.PaymentStatusPaid {
			return ErrOrderNotRefundable
		}

		// Retrieve store threshold
		settings, err := s.settings.GetStoreSettings(ctx, tx, storeID)
		if err != nil {
			return err
		}
		threshold := settings.VoidRefundOwnerThreshold
		if threshold <= 0 {
			threshold = 100000 // default Rp 100.000
		}

		var ownerOverrideID *uuid.UUID
		if payment.Amount > threshold {
			ownerPIN := ""
			if in.OwnerPIN != nil {
				ownerPIN = strings.TrimSpace(*in.OwnerPIN)
			}
			if ownerPIN == "" && actor.Staff.Role == domain.RoleOwner {
				ownerPIN = strings.TrimSpace(in.PIN)
			}
			if ownerPIN == "" {
				return ErrOwnerPINRequired
			}
			verifiedOwnerID, err := s.verifyOwnerPIN(ctx, tx, storeID, ownerPIN)
			if err != nil {
				return err
			}
			ownerOverrideID = &verifiedOwnerID
		} else {
			if strings.TrimSpace(in.PIN) == "" {
				return &ValidationError{Fields: map[string]string{"pin": "PIN staf wajib diisi"}}
			}
			if err := s.verifyStaffPIN(ctx, tx, storeID, actor.Staff.UserID, strings.TrimSpace(in.PIN)); err != nil {
				return err
			}
		}

		now := s.nowFunc().UTC()
		var openDrawer bool

		// If cash payment, enforce active cashier shift and drawer balance check
		if payment.Method == domain.PaymentMethodCash {
			// Find active open shift for current cashier
			activeShift, err := s.shiftRepo.GetActiveShiftForStaff(ctx, tx, storeID, actor.Staff.UserID, nil)
			if err != nil || activeShift == nil {
				return ErrNoOpenShift
			}

			// Calculate current drawer cash balance
			summary, err := s.shiftRepo.AggregateCashTotalsByShift(ctx, tx, storeID, activeShift.ID)
			if err != nil {
				return err
			}
			expectedCash := activeShift.OpeningFloat + summary.CashSales + summary.Tips + summary.CashIns - summary.CashRefunds - summary.PaidOuts - summary.StaffMeals
			if expectedCash < payment.Amount {
				return ErrInsufficientCashInDrawer
			}

			// Record cash movement for refund
			mov := &domain.CashMovement{
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
			}
			if err := s.shiftRepo.InsertCashMovement(ctx, tx, mov); err != nil {
				return fmt.Errorf("failed to record cash movement for refund: %w", err)
			}
			openDrawer = true
		}

		// Update payment status to refunded
		if _, err := tx.ExecContext(ctx, `
			UPDATE payments
			SET status = $1, refunded_amount = $2, updated_at = $3
			WHERE store_id = $4 AND id = $5
		`, domain.PaymentStatusRefunded, payment.Amount, now, storeID, payment.ID); err != nil {
			return fmt.Errorf("failed to update payment to refunded: %w", err)
		}
		payment.Status = domain.PaymentStatusRefunded
		payment.RefundedAmount = payment.Amount
		payment.UpdatedAt = now

		// Update split status if bound to split
		if payment.SplitID != nil {
			_, _ = tx.ExecContext(ctx, `
				UPDATE payment_splits
				SET status = 'refunded', updated_at = $1
				WHERE store_id = $2 AND id = $3
			`, now, storeID, *payment.SplitID)
		}

		// Check whether all non-cancelled splits or payments are refunded
		var remainingPaidCount int
		if err := tx.GetContext(ctx, &remainingPaidCount, `
			SELECT COUNT(*)
			FROM payments
			WHERE store_id = $1 AND order_id = $2 AND status = 'paid'
		`, storeID, order.ID); err != nil {
			return err
		}

		if remainingPaidCount == 0 {
			// Entire order is now refunded
			_, err = tx.ExecContext(ctx, `
				UPDATE orders
				SET payment_status = $1, updated_at = $2
				WHERE store_id = $3 AND id = $4
			`, domain.OrderPaymentStatusRefunded, now, storeID, order.ID)
			if err != nil {
				return fmt.Errorf("failed to update order to refunded: %w", err)
			}
			order.PaymentStatus = domain.OrderPaymentStatusRefunded
		} else {
			// Partially refunded
			_, err = tx.ExecContext(ctx, `
				UPDATE orders
				SET payment_status = $1, updated_at = $2
				WHERE store_id = $3 AND id = $4
			`, domain.OrderPaymentStatusPartiallyRefunded, now, storeID, order.ID)
			if err != nil {
				return fmt.Errorf("failed to update order to partially refunded: %w", err)
			}
			order.PaymentStatus = domain.OrderPaymentStatusPartiallyRefunded
		}

		// Stock restoration (if requested)
		if in.RestoreStock {
			var items []struct {
				ProductID uuid.UUID `db:"product_id"`
				Quantity  int       `db:"quantity"`
			}
			if err := tx.SelectContext(ctx, &items, `
				SELECT product_id, quantity
				FROM order_items
				WHERE store_id = $1 AND order_id = $2
			`, storeID, order.ID); err != nil {
				return fmt.Errorf("failed to fetch order items for restock: %w", err)
			}
			for _, it := range items {
				if err := s.orderRepo.RestoreProductStock(ctx, tx, storeID, it.ProductID, it.Quantity); err != nil {
					return fmt.Errorf("failed to restore stock for product %s: %w", it.ProductID, err)
				}
			}
		}

		// Audit Log
		actorID := actor.Staff.UserID
		auditPayload, _ := json.Marshal(map[string]any{
			"payment_id":        payment.ID,
			"order_id":          order.ID,
			"amount":            payment.Amount,
			"method":            payment.Method,
			"restore_stock":     in.RestoreStock,
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
			Payment:    payment,
			Order:      order,
			OpenDrawer: openDrawer,
			Message:    "Pengembalian dana (refund) berhasil diproses.",
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
	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID

		// Verify Owner PIN unconditionally (Decision O4)
		verifiedOwnerID, err := s.verifyOwnerPIN(ctx, tx, storeID, ownerPIN)
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
			return errors.New("product not found")
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

// verifyStaffPIN verifies the given PIN against the staff member's hashed PIN.
func (s *OperationalService) verifyStaffPIN(ctx context.Context, tx *sqlx.Tx, storeID, staffUserID uuid.UUID, pin string) error {
	var user struct {
		PinHash     *string    `db:"pin_hash"`
		LockedUntil *time.Time `db:"locked_until"`
		IsActive    bool       `db:"is_active"`
	}
	query := `SELECT pin_hash, locked_until, is_active FROM users WHERE store_id = $1 AND id = $2`
	if err := tx.GetContext(ctx, &user, query, storeID, staffUserID); err != nil {
		return err
	}
	if !user.IsActive {
		return errors.New("akun staf tidak aktif")
	}
	now := s.nowFunc().UTC()
	if user.LockedUntil != nil && user.LockedUntil.After(now) {
		return errors.New("akun staf sedang terkunci karena salah PIN")
	}
	if user.PinHash == nil || *user.PinHash == "" {
		return errors.New("PIN staf belum diatur")
	}
	ok, err := auth.VerifyPIN(pin, *user.PinHash)
	if err != nil || !ok {
		return ErrInvalidStaffPIN
	}
	return nil
}

// verifyOwnerPIN searches for an active owner of the store whose PIN matches.
func (s *OperationalService) verifyOwnerPIN(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, pin string) (uuid.UUID, error) {
	owners, err := s.authRepo.ListActiveOwners(ctx, tx, storeID)
	if err != nil {
		return uuid.Nil, err
	}
	now := s.nowFunc().UTC()
	for _, owner := range owners {
		if owner.LockedUntil != nil && owner.LockedUntil.After(now) {
			continue
		}
		ok, err := auth.VerifyPIN(pin, owner.PinHash)
		if err == nil && ok {
			return owner.ID, nil
		}
	}
	return uuid.Nil, ErrInvalidOwnerPIN
}
