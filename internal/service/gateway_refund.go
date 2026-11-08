package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/midtrans/midtrans-go/coreapi"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/midtrans"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
)

// Gateway refunds (RF1-a/b/f, PR 8.5b): the Midtrans Direct Refund API returns money to the customer's
// wallet. The request is kept in gateway_refunds; payment, order and stock change only when the bank
// confirms (bank_confirmed_at in a notification or a status check), through the same applyRefundTx as
// manual refunds.

const (
	RefundMethodGateway = "gateway"

	// gatewayRefundUnreceivedAfter fails a request Midtrans never acknowledged (transport error), so the
	// cashier can retry or refund manually.
	gatewayRefundUnreceivedAfter = 30 * time.Minute
)

var (
	ErrGatewayRefundUnsupported = errors.New("this payment cannot be refunded through the gateway; use a manual refund method")
	ErrGatewayPartialDisabled   = errors.New("partial gateway refunds are off for this store (RF1-b); use a manual refund method")
	ErrGatewayRefundPending     = errors.New("a gateway refund for this payment is still waiting for the bank")
	ErrGatewayRefundDenied      = errors.New("the bank denied the gateway refund; use a manual refund method")

	gatewayRefundMethods = map[domain.PaymentMethod]bool{
		domain.PaymentMethodQRIS: true, domain.PaymentMethodGoPay: true, domain.PaymentMethodShopeePay: true,
	}
)

// SetGateway enables refund_method "gateway" (RF1-a).
func (s *OperationalService) SetGateway(g GatewayClient) { s.gateway = g }

type gatewayRefundRequest struct {
	ID              uuid.UUID
	ProviderOrderID string
	Amount          int64
	Reason          string
}

// openGatewayRefundTx validates RF1-a/b and stores a pending request inside the refund transaction,
// after the actor was authorized. Nothing about the money changes yet.
func (s *OperationalService) openGatewayRefundTx(ctx context.Context, tx *sqlx.Tx, order *domain.Order, payment *domain.Payment,
	amount int64, lineQty map[uuid.UUID]int, allLines, restoreStock, cancelTender bool, reason string,
	actor *AuthenticatedStaff, approvedBy *uuid.UUID, now time.Time) (*gatewayRefundRequest, error) {
	if s.gateway == nil || cancelTender || payment.Provider != "midtrans" || !gatewayRefundMethods[payment.Method] {
		return nil, ErrGatewayRefundUnsupported
	}
	if amount != payment.Amount {
		settings, err := s.settings.GetStoreSettings(ctx, tx, actor.Staff.Store.ID)
		if err != nil {
			return nil, err
		}
		if !settings.GatewayPartialRefunds {
			return nil, ErrGatewayPartialDisabled
		}
	}
	linesJSON, _ := json.Marshal(lineQty)
	req := &gatewayRefundRequest{ID: uuid.New(), ProviderOrderID: payment.ProviderOrderID, Amount: amount, Reason: reason}
	_, err := tx.ExecContext(ctx, `INSERT INTO gateway_refunds (id, store_id, payment_id, order_id, amount, lines, all_lines, restore_stock,
		reason, actor_id, approved_by, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12)`,
		req.ID, actor.Staff.Store.ID, payment.ID, order.ID, amount, linesJSON, allLines, restoreStock, reason, actor.Staff.UserID, approvedBy, now)
	if _, dup := postgres.UniqueViolation(err); dup {
		return nil, ErrGatewayRefundPending
	}
	if err != nil {
		return nil, fmt.Errorf("failed to store gateway refund request: %w", err)
	}
	return req, nil
}

