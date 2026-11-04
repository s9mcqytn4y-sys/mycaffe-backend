package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
)

var ErrPaymentNotFound = errors.New("payment not found")

type WebhookPaymentContext struct {
	StoreID        uuid.UUID                   `db:"store_id"`
	OrderID        uuid.UUID                   `db:"order_id"`
	PaymentID      uuid.UUID                   `db:"payment_id"`
	ExpectedAmount int64                       `db:"expected_amount"`
	CurrentStatus  domain.PaymentAttemptStatus `db:"current_status"`
}

type PaymentRepository struct{}

func NewPaymentRepository() *PaymentRepository {
	return &PaymentRepository{}
}

// ResolveWebhookContext calls the narrowly-scoped SECURITY DEFINER function after
// the HTTP layer has verified the Midtrans signature. It is the only cross-tenant lookup.
func (r *PaymentRepository) ResolveWebhookContext(ctx context.Context, db *sqlx.DB, providerOrderID string) (*WebhookPaymentContext, error) {
	if db == nil {
		return nil, errors.New("database connection pool is not initialized")
	}
	if providerOrderID == "" {
		return nil, errors.New("provider order ID is required")
	}

	var result WebhookPaymentContext
	query := `
		SELECT store_id, order_id, payment_id, expected_amount, current_status
		FROM resolve_payment_webhook_context($1)
	`
	if err := db.GetContext(ctx, &result, query, providerOrderID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrPaymentNotFound
		}
		return nil, fmt.Errorf("failed to resolve Midtrans webhook tenant context: %w", err)
	}
	return &result, nil
}

const paymentSelectColumns = `
	id, store_id, order_id, split_id, shift_id, attempt_no, idempotency_key, request_hash,
	provider, provider_order_id, amount, refunded_amount, rounding_amount, method, status,
	raw_provider_status, fraud_status, COALESCE(transaction_id, '') AS transaction_id,
	COALESCE(payment_type, '') AS payment_type, COALESCE(acquirer, '') AS acquirer,
	COALESCE(qr_string, '') AS qr_string, COALESCE(deeplink_url, '') AS deeplink_url,
	COALESCE(va_number, '') AS va_number, COALESCE(bank, '') AS bank,
	COALESCE(biller_code, '') AS biller_code, COALESCE(bill_key, '') AS bill_key,
	COALESCE(payment_code, '') AS payment_code, COALESCE(reconciliation_ref, '') AS reconciliation_ref,
	verified_by_user_id, cash_tendered_amount, change_given_amount,
	COALESCE(failure_code, '') AS failure_code, COALESCE(failure_reason, '') AS failure_reason,
	paid_at, expires_at, created_at, updated_at
`

func (r *PaymentRepository) GetPaymentByProviderOrderIDForUpdate(
	ctx context.Context,
	tx *sqlx.Tx,
	storeID uuid.UUID,
	providerOrderID string,
) (*domain.Payment, error) {
	if tx == nil {
		return nil, ErrNilTransaction
	}
	if storeID == uuid.Nil || providerOrderID == "" {
		return nil, ErrEmptyTenantContext
	}

	var payment domain.Payment
	query := `SELECT ` + paymentSelectColumns + `
		FROM payments
		WHERE store_id = $1 AND provider = 'midtrans' AND provider_order_id = $2
		FOR UPDATE
	`
	if err := tx.GetContext(ctx, &payment, query, storeID, providerOrderID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrPaymentNotFound
		}
		return nil, fmt.Errorf("failed to lock Midtrans payment attempt: %w", err)
	}
	return &payment, nil
}

func (r *PaymentRepository) GetPaymentByIdempotencyKeyForUpdate(
	ctx context.Context,
	tx *sqlx.Tx,
	storeID uuid.UUID,
	idempotencyKey string,
) (*domain.Payment, error) {
	if tx == nil {
		return nil, ErrNilTransaction
	}
	if storeID == uuid.Nil || idempotencyKey == "" {
		return nil, ErrEmptyTenantContext
	}

	var payment domain.Payment
	query := `SELECT ` + paymentSelectColumns + `
		FROM payments
		WHERE store_id = $1 AND idempotency_key = $2
		FOR UPDATE
	`
	if err := tx.GetContext(ctx, &payment, query, storeID, idempotencyKey); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrPaymentNotFound
		}
		return nil, fmt.Errorf("failed to lock payment by idempotency key: %w", err)
	}
	return &payment, nil
}

