package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/midtrans/midtrans-go/coreapi"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/midtrans"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

var (
	ErrInvalidMidtransWebhook        = errors.New("invalid Midtrans webhook")
	ErrPaymentReconciliation         = errors.New("payment reconciliation failed")
	ErrPaymentIdempotencyKeyRequired = errors.New("payment idempotency_key is required")
	ErrPaymentIdempotencyConflict    = errors.New("idempotency_key already used with a different payment request payload")
	ErrPaymentMethodNotEnabled       = errors.New("payment method is not enabled for this store")
	ErrUnsupportedPaymentMethod      = errors.New("unsupported payment method")
	ErrOrderNotPayable               = errors.New("order is not in a payable state")
	ErrSplitAlreadyPaid              = errors.New("payment split is already paid")
	ErrUnauthorizedCashierTender     = errors.New("direct cashier tender requires an active owner or cashier actor")
	ErrInsufficientCashTendered      = errors.New("cash_tendered_amount cannot be less than total payable amount")
	ErrReconciliationRefRequired     = errors.New("reconciliation_ref is required for static_qris and edc payments")
)

// GatewayClient abstracts Midtrans Core API operations for deterministic unit and integration testing.
type GatewayClient interface {
	ChargeCoreAPI(ctx context.Context, input midtrans.ChargeRequest) (*coreapi.ChargeResponse, error)
	CheckTransaction(ctx context.Context, providerOrderID string) (*coreapi.TransactionStatusResponse, error)
}

type PaymentServiceOption func(*PaymentService)

func WithPaymentClock(clock func() time.Time) PaymentServiceOption {
	return func(s *PaymentService) {
		if clock != nil {
			s.nowFunc = clock
		}
	}
}

func WithGatewayClient(client GatewayClient) PaymentServiceOption {
	return func(s *PaymentService) {
		s.midtransClient = normalizeGatewayClient(client)
	}
}

func normalizeGatewayClient(client GatewayClient) GatewayClient {
	if client == nil {
		return nil
	}
	v := reflect.ValueOf(client)
	if v.Kind() == reflect.Ptr && v.IsNil() {
		return nil
	}
	return client
}

type InitiatePaymentInput struct {
	OrderID            uuid.UUID
	SplitID            *uuid.UUID
	IdempotencyKey     string
	Method             domain.PaymentMethod
	Bank               string
	CashTenderedAmount int64
	ReconciliationRef  string
	CallbackURL        string
	ActorUserID        *uuid.UUID
}

type InitiatePaymentResult struct {
	Payment            *domain.Payment      `json:"payment"`
	Order              *domain.Order        `json:"order"`
	Split              *domain.PaymentSplit `json:"split"`
	IsIdempotentReplay bool                 `json:"is_idempotent_replay"`
}

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
	midtransClient GatewayClient
	nowFunc        func() time.Time
}

func NewPaymentService(
	db *sqlx.DB,
	paymentRepo *postgres.PaymentRepository,
	orderRepo *postgres.OrderRepository,
	midtransClient GatewayClient,
	opts ...PaymentServiceOption,
) *PaymentService {
	if paymentRepo == nil {
		paymentRepo = postgres.NewPaymentRepository()
	}
	if orderRepo == nil {
		orderRepo = postgres.NewOrderRepository()
	}
	svc := &PaymentService{
		db:             db,
		paymentRepo:    paymentRepo,
		orderRepo:      orderRepo,
		midtransClient: normalizeGatewayClient(midtransClient),
		nowFunc:        time.Now,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(svc)
		}
	}
	return svc
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

