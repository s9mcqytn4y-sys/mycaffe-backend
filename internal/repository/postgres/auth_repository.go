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

var (
	ErrUserNotFound        = errors.New("user not found in tenant context")
	ErrAuthSessionNotFound = errors.New("auth session not found in tenant context")
)

type AuthRepository struct{}

func NewAuthRepository() *AuthRepository {
	return &AuthRepository{}
}

// ResolveStoreForLogin calls the narrowly-scoped SECURITY DEFINER resolver. It is the only
// lookup that runs before a tenant context exists and returns nothing but (store_id, is_active).
func (r *AuthRepository) ResolveStoreForLogin(ctx context.Context, db *sqlx.DB, slug string) (uuid.UUID, bool, error) {
	if db == nil {
		return uuid.Nil, false, errors.New("database connection pool is not initialized")
	}
	var row struct {
		StoreID  uuid.UUID `db:"store_id"`
		IsActive bool      `db:"is_active"`
	}
	if err := db.GetContext(ctx, &row, `SELECT store_id, is_active FROM resolve_store_for_login($1)`, slug); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return uuid.Nil, false, ErrStoreNotFound
		}
		return uuid.Nil, false, fmt.Errorf("failed to resolve store for login: %w", err)
	}
	return row.StoreID, row.IsActive, nil
}

const userAuthColumns = `id, store_id, email, full_name, role, pin_hash, is_active,
	created_at, updated_at, failed_pin_attempts, locked_until`

// GetUserByEmailForUpdate locks the user row so concurrent wrong-PIN attempts cannot
// overshoot the lockout threshold (ADR-003 D3). Email matching is case-insensitive.
func (r *AuthRepository) GetUserByEmailForUpdate(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, email string) (*domain.User, error) {
	if tx == nil {
		return nil, ErrNilTransaction
	}
	var user domain.User
	query := `SELECT ` + userAuthColumns + ` FROM users WHERE store_id = $1 AND lower(email) = lower($2) FOR UPDATE`
	if err := tx.GetContext(ctx, &user, query, storeID, email); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("failed to load user for login: %w", err)
	}
	return &user, nil
}

func (r *AuthRepository) GetUserByID(ctx context.Context, tx *sqlx.Tx, storeID, userID uuid.UUID) (*domain.User, error) {
	if tx == nil {
		return nil, ErrNilTransaction
	}
	var user domain.User
	query := `SELECT ` + userAuthColumns + ` FROM users WHERE store_id = $1 AND id = $2`
	if err := tx.GetContext(ctx, &user, query, storeID, userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("failed to load user: %w", err)
	}
	return &user, nil
}

// UpdateUserLockout persists failed_pin_attempts and locked_until for a locked user row.
func (r *AuthRepository) UpdateUserLockout(ctx context.Context, tx *sqlx.Tx, user *domain.User) error {
	if tx == nil {
		return ErrNilTransaction
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE users SET failed_pin_attempts = $3, locked_until = $4
		WHERE store_id = $1 AND id = $2
	`, user.StoreID, user.ID, user.FailedPINAttempts, user.LockedUntil)
	if err != nil {
		return fmt.Errorf("failed to update user lockout state: %w", err)
	}
	return nil
}

// UpdateUserPIN stores a new PIN hash and clears lockout state.
func (r *AuthRepository) UpdateUserPIN(ctx context.Context, tx *sqlx.Tx, storeID, userID uuid.UUID, pinHash string) error {
	if tx == nil {
		return ErrNilTransaction
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE users SET pin_hash = $3, failed_pin_attempts = 0, locked_until = NULL
		WHERE store_id = $1 AND id = $2
	`, storeID, userID, pinHash)
	if err != nil {
		return fmt.Errorf("failed to update user PIN: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrUserNotFound
	}
	return nil
}

func (r *AuthRepository) InsertAuthSession(ctx context.Context, tx *sqlx.Tx, s *domain.AuthSession) error {
	if tx == nil {
		return ErrNilTransaction
	}
	return tx.QueryRowxContext(ctx, `
		INSERT INTO auth_sessions (
			id, store_id, user_id, refresh_token_hash, created_at, last_used_at, expires_at, user_agent, client_ip
		) VALUES ($1, $2, $3, $4, $5, $5, $6, $7, $8)
		RETURNING created_at
	`, s.ID, s.StoreID, s.UserID, s.RefreshTokenHash, s.CreatedAt, s.ExpiresAt, s.UserAgent, s.ClientIP).Scan(&s.CreatedAt)
}

const authSessionColumns = `id, store_id, user_id, refresh_token_hash, previous_refresh_token_hash,
	created_at, last_used_at, expires_at, revoked_at, revoked_reason, user_agent, client_ip`

func (r *AuthRepository) GetAuthSessionForUpdate(ctx context.Context, tx *sqlx.Tx, storeID, sessionID uuid.UUID) (*domain.AuthSession, error) {
	return r.getAuthSession(ctx, tx, storeID, sessionID, " FOR UPDATE")
}

func (r *AuthRepository) GetAuthSession(ctx context.Context, tx *sqlx.Tx, storeID, sessionID uuid.UUID) (*domain.AuthSession, error) {
	return r.getAuthSession(ctx, tx, storeID, sessionID, "")
}

func (r *AuthRepository) getAuthSession(ctx context.Context, tx *sqlx.Tx, storeID, sessionID uuid.UUID, lock string) (*domain.AuthSession, error) {
	if tx == nil {
		return nil, ErrNilTransaction
	}
	var s domain.AuthSession
	query := `SELECT ` + authSessionColumns + ` FROM auth_sessions WHERE store_id = $1 AND id = $2` + lock
	if err := tx.GetContext(ctx, &s, query, storeID, sessionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrAuthSessionNotFound
		}
		return nil, fmt.Errorf("failed to load auth session: %w", err)
	}
	return &s, nil
}

