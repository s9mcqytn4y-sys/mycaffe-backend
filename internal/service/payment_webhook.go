package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/midtrans"
)

// ProcessMidtransWebhook persists a verified notification in the durable inbox and
// reconciles the payment/order in one tenant-scoped PostgreSQL transaction.
// The HTTP handler must verify the SHA-512 signature before calling this method.
func (s *PaymentService) ProcessMidtransWebhook(
	ctx context.Context,
	input MidtransWebhookInput,
	sanitizedRawPayload string,
) (*PaymentWebhookResult, error) {
	if s == nil || s.db == nil || s.paymentRepo == nil || s.orderRepo == nil || s.midtransClient == nil {
		return nil, errors.New("payment service dependencies are not initialized")
	}
	input.OrderID = strings.TrimSpace(input.OrderID)
	input.Currency = strings.ToUpper(strings.TrimSpace(input.Currency))
	if input.OrderID == "" || input.StatusCode == "" || input.TransactionStatus == "" || input.GrossAmount == "" {
		return nil, ErrInvalidMidtransWebhook
	}
	if input.Currency != "" && input.Currency != "IDR" {
		return nil, fmt.Errorf("%w: only IDR is accepted", ErrInvalidMidtransWebhook)
	}

	grossAmountIDR, err := midtrans.ParseGrossAmountIDR(input.GrossAmount)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid gross_amount", ErrInvalidMidtransWebhook)
	}
	mappedStatus, err := midtrans.MapMidtransStatus(input.TransactionStatus, input.FraudStatus)
	if err != nil {
		return nil, fmt.Errorf("%w: unsupported transaction/fraud status", ErrInvalidMidtransWebhook)
	}
	if mappedStatus == domain.PaymentStatusPaid && input.StatusCode != "200" {
		return nil, fmt.Errorf("%w: paid notification must have status_code 200", ErrInvalidMidtransWebhook)
	}
	if sanitizedRawPayload == "" || !json.Valid([]byte(sanitizedRawPayload)) {
		sanitizedRawPayload = "{}"
	}

	resolved, err := s.paymentRepo.ResolveWebhookContext(ctx, s.db, input.OrderID)
	if err != nil {
		return nil, err
	}

	// Midtrans notifications can be delivered out of order. If local state is already
	// paid and an older non-paid notification arrives, query provider status before
	// acknowledging. Never downgrade or overwrite a locally settled payment attempt:
	// refunds and chargebacks are routed to the dedicated reconciliation branch below.
	// A provider lookup failure returns an error so Midtrans retries the notification.
	ignoreIncoming := false
	if resolved.CurrentStatus == domain.PaymentStatusPaid && mappedStatus != domain.PaymentStatusPaid {
		latest, statusErr := s.midtransClient.CheckTransaction(ctx, input.OrderID)
		if statusErr != nil {
			return nil, fmt.Errorf("failed to reconcile out-of-order Midtrans notification: %w", statusErr)
		}
		if _, mapErr := midtrans.MapMidtransStatus(latest.TransactionStatus, latest.FraudStatus); mapErr != nil {
			return nil, fmt.Errorf("%w: provider status lookup returned unsupported state", ErrPaymentReconciliation)
		}
		ignoreIncoming = true
	}

	result := &PaymentWebhookResult{Status: mappedStatus}
	eventHash := midtransWebhookEventHash(input, grossAmountIDR)
	err = database.WithTenantTx(ctx, s.db, resolved.StoreID, func(tx *sqlx.Tx) error {
		order, _, err := s.orderRepo.GetOrderByIDForUpdate(ctx, tx, resolved.StoreID, resolved.OrderID)
		if err != nil {
			return err
		}
		payment, err := s.paymentRepo.GetPaymentByProviderOrderIDForUpdate(ctx, tx, resolved.StoreID, input.OrderID)
		if err != nil {
			return err
		}
		if payment.ID != resolved.PaymentID || payment.OrderID != order.ID {
			return fmt.Errorf("%w: resolver and tenant-scoped payment row disagree", ErrPaymentReconciliation)
		}

		orderID := order.ID
		logEntry := &domain.PaymentNotificationLog{
			ID:                uuid.New(),
			StoreID:           resolved.StoreID,
			OrderID:           &orderID,
			Provider:          "midtrans",
			ProviderOrderID:   input.OrderID,
			EventHash:         eventHash,
			TransactionID:     input.TransactionID,
			OrderNumber:       order.OrderNumber,
			StatusCode:        input.StatusCode,
			TransactionStatus: input.TransactionStatus,
			GrossAmount:       grossAmountIDR,
			RawPayload:        sanitizedRawPayload,
			IsProcessed:       false,
		}
		if payment.Amount != grossAmountIDR || resolved.ExpectedAmount != payment.Amount {
			message := fmt.Sprintf("gross_amount mismatch: notification=%d expected=%d", grossAmountIDR, payment.Amount)
			logEntry.ErrorMessage = &message
			inserted, insertErr := s.paymentRepo.InsertPaymentNotificationLog(ctx, tx, logEntry)
			if insertErr != nil {
				return insertErr
			}
			result.Duplicate = !inserted
			result.Anomaly = inserted
			return nil
		}

		inserted, err := s.paymentRepo.InsertPaymentNotificationLog(ctx, tx, logEntry)
		if err != nil {
			return err
		}
		if !inserted {
			result.Duplicate = true
			return nil
		}

		// Refund and chargeback transitions require a separate ledger workflow. Preserve
		// the signed event for reconciliation, but do not mutate financial totals here.
		if mappedStatus == domain.PaymentStatusRefunded ||
			mappedStatus == domain.PaymentStatusPartiallyRefunded ||
			mappedStatus == domain.PaymentStatusChargeback ||
			mappedStatus == domain.PaymentStatusPartialChargeback {
			message := "refund/chargeback event stored for dedicated reconciliation workflow"
			if _, err := tx.ExecContext(ctx, "UPDATE payment_notification_logs SET error_message = $3 WHERE store_id = $1 AND id = $2", resolved.StoreID, logEntry.ID, message); err != nil {
				return fmt.Errorf("failed to annotate deferred payment event: %w", err)
			}
			result.Anomaly = true
			return nil
		}

		if ignoreIncoming || !domain.CanTransitionPaymentStatus(payment.Status, mappedStatus) {
			if err := s.paymentRepo.MarkNotificationProcessed(ctx, tx, resolved.StoreID, logEntry.ID); err != nil {
				return err
			}
			result.Ignored = true
			result.Status = payment.Status
			return nil
		}

		priorStatus := payment.Status
		if err := s.paymentRepo.UpdatePaymentStatus(
			ctx, tx, payment, mappedStatus, input.TransactionStatus,
			input.FraudStatus, input.TransactionID, input.PaymentType,
		); err != nil {
			return err
		}

		if mappedStatus == domain.PaymentStatusPaid {
			if payment.SplitID == nil || *payment.SplitID == uuid.Nil {
				return fmt.Errorf("%w: paid payment attempt has no split_id", ErrPaymentReconciliation)
			}
			split, err := s.paymentRepo.GetPaymentSplitForUpdate(ctx, tx, resolved.StoreID, *payment.SplitID)
			if err != nil {
				return err
			}
			duplicate, err := s.flagDuplicateSettlementTx(ctx, tx, order, priorStatus)
			if err != nil {
				return err
			}
			if duplicate {
				message := "duplicate settlement: order already paid by another attempt; refund required"
				if _, err := tx.ExecContext(ctx, "UPDATE payment_notification_logs SET error_message = $3 WHERE store_id = $1 AND id = $2", resolved.StoreID, logEntry.ID, message); err != nil {
					return err
				}
				result.Anomaly = true
				return nil
			}
			if split.OrderID != order.ID || split.TotalAmount != payment.Amount {
				message := "payment amount does not match the full payment split; manual reconciliation required"
				if _, err := tx.ExecContext(ctx, "UPDATE payment_notification_logs SET error_message = $3 WHERE store_id = $1 AND id = $2", resolved.StoreID, logEntry.ID, message); err != nil {
					return err
				}
				result.Anomaly = true
				return nil
			}
			if split.Status != domain.SplitBillStatusPaid {
				if _, err := tx.ExecContext(ctx, "UPDATE payment_splits SET status = 'paid', paid_amount = total_amount WHERE store_id = $1 AND id = $2", resolved.StoreID, split.ID); err != nil {
					return fmt.Errorf("failed to settle payment split: %w", err)
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

		if !result.Anomaly {
			if err := s.paymentRepo.MarkNotificationProcessed(ctx, tx, resolved.StoreID, logEntry.ID); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func midtransWebhookEventHash(input MidtransWebhookInput, grossAmount int64) string {
	canonical, _ := json.Marshal(struct {
		ProviderOrderID   string `json:"provider_order_id"`
		TransactionID     string `json:"transaction_id"`
		StatusCode        string `json:"status_code"`
		TransactionStatus string `json:"transaction_status"`
		FraudStatus       string `json:"fraud_status"`
		GrossAmount       int64  `json:"gross_amount"`
	}{
		ProviderOrderID: input.OrderID, TransactionID: input.TransactionID,
		StatusCode: input.StatusCode, TransactionStatus: input.TransactionStatus,
		FraudStatus: input.FraudStatus, GrossAmount: grossAmount,
	})
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}
