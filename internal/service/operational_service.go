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
	gateway     GatewayClient // nil: refund_method gateway is unsupported
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
				  AND status IN ('pending', 'authorized', 'paid', 'partially_refunded') -- a refunded (handed-back) tender holds no money
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
	// RefundMethod (RF1-c, V13): "cash" from the actor's open drawer (default), "outlet_cash" from the
	// outlet fund, or "bank_transfer" made by the owner outside the system, recorded with bank, last 4
	// account digits and the transfer reference.
	RefundMethod      string `json:"refund_method"`
	BankName          string `json:"bank_name"`
	AccountLast4      string `json:"account_last4"`
	TransferReference string `json:"transfer_reference"`
	// Lines refunds only these order lines (partial refund, PR 8.5a); empty refunds what is left of the
	// payment. A partial refund needs IdempotencyKey so a double tap cannot refund twice.
	Lines          []RefundLineInput `json:"lines"`
	IdempotencyKey string            `json:"idempotency_key"`
}

type RefundLineInput struct {
	OrderItemID uuid.UUID `json:"order_item_id"`
	Quantity    int       `json:"quantity"`
}

const (
	RefundMethodCash         = "cash"
	RefundMethodBankTransfer = "bank_transfer"
	RefundMethodOutletCash   = "outlet_cash"
)

var (
	accountLast4Pattern      = regexp.MustCompile(`^[0-9]{4}$`)
	ErrRefundExceedsPayment  = errors.New("refund amount exceeds what is left of this payment")
	ErrRefundLinesNotAllowed = errors.New("line refunds need a fully paid bill and a payment that is not a by-item split")
)

type RefundPaymentResult struct {
	Payment       *domain.Payment `json:"payment"`
	Order         *domain.Order   `json:"order"`
	RefundAmount  int64           `json:"refund_amount"`
	OpenDrawer    bool            `json:"open_drawer"`
	StockRestored bool            `json:"stock_restored"`
	Replay        bool            `json:"is_idempotent_replay,omitempty"`
	// RefundStatus is "pending" or "requested" for a gateway refund waiting for bank confirmation (RF1-f).
	RefundStatus string `json:"refund_status,omitempty"`
	Message      string `json:"message"`
}

