package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/midtrans"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

var (
	ErrInvalidMidtransWebhook = errors.New("invalid Midtrans webhook")
	ErrPaymentReconciliation  = errors.New("payment reconciliation failed")
)

type MidtransWebhookInput struct {
	OrderID           string
	TransactionID     string
	StatusCode        string
	TransactionStatus string
	GrossAmount       string
	FraudStatus       string
	PaymentType       string
	Currency          string
}

type PaymentWebhookResult struct {
	Duplicate bool
	Ignored   bool
	Anomaly   bool
	Status    domain.PaymentAttemptStatus
}

type PaymentService struct {
	db             *sqlx.DB
	paymentRepo    *postgres.PaymentRepository
	orderRepo      *postgres.OrderRepository
	midtransClient *midtrans.Client
	nowFunc        func() time.Time
}

func NewPaymentService(
	db *sqlx.DB,
	paymentRepo *postgres.PaymentRepository,
	orderRepo *postgres.OrderRepository,
	midtransClient *midtrans.Client,
) *PaymentService {
	if paymentRepo == nil {
		paymentRepo = postgres.NewPaymentRepository()
	}
	if orderRepo == nil {
		orderRepo = postgres.NewOrderRepository()
	}
	return &PaymentService{
		db:             db,
		paymentRepo:    paymentRepo,
		orderRepo:      orderRepo,
		midtransClient: midtransClient,
		nowFunc:        time.Now,
	}
}

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
	if input.Currency != "IDR" {
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
	// considering any transition. Never downgrade a locally settled payment.
	ignoreIncoming := false
	if resolved.CurrentStatus == domain.PaymentStatusPaid && mappedStatus != domain.PaymentStatusPaid {
		latest, statusErr := s.midtransClient.CheckTransaction(ctx, input.OrderID)
		if statusErr != nil {
			return nil, fmt.Errorf("failed to reconcile out-of-order Midtrans notification: %w", statusErr)
		}
		latestStatus, mapErr := midtrans.MapMidtransStatus(latest.TransactionStatus, latest.FraudStatus)
		if mapErr != nil {
			return nil, fmt.Errorf("%w: provider status lookup returned unsupported state", ErrPaymentReconciliation)
		}
		if latest.StatusCode == "200" && latestStatus == domain.PaymentStatusPaid {
			mappedStatus = domain.PaymentStatusPaid
		} else {
			ignoreIncoming = true
		}
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

func (s *PaymentService) commitPaidOrderTx(ctx context.Context, tx *sqlx.Tx, order *domain.Order) error {
	if order == nil {
		return errors.New("order is required")
	}
	if order.PaymentStatus == domain.OrderPaymentStatusPaid &&
		(order.StockStatus == domain.OrderStockStatusCommitted || order.StockStatus == domain.OrderStockStatusNotTracked) {
		return nil
	}

	if order.Status == domain.OrderStatusCancelled && order.PaymentStatus != domain.OrderPaymentStatusExpired {
		order.PaymentStatus = domain.OrderPaymentStatusPaid
		order.PaidAmount = order.TotalAmount
		order.ExpiresAt = nil
		if err := s.orderRepo.UpdateOrderStatusAndStock(ctx, tx, order); err != nil {
			return err
		}
		return s.insertPaymentOutboxTx(ctx, tx, order, "payment.refund_required", map[string]any{
			"reason": "late settlement after manual cancellation",
		})
	}

	if order.StockStatus == domain.OrderStockStatusReleased {
		if _, err := tx.ExecContext(ctx, "SAVEPOINT payment_stock_commit"); err != nil {
			return fmt.Errorf("failed to create stock commit savepoint: %w", err)
		}
		products, options := computeOrderStockQuantities(order)
		var stockErr error
		for _, productID := range sortedUUIDKeysFromIntMap(products) {
			if err := s.orderRepo.DeductProductStock(ctx, tx, order.StoreID, productID, products[productID]); err != nil {
				stockErr = err
				break
			}
		}
		if stockErr == nil {
			for _, optionID := range sortedUUIDKeysFromIntMap(options) {
				if err := s.orderRepo.DeductModifierOptionStock(ctx, tx, order.StoreID, optionID, options[optionID]); err != nil {
					stockErr = err
					break
				}
			}
		}
		if errors.Is(stockErr, postgres.ErrStockUpdateFailed) {
			if _, err := tx.ExecContext(ctx, "ROLLBACK TO SAVEPOINT payment_stock_commit"); err != nil {
				return fmt.Errorf("failed to roll back partial stock commit: %w", err)
			}
			if _, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT payment_stock_commit"); err != nil {
				return err
			}
			order.PaymentStatus = domain.OrderPaymentStatusPaid
			order.PaidAmount = order.TotalAmount
			order.Status = domain.OrderStatusCancelled
			order.StockStatus = domain.OrderStockStatusReleased
			order.ExpiresAt = nil
			if err := s.orderRepo.UpdateOrderStatusAndStock(ctx, tx, order); err != nil {
				return err
			}
			return s.insertPaymentOutboxTx(ctx, tx, order, "payment.late_settlement_stock_exhausted", map[string]any{
				"reason": "payment settled after stock reservation was released and stock is no longer available",
			})
		}
		if stockErr != nil {
			return fmt.Errorf("failed to recommit released stock: %w", stockErr)
		}
		if _, err := tx.ExecContext(ctx, "RELEASE SAVEPOINT payment_stock_commit"); err != nil {
			return err
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
	if err := s.orderRepo.UpdateOrderStatusAndStock(ctx, tx, order); err != nil {
		return fmt.Errorf("failed to update paid order: %w", err)
	}
	if err := s.orderRepo.UpdatePaymentSplitsStatusByOrder(ctx, tx, order.StoreID, order.ID, domain.SplitBillStatusPaid, true); err != nil {
		return err
	}
	return s.insertPaymentOutboxTx(ctx, tx, order, "order.paid", map[string]any{
		"paid_amount":  order.PaidAmount,
		"status":       order.Status,
		"stock_status": order.StockStatus,
	})
}

func (s *PaymentService) applyNonPaidStatusTx(
	ctx context.Context,
	tx *sqlx.Tx,
	order *domain.Order,
	status domain.PaymentAttemptStatus,
) error {
	if order == nil || order.PaymentStatus == domain.OrderPaymentStatusPaid {
		return nil
	}
	order.PaymentStatus = orderPaymentStatusFromAttempt(status)

	deadlinePassed := order.ExpiresAt != nil && !s.nowFunc().Before(*order.ExpiresAt)
	if deadlinePassed {
		if order.StockStatus == domain.OrderStockStatusReserved {
			products, options := computeOrderStockQuantities(order)
			for _, productID := range sortedUUIDKeysFromIntMap(products) {
				if err := s.orderRepo.RestoreProductStock(ctx, tx, order.StoreID, productID, products[productID]); err != nil {
					return err
				}
			}
			for _, optionID := range sortedUUIDKeysFromIntMap(options) {
				if err := s.orderRepo.RestoreModifierOptionStock(ctx, tx, order.StoreID, optionID, options[optionID]); err != nil {
					return err
				}
			}
			order.StockStatus = domain.OrderStockStatusReleased
		}
		order.Status = domain.OrderStatusCancelled
		order.PaymentStatus = domain.OrderPaymentStatusExpired
		order.ExpiresAt = nil
		if err := s.orderRepo.UpdatePaymentSplitsStatusByOrder(ctx, tx, order.StoreID, order.ID, domain.SplitBillStatusCancelled, false); err != nil {
			return err
		}
		if err := s.insertPaymentOutboxTx(ctx, tx, order, "order.expired", map[string]any{
			"reason": "payment reservation deadline elapsed",
		}); err != nil {
			return err
		}
		return s.orderRepo.UpdateOrderStatusAndStock(ctx, tx, order)
	}
	return s.orderRepo.UpdateOrderStatusAndStock(ctx, tx, order)
}

func orderPaymentStatusFromAttempt(status domain.PaymentAttemptStatus) domain.OrderPaymentStatus {
	switch status {
	case domain.PaymentStatusPending, domain.PaymentStatusAuthorized:
		return domain.OrderPaymentStatusPending
	case domain.PaymentStatusFailed:
		return domain.OrderPaymentStatusFailed
	case domain.PaymentStatusCancelled:
		return domain.OrderPaymentStatusCancelled
	case domain.PaymentStatusExpired:
		return domain.OrderPaymentStatusExpired
	case domain.PaymentStatusPaid:
		return domain.OrderPaymentStatusPaid
	default:
		return domain.OrderPaymentStatusPending
	}
}

func (s *PaymentService) insertPaymentOutboxTx(
	ctx context.Context,
	tx *sqlx.Tx,
	order *domain.Order,
	eventType string,
	extra map[string]any,
) error {
	payload := map[string]any{
		"order_id":       order.ID.String(),
		"store_id":       order.StoreID.String(),
		"order_number":   order.OrderNumber,
		"queue_number":   order.QueueNumber,
		"status":         order.Status,
		"payment_status": order.PaymentStatus,
		"stock_status":   order.StockStatus,
		"paid_amount":    order.PaidAmount,
	}
	for key, value := range extra {
		payload[key] = value
	}
	bytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	event := domain.OutboxEvent{
		ID:            uuid.New(),
		StoreID:       order.StoreID,
		EventType:     eventType,
		AggregateType: "order",
		AggregateID:   order.ID.String(),
		Payload:       string(bytes),
	}
	return s.orderRepo.InsertOutboxEvent(ctx, tx, &event)
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
