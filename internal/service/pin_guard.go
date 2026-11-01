package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/auth"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

// shortcut: mirrors the STAFF_PIN_MAX_FAILURES / STAFF_PIN_LOCKOUT defaults (ADR-003 D3); wire the
// configured AuthPolicy here if those env values ever diverge from 5 / 15m.
const (
	posPINMaxFailures = 5
	posPINLockout     = 15 * time.Minute
)

var (
	ErrPINLocked           = errors.New("account is locked due to too many failed PIN attempts")
	ErrPINAttemptsExceeded = errors.New("too many failed owner PIN attempts by this staff member")
)

// pinFailure marks a wrong PIN inside a business transaction. The transaction rolls back, so
// pinGuard.withPINTx commits the failure counters in a separate transaction afterwards.
type pinFailure struct {
	err       error
	userIDs   []uuid.UUID
	actorID   uuid.UUID
	actorName string
	ownerPIN  bool
}

func (f *pinFailure) Error() string { return f.err.Error() }
func (f *pinFailure) Unwrap() error { return f.err }

// pinGuard verifies PINs re-entered for sensitive POS actions (void, refund, stock override, shift
// variance) with the same failed-attempt counter and lockout as staff login, plus a per-actor cap on
// owner-PIN guesses so a cashier cannot brute force the owner PIN.
type pinGuard struct {
	db      *sqlx.DB
	repo    *postgres.AuthRepository
	nowFunc func() time.Time
}

func newPINGuard(db *sqlx.DB, repo *postgres.AuthRepository, nowFunc func() time.Time) *pinGuard {
	return &pinGuard{db: db, repo: repo, nowFunc: nowFunc}
}

// withPINTx runs fn in a tenant transaction and persists PIN failures even though fn's work rolls back.
func (g *pinGuard) withPINTx(ctx context.Context, storeID uuid.UUID, fn func(tx *sqlx.Tx) error) error {
	err := database.WithTenantTx(ctx, g.db, storeID, fn)
	var failure *pinFailure
	if errors.As(err, &failure) {
		if recErr := database.WithTenantTx(ctx, g.db, storeID, func(tx *sqlx.Tx) error {
			return g.recordFailure(ctx, tx, storeID, failure)
		}); recErr != nil {
			return recErr
		}
	}
	return err
}

// verifyStaff checks the actor's own PIN.
func (g *pinGuard) verifyStaff(ctx context.Context, tx *sqlx.Tx, actor *AuthenticatedStaff, pin string) error {
	user, err := g.repo.GetUserByIDForUpdate(ctx, tx, actor.Staff.Store.ID, actor.Staff.UserID)
	if err != nil {
		return err
	}
	now := g.nowFunc().UTC()
	if !user.IsActive || user.PinHash == "" {
		return ErrInvalidStaffPIN
	}
	if user.LockedUntil != nil && now.Before(*user.LockedUntil) {
		auth.BurnPINVerification(pin)
		return ErrPINLocked
	}
	if ok, err := auth.VerifyPIN(pin, user.PinHash); err != nil || !ok {
		return &pinFailure{err: ErrInvalidStaffPIN, userIDs: []uuid.UUID{user.ID}, actorID: actor.Staff.UserID, actorName: actor.Staff.FullName}
	}
	return g.resetCounter(ctx, tx, user)
}

// verifyOwner returns the active, unlocked owner whose PIN matches.
func (g *pinGuard) verifyOwner(ctx context.Context, tx *sqlx.Tx, actor *AuthenticatedStaff, pin string) (uuid.UUID, error) {
	storeID := actor.Staff.Store.ID
	now := g.nowFunc().UTC()

	var recentFailures int
	if err := tx.GetContext(ctx, &recentFailures, `
		SELECT COUNT(*) FROM audit_logs
		WHERE store_id = $1 AND action = 'OWNER_PIN_FAILED' AND reference_id = $2 AND created_at > $3
	`, storeID, actor.Staff.UserID.String(), now.Add(-posPINLockout)); err != nil {
		return uuid.Nil, fmt.Errorf("failed to count owner PIN failures: %w", err)
	}
	if recentFailures >= posPINMaxFailures {
		auth.BurnPINVerification(pin)
		return uuid.Nil, ErrPINAttemptsExceeded
	}

	owners, err := g.repo.ListActiveOwners(ctx, tx, storeID)
	if err != nil {
		return uuid.Nil, err
	}
	var candidates []uuid.UUID
	for i := range owners {
		owner := &owners[i]
		if owner.PinHash == "" || (owner.LockedUntil != nil && now.Before(*owner.LockedUntil)) {
			continue
		}
		candidates = append(candidates, owner.ID)
		if ok, err := auth.VerifyPIN(pin, owner.PinHash); err == nil && ok {
			locked, err := g.repo.GetUserByIDForUpdate(ctx, tx, storeID, owner.ID)
			if err != nil {
				return uuid.Nil, err
			}
			return owner.ID, g.resetCounter(ctx, tx, locked)
		}
	}
	if len(candidates) == 0 {
		return uuid.Nil, ErrPINLocked
	}
	// Which owner was targeted is unknown, so every candidate's counter advances (single-owner MVP: the owner).
	return uuid.Nil, &pinFailure{err: ErrInvalidOwnerPIN, userIDs: candidates, actorID: actor.Staff.UserID, actorName: actor.Staff.FullName, ownerPIN: true}
}

func (g *pinGuard) resetCounter(ctx context.Context, tx *sqlx.Tx, user *domain.User) error {
	if user.FailedPINAttempts == 0 && user.LockedUntil == nil {
		return nil
	}
	user.FailedPINAttempts, user.LockedUntil = 0, nil
	return g.repo.UpdateUserLockout(ctx, tx, user)
}

func (g *pinGuard) recordFailure(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, f *pinFailure) error {
	now := g.nowFunc().UTC()
	actorID := f.actorID
	for _, userID := range f.userIDs {
		user, err := g.repo.GetUserByIDForUpdate(ctx, tx, storeID, userID)
		if err != nil {
			return err
		}
		user.FailedPINAttempts++
		if user.FailedPINAttempts < posPINMaxFailures {
			if err := g.repo.UpdateUserLockout(ctx, tx, user); err != nil {
				return err
			}
			continue
		}
		lockedUntil := now.Add(posPINLockout)
		user.FailedPINAttempts, user.LockedUntil = 0, &lockedUntil
		if err := g.repo.UpdateUserLockout(ctx, tx, user); err != nil {
			return err
		}
		meta, _ := json.Marshal(map[string]any{"locked_until": lockedUntil, "triggered_by": f.actorID, "source": "pos_authorization"})
		if err := postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{
			StoreID:     storeID,
			ActorID:     &actorID,
			ActorName:   f.actorName,
			Action:      "STAFF_LOGIN_LOCKED",
			Reason:      fmt.Sprintf("Akun dikunci otomatis setelah %d kali PIN salah pada otorisasi POS", posPINMaxFailures),
			ReferenceID: user.ID.String(),
			Metadata:    string(meta),
			CreatedAt:   now,
		}); err != nil {
			return err
		}
	}
	if !f.ownerPIN {
		return nil
	}
	return postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{
		StoreID:     storeID,
		ActorID:     &actorID,
		ActorName:   f.actorName,
		Action:      "OWNER_PIN_FAILED",
		Reason:      "PIN Owner salah pada otorisasi POS",
		ReferenceID: f.actorID.String(),
		Metadata:    "{}",
		CreatedAt:   now,
	})
}