// RefundPayment refunds one settled payment, wholly or by order lines (tax, service and discount follow
// the lines proportionally). On a bill that is only partly paid it cancels that tender instead: the
// money goes back and the bill owes it again. Stock comes back only when asked.
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
	in.IdempotencyKey = strings.TrimSpace(in.IdempotencyKey)
	fields := map[string]string{}
	if reason == "" {
		fields["reason"] = "alasan pengembalian dana (refund) wajib diisi"
	}
	if in.RestoreStock == nil {
		fields["restore_stock"] = "wajib diisi: true untuk mengembalikan stok, false bila barang dibuang (waste)"
	}
	if len(in.IdempotencyKey) > 100 || (len(in.Lines) > 0 && in.IdempotencyKey == "") {
		fields["idempotency_key"] = "wajib diisi untuk refund per item (maks. 100 karakter)"
	}
	seen := map[uuid.UUID]bool{}
	for i, l := range in.Lines {
		if l.OrderItemID == uuid.Nil || l.Quantity <= 0 || seen[l.OrderItemID] {
			fields[fmt.Sprintf("lines[%d]", i)] = "order_item_id unik dan quantity > 0 wajib diisi"
		}
		seen[l.OrderItemID] = true
	}
	bankName, last4, transferRef := strings.TrimSpace(in.BankName), strings.TrimSpace(in.AccountLast4), strings.TrimSpace(in.TransferReference)
	switch in.RefundMethod {
	case "", RefundMethodCash:
		in.RefundMethod = RefundMethodCash
	case RefundMethodOutletCash, RefundMethodGateway:
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
		fields["refund_method"] = "harus cash, outlet_cash, bank_transfer atau gateway"
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
	var gatewayReq *gatewayRefundRequest
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
		if in.IdempotencyKey != "" {
			var prior int64
			err := tx.GetContext(ctx, &prior, `SELECT amount FROM payment_refunds WHERE store_id = $1 AND idempotency_key = $2 AND payment_id = $3`,
				storeID, in.IdempotencyKey, payment.ID)
			if err == nil {
				result = &RefundPaymentResult{Payment: payment, Order: order, RefundAmount: prior, Replay: true, Message: "Refund ini sudah diproses sebelumnya."}
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if payment.Status == domain.PaymentStatusRefunded {
			return ErrPaymentAlreadyRefunded
		}
		// A gateway refund waiting for the bank blocks every other refund of the payment (no double refund).
		var gatewayOpen bool
		if err := tx.GetContext(ctx, &gatewayOpen, `SELECT EXISTS (SELECT 1 FROM gateway_refunds
			WHERE store_id = $1 AND payment_id = $2 AND status IN ('pending', 'requested'))`, storeID, payment.ID); err != nil {
			return err
		}
		if gatewayOpen {
			return ErrGatewayRefundPending
		}
		cancelTender := order.PaymentStatus == domain.OrderPaymentStatusPartiallyPaid && order.BillingMode != domain.BillingModeOpenTab
		// Refunds start once the whole bill is settled; a half-paid pay-first bill can only hand a tender back.
		if (payment.Status != domain.PaymentStatusPaid && payment.Status != domain.PaymentStatusPartiallyRefunded) ||
			(!cancelTender && order.PaymentStatus != domain.OrderPaymentStatusPaid && order.PaymentStatus != domain.OrderPaymentStatusPartiallyRefunded) {
			return ErrOrderNotRefundable
		}

		splitMode := domain.SplitModeFull
		if payment.SplitID != nil {
			if err := tx.GetContext(ctx, &splitMode, `SELECT split_mode FROM payment_splits WHERE store_id = $1 AND id = $2`, storeID, *payment.SplitID); err != nil {
				return fmt.Errorf("failed to load split mode: %w", err)
			}
		}
		remaining := payment.Amount - payment.RefundedAmount
		amount := remaining
		lineQty := map[uuid.UUID]int{}
		allLinesRefunded := false
		if len(in.Lines) > 0 {
			if cancelTender || splitMode == domain.SplitModeByItem {
				return ErrRefundLinesNotAllowed
			}
			amount, allLinesRefunded, err = refundLinesAmountTx(ctx, tx, order, in.Lines, lineQty)
			if err != nil {
				return err
			}
			if amount > remaining {
				return fmt.Errorf("%w: lines=%d left=%d", ErrRefundExceedsPayment, amount, remaining)
			}
		}

		now := s.nowFunc().UTC()
		ownerWindow, err := s.checkRefundWindow(ctx, tx, actor, payment, now)
		if err != nil {
			return err
		}
		claim := &approvalClaim{ID: in.ApprovalID, Action: ApprovalRefund, Ref: payment.ID}
		ownerOverrideID, err := s.authorizeAmount(ctx, tx, actor, amount, in.PIN, in.OwnerPIN, ownerWindow, claim)
		if err != nil {
			return err
		}

		if in.RefundMethod == RefundMethodGateway {
			gatewayReq, err = s.openGatewayRefundTx(ctx, tx, order, payment, amount, lineQty, allLinesRefunded, *in.RestoreStock,
				cancelTender, reason, actor, ownerOverrideID, now)
			if err != nil {
				return err
			}
			result = &RefundPaymentResult{Payment: payment, Order: order, RefundAmount: amount, RefundStatus: "pending",
				Message: "Permintaan refund dikirim ke penyedia pembayaran; menunggu konfirmasi bank."}
			return nil
		}

		// RF1-c: any original method is refunded manually; the payment keeps its method and reference.
		ledger := &paymentRefund{storeID: storeID, paymentID: payment.ID, orderID: order.ID, amount: amount,
			method: in.RefundMethod, reason: reason, actorID: actor.Staff.UserID, approvedBy: ownerOverrideID, at: now}
		if in.IdempotencyKey != "" {
			ledger.idempotencyKey = &in.IdempotencyKey
		}
		switch in.RefundMethod {
		case RefundMethodBankTransfer:
			ledger.bankName, ledger.accountLast4, ledger.transferReference = &bankName, &last4, &transferRef
		case RefundMethodOutletCash:
			if err := outletRefundTx(ctx, tx, actor, payment.ID, amount, reason, ownerOverrideID, now); err != nil {
				return err
			}
		default:
			activeShift, err := s.shiftRepo.GetActiveShiftForStaff(ctx, tx, storeID, actor.Staff.UserID, nil)
			if err != nil || activeShift == nil {
				return ErrNoOpenShift
			}
			summary, err := s.shiftRepo.AggregateCashTotalsByShift(ctx, tx, storeID, activeShift.ID)
			if err != nil {
				return err
			}
			if summary.ExpectedCash(activeShift.OpeningFloat) < amount {
				return ErrInsufficientCashInDrawer
			}
			movementID := uuid.New()
			if err := s.shiftRepo.InsertCashMovement(ctx, tx, &domain.CashMovement{
				ID: movementID, StoreID: storeID, ShiftID: activeShift.ID, MovementType: domain.CashMovementRefund,
				Amount: amount, ActorID: actor.Staff.UserID, Reason: &reason, OrderID: &order.ID, PaymentID: &payment.ID, CreatedAt: now,
			}); err != nil {
				return fmt.Errorf("failed to record cash movement for refund: %w", err)
			}
			ledger.cashMovementID = &movementID
		}
		stockRestored, err := applyRefundTx(ctx, tx, s.orderRepo, &refundApplication{
			order: order, payment: payment, amount: amount, lineQty: lineQty, splitMode: splitMode,
			restoreStock: *in.RestoreStock, cancelTender: cancelTender, allLinesRefunded: allLinesRefunded,
			ledger: ledger, actorID: actor.Staff.UserID, actorName: actor.Staff.FullName, reason: reason,
			audit: map[string]any{"refund_method": in.RefundMethod, "owner_override_by": ownerOverrideID, "approval_id": claim.usedID()},
		}, now)
		if err != nil {
			return err
		}

		result = &RefundPaymentResult{
			Payment: payment, Order: order, RefundAmount: amount, OpenDrawer: in.RefundMethod == RefundMethodCash,
			StockRestored: stockRestored, Message: "Pengembalian dana (refund) tunai berhasil diproses.",
		}
		switch in.RefundMethod {
		case RefundMethodBankTransfer:
			result.Message = "Pengembalian dana (refund) lewat transfer bank tercatat."
		case RefundMethodOutletCash:
			result.Message = "Pengembalian dana (refund) dari kas outlet berhasil diproses."
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if gatewayReq != nil {
		// RF1-f: the money moves only when the bank confirms; until then the request is pending.
		status, err := s.dispatchGatewayRefund(ctx, storeID, gatewayReq)
		if err != nil {
			return nil, err
		}
		result.RefundStatus = status
	}
	return result, nil
}

// refundApplication is one refund whose money already left the store (drawer, outlet fund, owner's
// transfer) or was confirmed by the bank (gateway). applyRefundTx writes its ledger row and every
// effect on the payment, split, order and stock.
type refundApplication struct {
	order                                        *domain.Order
	payment                                      *domain.Payment
	amount                                       int64
	lineQty                                      map[uuid.UUID]int
	splitMode                                    domain.SplitMode
	restoreStock, cancelTender, allLinesRefunded bool
	ledger                                       *paymentRefund
	actorID                                      uuid.UUID
	actorName, reason                            string
	audit                                        map[string]any
}

func applyRefundTx(ctx context.Context, tx *sqlx.Tx, orderRepo *postgres.OrderRepository, a *refundApplication, now time.Time) (bool, error) {
	storeID, order, payment := a.ledger.storeID, a.order, a.payment
	if a.lineQty == nil {
		a.lineQty = map[uuid.UUID]int{}
	}
	refundID, err := a.ledger.insert(ctx, tx)
	if err != nil {
		return false, err
	}
	for itemID, qty := range a.lineQty {
		if _, err := tx.ExecContext(ctx, `INSERT INTO payment_refund_lines (store_id, refund_id, order_item_id, quantity) VALUES ($1, $2, $3, $4)`,
			storeID, refundID, itemID, qty); err != nil {
			return false, fmt.Errorf("failed to record refund lines: %w", err)
		}
	}

	payment.RefundedAmount += a.amount
	payment.Status = domain.PaymentStatusPartiallyRefunded
	if payment.RefundedAmount == payment.Amount {
		payment.Status = domain.PaymentStatusRefunded
	}
	if _, err := tx.ExecContext(ctx, `UPDATE payments SET status = $3, refunded_amount = $4, updated_at = $5 WHERE store_id = $1 AND id = $2`,
		storeID, payment.ID, payment.Status, payment.RefundedAmount, now); err != nil {
		return false, fmt.Errorf("failed to update refunded payment: %w", err)
	}
	payment.UpdatedAt = now

	stockRestored := false
	var wastedLines map[uuid.UUID]int
	if a.cancelTender {
		if payment.SplitID == nil {
			return false, ErrOrderNotRefundable
		}
		// The tender is handed back: the split and the bill owe it again; stock was never committed.
		// The CTE update is not visible to the outer SELECT, so the handed-back amount is subtracted there.
		if err := tx.GetContext(ctx, &order.PaidAmount, `
			WITH s AS (UPDATE payment_splits SET paid_amount = paid_amount - $3,
				status = CASE WHEN paid_amount - $3 = 0 THEN 'unpaid' ELSE 'partially_paid' END, updated_at = $4
				WHERE store_id = $1 AND id = $2 RETURNING order_id)
			SELECT COALESCE(SUM(paid_amount), 0) - $3 FROM payment_splits WHERE store_id = $1 AND order_id = (SELECT order_id FROM s)`,
			storeID, *payment.SplitID, a.amount, now); err != nil {
			return false, fmt.Errorf("failed to hand the tender back: %w", err)
		}
		order.PaymentStatus = domain.OrderPaymentStatusPartiallyPaid
		if order.PaidAmount == 0 {
			order.PaymentStatus = domain.OrderPaymentStatusUnpaid
		}
	} else {
		if payment.Status == domain.PaymentStatusRefunded && payment.SplitID != nil {
			// paid_amount stays gross (like orders.paid_amount); the refunded value lives on the payment
			// and orders.refunded_amount.
			if _, err := tx.ExecContext(ctx, `UPDATE payment_splits SET status = 'refunded', updated_at = $3 WHERE store_id = $1 AND id = $2`,
				storeID, *payment.SplitID, now); err != nil {
				return false, fmt.Errorf("failed to mark split refunded: %w", err)
			}
		}
		order.RefundedAmount += a.amount
		fullyRefunded := order.RefundedAmount >= order.PaidAmount
		order.PaymentStatus = domain.OrderPaymentStatusPartiallyRefunded
		if fullyRefunded {
			order.PaymentStatus = domain.OrderPaymentStatusRefunded
		}
		switch {
		case len(a.lineQty) > 0 || a.splitMode == domain.SplitModeByItem:
			var splitItems []struct {
				OrderItemID uuid.UUID `db:"order_item_id"`
				Quantity    int       `db:"quantity"`
			}
			if a.splitMode == domain.SplitModeByItem && payment.Status == domain.PaymentStatusRefunded {
				if err := tx.SelectContext(ctx, &splitItems, `SELECT order_item_id, quantity FROM payment_split_items WHERE store_id = $1 AND split_id = $2`,
					storeID, *payment.SplitID); err != nil {
					return false, fmt.Errorf("failed to load split items for restock: %w", err)
				}
			}
			for _, l := range splitItems {
				a.lineQty[l.OrderItemID] += l.Quantity
			}
			if a.restoreStock {
				products, options := computeItemStockQuantities(order, a.lineQty)
				if err := restoreStockTx(ctx, tx, orderRepo, storeID, products, options); err != nil {
					return false, err
				}
				stockRestored = len(a.lineQty) > 0
			} else {
				wastedLines = a.lineQty
			}
			// Every line has now been refunded and restocked or wasted (review follow-up F3).
			if (fullyRefunded || a.allLinesRefunded) && (order.StockStatus == domain.OrderStockStatusReserved || order.StockStatus == domain.OrderStockStatusCommitted) {
				order.StockStatus = domain.OrderStockStatusReleased
			}
		case fullyRefunded && a.restoreStock:
			before := order.StockStatus
			if err := releaseOrderStockTx(ctx, tx, orderRepo, order); err != nil {
				return false, err
			}
			stockRestored = order.StockStatus != before
		case fullyRefunded:
			wastedLines = make(map[uuid.UUID]int, len(order.Items))
			for _, item := range order.Items {
				wastedLines[item.ID] += item.Quantity
			}
		}
	}

	if err := tx.QueryRowxContext(ctx, `
		UPDATE orders SET payment_status = $3, refunded_amount = $4, stock_status = $5, paid_amount = $6, updated_at = $7
		WHERE store_id = $1 AND id = $2
		RETURNING updated_at
	`, storeID, order.ID, order.PaymentStatus, order.RefundedAmount, order.StockStatus, order.PaidAmount, now).Scan(&order.UpdatedAt); err != nil {
		return false, fmt.Errorf("failed to update refunded order: %w", err)
	}

	actorID := a.actorID
	auditFields := map[string]any{
		"payment_id":     payment.ID,
		"refund_id":      refundID,
		"order_id":       order.ID,
		"split_id":       payment.SplitID,
		"split_mode":     a.splitMode,
		"amount":         a.amount,
		"lines":          a.lineQty,
		"cancel_tender":  a.cancelTender,
		"method":         payment.Method,
		"restore_stock":  a.restoreStock,
		"stock_restored": stockRestored,
		"wasted_lines":   wastedLines,
		"stock_after":    order.StockStatus,
	}
	for k, v := range a.audit {
		auditFields[k] = v
	}
	auditPayload, _ := json.Marshal(auditFields)
	if err := postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{
		StoreID: storeID, ActorID: &actorID, ActorName: a.actorName, Action: "REFUND", Reason: a.reason,
		ReferenceID: payment.ID.String(), Metadata: string(auditPayload), CreatedAt: now,
	}); err != nil {
		return false, fmt.Errorf("failed to record refund audit log: %w", err)
	}

	return stockRestored, nil
}

// refundLinesAmountTx checks the requested lines against what is still refundable and prices them:
// total_amount × lines subtotal / order subtotal, so tax, service and discount follow the lines. The
// refund that returns the last line takes whatever money is left, absorbing rounding residue.
func refundLinesAmountTx(ctx context.Context, tx *sqlx.Tx, order *domain.Order, lines []RefundLineInput, lineQty map[uuid.UUID]int) (int64, bool, error) {
	var refunded []struct {
		OrderItemID uuid.UUID `db:"order_item_id"`
		Quantity    int       `db:"quantity"`
	}
	if err := tx.SelectContext(ctx, &refunded, `SELECT l.order_item_id, SUM(l.quantity) AS quantity FROM payment_refund_lines l
		JOIN payment_refunds r ON r.store_id = l.store_id AND r.id = l.refund_id
		WHERE r.store_id = $1 AND r.order_id = $2 GROUP BY l.order_item_id`, order.StoreID, order.ID); err != nil {
		return 0, false, err
	}
	already := map[uuid.UUID]int{}
	for _, r := range refunded {
		already[r.OrderItemID] = r.Quantity
	}
	items := map[uuid.UUID]domain.OrderItem{}
	for _, it := range order.Items {
		items[it.ID] = it
	}
	fields := map[string]string{}
	var subtotal int64
	for i, l := range lines {
		it, ok := items[l.OrderItemID]
		if !ok || l.Quantity > it.Quantity-already[l.OrderItemID] {
			fields[fmt.Sprintf("lines[%d].quantity", i)] = "melebihi jumlah yang masih bisa direfund"
			continue
		}
		lineQty[l.OrderItemID] = l.Quantity
		subtotal += it.SubtotalAmount * int64(l.Quantity) / int64(it.Quantity)
	}
	if len(fields) > 0 {
		return 0, false, &ValidationError{Fields: fields}
	}
	all := true
	for _, it := range order.Items {
		if already[it.ID]+lineQty[it.ID] < it.Quantity {
			all = false
		}
	}
	if all {
		return order.PaidAmount - order.RefundedAmount, true, nil
	}
	if order.SubtotalAmount <= 0 {
		return 0, false, &ValidationError{Fields: map[string]string{"lines": "pesanan tanpa subtotal tidak bisa direfund per item"}}
	}
	return order.TotalAmount * subtotal / order.SubtotalAmount, false, nil
}

// outletRefundTx pays a refund from the outlet cash fund (V13), never below zero.
func outletRefundTx(ctx context.Context, tx *sqlx.Tx, actor *AuthenticatedStaff, paymentID uuid.UUID, amount int64, reason string, approvedBy *uuid.UUID, now time.Time) error {
	storeID := actor.Staff.Store.ID
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('outlet_cash:' || $1::text))`, storeID); err != nil {
		return err
	}
	var balance int64
	if err := tx.GetContext(ctx, &balance, `SELECT COALESCE(SUM(`+outletSignedAmount+`), 0) FROM outlet_cash_movements WHERE store_id = $1`, storeID); err != nil {
		return err
	}
	if balance < amount {
		return ErrOutletInsufficientFunds
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO outlet_cash_movements (store_id, movement_type, amount, note, payment_id, actor_id, approved_by, created_at)
		VALUES ($1, 'refund', $2, $3, $4, $5, $6, $7)`, storeID, amount, "Refund: "+reason, paymentID, actor.Staff.UserID, approvedBy, now)
	return err
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
	idempotencyKey                            *string
	at                                        time.Time
}

func (r *paymentRefund) insert(ctx context.Context, tx *sqlx.Tx) (uuid.UUID, error) {
	var id uuid.UUID
	if err := tx.GetContext(ctx, &id, `
		INSERT INTO payment_refunds (store_id, payment_id, order_id, amount, method, bank_name, account_last4,
			transfer_reference, cash_movement_id, reason, actor_id, approved_by, idempotency_key, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14) RETURNING id`,
		r.storeID, r.paymentID, r.orderID, r.amount, r.method, r.bankName, r.accountLast4,
		r.transferReference, r.cashMovementID, r.reason, r.actorID, r.approvedBy, r.idempotencyKey, r.at); err != nil {
		if _, dup := postgres.UniqueViolation(err); dup {
			return uuid.Nil, ErrPaymentAlreadyRefunded
		}
		return uuid.Nil, fmt.Errorf("failed to record refund ledger: %w", err)
	}
	return id, nil
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
