package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
)

var ErrPaymentNotFound = errors.New("payment not found")

type WebhookPaymentContext struct {
	StoreID       uuid.UUID                 `db:"store_id"`
	OrderID       uuid.UUID                 `db:"order_id"`
	PaymentID     uuid.UUID                 `db:"payment_id"`
	ExpectedAmount int64                    `db:"expected_amount"`
	CurrentStatus domain.PaymentAttemptStatus `db:"current_status"`
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
	id, store_id, order_id, split_id, attempt_no, idempotency_key, request_hash,
	provider, provider_order_id, amount, refunded_amount, method, status,
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
		SET status = $3,
			raw_provider_status = $4,
			fraud_status = $5,
			transaction_id = $6,
			payment_type = $7,
			paid_at = CASE WHEN $3 = 'paid' THEN COALESCE(paid_at, NOW()) ELSE paid_at END
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