func (r *PaymentRepository) GetPaymentSplitForUpdate(
	ctx context.Context,
	tx *sqlx.Tx,
	storeID, splitID uuid.UUID,
) (*domain.PaymentSplit, error) {
	if tx == nil {
		return nil, ErrNilTransaction
	}
	if storeID == uuid.Nil || splitID == uuid.Nil {
		return nil, ErrEmptyTenantContext
	}

	var split domain.PaymentSplit
	query := `
		SELECT id, store_id, order_id, split_number, COALESCE(label, '') AS label,
			split_mode, subtotal_amount, tax_amount, service_charge_amount,
			total_amount, paid_amount, status, created_at, updated_at
		FROM payment_splits
		WHERE store_id = $1 AND id = $2
		FOR UPDATE
	`
	if err := tx.GetContext(ctx, &split, query, storeID, splitID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrderNotFound
		}
		return nil, fmt.Errorf("failed to lock payment split: %w", err)
	}
	return &split, nil
}

func (r *PaymentRepository) InsertPaymentNotificationLog(
	ctx context.Context,
	tx *sqlx.Tx,
	logEntry *domain.PaymentNotificationLog,
) (bool, error) {
	if tx == nil {
		return false, ErrNilTransaction
	}
	if logEntry == nil || logEntry.StoreID == uuid.Nil || logEntry.OrderID == nil || logEntry.EventHash == "" {
		return false, errors.New("payment notification log requires store, order, and event hash")
	}

	query := `
		INSERT INTO payment_notification_logs (
			id, store_id, order_id, provider, provider_order_id, event_hash,
			transaction_id, order_number, status_code, transaction_status,
			gross_amount, raw_payload, is_processed, processed_at, error_message
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb, false, NULL, $13
		)
		ON CONFLICT (store_id, event_hash) WHERE event_hash <> '' DO NOTHING
		RETURNING id
	`
	var insertedID uuid.UUID
	err := tx.QueryRowxContext(
		ctx, query,
		logEntry.ID, logEntry.StoreID, logEntry.OrderID, logEntry.Provider,
		logEntry.ProviderOrderID, logEntry.EventHash, logEntry.TransactionID,
		logEntry.OrderNumber, logEntry.StatusCode, logEntry.TransactionStatus,
		logEntry.GrossAmount, logEntry.RawPayload, logEntry.ErrorMessage,
	).Scan(&insertedID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("failed to persist Midtrans notification inbox event: %w", err)
	}
	logEntry.ID = insertedID
	return true, nil
}