// dispatchGatewayRefund calls Midtrans outside any transaction and records the answer: 200 → requested
// (waiting for bank_confirmed_at), 202 or a business error → failed, transport error → left pending for
// the reconciliation worker.
func (s *OperationalService) dispatchGatewayRefund(ctx context.Context, storeID uuid.UUID, req *gatewayRefundRequest) (string, error) {
	resp, callErr := s.gateway.DirectRefund(ctx, req.ProviderOrderID, req.ID.String(), req.Amount, req.Reason)
	status, providerStatus, failure := "pending", "", ""
	switch {
	case callErr != nil:
		log.Printf("[REFUND] gateway refund %s unanswered: %v", req.ID, callErr)
	case resp.StatusCode == "200":
		status, providerStatus = "requested", resp.StatusCode
	default:
		status, providerStatus, failure = "failed", resp.StatusCode, resp.StatusCode+" "+resp.StatusMessage
	}
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE gateway_refunds SET status = $3, provider_status = $4, failure_reason = left($5, 500), updated_at = $6
			WHERE store_id = $1 AND id = $2 AND status = 'pending'`, storeID, req.ID, status, providerStatus, failure, s.nowFunc().UTC())
		return err
	})
	if err != nil {
		return "", err
	}
	if status == "failed" {
		return status, fmt.Errorf("%w (%s)", ErrGatewayRefundDenied, failure)
	}
	return status, nil
}

// confirmGatewayRefundsTx applies every open request of the payment that the provider reports as
// bank-confirmed. Used by the webhook and by the reconciliation worker; already confirmed requests are
// skipped, so repeated notifications are harmless.
func confirmGatewayRefundsTx(ctx context.Context, tx *sqlx.Tx, orderRepo *postgres.OrderRepository, paymentRepo *postgres.PaymentRepository,
	storeID, paymentID uuid.UUID, refunds []coreapi.RefundDetails, now time.Time) (int, error) {
	confirmed := 0
	for _, r := range refunds {
		id, err := uuid.Parse(r.RefundKey)
		if err != nil || r.BankConfirmedAt == "" {
			continue
		}
		var g struct {
			Amount       int64           `db:"amount"`
			Lines        json.RawMessage `db:"lines"`
			AllLines     bool            `db:"all_lines"`
			RestoreStock bool            `db:"restore_stock"`
			Reason       string          `db:"reason"`
			ActorID      uuid.UUID       `db:"actor_id"`
			ActorName    string          `db:"actor_name"`
			ApprovedBy   *uuid.UUID      `db:"approved_by"`
			OrderID      uuid.UUID       `db:"order_id"`
		}
		err = tx.GetContext(ctx, &g, `SELECT g.amount, g.lines, g.all_lines, g.restore_stock, g.reason, g.actor_id, COALESCE(u.full_name, '') AS actor_name,
				g.approved_by, g.order_id
			FROM gateway_refunds g LEFT JOIN users u ON u.store_id = g.store_id AND u.id = g.actor_id
			WHERE g.store_id = $1 AND g.id = $2 AND g.payment_id = $3 AND g.status IN ('pending', 'requested') FOR UPDATE OF g`, storeID, id, paymentID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return confirmed, err
		}
		if amount, err := midtrans.ParseGrossAmountIDR(r.RefundAmount); err != nil || amount != g.Amount {
			log.Printf("[REFUND] gateway refund %s: provider amount %q does not match %d; left open for review", id, r.RefundAmount, g.Amount)
			continue
		}
		order, _, err := orderRepo.GetOrderByIDForUpdate(ctx, tx, storeID, g.OrderID)
		if err != nil {
			return confirmed, err
		}
		payment, err := paymentRepo.GetPaymentByIDForUpdate(ctx, tx, storeID, paymentID)
		if err != nil {
			return confirmed, err
		}
		splitMode := domain.SplitModeFull
		if payment.SplitID != nil {
			if err := tx.GetContext(ctx, &splitMode, `SELECT split_mode FROM payment_splits WHERE store_id = $1 AND id = $2`, storeID, *payment.SplitID); err != nil {
				return confirmed, err
			}
		}
		lineQty := map[uuid.UUID]int{}
		_ = json.Unmarshal(g.Lines, &lineQty)
		if _, err := applyRefundTx(ctx, tx, orderRepo, &refundApplication{
			order: order, payment: payment, amount: g.Amount, lineQty: lineQty, splitMode: splitMode,
			restoreStock: g.RestoreStock, allLinesRefunded: g.AllLines,
			ledger:  &paymentRefund{storeID: storeID, paymentID: paymentID, orderID: order.ID, amount: g.Amount, method: RefundMethodGateway, reason: g.Reason, actorID: g.ActorID, approvedBy: g.ApprovedBy, at: now},
			actorID: g.ActorID, actorName: g.ActorName, reason: g.Reason,
			audit: map[string]any{"refund_method": RefundMethodGateway, "gateway_refund_id": id, "bank_confirmed_at": r.BankConfirmedAt, "owner_override_by": g.ApprovedBy},
		}, now); err != nil {
			return confirmed, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE gateway_refunds SET status = 'confirmed', confirmed_at = $3, updated_at = $3 WHERE store_id = $1 AND id = $2`,
			storeID, id, now); err != nil {
			return confirmed, err
		}
		confirmed++
	}
	return confirmed, nil
}

