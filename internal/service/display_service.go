package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
)

// Queue TV displays (v0.9 PR 9.3, decision W7). A display token "<store_id>.<token_id>.<secret>" is
// shown once to the owner; only the SHA-256 of the secret is stored. The store id inside the token
// selects the tenant, so no SECURITY DEFINER lookup is needed. A display sees queue numbers and
// order status only: never names, phone numbers, items or amounts.

var (
	ErrDisplayTokenInvalid  = errors.New("display token invalid or revoked")
	ErrDisplayTokenNotFound = errors.New("display token not found")
	ErrDisplayTokenLimit    = errors.New("too many active display tokens")
)

const maxActiveDisplayTokens = 10

type DisplayToken struct {
	ID         uuid.UUID  `json:"id" db:"id"`
	Name       string     `json:"name" db:"name"`
	CreatedAt  time.Time  `json:"created_at" db:"created_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty" db:"revoked_at"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty" db:"last_seen_at"`
	// Token is returned once, when the display is created.
	Token string `json:"token,omitempty" db:"-"`
}

// QueueEntry is everything a queue display may show about an order.
type QueueEntry struct {
	QueueNumber string    `json:"queue_number" db:"queue_number"`
	Status      string    `json:"status" db:"status"`
	UpdatedAt   time.Time `json:"updated_at" db:"updated_at"`
}

type QueueView struct {
	StoreName string       `json:"store_name"`
	Orders    []QueueEntry `json:"orders"`
}

type DisplayService struct {
	db      *sqlx.DB
	nowFunc func() time.Time
}

func NewDisplayService(db *sqlx.DB) *DisplayService {
	return &DisplayService{db: db, nowFunc: time.Now}
}

func requireStoreSettings(actor *AuthenticatedStaff) error {
	if actor == nil {
		return ErrInvalidSession
	}
	if !actor.Can(rbac.StoreSettingsManage) {
		return ErrForbidden
	}
	return nil
}

// CreateToken registers a new display; at most 10 active displays per store.
func (s *DisplayService) CreateToken(ctx context.Context, actor *AuthenticatedStaff, name string) (*DisplayToken, error) {
	if err := requireStoreSettings(actor); err != nil {
		return nil, err
	}
	f := fieldErrors{}
	name = f.text("name", name, 1, 50)
	if err := f.err(); err != nil {
		return nil, err
	}
	secret, secretHash, err := newRefreshSecret()
	if err != nil {
		return nil, err
	}
	storeID := actor.Staff.Store.ID
	out := &DisplayToken{ID: uuid.New(), Name: name, CreatedAt: s.nowFunc().UTC()}
	err = database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		var active int
		if err := tx.GetContext(ctx, &active, `SELECT COUNT(*) FROM display_tokens WHERE store_id = $1 AND revoked_at IS NULL`, storeID); err != nil {
			return err
		}
		if active >= maxActiveDisplayTokens {
			return ErrDisplayTokenLimit
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO display_tokens (id, store_id, name, token_hash, created_by, created_at)
			VALUES ($1, $2, $3, $4, $5, $6)`, out.ID, storeID, name, secretHash, actor.Staff.UserID, out.CreatedAt); err != nil {
			return fmt.Errorf("failed to create display token: %w", err)
		}
		return s.audit(ctx, tx, actor, "DISPLAY_TOKEN_CREATED", "Layar antrean TV didaftarkan", out.ID, map[string]any{"name": name})
	})
	if err != nil {
		return nil, err
	}
	out.Token = formatRefreshToken(storeID, out.ID, secret)
	return out, nil
}

func (s *DisplayService) ListTokens(ctx context.Context, actor *AuthenticatedStaff) ([]DisplayToken, error) {
	if err := requireStoreSettings(actor); err != nil {
		return nil, err
	}
	out := []DisplayToken{}
	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `SELECT id, name, created_at, revoked_at, last_seen_at FROM display_tokens
			WHERE store_id = $1 ORDER BY revoked_at IS NOT NULL, created_at DESC`, actor.Staff.Store.ID)
	})
	return out, err
}

// RevokeToken disables a display at once; revoking twice is a no-op.
func (s *DisplayService) RevokeToken(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID) error {
	if err := requireStoreSettings(actor); err != nil {
		return err
	}
	storeID := actor.Staff.Store.ID
	return database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		var revoked *time.Time
		err := tx.GetContext(ctx, &revoked, `SELECT revoked_at FROM display_tokens WHERE store_id = $1 AND id = $2 FOR UPDATE`, storeID, id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrDisplayTokenNotFound
		}
		if err != nil || revoked != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE display_tokens SET revoked_at = $3 WHERE store_id = $1 AND id = $2`, storeID, id, s.nowFunc().UTC()); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "DISPLAY_TOKEN_REVOKED", "Layar antrean TV dicabut", id, nil)
	})
}

