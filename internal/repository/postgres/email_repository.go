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

var ErrAccountTokenNotFound = errors.New("account token not found in tenant context")

// scrubbedBody replaces delivered/failed bodies: activation and reset emails carry link secrets.
const scrubbedBody = "[dihapus setelah pengiriman]"

type EmailRepository struct{}

func NewEmailRepository() *EmailRepository {
	return &EmailRepository{}
}

func (r *EmailRepository) EnqueueEmail(ctx context.Context, tx *sqlx.Tx, e *domain.EmailOutbox) error {
	if tx == nil {
		return ErrNilTransaction
	}
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO email_outbox (id, store_id, template, to_email, to_name, subject, html_body, text_body, reply_to, reference_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, e.ID, e.StoreID, e.Template, e.ToEmail, e.ToName, e.Subject, e.HTMLBody, e.TextBody, e.ReplyTo, e.ReferenceID)
	if err != nil {
		return fmt.Errorf("failed to enqueue email: %w", err)
	}
	return nil
}

// ListStoresWithDueEmail calls the narrow SECURITY DEFINER discovery function.
func (r *EmailRepository) ListStoresWithDueEmail(ctx context.Context, db *sqlx.DB) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	if err := db.SelectContext(ctx, &ids, `SELECT store_id FROM list_stores_with_due_email()`); err != nil {
		return nil, fmt.Errorf("failed to list stores with due email: %w", err)
	}
	return ids, nil
}

// ClaimDueEmails locks deliverable rows (SKIP LOCKED, so parallel workers never double-send),
// marks them sending with a lease, and increments attempts. Expired leases are reclaimed.
func (r *EmailRepository) ClaimDueEmails(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, now time.Time, lease time.Duration, limit int) ([]domain.EmailOutbox, error) {
	if tx == nil {
		return nil, ErrNilTransaction
	}
	var rows []domain.EmailOutbox
	err := tx.SelectContext(ctx, &rows, `
		UPDATE email_outbox e
		SET status = 'sending', attempts = e.attempts + 1, locked_until = $3
		WHERE e.id IN (
			SELECT id FROM email_outbox
			WHERE store_id = $1
			  AND ((status = 'pending' AND next_attempt_at <= $2)
			    OR (status = 'sending' AND locked_until IS NOT NULL AND locked_until <= $2))
			ORDER BY next_attempt_at
			LIMIT $4
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, store_id, template, to_email, to_name, subject, html_body, text_body, reply_to,
		          reference_id, status, attempts, next_attempt_at, last_error, created_at
	`, storeID, now, now.Add(lease), limit)
	if err != nil {
		return nil, fmt.Errorf("failed to claim due emails: %w", err)
	}
	return rows, nil
}

func (r *EmailRepository) MarkEmailSent(ctx context.Context, tx *sqlx.Tx, storeID, id uuid.UUID, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE email_outbox
		SET status = 'sent', sent_at = $3, locked_until = NULL, last_error = '', html_body = $4, text_body = $4
		WHERE store_id = $1 AND id = $2
	`, storeID, id, now, scrubbedBody)
	return err
}

func (r *EmailRepository) MarkEmailRetry(ctx context.Context, tx *sqlx.Tx, storeID, id uuid.UUID, next time.Time, lastErr string) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE email_outbox
		SET status = 'pending', next_attempt_at = $3, locked_until = NULL, last_error = $4
		WHERE store_id = $1 AND id = $2
	`, storeID, id, next, truncateString(lastErr, 500))
	return err
}

func (r *EmailRepository) MarkEmailFailed(ctx context.Context, tx *sqlx.Tx, storeID, id uuid.UUID, lastErr string) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE email_outbox
		SET status = 'failed', locked_until = NULL, last_error = $3, html_body = $4, text_body = $4
		WHERE store_id = $1 AND id = $2
	`, storeID, id, truncateString(lastErr, 500), scrubbedBody)
	return err
}

func (r *EmailRepository) InsertAccountToken(ctx context.Context, tx *sqlx.Tx, t *domain.AccountToken) error {
	if tx == nil {
		return ErrNilTransaction
	}
	return tx.QueryRowxContext(ctx, `
		INSERT INTO account_tokens (id, store_id, user_id, purpose, token_hash, expires_at, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING created_at
	`, t.ID, t.StoreID, t.UserID, t.Purpose, t.TokenHash, t.ExpiresAt, t.CreatedBy, t.CreatedAt).Scan(&t.CreatedAt)
}

func (r *EmailRepository) GetAccountTokenForUpdate(ctx context.Context, tx *sqlx.Tx, storeID, id uuid.UUID) (*domain.AccountToken, error) {
	if tx == nil {
		return nil, ErrNilTransaction
	}
	var t domain.AccountToken
	err := tx.GetContext(ctx, &t, `
		SELECT id, store_id, user_id, purpose, token_hash, expires_at, used_at, created_by, created_at
		FROM account_tokens WHERE store_id = $1 AND id = $2 FOR UPDATE
	`, storeID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrAccountTokenNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load account token: %w", err)
	}
	return &t, nil
}

// ConsumeOpenTokens marks every unused token of a user as used (a new link or a completed
// PIN setup invalidates older links).
func (r *EmailRepository) ConsumeOpenTokens(ctx context.Context, tx *sqlx.Tx, storeID, userID uuid.UUID, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE account_tokens SET used_at = $3
		WHERE store_id = $1 AND user_id = $2 AND used_at IS NULL
	`, storeID, userID, now)
	if err != nil {
		return fmt.Errorf("failed to consume account tokens: %w", err)
	}
	return nil
}

func truncateString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// HasRecentToken reports whether a token of this purpose was issued to the user since `since`.
func (r *EmailRepository) HasRecentToken(ctx context.Context, tx *sqlx.Tx, storeID, userID uuid.UUID, purpose string, since time.Time) (bool, error) {
	var exists bool
	err := tx.GetContext(ctx, &exists, `
		SELECT EXISTS (SELECT 1 FROM account_tokens WHERE store_id = $1 AND user_id = $2 AND purpose = $3 AND created_at > $4)
	`, storeID, userID, purpose, since)
	if err != nil {
		return false, fmt.Errorf("failed to check recent account tokens: %w", err)
	}
	return exists, nil
}