// ReconcileGatewayRefunds checks open requests older than staleAfter with a status lookup: confirmed
// refunds are applied, and a request Midtrans never received is failed after 30 minutes.
func (s *PaymentService) ReconcileGatewayRefunds(ctx context.Context, staleAfter time.Duration) error {
	now := s.nowFunc().UTC()
	var stores []uuid.UUID
	if err := s.db.SelectContext(ctx, &stores, `SELECT store_id FROM list_stores_with_open_gateway_refunds($1)`, now.Add(-staleAfter)); err != nil {
		return fmt.Errorf("failed to list stores with open gateway refunds: %w", err)
	}
	for _, storeID := range stores {
		var open []struct {
			ID              uuid.UUID `db:"id"`
			PaymentID       uuid.UUID `db:"payment_id"`
			ProviderOrderID string    `db:"provider_order_id"`
			Status          string    `db:"status"`
			CreatedAt       time.Time `db:"created_at"`
		}
		if err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
			return tx.SelectContext(ctx, &open, `SELECT g.id, g.payment_id, p.provider_order_id, g.status, g.created_at
				FROM gateway_refunds g JOIN payments p ON p.store_id = g.store_id AND p.id = g.payment_id
				WHERE g.store_id = $1 AND g.status IN ('pending', 'requested') AND g.updated_at < $2 LIMIT 20`, storeID, now.Add(-staleAfter))
		}); err != nil {
			log.Printf("[REFUND RECONCILE] store %s: %v", storeID, err)
			continue
		}
		for _, g := range open {
			resp, err := s.midtransClient.CheckTransaction(ctx, g.ProviderOrderID)
			if err != nil {
				log.Printf("[REFUND RECONCILE] gateway refund %s: status lookup failed: %v", g.ID, err)
				continue
			}
			if err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
				n, err := confirmGatewayRefundsTx(ctx, tx, s.orderRepo, s.paymentRepo, storeID, g.PaymentID, resp.Refunds, now)
				if err != nil || n > 0 {
					return err
				}
				known := false
				for _, r := range resp.Refunds {
					known = known || r.RefundKey == g.ID.String()
				}
				if g.Status == "pending" && !known && now.Sub(g.CreatedAt) > gatewayRefundUnreceivedAfter {
					_, err := tx.ExecContext(ctx, `UPDATE gateway_refunds SET status = 'failed', failure_reason = 'not received by the provider', updated_at = $3
						WHERE store_id = $1 AND id = $2`, storeID, g.ID, now)
					return err
				}
				_, err = tx.ExecContext(ctx, `UPDATE gateway_refunds SET updated_at = $3 WHERE store_id = $1 AND id = $2`, storeID, g.ID, now)
				return err
			}); err != nil {
				log.Printf("[REFUND RECONCILE] gateway refund %s: %v", g.ID, err)
			}
		}
	}
	return nil
}