// RotateAuthSession replaces the current refresh hash and keeps the old one for reuse detection.
func (r *AuthRepository) RotateAuthSession(ctx context.Context, tx *sqlx.Tx, s *domain.AuthSession, newHash string, now time.Time) error {
	if tx == nil {
		return ErrNilTransaction
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE auth_sessions
		SET previous_refresh_token_hash = refresh_token_hash, refresh_token_hash = $3, last_used_at = $4
		WHERE store_id = $1 AND id = $2 AND revoked_at IS NULL
	`, s.StoreID, s.ID, newHash, now)
	if err != nil {
		return fmt.Errorf("failed to rotate auth session: %w", err)
	}
	s.PreviousRefreshTokenHash, s.RefreshTokenHash, s.LastUsedAt = s.RefreshTokenHash, newHash, now
	return nil
}

func (r *AuthRepository) RevokeAuthSession(ctx context.Context, tx *sqlx.Tx, storeID, sessionID uuid.UUID, reason string, now time.Time) error {
	if tx == nil {
		return ErrNilTransaction
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE auth_sessions SET revoked_at = $3, revoked_reason = $4
		WHERE store_id = $1 AND id = $2 AND revoked_at IS NULL
	`, storeID, sessionID, now, reason)
	if err != nil {
		return fmt.Errorf("failed to revoke auth session: %w", err)
	}
	return nil
}

// RevokeAllUserSessions revokes every active session of a user (deactivation, PIN reset, role change).
func (r *AuthRepository) RevokeAllUserSessions(ctx context.Context, tx *sqlx.Tx, storeID, userID uuid.UUID, reason string, now time.Time) (int64, error) {
	if tx == nil {
		return 0, ErrNilTransaction
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE auth_sessions SET revoked_at = $3, revoked_reason = $4
		WHERE store_id = $1 AND user_id = $2 AND revoked_at IS NULL
	`, storeID, userID, now, reason)
	if err != nil {
		return 0, fmt.Errorf("failed to revoke user sessions: %w", err)
	}
	return res.RowsAffected()
}

// RevokeSessionsBeyondLimit keeps the newest `keep` active, unexpired sessions of a user and
// revokes the rest as session_limit (owner decision O3). Expired rows are revoked too so they
// never count toward the cap.
func (r *AuthRepository) RevokeSessionsBeyondLimit(ctx context.Context, tx *sqlx.Tx, storeID, userID uuid.UUID, keep int, now time.Time) error {
	if tx == nil {
		return ErrNilTransaction
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE auth_sessions SET revoked_at = $4, revoked_reason = 'session_limit'
		WHERE store_id = $1 AND user_id = $2 AND revoked_at IS NULL
		  AND id NOT IN (
			SELECT id FROM auth_sessions
			WHERE store_id = $1 AND user_id = $2 AND revoked_at IS NULL AND expires_at > $4
			ORDER BY created_at DESC, id DESC
			LIMIT $3
		  )
	`, storeID, userID, keep, now)
	if err != nil {
		return fmt.Errorf("failed to enforce session limit: %w", err)
	}
	return nil
}

// GetStoreProfile returns the public store fields shown in the staff profile (RLS-scoped).
func (r *AuthRepository) GetStoreProfile(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID) (string, string, error) {
	if tx == nil {
		return "", "", ErrNilTransaction
	}
	var row struct {
		Slug string `db:"slug"`
		Name string `db:"name"`
	}
	if err := tx.GetContext(ctx, &row, `SELECT slug, name FROM stores WHERE id = $1`, storeID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", ErrStoreNotFound
		}
		return "", "", fmt.Errorf("failed to load store profile: %w", err)
	}
	return row.Slug, row.Name, nil
}

func (r *AuthRepository) InsertAuditLog(ctx context.Context, tx *sqlx.Tx, audit *domain.AuditLog) error {
	return InsertAuditLog(ctx, tx, audit)
}
