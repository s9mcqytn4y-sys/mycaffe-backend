package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

// Order discounts (PR 8.6, V12): one order-level discount on an unpaid pay-first order, taken off the
// subtotal before service and tax. Above cashier_max_discount_bps it needs the owner (own PIN or an
// approved "discount" request). A 100 % discount is a compliment: the order is settled without payment.

var ErrOrderNotDiscountable = errors.New("only an unpaid pay-first order without payments can be discounted")

type ApplyDiscountInput struct {
	// Exactly one of DiscountAmount (rupiah) and DiscountBPS (10000 = 100 %).
	DiscountAmount *int64     `json:"discount_amount"`
	DiscountBPS    *int64     `json:"discount_bps"`
	Reason         string     `json:"reason"`
	OwnerPIN       *string    `json:"owner_pin"`
	ApprovalID     *uuid.UUID `json:"approval_id"`
}

type ApplyDiscountResult struct {
	Order      *domain.Order `json:"order"`
	Compliment bool          `json:"compliment"`
}

// orderTotals recomputes service, tax and total after a discount, the same way as pricing.go.
func orderTotals(subtotal, discount int64, store *domain.Store) (service, tax, total int64, err error) {
	net := subtotal - discount
	if service, err = CalculateRoundHalfUpBPS(net, store.ServiceBps); err != nil {
		return
	}
	if tax, err = CalculateRoundHalfUpBPS(net+service, store.TaxBps); err != nil {
		return
	}
	return service, tax, net + service + tax, nil
}

// discountNeedsOwner reports whether a discount is above the cashier limit of the store.
func discountNeedsOwner(discount, subtotal, limitBPS int64) bool {
	return discount*10000 > subtotal*limitBPS
}