func (r *PaymentRepository) MarkNotificationProcessed(ctx context.Context, tx *sqlx.Tx, storeID, logID uuid.UUID) error {
	if tx == nil {
		return ErrNilTransaction
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE payment_notification_logs
		SET is_processed = true, processed_at = NOW(), error_message = NULL
		WHERE store_id = $1 AND id = $2
	`, storeID, logID)
	if err != nil {
		return fmt.Errorf("failed to mark payment notification as processed: %w", err)
	}
	return nil
}

func (r *PaymentRepository) UpdatePaymentStatus(
	ctx context.Context,
	tx *sqlx.Tx,
	payment *domain.Payment,
	status domain.PaymentAttemptStatus,
	rawStatus, fraudStatus, transactionID, paymentType string,
) error {
	if tx == nil {
		return ErrNilTransaction
	}
	if payment == nil || payment.ID == uuid.Nil || payment.StoreID == uuid.Nil {
		return errors.New("payment identity is required")
	}

	query := `
		UPDATE payments
		SET status = $3::varchar,
			raw_provider_status = $4,
			fraud_status = $5,
			transaction_id = $6,
			payment_type = $7,
			paid_at = CASE WHEN $3::varchar = 'paid' THEN COALESCE(paid_at, NOW()) ELSE paid_at END
		WHERE store_id = $1 AND id = $2
		RETURNING updated_at, paid_at
	`
	if err := tx.QueryRowxContext(
		ctx, query, payment.StoreID, payment.ID, status, rawStatus, fraudStatus, transactionID, paymentType,
	).Scan(&payment.UpdatedAt, &payment.PaidAt); err != nil {
		return fmt.Errorf("failed to update payment attempt status: %w", err)
	}
	payment.Status = status
	payment.RawProviderStatus = rawStatus
	payment.FraudStatus = fraudStatus
	payment.TransactionID = transactionID
	payment.PaymentType = paymentType
	return nil
}

func (r *PaymentRepository) UpdatePaymentGatewayDetails(
	ctx context.Context,
	tx *sqlx.Tx,
	payment *domain.Payment,
) error {
	if tx == nil {
		return ErrNilTransaction
	}
	if payment == nil || payment.ID == uuid.Nil || payment.StoreID == uuid.Nil {
		return errors.New("payment identity is required")
	}
	query := `
		UPDATE payments
		SET transaction_id = $3, payment_type = $4, acquirer = $5,
			qr_string = $6, deeplink_url = $7, va_number = $8, bank = $9,
			biller_code = $10, bill_key = $11, payment_code = $12,
			expires_at = $13, raw_provider_status = $14, fraud_status = $15
		WHERE store_id = $1 AND id = $2
		RETURNING updated_at
	`
	return tx.QueryRowxContext(
		ctx, query, payment.StoreID, payment.ID, payment.TransactionID,
		payment.PaymentType, payment.Acquirer, payment.QRString, payment.DeeplinkURL,
		payment.VANumber, payment.Bank, payment.BillerCode, payment.BillKey,
		payment.PaymentCode, payment.ExpiresAt, payment.RawProviderStatus, payment.FraudStatus,
	).Scan(&payment.UpdatedAt)
}

func (r *PaymentRepository) InsertPayment(
	ctx context.Context,
	tx *sqlx.Tx,
	payment *domain.Payment,
) error {
	if tx == nil {
		return ErrNilTransaction
	}
	if payment == nil || payment.ID == uuid.Nil || payment.StoreID == uuid.Nil || payment.OrderID == uuid.Nil {
		return errors.New("payment identity, store_id, and order_id are required")
	}

	query := `
		INSERT INTO payments (
			id, store_id, order_id, split_id, shift_id, attempt_no, idempotency_key, request_hash,
			provider, provider_order_id, amount, refunded_amount, method, status,
			raw_provider_status, fraud_status, transaction_id, payment_type, acquirer,
			qr_string, deeplink_url, va_number, bank, biller_code, bill_key,
			payment_code, reconciliation_ref, verified_by_user_id,
			cash_tendered_amount, change_given_amount, failure_code, failure_reason,
			paid_at, expires_at, rounding_amount
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8,
			$9, $10, $11, $12, $13, $14,
			$15, $16, $17, $18, $19,
			$20, $21, $22, $23, $24, $25,
			$26, $27, $28,
			$29, $30, $31, $32,
			$33, $34, $35
		)
		RETURNING created_at, updated_at
	`
	return tx.QueryRowxContext(
		ctx, query,
		payment.ID, payment.StoreID, payment.OrderID, payment.SplitID, payment.ShiftID, payment.AttemptNo,
		payment.IdempotencyKey, payment.RequestHash, payment.Provider, payment.ProviderOrderID,
		payment.Amount, payment.RefundedAmount, payment.Method, payment.Status,
		payment.RawProviderStatus, payment.FraudStatus, payment.TransactionID,
		payment.PaymentType, payment.Acquirer, payment.QRString, payment.DeeplinkURL,
		payment.VANumber, payment.Bank, payment.BillerCode, payment.BillKey,
		payment.PaymentCode, payment.ReconciliationRef, payment.VerifiedByUserID,
		payment.CashTenderedAmount, payment.ChangeGivenAmount,
		payment.FailureCode, payment.FailureReason, payment.PaidAt, payment.ExpiresAt, payment.RoundingAmount,
	).Scan(&payment.CreatedAt, &payment.UpdatedAt)
}

func (r *PaymentRepository) GetActivePaymentForSplitForUpdate(
	ctx context.Context,
	tx *sqlx.Tx,
	storeID, splitID uuid.UUID,
) (*domain.Payment, error) {
	if tx == nil {
		return nil, ErrNilTransaction
	}
	if storeID == uuid.Nil || splitID == uuid.Nil {
		return nil, ErrEmptyTenantContext
	}

	var payment domain.Payment
	query := `SELECT ` + paymentSelectColumns + `
		FROM payments
		WHERE store_id = $1
		  AND split_id = $2
		  AND status IN ('pending', 'authorized')
		ORDER BY attempt_no DESC, created_at DESC
		LIMIT 1
		FOR UPDATE
	`
	if err := tx.GetContext(ctx, &payment, query, storeID, splitID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrPaymentNotFound
		}
		return nil, fmt.Errorf("failed to query active payment attempt for split: %w", err)
	}
	return &payment, nil
}

func (r *PaymentRepository) NextAttemptNumberForSplit(
	ctx context.Context,
	tx *sqlx.Tx,
	storeID, splitID uuid.UUID,
) (int, error) {
	if tx == nil {
		return 0, ErrNilTransaction
	}
	if storeID == uuid.Nil || splitID == uuid.Nil {
		return 0, ErrEmptyTenantContext
	}

	var nextAttempt int
	query := `
		SELECT COALESCE(MAX(attempt_no), 0) + 1
		FROM payments
		WHERE store_id = $1 AND split_id = $2
	`
	if err := tx.GetContext(ctx, &nextAttempt, query, storeID, splitID); err != nil {
		return 0, fmt.Errorf("failed to calculate next payment attempt number: %w", err)
	}
	return nextAttempt, nil
}

func (r *PaymentRepository) GetPaymentByIDForUpdate(
	ctx context.Context,
	tx *sqlx.Tx,
	storeID, paymentID uuid.UUID,
) (*domain.Payment, error) {
	if tx == nil {
		return nil, ErrNilTransaction
	}
	if storeID == uuid.Nil || paymentID == uuid.Nil {
		return nil, ErrEmptyTenantContext
	}

	var payment domain.Payment
	query := `SELECT ` + paymentSelectColumns + `
		FROM payments
		WHERE store_id = $1 AND id = $2
		FOR UPDATE
	`
	if err := tx.GetContext(ctx, &payment, query, storeID, paymentID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrPaymentNotFound
		}
		return nil, fmt.Errorf("failed to lock payment attempt by id: %w", err)
	}
	return &payment, nil
}

func (r *PaymentRepository) MarkPaymentFailed(
	ctx context.Context,
	tx *sqlx.Tx,
	payment *domain.Payment,
	failureCode, failureReason string,
) error {
	if tx == nil {
		return ErrNilTransaction
	}
	if payment == nil || payment.ID == uuid.Nil || payment.StoreID == uuid.Nil {
		return errors.New("payment identity is required")
	}

	query := `
		UPDATE payments
		SET status = 'failed',
			failure_code = $3,
			failure_reason = $4
		WHERE store_id = $1 AND id = $2
		RETURNING updated_at
	`
	if err := tx.QueryRowxContext(ctx, query, payment.StoreID, payment.ID, failureCode, failureReason).Scan(&payment.UpdatedAt); err != nil {
		return fmt.Errorf("failed to mark payment attempt failed: %w", err)
	}
	payment.Status = domain.PaymentStatusFailed
	payment.FailureCode = failureCode
	payment.FailureReason = failureReason
	return nil
}

func (r *PaymentRepository) GetActiveCashierOrOwnerActor(
	ctx context.Context,
	tx *sqlx.Tx,
	storeID, userID uuid.UUID,
) (*domain.User, error) {
	if tx == nil {
		return nil, ErrNilTransaction
	}
	if storeID == uuid.Nil || userID == uuid.Nil {
		return nil, nil
	}

	var user domain.User
	query := `
		SELECT id, store_id, email, full_name, role, is_active, created_at, updated_at
		FROM users
		WHERE store_id = $1
		  AND id = $2
		  AND is_active = true
		  AND role IN ('owner', 'cashier')
	`
	if err := tx.GetContext(ctx, &user, query, storeID, userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to load active cashier/owner actor: %w", err)
	}
	return &user, nil
}

func (r *PaymentRepository) InsertAuditLog(
	ctx context.Context,
	tx *sqlx.Tx,
	audit *domain.AuditLog,
) error {
	return InsertAuditLog(ctx, tx, audit)
}

// InsertAuditLog appends an audit_logs row inside the caller's tenant transaction.
func InsertAuditLog(ctx context.Context, tx *sqlx.Tx, audit *domain.AuditLog) error {
	if tx == nil {
		return ErrNilTransaction
	}
	if audit == nil || audit.StoreID == uuid.Nil || audit.Action == "" || audit.Reason == "" {
		return errors.New("audit log store_id, action, and reason are required")
	}
	if audit.ID == uuid.Nil {
		audit.ID = uuid.New()
	}
	if audit.Metadata == "" {
		audit.Metadata = "{}"
	}

	query := `
		INSERT INTO audit_logs (
			id, store_id, actor_id, actor_name, action, reason, reference_id, metadata
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8::jsonb
		)
		RETURNING created_at
	`
	return tx.QueryRowxContext(
		ctx, query,
		audit.ID, audit.StoreID, audit.ActorID, audit.ActorName,
		audit.Action, audit.Reason, audit.ReferenceID, audit.Metadata,
	).Scan(&audit.CreatedAt)
}

func (r *PaymentRepository) ExpirePendingPaymentsByOrder(
	ctx context.Context,
	tx *sqlx.Tx,
	storeID, orderID uuid.UUID,
) error {
	if tx == nil {
		return ErrNilTransaction
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE payments
		SET status = 'expired',
			raw_provider_status = CASE WHEN raw_provider_status = '' THEN 'expire' ELSE raw_provider_status END
		WHERE store_id = $1
		  AND order_id = $2
		  AND status IN ('pending', 'authorized')
	`, storeID, orderID)
	if err != nil {
		return fmt.Errorf("failed to expire pending payment attempts for order: %w", err)
	}
	return nil
}

