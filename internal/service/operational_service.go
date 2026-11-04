package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
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
	ErrRefundWindowExceeded     = errors.New("payment is older than the refund window")
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
// requireOwner forces the owner PIN regardless of the amount (a refund past the cashier window, RF1-e).
// An approved remote request (ADR-007) stands in for the owner PIN when none is given.
func (s *OperationalService) authorizeAmount(ctx context.Context, tx *sqlx.Tx, actor *AuthenticatedStaff, amount int64, pin string, ownerPIN *string, requireOwner bool, approval *approvalClaim) (*uuid.UUID, error) {
	settings, err := s.settings.GetStoreSettings(ctx, tx, actor.Staff.Store.ID)
	if err != nil {
		return nil, err
	}
	threshold := settings.VoidRefundOwnerThreshold
	if threshold <= 0 {
		threshold = 100000 // default Rp 100.000
	}
	pin = strings.TrimSpace(pin)
	if amount > threshold || requireOwner {
		owner := ""
		if ownerPIN != nil {
			owner = strings.TrimSpace(*ownerPIN)
		}
		if owner == "" && actor.Staff.Role == domain.RoleOwner {
			owner = pin
		}
		if owner == "" && approval.ID != nil {
			ownerID, err := consumeApproval(ctx, tx, actor, approval, amount, s.nowFunc().UTC())
			if err != nil {
				return nil, err
			}
			return &ownerID, nil
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
	Reason     string     `json:"reason"`
	PIN        string     `json:"pin"`
	OwnerPIN   *string    `json:"owner_pin"`
	ApprovalID *uuid.UUID `json:"approval_id"`
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

		claim := &approvalClaim{ID: in.ApprovalID, Action: ApprovalVoid, Ref: order.ID}
		ownerOverrideID, err := s.authorizeAmount(ctx, tx, actor, order.TotalAmount, in.PIN, in.OwnerPIN, false, claim)
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
			"approval_id":       claim.usedID(),
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
	Reason     string     `json:"reason"`
	PIN        string     `json:"pin"`
	OwnerPIN   *string    `json:"owner_pin"`
	ApprovalID *uuid.UUID `json:"approval_id"`
	// RestoreStock must be sent explicitly: true returns the goods to stock, false records them as waste.
	RestoreStock *bool `json:"restore_stock"`
	// RefundMethod (RF1-c): "cash" from the actor's open drawer (default) or "bank_transfer" made by the
	// owner outside the system, recorded with bank, last 4 account digits and the transfer reference.
	RefundMethod      string `json:"refund_method"`
	BankName          string `json:"bank_name"`
	AccountLast4      string `json:"account_last4"`
	TransferReference string `json:"transfer_reference"`
}

const (
	RefundMethodCash         = "cash"
	RefundMethodBankTransfer = "bank_transfer"
)

var accountLast4Pattern = regexp.MustCompile(`^[0-9]{4}$`)

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
	bankName, last4, transferRef := strings.TrimSpace(in.BankName), strings.TrimSpace(in.AccountLast4), strings.TrimSpace(in.TransferReference)
	switch in.RefundMethod {
	case "", RefundMethodCash:
		in.RefundMethod = RefundMethodCash
	case RefundMethodBankTransfer:
		if bankName == "" || len(bankName) > 100 {
			fields["bank_name"] = "nama bank wajib diisi (maks. 100 karakter)"
		}
		if !accountLast4Pattern.MatchString(last4) {
			fields["account_last4"] = "4 digit terakhir nomor rekening tujuan (jangan isi nomor lengkap)"
		}
		if transferRef == "" || len(transferRef) > 100 {
			fields["transfer_reference"] = "nomor referensi transfer wajib diisi (maks. 100 karakter)"
		}
	default:
		fields["refund_method"] = "harus cash atau bank_transfer"
	}
	if len(fields) > 0 {
		return nil, &ValidationError{Fields: fields}
	}
	// A manual bank transfer is made by the owner personally (RF1-c), not approved by owner PIN.
	if in.RefundMethod == RefundMethodBankTransfer && actor.Staff.Role != domain.RoleOwner {
		return nil, ErrForbidden
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
		now := s.nowFunc().UTC()
		ownerWindow, err := s.checkRefundWindow(ctx, tx, actor, payment, now)
		if err != nil {
			return err
		}
		claim := &approvalClaim{ID: in.ApprovalID, Action: ApprovalRefund, Ref: payment.ID}
		ownerOverrideID, err := s.authorizeAmount(ctx, tx, actor, payment.Amount, in.PIN, in.OwnerPIN, ownerWindow, claim)
		if err != nil {
			return err
		}

		// RF1-c: any original method is refunded manually; the payment keeps its method and reference.
		ledger := &paymentRefund{storeID: storeID, paymentID: payment.ID, orderID: order.ID, amount: payment.Amount,
			method: in.RefundMethod, reason: reason, actorID: actor.Staff.UserID, approvedBy: ownerOverrideID, at: now}
		if in.RefundMethod == RefundMethodBankTransfer {
			ledger.bankName, ledger.accountLast4, ledger.transferReference = &bankName, &last4, &transferRef
		} else {
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
			movementID := uuid.New()
			if err := s.shiftRepo.InsertCashMovement(ctx, tx, &domain.CashMovement{
				ID:           movementID,
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
			ledger.cashMovementID = &movementID
		}
		if err := ledger.insert(ctx, tx); err != nil {
			return err
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

		// restore_stock=false records the refunded lines as waste (order_item_id -> quantity) in the audit.
		stockRestored := false
		var wastedLines map[uuid.UUID]int
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
			if *in.RestoreStock {
				products, options := computeItemStockQuantities(order, lineQty)
				if err := restoreStockTx(ctx, tx, s.orderRepo, storeID, products, options); err != nil {
					return err
				}
				stockRestored = len(lines) > 0
			} else {
				wastedLines = lineQty
			}
			// Every split has now been refunded and its lines restocked or wasted (review follow-up F3).
			if fullyRefunded && (order.StockStatus == domain.OrderStockStatusReserved || order.StockStatus == domain.OrderStockStatusCommitted) {
				order.StockStatus = domain.OrderStockStatusReleased
			}
		case fullyRefunded && *in.RestoreStock:
			before := order.StockStatus
			if err := releaseOrderStockTx(ctx, tx, s.orderRepo, order); err != nil {
				return err
			}
			stockRestored = order.StockStatus != before
		case fullyRefunded:
			wastedLines = make(map[uuid.UUID]int, len(order.Items))
			for _, item := range order.Items {
				wastedLines[item.ID] += item.Quantity
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
			"refund_method":     in.RefundMethod,
			"restore_stock":     *in.RestoreStock,
			"stock_restored":    stockRestored,
			"wasted_lines":      wastedLines,
			"stock_after":       order.StockStatus,
			"owner_override_by": ownerOverrideID,
			"approval_id":       claim.usedID(),
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
			OpenDrawer:    in.RefundMethod == RefundMethodCash,
			StockRestored: stockRestored,
			Message:       "Pengembalian dana (refund) tunai berhasil diproses.",
		}
		if in.RefundMethod == RefundMethodBankTransfer {
			result.Message = "Pengembalian dana (refund) lewat transfer bank tercatat."
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// checkRefundWindow applies RF1-e: a payment paid more business days ago (store time zone) than the
// owner window cannot be refunded; past the cashier window it needs the owner (own login or owner PIN).
// It reports whether the owner is required.
func (s *OperationalService) checkRefundWindow(ctx context.Context, tx *sqlx.Tx, actor *AuthenticatedStaff, payment *domain.Payment, now time.Time) (bool, error) {
	if payment.PaidAt == nil {
		return false, nil // settled without a timestamp (legacy rows): no window to apply
	}
	settings, err := s.settings.GetStoreSettings(ctx, tx, actor.Staff.Store.ID)
	if err != nil {
		return false, err
	}
	store, err := s.orderRepo.GetStoreForOrder(ctx, tx, actor.Staff.Store.ID)
	if err != nil {
		return false, err
	}
	loc, err := time.LoadLocation(store.Timezone)
	if err != nil {
		loc = time.FixedZone("WIB", 7*3600)
	}
	day := func(t time.Time) time.Time {
		y, m, d := t.In(loc).Date()
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	}
	days := int(day(now).Sub(day(*payment.PaidAt)).Hours() / 24)
	if days > settings.RefundWindowOwnerDays {
		return false, ErrRefundWindowExceeded
	}
	return days > settings.RefundWindowCashierDays && actor.Staff.Role != domain.RoleOwner, nil
}

// paymentRefund is one row of the append-only refund ledger (payment_refunds).
type paymentRefund struct {
	storeID, paymentID, orderID, actorID      uuid.UUID
	amount                                    int64
	method, reason                            string
	bankName, accountLast4, transferReference *string
	cashMovementID, approvedBy                *uuid.UUID
	at                                        time.Time
}

func (r *paymentRefund) insert(ctx context.Context, tx *sqlx.Tx) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO payment_refunds (store_id, payment_id, order_id, amount, method, bank_name, account_last4,
			transfer_reference, cash_movement_id, reason, actor_id, approved_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		r.storeID, r.paymentID, r.orderID, r.amount, r.method, r.bankName, r.accountLast4,
		r.transferReference, r.cashMovementID, r.reason, r.actorID, r.approvedBy, r.at); err != nil {
		if _, dup := postgres.UniqueViolation(err); dup {
			return ErrPaymentAlreadyRefunded
		}
		return fmt.Errorf("failed to record refund ledger: %w", err)
	}
	return nil
}

type StockOverrideInput struct {
	NewQuantity int        `json:"new_quantity"`
	Reason      string     `json:"reason"`
	OwnerPIN    string     `json:"owner_pin"`
	ApprovalID  *uuid.UUID `json:"approval_id"`
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
	if ownerPIN == "" && in.ApprovalID == nil {
		return nil, &ValidationError{Fields: map[string]string{"owner_pin": "PIN Owner atau approval_id wajib diisi untuk penyesuaian stok"}}
	}

	var result *StockOverrideResult
	err := s.pins.withPINTx(ctx, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID

		// Owner PIN or a remote approval, unconditionally (Decision O4, ADR-007)
		qty := in.NewQuantity
		claim := &approvalClaim{ID: in.ApprovalID, Action: ApprovalStockOverride, Ref: productID, Params: approvalParams{NewQuantity: &qty}}
		var verifiedOwnerID uuid.UUID
		var err error
		if ownerPIN != "" {
			verifiedOwnerID, err = s.pins.verifyOwner(ctx, tx, actor, ownerPIN)
		} else {
			verifiedOwnerID, err = consumeApproval(ctx, tx, actor, claim, int64(in.NewQuantity), s.nowFunc().UTC())
		}
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
			"approval_id":       claim.usedID(),
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

// SetPINPolicy applies the configured PIN lockout policy and the email service for owner alerts.
func (s *OperationalService) SetPINPolicy(policy AuthPolicy, email *EmailService) {
	s.pins.setPolicy(policy, email)
}
