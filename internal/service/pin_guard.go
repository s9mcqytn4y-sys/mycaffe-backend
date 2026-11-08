package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/auth"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/mailer"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
)

// ownerPINStoreFactor sets the shop-wide owner-PIN failure cap as a multiple of the per-actor cap
// (HF5: 3 × STAFF_PIN_MAX_FAILURES per STAFF_PIN_LOCKOUT window).
const ownerPINStoreFactor = 3

var (
	ErrPINLocked           = errors.New("account is locked due to too many failed PIN attempts")
	ErrPINAttemptsExceeded = errors.New("too many failed owner PIN attempts by this staff member")
	ErrOwnerPINStoreLimit  = errors.New("too many failed owner PIN attempts in this store")
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
// variance) with the login failure counter for the actor's own PIN. Wrong owner PINs typed by other
// staff never touch the owner's counter (HF5); they are capped per actor and per store instead, and
// the owners are emailed when the store cap is reached.
type pinGuard struct {
	db          *sqlx.DB
	repo        *postgres.AuthRepository
	nowFunc     func() time.Time
	maxFailures int
	lockout     time.Duration
	email       *EmailService
}

// newPINGuard starts with the STAFF_PIN_MAX_FAILURES / STAFF_PIN_LOCKOUT defaults; main wires the
// configured policy through SetPINPolicy.
func newPINGuard(db *sqlx.DB, repo *postgres.AuthRepository, nowFunc func() time.Time) *pinGuard {
	return &pinGuard{db: db, repo: repo, nowFunc: nowFunc, maxFailures: 5, lockout: 15 * time.Minute}
}

// setPolicy applies the configured login lockout policy; zero values keep the defaults.
func (g *pinGuard) setPolicy(policy AuthPolicy, email *EmailService) {
	if policy.PINMaxFailures > 0 {
		g.maxFailures = policy.PINMaxFailures
	}
	if policy.PINLockout > 0 {
		g.lockout = policy.PINLockout
	}
	g.email = email
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

	actorFailures, storeFailures, err := g.countOwnerPINFailures(ctx, tx, storeID, actor.Staff.UserID, now)
	if err != nil {
		return uuid.Nil, err
	}
	if storeFailures >= g.storeLimit() {
		auth.BurnPINVerification(pin)
		return uuid.Nil, ErrOwnerPINStoreLimit
	}
	if actorFailures >= g.maxFailures {
		auth.BurnPINVerification(pin)
		return uuid.Nil, ErrPINAttemptsExceeded
	}

	owners, err := g.repo.ListActiveOwners(ctx, tx, storeID)
	if err != nil {
		return uuid.Nil, err
	}
	candidates, actorIsCandidate := 0, false
	for i := range owners {
		owner := &owners[i]
		if owner.PinHash == "" || (owner.LockedUntil != nil && now.Before(*owner.LockedUntil)) {
			continue
		}
		candidates++
		actorIsCandidate = actorIsCandidate || owner.ID == actor.Staff.UserID
		if ok, err := auth.VerifyPIN(pin, owner.PinHash); err == nil && ok {
			locked, err := g.repo.GetUserByIDForUpdate(ctx, tx, storeID, owner.ID)
			if err != nil {
				return uuid.Nil, err
			}
			return owner.ID, g.resetCounter(ctx, tx, locked)
		}
	}
	if candidates == 0 {
		return uuid.Nil, ErrPINLocked
	}
	// HF5: only an owner typing a wrong PIN advances a login counter (their own); other staff are
	// held back by the per-actor and per-store caps.
	var userIDs []uuid.UUID
	if actorIsCandidate {
		userIDs = []uuid.UUID{actor.Staff.UserID}
	}
	return uuid.Nil, &pinFailure{err: ErrInvalidOwnerPIN, userIDs: userIDs, actorID: actor.Staff.UserID, actorName: actor.Staff.FullName, ownerPIN: true}
}

func (g *pinGuard) storeLimit() int { return ownerPINStoreFactor * g.maxFailures }

// countOwnerPINFailures counts OWNER_PIN_FAILED audits in the lockout window for one actor and the store.
func (g *pinGuard) countOwnerPINFailures(ctx context.Context, tx *sqlx.Tx, storeID, actorID uuid.UUID, now time.Time) (actor, store int, err error) {
	var counts struct {
		Actor int `db:"actor"`
		Store int `db:"store"`
	}
	if err := tx.GetContext(ctx, &counts, `
		SELECT COUNT(*) FILTER (WHERE reference_id = $2) AS actor, COUNT(*) AS store FROM audit_logs
		WHERE store_id = $1 AND action = 'OWNER_PIN_FAILED' AND created_at > $3
	`, storeID, actorID.String(), now.Add(-g.lockout)); err != nil {
		return 0, 0, fmt.Errorf("failed to count owner PIN failures: %w", err)
	}
	return counts.Actor, counts.Store, nil
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
		if user.FailedPINAttempts < g.maxFailures {
			if err := g.repo.UpdateUserLockout(ctx, tx, user); err != nil {
				return err
			}
			continue
		}
		lockedUntil := now.Add(g.lockout)
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
			Reason:      fmt.Sprintf("Akun dikunci otomatis setelah %d kali PIN salah pada otorisasi POS", g.maxFailures),
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
	if err := postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{
		StoreID:     storeID,
		ActorID:     &actorID,
		ActorName:   f.actorName,
		Action:      "OWNER_PIN_FAILED",
		Reason:      "PIN Owner salah pada otorisasi POS",
		ReferenceID: f.actorID.String(),
		Metadata:    "{}",
		CreatedAt:   now,
	}); err != nil {
		return err
	}
	_, storeFailures, err := g.countOwnerPINFailures(ctx, tx, storeID, f.actorID, now)
	if err != nil || storeFailures != g.storeLimit() {
		return err
	}
	return g.notifyOwnersTx(ctx, tx, storeID, f.actorName, now)
}

// notifyOwnersTx emails every active owner once when the store-wide owner-PIN cap is reached.
func (g *pinGuard) notifyOwnersTx(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, actorName string, now time.Time) error {
	if g.email == nil {
		return nil
	}
	owners, err := g.repo.ListActiveOwners(ctx, tx, storeID)
	if err != nil {
		return err
	}
	for _, owner := range owners {
		if err := g.email.EnqueueTx(ctx, tx, storeID, mailer.TemplateOwnerPINAttempts, owner.Email, owner.FullName, storeID.String(), mailer.Data{
			StaffName: actorName, ExpiresAt: formatLocalTime(now.Add(g.lockout)), EventAt: formatLocalTime(now),
		}); err != nil {
			return err
		}
	}
	return nil
}
