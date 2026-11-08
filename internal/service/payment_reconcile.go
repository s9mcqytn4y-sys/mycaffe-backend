package service

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/metrics"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/midtrans"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
)

// ExpireOverdueOrders cancels unpaid pending_payment orders whose reservation deadline (expires_at)
// has elapsed, restores reserved catalog stock, expires pending payment attempts, and emits order.expired.
func (s *PaymentService) ExpireOverdueOrders(
	ctx context.Context,
	trustedStoreID uuid.UUID,
	batchLimit int,
) (int, error) {
	if s == nil || s.db == nil || s.paymentRepo == nil || s.orderRepo == nil {
		return 0, errors.New("payment service dependencies are not initialized")
	}
	if trustedStoreID == uuid.Nil {
		return 0, postgres.ErrEmptyTenantContext
	}
	if batchLimit <= 0 {
		batchLimit = 50
	}

	var expiredCount int
	now := s.nowFunc().UTC()
	err := database.WithTenantTx(ctx, s.db, trustedStoreID, func(tx *sqlx.Tx) error {
		orderIDs, err := s.paymentRepo.ListOverdueUnpaidOrderIDs(ctx, tx, trustedStoreID, now, batchLimit)
		if err != nil {
			return err
		}
		for _, orderID := range orderIDs {
			order, _, err := s.orderRepo.GetOrderByIDForUpdate(ctx, tx, trustedStoreID, orderID)
			if err != nil {
				return err
			}
			if order.PaymentStatus == domain.OrderPaymentStatusPaid || order.Status != domain.OrderStatusPendingPayment {
				continue
			}
			if order.StockStatus == domain.OrderStockStatusReserved {
				if err := releaseOrderStockTx(ctx, tx, s.orderRepo, order); err != nil {
					return err
				}
			}
			order.Status = domain.OrderStatusCancelled
			order.PaymentStatus = domain.OrderPaymentStatusExpired
			order.ExpiresAt = nil
			if err := s.orderRepo.UpdateOrderStatusAndStock(ctx, tx, order); err != nil {
				return err
			}
			if err := s.orderRepo.UpdatePaymentSplitsStatusByOrder(ctx, tx, trustedStoreID, order.ID, domain.SplitBillStatusCancelled, false); err != nil {
				return err
			}
			if err := s.paymentRepo.ExpirePendingPaymentsByOrder(ctx, tx, trustedStoreID, order.ID); err != nil {
				return err
			}
			if err := s.insertPaymentOutboxTx(ctx, tx, order, "order.expired", map[string]any{
				"reason": "payment reservation deadline elapsed",
			}); err != nil {
				return err
			}
			expiredCount++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return expiredCount, nil
}

// ReconcileStalePendingPayments queries Midtrans CheckTransaction for pending payment attempts
// older than olderThan and reconciles any state transition without holding DB locks during HTTP calls.
func (s *PaymentService) ReconcileStalePendingPayments(
	ctx context.Context,
	trustedStoreID uuid.UUID,
	olderThan time.Duration,
	batchLimit int,
) (int, error) {
	if s == nil || s.db == nil || s.paymentRepo == nil || s.orderRepo == nil || s.midtransClient == nil {
		return 0, errors.New("payment service dependencies are not initialized")
	}
	if trustedStoreID == uuid.Nil {
		return 0, postgres.ErrEmptyTenantContext
	}
	if batchLimit <= 0 {
		batchLimit = 50
	}

	cutoff := s.nowFunc().UTC().Add(-olderThan)
	var candidates []domain.Payment
	err := database.WithTenantTx(ctx, s.db, trustedStoreID, func(tx *sqlx.Tx) error {
		list, err := s.paymentRepo.ListStalePendingPayments(ctx, tx, trustedStoreID, cutoff, batchLimit)
		if err != nil {
			return err
		}
		candidates = list
		return nil
	})
	if err != nil {
		return 0, err
	}

	var reconciledCount int
	for _, candidate := range candidates {
		statusResp, checkErr := s.midtransClient.CheckTransaction(ctx, candidate.ProviderOrderID)
		if checkErr != nil || statusResp == nil {
			continue
		}
		mappedStatus, mapErr := midtrans.MapMidtransStatus(statusResp.TransactionStatus, statusResp.FraudStatus)
		if mapErr != nil {
			continue
		}
		if mappedStatus == domain.PaymentStatusPaid && statusResp.StatusCode != "200" {
			continue
		}
		if mappedStatus == candidate.Status {
			continue
		}

		err := database.WithTenantTx(ctx, s.db, trustedStoreID, func(tx *sqlx.Tx) error {
			order, _, err := s.orderRepo.GetOrderByIDForUpdate(ctx, tx, trustedStoreID, candidate.OrderID)
			if err != nil {
				return err
			}
			payment, err := s.paymentRepo.GetPaymentByIDForUpdate(ctx, tx, trustedStoreID, candidate.ID)
			if err != nil {
				return err
			}
			if !domain.CanTransitionPaymentStatus(payment.Status, mappedStatus) || payment.Status == mappedStatus {
				return nil
			}
			if statusResp.GrossAmount != "" {
				if grossIDR, parseErr := midtrans.ParseGrossAmountIDR(statusResp.GrossAmount); parseErr != nil || grossIDR != payment.Amount {
					return nil
				}
			}
			applyMidtransStatusResponse(payment, statusResp)
			if err := s.paymentRepo.UpdatePaymentGatewayDetails(ctx, tx, payment); err != nil {
				return err
			}
			priorStatus := payment.Status
			if err := s.paymentRepo.UpdatePaymentStatus(
				ctx, tx, payment, mappedStatus, statusResp.TransactionStatus,
				statusResp.FraudStatus, statusResp.TransactionID, statusResp.PaymentType,
			); err != nil {
				return err
			}
			if mappedStatus == domain.PaymentStatusPaid {
				if duplicate, err := s.flagDuplicateSettlementTx(ctx, tx, order, priorStatus); err != nil || duplicate {
					return err
				}
				if payment.SplitID != nil && *payment.SplitID != uuid.Nil {
					split, err := s.paymentRepo.GetPaymentSplitForUpdate(ctx, tx, trustedStoreID, *payment.SplitID)
					if err != nil {
						return err
					}
					if split.Status != domain.SplitBillStatusPaid {
						if _, err := tx.ExecContext(ctx, `UPDATE payment_splits SET status = 'paid', paid_amount = total_amount WHERE store_id = $1 AND id = $2`, trustedStoreID, split.ID); err != nil {
							return err
						}
					}
				}
				if err := s.commitPaidOrderTx(ctx, tx, order); err != nil {
					return err
				}
			} else {
				if err := s.applyNonPaidStatusTx(ctx, tx, order, mappedStatus); err != nil {
					return err
				}
			}
			reconciledCount++
			return nil
		})
		if err != nil {
			return reconciledCount, err
		}
	}
	return reconciledCount, nil
}

// RunReconciliationCycle discovers tenants with overdue unpaid orders or pending Midtrans
// payment attempts via list_stores_with_pending_reconciliation() and reconciles each tenant
// inside an isolated database.WithTenantTx context.
func (s *PaymentService) RunReconciliationCycle(
	ctx context.Context,
	olderThan time.Duration,
	batchLimit int,
) error {
	if s == nil || s.db == nil || s.paymentRepo == nil {
		return errors.New("payment service is not initialized")
	}
	storeIDs, err := s.paymentRepo.ListStoresWithPendingReconciliation(ctx, s.db)
	if err != nil {
		return err
	}
	for _, storeID := range storeIDs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := s.ExpireOverdueOrders(ctx, storeID, batchLimit); err != nil {
			log.Printf("[RECONCILIATION WORKER] store_id=%s expire overdue orders error: %v", storeID, err)
		}
		if s.midtransClient != nil {
			if _, err := s.ReconcileStalePendingPayments(ctx, storeID, olderThan, batchLimit); err != nil {
				log.Printf("[RECONCILIATION WORKER] store_id=%s reconcile pending payments error: %v", storeID, err)
			}
		}
	}
	return nil
}

// StartReconciliationWorker runs RunReconciliationCycle periodically until ctx is cancelled.
func (s *PaymentService) StartReconciliationWorker(
	ctx context.Context,
	interval time.Duration,
	olderThan time.Duration,
	batchLimit int,
) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if olderThan <= 0 {
		olderThan = 2 * time.Minute
	}
	if batchLimit <= 0 {
		batchLimit = 50
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.ReconcileGatewayRefunds(ctx, olderThan); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("[RECONCILIATION WORKER] gateway refunds: %v", err)
			}
			if err := s.RunReconciliationCycle(ctx, olderThan, batchLimit); err != nil {
				if !errors.Is(err, context.Canceled) {
					log.Printf("[RECONCILIATION WORKER] cycle error: %v", err)
				}
			} else {
				metrics.WorkerSucceeded("reconciliation")
			}
		}
	}
}