func (r *PaymentRepository) ListStoresWithPendingReconciliation(
	ctx context.Context,
	db *sqlx.DB,
) ([]uuid.UUID, error) {
	if db == nil {
		return nil, errors.New("database connection pool is not initialized")
	}
	var storeIDs []uuid.UUID
	query := `SELECT store_id FROM list_stores_with_pending_reconciliation()`
	if err := db.SelectContext(ctx, &storeIDs, query); err != nil {
		return nil, fmt.Errorf("failed to list stores with pending reconciliation: %w", err)
	}
	return storeIDs, nil
}

func (r *PaymentRepository) ListOverdueUnpaidOrderIDs(
	ctx context.Context,
	tx *sqlx.Tx,
	storeID uuid.UUID,
	asOf time.Time,
	limit int,
) ([]uuid.UUID, error) {
	if tx == nil {
		return nil, ErrNilTransaction
	}
	if limit <= 0 {
		limit = 50
	}
	var orderIDs []uuid.UUID
	query := `
		SELECT id
		FROM orders
		WHERE store_id = $1
		  AND status = 'pending_payment'
		  AND payment_status IN ('unpaid', 'pending', 'failed')
		  AND expires_at IS NOT NULL
		  AND expires_at <= $2
		ORDER BY expires_at ASC, id ASC
		LIMIT $3
		FOR UPDATE SKIP LOCKED
	`
	if err := tx.SelectContext(ctx, &orderIDs, query, storeID, asOf, limit); err != nil {
		return nil, fmt.Errorf("failed to list overdue unpaid orders: %w", err)
	}
	return orderIDs, nil
}

func (r *PaymentRepository) ListStalePendingPayments(
	ctx context.Context,
	tx *sqlx.Tx,
	storeID uuid.UUID,
	createdBefore time.Time,
	limit int,
) ([]domain.Payment, error) {
	if tx == nil {
		return nil, ErrNilTransaction
	}
	if limit <= 0 {
		limit = 50
	}
	var payments []domain.Payment
	query := `SELECT ` + paymentSelectColumns + `
		FROM payments
		WHERE store_id = $1
		  AND provider = 'midtrans'
		  AND status IN ('pending', 'authorized')
		  AND created_at <= $2
		ORDER BY created_at ASC, id ASC
		LIMIT $3
	`
	if err := tx.SelectContext(ctx, &payments, query, storeID, createdBefore, limit); err != nil {
		return nil, fmt.Errorf("failed to list stale pending Midtrans payments: %w", err)
	}
	return payments, nil
}