// Authenticate checks a display token and returns its store and token id.
func (s *DisplayService) Authenticate(ctx context.Context, token string) (uuid.UUID, uuid.UUID, error) {
	storeID, tokenID, secret, ok := parseRefreshToken(token)
	if !ok {
		return uuid.Nil, uuid.Nil, ErrDisplayTokenInvalid
	}
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		return s.authTx(ctx, tx, storeID, tokenID, secret)
	})
	return storeID, tokenID, err
}

// Queue returns today's (store time zone) orders a display may show: queued, preparing and ready.
func (s *DisplayService) Queue(ctx context.Context, token string) (*QueueView, error) {
	storeID, tokenID, secret, ok := parseRefreshToken(token)
	if !ok {
		return nil, ErrDisplayTokenInvalid
	}
	view := &QueueView{Orders: []QueueEntry{}}
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		if err := s.authTx(ctx, tx, storeID, tokenID, secret); err != nil {
			return err
		}
		if err := tx.GetContext(ctx, &view.StoreName, `SELECT name FROM stores WHERE id = $1`, storeID); err != nil {
			return err
		}
		return tx.SelectContext(ctx, &view.Orders, `
			SELECT o.queue_number, o.status, o.updated_at
			FROM orders o JOIN stores st ON st.id = o.store_id
			WHERE o.store_id = $1 AND o.status IN ('queued', 'preparing', 'ready')
			  AND COALESCE(o.queue_number, '') <> ''
			  AND (o.created_at AT TIME ZONE st.timezone)::date = ($2::timestamptz AT TIME ZONE st.timezone)::date
			ORDER BY o.created_at
			LIMIT 200`, storeID, s.nowFunc().UTC())
	})
	if err != nil {
		return nil, err
	}
	return view, nil
}

// authTx accepts an active token of an active store and records when the display was last seen
// (at most once a minute, so polling does not write on every request).
func (s *DisplayService) authTx(ctx context.Context, tx *sqlx.Tx, storeID, tokenID uuid.UUID, secret string) error {
	var row struct {
		TokenHash string `db:"token_hash"`
		Active    bool   `db:"is_active"`
	}
	err := tx.GetContext(ctx, &row, `SELECT d.token_hash, s.is_active FROM display_tokens d JOIN stores s ON s.id = d.store_id
		WHERE d.store_id = $1 AND d.id = $2 AND d.revoked_at IS NULL`, storeID, tokenID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDisplayTokenInvalid
	}
	if err != nil {
		return err
	}
	if !row.Active || !constantTimeEqual(hashRefreshSecret(secret), row.TokenHash) {
		return ErrDisplayTokenInvalid
	}
	now := s.nowFunc().UTC()
	_, err = tx.ExecContext(ctx, `UPDATE display_tokens SET last_seen_at = $3
		WHERE store_id = $1 AND id = $2 AND (last_seen_at IS NULL OR last_seen_at < $3::timestamptz - INTERVAL '1 minute')`, storeID, tokenID, now)
	return err
}

func (s *DisplayService) audit(ctx context.Context, tx *sqlx.Tx, actor *AuthenticatedStaff, action, reason string, ref uuid.UUID, meta map[string]any) error {
	payload, _ := json.Marshal(meta)
	actorID := actor.Staff.UserID
	return postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{
		StoreID: actor.Staff.Store.ID, ActorID: &actorID, ActorName: actor.Staff.FullName, Action: action,
		Reason: reason, ReferenceID: ref.String(), Metadata: string(payload), CreatedAt: s.nowFunc().UTC(),
	})
}
