package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

// Staff ID card badges (decision V14): the QR holds "<store_id>.<badge_id>.<secret>" and only replaces
// typing the store code and e-mail at login; the PIN is still checked with its usual limits.

type StaffBadge struct {
	UserID uuid.UUID `json:"user_id"`
	// Token is returned once, to print on the card as a QR code; only its SHA-256 is stored.
	Token string `json:"token"`
}

// IssueBadge creates a badge for a staff member and revokes the previous one (lost card, reprint).
func (s *AuthService) IssueBadge(ctx context.Context, actor *AuthenticatedStaff, userID uuid.UUID) (*StaffBadge, error) {
	if err := requireOwner(actor); err != nil {
		return nil, err
	}
	secret, secretHash, err := newRefreshSecret()
	if err != nil {
		return nil, err
	}
	storeID, badgeID, now := actor.Staff.Store.ID, uuid.New(), s.nowFunc().UTC()
	err = database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		if _, err := s.repo.GetUserByID(ctx, tx, storeID, userID); errors.Is(err, postgres.ErrUserNotFound) {
			return ErrStaffNotFound
		} else if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE staff_badges SET revoked_at = $3 WHERE store_id = $1 AND user_id = $2 AND revoked_at IS NULL`,
			storeID, userID, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO staff_badges (id, store_id, user_id, token_hash, created_by, created_at) VALUES ($1, $2, $3, $4, $5, $6)`,
			badgeID, storeID, userID, secretHash, actor.Staff.UserID, now); err != nil {
			return fmt.Errorf("failed to issue staff badge: %w", err)
		}
		return s.auditTx(ctx, tx, actor, "STAFF_BADGE_ISSUED", "Kartu ID QR staf diterbitkan (kartu lama dicabut)", userID, map[string]any{"badge_id": badgeID})
	})
	if err != nil {
		return nil, err
	}
	return &StaffBadge{UserID: userID, Token: formatRefreshToken(storeID, badgeID, secret)}, nil
}

// RevokeBadge disables the staff member's active badge.
func (s *AuthService) RevokeBadge(ctx context.Context, actor *AuthenticatedStaff, userID uuid.UUID) error {
	if err := requireOwner(actor); err != nil {
		return err
	}
	storeID := actor.Staff.Store.ID
	return database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE staff_badges SET revoked_at = $3 WHERE store_id = $1 AND user_id = $2 AND revoked_at IS NULL`,
			storeID, userID, s.nowFunc().UTC())
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrStaffNotFound
		}
		return s.auditTx(ctx, tx, actor, "STAFF_BADGE_REVOKED", "Kartu ID QR staf dicabut", userID, nil)
	})
}

// resolveBadge maps a scanned badge to its store and the staff e-mail; any problem looks like a wrong
// credential, and the login then runs the normal PIN check.
func (s *AuthService) resolveBadge(ctx context.Context, token string) (uuid.UUID, string, bool) {
	storeID, badgeID, secret, ok := parseRefreshToken(token)
	if !ok {
		return uuid.Nil, "", false
	}
	var row struct {
		TokenHash string `db:"token_hash"`
		Email     string `db:"email"`
		Active    bool   `db:"is_active"`
	}
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &row, `SELECT b.token_hash, u.email, s.is_active FROM staff_badges b
			JOIN users u ON u.store_id = b.store_id AND u.id = b.user_id JOIN stores s ON s.id = b.store_id
			WHERE b.store_id = $1 AND b.id = $2 AND b.revoked_at IS NULL`, storeID, badgeID)
	})
	if err != nil || !row.Active || !constantTimeEqual(hashRefreshSecret(secret), row.TokenHash) {
		return uuid.Nil, "", false
	}
	return storeID, row.Email, true
}