// InitiatePayment initiates or settles a payment attempt for a tenant-scoped order split.
//   - Direct cashier tender methods (cash, static_qris, edc) execute in a single ACID transaction,
//     verifying an active owner/cashier actor, validating tendered amount or reconciliation_ref,
//     recording an immutable audit_logs entry, and committing the order to KDS.
//   - Midtrans Core API methods (qris, gopay, shopeepay, bank_transfer) execute via a 2-phase
//     transaction so PostgreSQL row locks are never held during external HTTP calls, with
//     CheckTransaction fallback reconciliation on network timeouts or ambiguous retries.
func (s *PaymentService) InitiatePayment(
	ctx context.Context,
	trustedStoreID uuid.UUID,
	input InitiatePaymentInput,
) (*InitiatePaymentResult, error) {
	if s == nil || s.db == nil || s.paymentRepo == nil || s.orderRepo == nil {
		return nil, errors.New("payment service dependencies are not initialized")
	}
	if trustedStoreID == uuid.Nil {
		return nil, postgres.ErrEmptyTenantContext
	}
	if input.OrderID == uuid.Nil {
		return nil, errors.New("order_id is required")
	}
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if input.IdempotencyKey == "" || len(input.IdempotencyKey) > 100 {
		return nil, ErrPaymentIdempotencyKeyRequired
	}
	input.Bank = strings.ToLower(strings.TrimSpace(input.Bank))
	input.ReconciliationRef = strings.TrimSpace(input.ReconciliationRef)
	input.CallbackURL = strings.TrimSpace(input.CallbackURL)

	isDirectCashier := isDirectCashierMethod(input.Method)
	isMidtransGateway := isMidtransGatewayMethod(input.Method)
	if !isDirectCashier && !isMidtransGateway {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedPaymentMethod, input.Method)
	}

	if isDirectCashier {
		if input.ActorUserID == nil || *input.ActorUserID == uuid.Nil {
			return nil, ErrUnauthorizedCashierTender
		}
		if (input.Method == domain.PaymentMethodStaticQRIS || input.Method == domain.PaymentMethodEDC) && input.ReconciliationRef == "" {
			return nil, ErrReconciliationRefRequired
		}
		return s.initiateDirectCashierPayment(ctx, trustedStoreID, input)
	}

	if s.midtransClient == nil {
		return nil, errors.New("midtrans gateway client is not configured")
	}
	if input.Method == domain.PaymentMethodBankTransfer {
		switch input.Bank {
		case "bca", "bni", "bri", "mandiri", "permata":
		default:
			return nil, fmt.Errorf("%w: unsupported bank %q", ErrUnsupportedPaymentMethod, input.Bank)
		}
	}
	return s.initiateMidtransGatewayPayment(ctx, trustedStoreID, input)
}