func (s *OperationalService) ApplyDiscount(ctx context.Context, actor *AuthenticatedStaff, orderID uuid.UUID, in ApplyDiscountInput) (*ApplyDiscountResult, error) {
	if actor == nil {
		return nil, ErrInvalidSession
	}
	if !actor.Can(rbac.OrderDiscountApply) {
		return nil, ErrForbidden
	}
	reason := strings.TrimSpace(in.Reason)
	fields := map[string]string{}
	if reason == "" || len(reason) > 500 {
		fields["reason"] = "alasan diskon wajib diisi (maks. 500 karakter)"
	}
	if (in.DiscountAmount == nil) == (in.DiscountBPS == nil) {
		fields["discount_amount"] = "isi salah satu: discount_amount atau discount_bps"
	}
	if in.DiscountBPS != nil && (*in.DiscountBPS <= 0 || *in.DiscountBPS > 10000) {
		fields["discount_bps"] = "harus 1 - 10000 (100%)"
	}
	if in.DiscountAmount != nil && *in.DiscountAmount <= 0 {
		fields["discount_amount"] = "harus lebih dari 0"
	}
	if len(fields) > 0 {
		return nil, &ValidationError{Fields: fields}
	}

	storeID := actor.Staff.Store.ID
	var result *ApplyDiscountResult
	err := s.pins.withPINTx(ctx, storeID, func(tx *sqlx.Tx) error {
		order, splits, err := s.orderRepo.GetOrderByIDForUpdate(ctx, tx, storeID, orderID)
		if err != nil {
			return err
		}
		if order.BillingMode != domain.BillingModePayFirst || order.Status != domain.OrderStatusPendingPayment ||
			order.PaidAmount != 0 || order.DiscountAmount != 0 || len(splits) != 1 ||
			(order.PaymentStatus != domain.OrderPaymentStatusUnpaid && order.PaymentStatus != domain.OrderPaymentStatusFailed && order.PaymentStatus != domain.OrderPaymentStatusExpired) {
			return ErrOrderNotDiscountable
		}
		var pending bool
		if err := tx.GetContext(ctx, &pending, `SELECT EXISTS (SELECT 1 FROM payments WHERE store_id = $1 AND order_id = $2
			AND status IN ('pending', 'authorized', 'paid', 'partially_refunded'))`, storeID, order.ID); err != nil {
			return err
		}
		if pending {
			return ErrOrderNotDiscountable // a QRIS charged for the old total must not be undercut
		}

		discount := int64(0)
		if in.DiscountAmount != nil {
			discount = *in.DiscountAmount
		} else if discount, err = CalculateRoundHalfUpBPS(order.SubtotalAmount, *in.DiscountBPS); err != nil {
			return err
		}
		if discount > order.SubtotalAmount {
			return &ValidationError{Fields: map[string]string{"discount_amount": "tidak boleh melebihi subtotal"}}
		}

		settings, err := s.settings.GetStoreSettings(ctx, tx, storeID)
		if err != nil {
			return err
		}
		now := s.nowFunc().UTC()
		var approvedBy *uuid.UUID
		claim := &approvalClaim{ID: in.ApprovalID, Action: ApprovalDiscount, Ref: order.ID, Params: approvalParams{Amount: &discount}}
		if discountNeedsOwner(discount, order.SubtotalAmount, settings.CashierMaxDiscountBPS) && actor.Staff.Role != domain.RoleOwner {
			switch {
			case in.OwnerPIN != nil && strings.TrimSpace(*in.OwnerPIN) != "":
				owner, err := s.pins.verifyOwner(ctx, tx, actor, strings.TrimSpace(*in.OwnerPIN))
				if err != nil {
					return err
				}
				approvedBy = &owner
			case in.ApprovalID != nil:
				owner, err := consumeApproval(ctx, tx, actor, claim, discount, now)
				if err != nil {
					return err
				}
				approvedBy = &owner
			default:
				return ErrOwnerPINRequired
			}
		}

		store, err := s.orderRepo.GetStoreForOrder(ctx, tx, storeID)
		if err != nil {
			return err
		}
		service, tax, total, err := orderTotals(order.SubtotalAmount, discount, store)
		if err != nil {
			return err
		}
		order.DiscountAmount, order.ServiceChargeAmount, order.TaxAmount, order.TotalAmount = discount, service, tax, total
		if _, err := tx.ExecContext(ctx, `UPDATE orders SET discount_amount = $3, service_charge_amount = $4, tax_amount = $5, total_amount = $6,
			discount_reason = $7, updated_at = $8 WHERE store_id = $1 AND id = $2`, storeID, order.ID, discount, service, tax, total, reason, now); err != nil {
			return fmt.Errorf("failed to apply discount: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE payment_splits SET subtotal_amount = $3, service_charge_amount = $4, tax_amount = $5, total_amount = $6, updated_at = $7
			WHERE store_id = $1 AND id = $2`, storeID, splits[0].ID, order.SubtotalAmount-discount, service, tax, total, now); err != nil {
			return fmt.Errorf("failed to re-total the split: %w", err)
		}

		compliment := total == 0
		if compliment {
			// Nothing left to pay: settle like a payment (stock commit, KDS tickets, order.paid event).
			if _, err := tx.ExecContext(ctx, `UPDATE payment_splits SET status = 'paid' WHERE store_id = $1 AND id = $2`, storeID, splits[0].ID); err != nil {
				return err
			}
			settle := &PaymentService{db: s.db, orderRepo: s.orderRepo, paymentRepo: s.paymentRepo, nowFunc: s.nowFunc}
			if err := settle.commitPaidOrderTx(ctx, tx, order); err != nil {
				return err
			}
		}

		actorID := actor.Staff.UserID
		meta, _ := json.Marshal(map[string]any{"order_id": order.ID, "discount_amount": discount, "discount_bps": in.DiscountBPS,
			"subtotal": order.SubtotalAmount, "new_total": total, "compliment": compliment, "approved_by": approvedBy, "approval_id": claim.usedID()})
		if err := postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{StoreID: storeID, ActorID: &actorID, ActorName: actor.Staff.FullName,
			Action: "ORDER_DISCOUNT", Reason: reason, ReferenceID: order.OrderNumber, Metadata: string(meta), CreatedAt: now}); err != nil {
			return err
		}
		result = &ApplyDiscountResult{Order: order, Compliment: compliment}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