func (s *PaymentService) initiateDirectCashierPayment(
	ctx context.Context,
	trustedStoreID uuid.UUID,
	input InitiatePaymentInput,
) (*InitiatePaymentResult, error) {
	requestHash := computePaymentInitiationHash(trustedStoreID, input)
	var result *InitiatePaymentResult

	err := database.WithTenantTx(ctx, s.db, trustedStoreID, func(tx *sqlx.Tx) error {
		store, err := s.orderRepo.GetStoreForOrder(ctx, tx, trustedStoreID)
		if err != nil {
			return err
		}
		if !store.IsActive || store.DeletedAt != nil {
			return ErrStoreInactive
		}
		if !isPaymentMethodEnabled(store.EnabledPaymentMethods, input.Method) {
			return fmt.Errorf("%w: %s", ErrPaymentMethodNotEnabled, input.Method)
		}

		existing, err := s.paymentRepo.GetPaymentByIdempotencyKeyForUpdate(ctx, tx, trustedStoreID, input.IdempotencyKey)
		if err != nil && !errors.Is(err, postgres.ErrPaymentNotFound) {
			return err
		}
		if existing != nil {
			if existing.RequestHash == "" || existing.RequestHash != requestHash {
				return ErrPaymentIdempotencyConflict
			}
			order, splits, err := s.orderRepo.GetOrderByIDForUpdate(ctx, tx, trustedStoreID, existing.OrderID)
			if err != nil {
				return err
			}
			split, err := resolveOrderSplit(existing.SplitID, splits)
			if err != nil {
				return err
			}
			result = &InitiatePaymentResult{
				Payment:            existing,
				Order:              order,
				Split:              split,
				IsIdempotentReplay: true,
			}
			return nil
		}

		actor, err := s.paymentRepo.GetActiveCashierOrOwnerActor(ctx, tx, trustedStoreID, *input.ActorUserID)
		if err != nil {
			return err
		}
		if actor == nil {
			return ErrUnauthorizedCashierTender
		}

		order, splits, err := s.orderRepo.GetOrderByIDForUpdate(ctx, tx, trustedStoreID, input.OrderID)
		if err != nil {
			return err
		}
		targetSplit, err := resolveOrderSplit(input.SplitID, splits)
		if err != nil {
			return err
		}
		split, err := s.paymentRepo.GetPaymentSplitForUpdate(ctx, tx, trustedStoreID, targetSplit.ID)
		if err != nil {
			return err
		}

		if order.PaymentStatus == domain.OrderPaymentStatusPaid || split.Status == domain.SplitBillStatusPaid {
			return ErrSplitAlreadyPaid
		}
		if order.Status == domain.OrderStatusCancelled {
			return ErrOrderNotPayable
		}

		var cashTendered, changeGiven int64
		if input.Method == domain.PaymentMethodCash {
			if input.CashTenderedAmount < split.TotalAmount {
				return fmt.Errorf("%w: tendered=%d required=%d", ErrInsufficientCashTendered, input.CashTenderedAmount, split.TotalAmount)
			}
			cashTendered = input.CashTenderedAmount
			changeGiven = input.CashTenderedAmount - split.TotalAmount
		}

		if err := s.paymentRepo.ExpirePendingPaymentsByOrder(ctx, tx, trustedStoreID, order.ID); err != nil {
			return err
		}
		attemptNo, err := s.paymentRepo.NextAttemptNumberForSplit(ctx, tx, trustedStoreID, split.ID)
		if err != nil {
			return err
		}

		now := s.nowFunc().UTC()
		paymentID := uuid.New()
		splitID := split.ID
		actorID := actor.ID
		payment := &domain.Payment{
			ID:                 paymentID,
			StoreID:            trustedStoreID,
			OrderID:            order.ID,
			SplitID:            &splitID,
			AttemptNo:          attemptNo,
			IdempotencyKey:     input.IdempotencyKey,
			RequestHash:        requestHash,
			Provider:           "manual",
			ProviderOrderID:    paymentID.String(),
			Amount:             split.TotalAmount,
			RefundedAmount:     0,
			Method:             input.Method,
			Status:             domain.PaymentStatusPaid,
			RawProviderStatus:  "cashier_settled",
			PaymentType:        string(input.Method),
			ReconciliationRef:  input.ReconciliationRef,
			VerifiedByUserID:   &actorID,
			CashTenderedAmount: cashTendered,
			ChangeGivenAmount:  changeGiven,
			PaidAt:             &now,
		}
		if err := s.paymentRepo.InsertPayment(ctx, tx, payment); err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, `
			UPDATE payment_splits
			SET status = 'paid', paid_amount = total_amount
			WHERE store_id = $1 AND id = $2
		`, trustedStoreID, split.ID); err != nil {
			return fmt.Errorf("failed to settle split for cashier payment: %w", err)
		}
		split.Status = domain.SplitBillStatusPaid
		split.PaidAmount = split.TotalAmount

		if err := s.commitPaidOrderTx(ctx, tx, order); err != nil {
			return err
		}

		auditAction, auditReason := directCashierAuditDetails(input.Method, order.OrderNumber, input.ReconciliationRef)
		metaBytes, _ := json.Marshal(map[string]any{
			"payment_id":           payment.ID.String(),
			"order_id":             order.ID.String(),
			"split_id":             split.ID.String(),
			"method":               payment.Method,
			"amount":               payment.Amount,
			"cash_tendered_amount": payment.CashTenderedAmount,
			"change_given_amount":  payment.ChangeGivenAmount,
			"reconciliation_ref":   payment.ReconciliationRef,
		})
		auditEntry := &domain.AuditLog{
			ID:          uuid.New(),
			StoreID:     trustedStoreID,
			ActorID:     &actorID,
			ActorName:   actor.FullName,
			Action:      auditAction,
			Reason:      auditReason,
			ReferenceID: order.OrderNumber,
			Metadata:    string(metaBytes),
		}
		if err := s.paymentRepo.InsertAuditLog(ctx, tx, auditEntry); err != nil {
			return err
		}

		result = &InitiatePaymentResult{
			Payment:            payment,
			Order:              order,
			Split:              split,
			IsIdempotentReplay: false,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *PaymentService) initiateMidtransGatewayPayment(
	ctx context.Context,
	trustedStoreID uuid.UUID,
	input InitiatePaymentInput,
) (*InitiatePaymentResult, error) {
	requestHash := computePaymentInitiationHash(trustedStoreID, input)

	var (
		phase1Payment                 *domain.Payment
		phase1Order                   *domain.Order
		phase1Split                   *domain.PaymentSplit
		immediateReplay               bool
		reconcileExistingBeforeCharge bool
	)

	// Phase 1: Short database transaction to check idempotency and persist/lock the pending attempt.
	err := database.WithTenantTx(ctx, s.db, trustedStoreID, func(tx *sqlx.Tx) error {
		store, err := s.orderRepo.GetStoreForOrder(ctx, tx, trustedStoreID)
		if err != nil {
			return err
		}
		if !store.IsActive || store.DeletedAt != nil {
			return ErrStoreInactive
		}
		if !isPaymentMethodEnabled(store.EnabledPaymentMethods, input.Method) {
			return fmt.Errorf("%w: %s", ErrPaymentMethodNotEnabled, input.Method)
		}

		existing, err := s.paymentRepo.GetPaymentByIdempotencyKeyForUpdate(ctx, tx, trustedStoreID, input.IdempotencyKey)
		if err != nil && !errors.Is(err, postgres.ErrPaymentNotFound) {
			return err
		}
		if existing != nil {
			if existing.RequestHash == "" || existing.RequestHash != requestHash {
				return ErrPaymentIdempotencyConflict
			}
			order, splits, err := s.orderRepo.GetOrderByIDForUpdate(ctx, tx, trustedStoreID, existing.OrderID)
			if err != nil {
				return err
			}
			split, err := resolveOrderSplit(existing.SplitID, splits)
			if err != nil {
				return err
			}
			phase1Payment = existing
			phase1Order = order
			phase1Split = split

			if existing.Status == domain.PaymentStatusPaid ||
				((existing.Status == domain.PaymentStatusPending || existing.Status == domain.PaymentStatusAuthorized) && hasGatewayChargeDetails(existing)) {
				immediateReplay = true
				return nil
			}
			if existing.Status == domain.PaymentStatusPending || existing.Status == domain.PaymentStatusAuthorized {
				reconcileExistingBeforeCharge = true
				return nil
			}
			return fmt.Errorf("%w: previous attempt with idempotency_key is in terminal status %s", ErrOrderNotPayable, existing.Status)
		}

		order, splits, err := s.orderRepo.GetOrderByIDForUpdate(ctx, tx, trustedStoreID, input.OrderID)
		if err != nil {
			return err
		}
		targetSplit, err := resolveOrderSplit(input.SplitID, splits)
		if err != nil {
			return err
		}
		split, err := s.paymentRepo.GetPaymentSplitForUpdate(ctx, tx, trustedStoreID, targetSplit.ID)
		if err != nil {
			return err
		}

		if order.PaymentStatus == domain.OrderPaymentStatusPaid || split.Status == domain.SplitBillStatusPaid {
			return ErrSplitAlreadyPaid
		}
		now := s.nowFunc().UTC()
		if order.Status == domain.OrderStatusCancelled || (order.ExpiresAt != nil && !now.Before(*order.ExpiresAt)) {
			return ErrOrderNotPayable
		}

		activeAttempt, err := s.paymentRepo.GetActivePaymentForSplitForUpdate(ctx, tx, trustedStoreID, split.ID)
		if err != nil && !errors.Is(err, postgres.ErrPaymentNotFound) {
			return err
		}
		if activeAttempt != nil {
			activeNotExpired := activeAttempt.ExpiresAt == nil || now.Before(*activeAttempt.ExpiresAt)
			if activeAttempt.Provider == "midtrans" && activeAttempt.Method == input.Method && activeAttempt.Bank == input.Bank && activeNotExpired {
				phase1Payment = activeAttempt
				phase1Order = order
				phase1Split = split
				if hasGatewayChargeDetails(activeAttempt) {
					immediateReplay = true
					return nil
				}
				reconcileExistingBeforeCharge = true
				return nil
			}
			// Different method/bank or expired attempt on the same split: cancel older active attempt.
			if err := s.paymentRepo.UpdatePaymentStatus(
				ctx, tx, activeAttempt, domain.PaymentStatusCancelled,
				"superseded_by_new_attempt", activeAttempt.FraudStatus, activeAttempt.TransactionID, activeAttempt.PaymentType,
			); err != nil {
				return err
			}
		}

		attemptNo, err := s.paymentRepo.NextAttemptNumberForSplit(ctx, tx, trustedStoreID, split.ID)
		if err != nil {
			return err
		}

		paymentID := uuid.New()
		splitID := split.ID
		payment := &domain.Payment{
			ID:              paymentID,
			StoreID:         trustedStoreID,
			OrderID:         order.ID,
			SplitID:         &splitID,
			AttemptNo:       attemptNo,
			IdempotencyKey:  input.IdempotencyKey,
			RequestHash:     requestHash,
			Provider:        "midtrans",
			ProviderOrderID: paymentID.String(),
			Amount:          split.TotalAmount,
			RefundedAmount:  0,
			Method:          input.Method,
			Bank:            input.Bank,
			Status:          domain.PaymentStatusPending,
		}
		if err := s.paymentRepo.InsertPayment(ctx, tx, payment); err != nil {
			return err
		}

		if split.Status == domain.SplitBillStatusUnpaid {
			if _, err := tx.ExecContext(ctx, `UPDATE payment_splits SET status = 'pending' WHERE store_id = $1 AND id = $2`, trustedStoreID, split.ID); err != nil {
				return err
			}
			split.Status = domain.SplitBillStatusPending
		}
		if order.PaymentStatus == domain.OrderPaymentStatusUnpaid || order.PaymentStatus == domain.OrderPaymentStatusFailed {
			order.PaymentStatus = domain.OrderPaymentStatusPending
			if err := s.orderRepo.UpdateOrderStatusAndStock(ctx, tx, order); err != nil {
				return err
			}
		}

		phase1Payment = payment
		phase1Order = order
		phase1Split = split
		return nil
	})
	if err != nil {
		return nil, err
	}
	if immediateReplay {
		return &InitiatePaymentResult{
			Payment:            phase1Payment,
			Order:              phase1Order,
			Split:              phase1Split,
			IsIdempotentReplay: true,
		}, nil
	}

	// Network Phase (Outside DB Lock):
	// 1. If recovering an existing pending attempt that had no gateway details saved,
	//    check Midtrans status first before issuing ChargeCoreAPI.
	var (
		chargeResp        *coreapi.ChargeResponse
		statusResp        *coreapi.TransactionStatusResponse
		chargeErr         error
		recoveredByStatus bool
	)

	if reconcileExistingBeforeCharge {
		statusResp, err = s.midtransClient.CheckTransaction(ctx, phase1Payment.ProviderOrderID)
		if err == nil && statusResp != nil && (statusResp.StatusCode == "200" || statusResp.StatusCode == "201") {
			recoveredByStatus = true
		}
	}

	if !recoveredByStatus {
		chargeResp, chargeErr = s.midtransClient.ChargeCoreAPI(ctx, midtrans.ChargeRequest{
			OrderID:        phase1Payment.ProviderOrderID,
			Amount:         phase1Payment.Amount,
			Method:         phase1Payment.Method,
			Bank:           phase1Payment.Bank,
			IdempotencyKey: phase1Payment.ID.String(),
			CallbackURL:    input.CallbackURL,
		})
		if chargeErr != nil {
			// Fallback CheckTransaction per specification: determine whether the charge actually
			// reached Midtrans before the network error/timeout occurred.
			fallbackStatus, checkErr := s.midtransClient.CheckTransaction(ctx, phase1Payment.ProviderOrderID)
			if checkErr == nil && fallbackStatus != nil && (fallbackStatus.StatusCode == "200" || fallbackStatus.StatusCode == "201") {
				statusResp = fallbackStatus
				recoveredByStatus = true
				chargeErr = nil
			} else if isPermanentGatewayRejection(chargeErr) {
				_ = database.WithTenantTx(ctx, s.db, trustedStoreID, func(tx *sqlx.Tx) error {
					lockedPayment, lockErr := s.paymentRepo.GetPaymentByIDForUpdate(ctx, tx, trustedStoreID, phase1Payment.ID)
					if lockErr != nil {
						return lockErr
					}
					if lockedPayment.Status == domain.PaymentStatusPending {
						return s.paymentRepo.MarkPaymentFailed(ctx, tx, lockedPayment, "MIDTRANS_CHARGE_REJECTED", chargeErr.Error())
					}
					return nil
				})
				return nil, chargeErr
			} else {
				// Transient network timeout and transaction is not yet visible at Midtrans:
				// keep the pending payment row intact so an idempotent retry reuses the same payment.ID.
				return nil, chargeErr
			}
		}
	}

	// Phase 2: Short database transaction to persist gateway details or reconciled status.
	var finalResult *InitiatePaymentResult
	err = database.WithTenantTx(ctx, s.db, trustedStoreID, func(tx *sqlx.Tx) error {
		order, _, err := s.orderRepo.GetOrderByIDForUpdate(ctx, tx, trustedStoreID, phase1Order.ID)
		if err != nil {
			return err
		}
		split, err := s.paymentRepo.GetPaymentSplitForUpdate(ctx, tx, trustedStoreID, phase1Split.ID)
		if err != nil {
			return err
		}
		payment, err := s.paymentRepo.GetPaymentByIDForUpdate(ctx, tx, trustedStoreID, phase1Payment.ID)
		if err != nil {
			return err
		}

		if recoveredByStatus && statusResp != nil {
			applyMidtransStatusResponse(payment, statusResp)
			if err := s.paymentRepo.UpdatePaymentGatewayDetails(ctx, tx, payment); err != nil {
				return err
			}
			mapped, mapErr := midtrans.MapMidtransStatus(statusResp.TransactionStatus, statusResp.FraudStatus)
			if mapErr == nil && domain.CanTransitionPaymentStatus(payment.Status, mapped) {
				if err := s.paymentRepo.UpdatePaymentStatus(
					ctx, tx, payment, mapped, statusResp.TransactionStatus,
					statusResp.FraudStatus, statusResp.TransactionID, statusResp.PaymentType,
				); err != nil {
					return err
				}
				if mapped == domain.PaymentStatusPaid && statusResp.StatusCode == "200" {
					if split.Status != domain.SplitBillStatusPaid {
						if _, err := tx.ExecContext(ctx, `UPDATE payment_splits SET status = 'paid', paid_amount = total_amount WHERE store_id = $1 AND id = $2`, trustedStoreID, split.ID); err != nil {
							return err
						}
						split.Status = domain.SplitBillStatusPaid
						split.PaidAmount = split.TotalAmount
					}
					if err := s.commitPaidOrderTx(ctx, tx, order); err != nil {
						return err
					}
				}
			}
		} else if chargeResp != nil {
			if chargeResp.StatusCode != "" && chargeResp.StatusCode != "200" && chargeResp.StatusCode != "201" {
				if err := s.paymentRepo.MarkPaymentFailed(ctx, tx, payment, chargeResp.StatusCode, chargeResp.StatusMessage); err != nil {
					return err
				}
				return fmt.Errorf("midtrans rejected charge with status_code %s: %s", chargeResp.StatusCode, chargeResp.StatusMessage)
			}
			applyMidtransChargeResponse(payment, chargeResp)
			if err := s.paymentRepo.UpdatePaymentGatewayDetails(ctx, tx, payment); err != nil {
				return err
			}
		}

		finalResult = &InitiatePaymentResult{
			Payment:            payment,
			Order:              order,
			Split:              split,
			IsIdempotentReplay: reconcileExistingBeforeCharge,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return finalResult, nil
}

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
			if err := s.paymentRepo.UpdatePaymentStatus(
				ctx, tx, payment, mappedStatus, statusResp.TransactionStatus,
				statusResp.FraudStatus, statusResp.TransactionID, statusResp.PaymentType,
			); err != nil {
				return err
			}
			if mappedStatus == domain.PaymentStatusPaid {
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
			if err := s.RunReconciliationCycle(ctx, olderThan, batchLimit); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("[RECONCILIATION WORKER] cycle error: %v", err)
			}
		}
	}
}

func isDirectCashierMethod(method domain.PaymentMethod) bool {
	switch method {
	case domain.PaymentMethodCash, domain.PaymentMethodStaticQRIS, domain.PaymentMethodEDC:
		return true
	default:
		return false
	}
}

func isMidtransGatewayMethod(method domain.PaymentMethod) bool {
	switch method {
	case domain.PaymentMethodQRIS, domain.PaymentMethodGoPay, domain.PaymentMethodShopeePay, domain.PaymentMethodBankTransfer:
		return true
	default:
		return false
	}
}

func isPaymentMethodEnabled(enabledJSON string, method domain.PaymentMethod) bool {
	trimmed := strings.TrimSpace(enabledJSON)
	if trimmed == "" || trimmed == "[]" {
		return false
	}
	var methods []string
	if err := json.Unmarshal([]byte(trimmed), &methods); err != nil {
		return false
	}
	for _, m := range methods {
		if strings.EqualFold(strings.TrimSpace(m), string(method)) {
			return true
		}
	}
	return false
}

func resolveOrderSplit(requestedSplitID *uuid.UUID, splits []domain.PaymentSplit) (*domain.PaymentSplit, error) {
	if len(splits) == 0 {
		return nil, errors.New("order has no payment splits")
	}
	if requestedSplitID == nil || *requestedSplitID == uuid.Nil {
		if len(splits) == 1 {
			cp := splits[0]
			return &cp, nil
		}
		return nil, errors.New("split_id is required when an order has multiple payment splits")
	}
	for _, sp := range splits {
		if sp.ID == *requestedSplitID {
			cp := sp
			return &cp, nil
		}
	}
	return nil, fmt.Errorf("payment split %s not found on order", *requestedSplitID)
}

func computePaymentInitiationHash(storeID uuid.UUID, input InitiatePaymentInput) string {
	var splitIDStr, actorIDStr string
	if input.SplitID != nil && *input.SplitID != uuid.Nil {
		splitIDStr = input.SplitID.String()
	}
	if input.ActorUserID != nil && *input.ActorUserID != uuid.Nil {
		actorIDStr = input.ActorUserID.String()
	}
	canonical, _ := json.Marshal(struct {
		StoreID            string               `json:"store_id"`
		OrderID            string               `json:"order_id"`
		SplitID            string               `json:"split_id"`
		Method             domain.PaymentMethod `json:"method"`
		Bank               string               `json:"bank"`
		CashTenderedAmount int64                `json:"cash_tendered_amount"`
		ReconciliationRef  string               `json:"reconciliation_ref"`
		ActorUserID        string               `json:"actor_user_id"`
	}{
		StoreID:            storeID.String(),
		OrderID:            input.OrderID.String(),
		SplitID:            splitIDStr,
		Method:             input.Method,
		Bank:               input.Bank,
		CashTenderedAmount: input.CashTenderedAmount,
		ReconciliationRef:  input.ReconciliationRef,
		ActorUserID:        actorIDStr,
	})
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func directCashierAuditDetails(method domain.PaymentMethod, orderNumber, reconciliationRef string) (string, string) {
	switch method {
	case domain.PaymentMethodCash:
		return "CASH_PAYMENT_RECORDED", fmt.Sprintf("Cash payment settled by cashier for order %s", orderNumber)
	case domain.PaymentMethodStaticQRIS:
		return "STATIC_QRIS_RECONCILE", fmt.Sprintf("Static QRIS manually reconciled for order %s (ref: %s)", orderNumber, reconciliationRef)
	case domain.PaymentMethodEDC:
		return "EDC_RECONCILE", fmt.Sprintf("EDC terminal payment reconciled for order %s (ref: %s)", orderNumber, reconciliationRef)
	default:
		return "STATIC_QRIS_RECONCILE", fmt.Sprintf("Direct cashier tender for order %s", orderNumber)
	}
}

func hasGatewayChargeDetails(p *domain.Payment) bool {
	if p == nil {
		return false
	}
	return p.TransactionID != "" || p.QRString != "" || p.VANumber != "" || p.DeeplinkURL != "" || p.BillKey != ""
}

func isPermanentGatewayRejection(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "status 400") ||
		strings.Contains(msg, "status 401") ||
		strings.Contains(msg, "status 403") ||
		strings.Contains(msg, "status 404") ||
		strings.Contains(msg, "status 406") ||
		strings.Contains(msg, "unsupported")
}

func applyMidtransChargeResponse(payment *domain.Payment, resp *coreapi.ChargeResponse) {
	if payment == nil || resp == nil {
		return
	}
	payment.TransactionID = strings.TrimSpace(resp.TransactionID)
	payment.PaymentType = strings.TrimSpace(resp.PaymentType)
	payment.Acquirer = strings.TrimSpace(resp.Acquirer)
	payment.QRString = strings.TrimSpace(resp.QRString)
	payment.RawProviderStatus = strings.TrimSpace(resp.TransactionStatus)
	payment.FraudStatus = strings.TrimSpace(resp.FraudStatus)
	payment.BillerCode = strings.TrimSpace(resp.BillerCode)
	payment.BillKey = strings.TrimSpace(resp.BillKey)
	payment.PaymentCode = strings.TrimSpace(resp.PaymentCode)

	if len(resp.VaNumbers) > 0 {
		payment.VANumber = strings.TrimSpace(resp.VaNumbers[0].VANumber)
		if payment.Bank == "" {
			payment.Bank = strings.ToLower(strings.TrimSpace(resp.VaNumbers[0].Bank))
		}
	} else if resp.PermataVaNumber != "" {
		payment.VANumber = strings.TrimSpace(resp.PermataVaNumber)
		if payment.Bank == "" {
			payment.Bank = "permata"
		}
	}

	for _, action := range resp.Actions {
		if action.URL == "" {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(action.Name))
		if name == "deeplink-redirect" || payment.DeeplinkURL == "" {
			payment.DeeplinkURL = strings.TrimSpace(action.URL)
		}
	}

	if parsedExpiry := parseMidtransExpiryTime(resp.ExpiryTime); parsedExpiry != nil {
		payment.ExpiresAt = parsedExpiry
	}
}

func applyMidtransStatusResponse(payment *domain.Payment, resp *coreapi.TransactionStatusResponse) {
	if payment == nil || resp == nil {
		return
	}
	if resp.TransactionID != "" {
		payment.TransactionID = strings.TrimSpace(resp.TransactionID)
	}
	if resp.PaymentType != "" {
		payment.PaymentType = strings.TrimSpace(resp.PaymentType)
	}
	if resp.Acquirer != "" {
		payment.Acquirer = strings.TrimSpace(resp.Acquirer)
	}
	if resp.TransactionStatus != "" {
		payment.RawProviderStatus = strings.TrimSpace(resp.TransactionStatus)
	}
	if resp.FraudStatus != "" {
		payment.FraudStatus = strings.TrimSpace(resp.FraudStatus)
	}
	if resp.BillerCode != "" {
		payment.BillerCode = strings.TrimSpace(resp.BillerCode)
	}
	if resp.BillKey != "" {
		payment.BillKey = strings.TrimSpace(resp.BillKey)
	}
	if resp.PaymentCode != "" {
		payment.PaymentCode = strings.TrimSpace(resp.PaymentCode)
	}
	if len(resp.VaNumbers) > 0 {
		payment.VANumber = strings.TrimSpace(resp.VaNumbers[0].VANumber)
		if payment.Bank == "" {
			payment.Bank = strings.ToLower(strings.TrimSpace(resp.VaNumbers[0].Bank))
		}
	} else if resp.PermataVaNumber != "" {
		payment.VANumber = strings.TrimSpace(resp.PermataVaNumber)
		if payment.Bank == "" {
			payment.Bank = "permata"
		}
	}
	if parsedExpiry := parseMidtransExpiryTime(resp.ExpiryTime); parsedExpiry != nil {
		payment.ExpiresAt = parsedExpiry
	}
}

func parseMidtransExpiryTime(raw string) *time.Time {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	if t, err := time.Parse(time.RFC3339, trimmed); err == nil {
		utc := t.UTC()
		return &utc
	}
	wib := time.FixedZone("WIB", 7*3600)
	if t, err := time.ParseInLocation("2006-01-02 15:04:05", trimmed, wib); err == nil {
		utc := t.UTC()
		return &utc
	}
	return nil
}
