package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/auth"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/mailer"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
)

// Staff account lifecycle (owner decision E2): invitation and PIN-reset links by email, plus an
// owner-issued temporary PIN for staff without email.

const (
	activationTTL = 48 * time.Hour
	pinResetTTL   = 30 * time.Minute
	// forgotPINCooldown throttles the public "lupa PIN" endpoint per user.
	forgotPINCooldown = 2 * time.Minute
	// pinSetupPath is the frontend route; the token travels in the URL fragment, which browsers
	// never send to servers, so it does not land in Nginx/Cloudflare logs.
	pinSetupPath = "/staff/pin#token="
)

var (
	ErrForbidden          = errors.New("action not allowed for this role")
	ErrInvalidAccountLink = errors.New("account link is invalid, used, or expired")
	ErrInvalidRole        = errors.New("role must be owner, cashier, barista, or kitchen")
	ErrWrongCurrentPIN    = errors.New("current PIN is incorrect")
	ErrStaffNotFound      = errors.New("staff member not found")
)

// WithAuthEmail enables email delivery for the account lifecycle. publicURL is the frontend origin.
func WithAuthEmail(email *EmailService, publicURL, replyTo string) AuthServiceOption {
	return func(s *AuthService) {
		s.email = email
		s.publicURL = strings.TrimRight(publicURL, "/")
		s.replyTo = replyTo
	}
}

var roleLabels = map[domain.UserRole]string{
	domain.RoleOwner: "Pemilik", domain.RoleCashier: "Kasir", domain.RoleBarista: "Barista", domain.RoleKitchen: "Dapur",
}

type StaffSummary struct {
	ID                uuid.UUID       `json:"id"`
	FullName          string          `json:"full_name"`
	Email             string          `json:"email"`
	Role              domain.UserRole `json:"role"`
	IsActive          bool            `json:"is_active"`
	Activated         bool            `json:"activated"`
	PINChangeRequired bool            `json:"pin_change_required"`
	LockedUntil       *time.Time      `json:"locked_until,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
}

type InviteStaffInput struct {
	FullName string
	Email    string
	Role     domain.UserRole
}

func requireOwner(actor *AuthenticatedStaff) error {
	if !actor.Can(rbac.StaffManage) {
		return ErrForbidden
	}
	return nil
}

// NormalizeStaffEmail applies the staff email rules: trimmed, lower-case, RFC 5322 address
// without display name, at most 255 characters.
func NormalizeStaffEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if !isPlausibleEmail(email) {
		return "", ErrInvalidEmail
	}
	return email, nil
}

func (s *AuthService) ListStaff(ctx context.Context, actor *AuthenticatedStaff) ([]StaffSummary, error) {
	if err := requireOwner(actor); err != nil {
		return nil, err
	}
	var out []StaffSummary
	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		users, err := s.repo.ListUsers(ctx, tx, actor.Staff.Store.ID)
		if err != nil {
			return err
		}
		now := s.nowFunc()
		out = make([]StaffSummary, 0, len(users))
		for _, u := range users {
			sum := StaffSummary{
				ID: u.ID, FullName: u.FullName, Email: u.Email, Role: u.Role, IsActive: u.IsActive,
				Activated: u.PINSetAt != nil, PINChangeRequired: u.MustChangePIN, CreatedAt: u.CreatedAt,
			}
			if u.LockedUntil != nil && now.Before(*u.LockedUntil) {
				sum.LockedUntil = u.LockedUntil
			}
			out = append(out, sum)
		}
		return nil
	})
	return out, err
}

// InviteStaff creates a staff member without a PIN and emails a 48-hour activation link.
func (s *AuthService) InviteStaff(ctx context.Context, actor *AuthenticatedStaff, in InviteStaffInput) (*StaffSummary, error) {
	if err := requireOwner(actor); err != nil {
		return nil, err
	}
	email, err := NormalizeStaffEmail(in.Email)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.FullName)
	if name == "" || len(name) > 255 {
		return nil, ErrInvalidName
	}
	if roleLabels[in.Role] == "" {
		return nil, ErrInvalidRole
	}
	storeID := actor.Staff.Store.ID
	now := s.nowFunc().UTC()
	user := &domain.User{ID: uuid.New(), StoreID: storeID, Email: email, FullName: name, Role: in.Role, IsActive: true}

	err = database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		if err := s.repo.InsertInvitedUser(ctx, tx, user); err != nil {
			return err
		}
		if err := s.issueAccountLinkTx(ctx, tx, user, domain.AccountTokenActivation, actor, now); err != nil {
			return err
		}
		return s.auditTx(ctx, tx, actor, "STAFF_INVITED", fmt.Sprintf("Mengundang %s sebagai %s", name, roleLabels[in.Role]), user.ID, map[string]any{"email": email, "role": in.Role})
	})
	if err != nil {
		return nil, err
	}
	return &StaffSummary{ID: user.ID, FullName: name, Email: email, Role: in.Role, IsActive: true, CreatedAt: user.CreatedAt}, nil
}

// SendPINResetLink lets an owner email a reset link to any staff member of the store.
func (s *AuthService) SendPINResetLink(ctx context.Context, actor *AuthenticatedStaff, userID uuid.UUID) error {
	if err := requireOwner(actor); err != nil {
		return err
	}
	storeID := actor.Staff.Store.ID
	now := s.nowFunc().UTC()
	return database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		user, err := s.repo.GetUserByID(ctx, tx, storeID, userID)
		if errors.Is(err, postgres.ErrUserNotFound) {
			return ErrStaffNotFound
		}
		if err != nil {
			return err
		}
		if !user.IsActive {
			return ErrStaffNotFound
		}
		purpose := domain.AccountTokenPINReset
		if user.PINSetAt == nil {
			purpose = domain.AccountTokenActivation // never activated: resend the invitation
		}
		if err := s.issueAccountLinkTx(ctx, tx, user, purpose, actor, now); err != nil {
			return err
		}
		return s.auditTx(ctx, tx, actor, "STAFF_PIN_RESET", "Tautan atur ulang PIN dikirim oleh owner", user.ID, map[string]any{"purpose": purpose})
	})
}

// RequestPINReset is the public "lupa PIN" flow. It never reveals whether the store or email
// exists: callers always answer 202.
func (s *AuthService) RequestPINReset(ctx context.Context, storeSlug, email string) error {
	email, err := NormalizeStaffEmail(email)
	if err != nil {
		return nil
	}
	storeID, active, err := s.repo.ResolveStoreForLogin(ctx, s.db, strings.ToLower(strings.TrimSpace(storeSlug)))
	if errors.Is(err, postgres.ErrStoreNotFound) || (err == nil && !active) {
		return nil
	}
	if err != nil {
		return err
	}
	now := s.nowFunc().UTC()
	return database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		user, err := s.repo.GetUserByEmailForUpdate(ctx, tx, storeID, email)
		if errors.Is(err, postgres.ErrUserNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if !user.IsActive || user.PINSetAt == nil {
			return nil
		}
		// Public endpoint: at most one reset email per user every 2 minutes, even across many IPs.
		recent, err := s.emailRepo.HasRecentToken(ctx, tx, storeID, user.ID, domain.AccountTokenPINReset, now.Add(-forgotPINCooldown))
		if err != nil || recent {
			return err
		}
		return s.issueAccountLinkTx(ctx, tx, user, domain.AccountTokenPINReset, nil, now)
	})
}

// IssueTemporaryPIN sets an owner-chosen PIN that must be changed at the next login (staff
// without email). All sessions of the staff member are revoked.
func (s *AuthService) IssueTemporaryPIN(ctx context.Context, actor *AuthenticatedStaff, userID uuid.UUID, pin string) error {
	if err := requireOwner(actor); err != nil {
		return err
	}
	if userID == actor.Staff.UserID {
		return ErrForbidden // owners change their own PIN with the current PIN, not a temporary one
	}
	if err := auth.ValidatePINPolicy(pin); err != nil {
		return err
	}
	pinHash, err := s.hashPIN(pin)
	if err != nil {
		return err
	}
	storeID := actor.Staff.Store.ID
	now := s.nowFunc().UTC()
	return database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		user, err := s.repo.GetUserByID(ctx, tx, storeID, userID)
		if errors.Is(err, postgres.ErrUserNotFound) {
			return ErrStaffNotFound
		}
		if err != nil {
			return err
		}
		if err := s.repo.SetUserPIN(ctx, tx, storeID, user.ID, pinHash, true, now); err != nil {
			return err
		}
		if err := s.emailRepo.ConsumeOpenTokens(ctx, tx, storeID, user.ID, now); err != nil {
			return err
		}
		if _, err := s.repo.RevokeAllUserSessions(ctx, tx, storeID, user.ID, "pin_reset", now); err != nil {
			return err
		}
		return s.auditTx(ctx, tx, actor, "STAFF_TEMP_PIN_ISSUED", "PIN sementara diberikan owner; wajib diganti saat login", user.ID, nil)
	})
}

type PINSetupResult struct {
	StoreSlug string `json:"store_slug"`
	Email     string `json:"email"`
}

// SetPINWithToken consumes an activation or reset link. The token format matches refresh tokens:
// "<store_id>.<token_id>.<secret>" so the lookup runs under the right tenant RLS context.
func (s *AuthService) SetPINWithToken(ctx context.Context, token, pin string) (*PINSetupResult, error) {
	storeID, tokenID, secret, ok := parseRefreshToken(token)
	if !ok {
		return nil, ErrInvalidAccountLink
	}
	if err := auth.ValidatePINPolicy(pin); err != nil {
		return nil, err
	}
	pinHash, err := s.hashPIN(pin)
	if err != nil {
		return nil, err
	}
	now := s.nowFunc().UTC()
	var result *PINSetupResult
	linkErr := ErrInvalidAccountLink
	err = database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		t, err := s.emailRepo.GetAccountTokenForUpdate(ctx, tx, storeID, tokenID)
		if errors.Is(err, postgres.ErrAccountTokenNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if t.UsedAt != nil || !now.Before(t.ExpiresAt) || !constantTimeEqual(hashRefreshSecret(secret), t.TokenHash) {
			return nil
		}
		user, err := s.repo.GetUserByID(ctx, tx, storeID, t.UserID)
		if err != nil {
			return err
		}
		if !user.IsActive {
			return nil
		}
		if err := s.repo.SetUserPIN(ctx, tx, storeID, user.ID, pinHash, false, now); err != nil {
			return err
		}
		if err := s.emailRepo.ConsumeOpenTokens(ctx, tx, storeID, user.ID, now); err != nil {
			return err
		}
		if _, err := s.repo.RevokeAllUserSessions(ctx, tx, storeID, user.ID, "pin_reset", now); err != nil {
			return err
		}
		self := &AuthenticatedStaff{Staff: StaffIdentity{UserID: user.ID, FullName: user.FullName, Store: StaffStore{ID: storeID}}}
		reason := "PIN dibuat melalui tautan aktivasi"
		if t.Purpose == domain.AccountTokenPINReset {
			reason = "PIN diatur ulang melalui tautan reset"
			if err := s.enqueuePINChangedTx(ctx, tx, user, now); err != nil {
				return err
			}
		}
		if err := s.auditTx(ctx, tx, self, "STAFF_PIN_SET", reason, user.ID, map[string]any{"purpose": t.Purpose}); err != nil {
			return err
		}
		slug, _, err := s.repo.GetStoreProfile(ctx, tx, storeID)
		if err != nil {
			return err
		}
		result, linkErr = &PINSetupResult{StoreSlug: slug, Email: user.Email}, nil
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, linkErr
}

// ChangePIN lets a signed-in staff member replace their PIN (required after a temporary PIN).
// Other sessions are revoked; the current one stays signed in.
func (s *AuthService) ChangePIN(ctx context.Context, actor *AuthenticatedStaff, currentPIN, newPIN string) error {
	if actor == nil {
		return ErrInvalidSession
	}
	if err := auth.ValidatePINPolicy(newPIN); err != nil {
		return err
	}
	if currentPIN == newPIN {
		return auth.ErrPINTooWeak
	}
	select {
	case s.hashSem <- struct{}{}:
		defer func() { <-s.hashSem }()
	default:
		return ErrAuthBusy
	}
	storeID := actor.Staff.Store.ID
	now := s.nowFunc().UTC()
	changeErr := ErrWrongCurrentPIN
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		user, err := s.repo.GetUserByIDForUpdate(ctx, tx, storeID, actor.Staff.UserID)
		if err != nil {
			return err
		}
		if ok, _ := auth.VerifyPIN(currentPIN, user.PinHash); !ok {
			return s.recordFailedPIN(ctx, tx, user, now, "")
		}
		newHash, err := auth.HashPIN(newPIN)
		if err != nil {
			return err
		}
		if err := s.repo.SetUserPIN(ctx, tx, storeID, user.ID, newHash, false, now); err != nil {
			return err
		}
		if err := s.repo.RevokeOtherUserSessions(ctx, tx, storeID, user.ID, actor.SessionID, "pin_reset", now); err != nil {
			return err
		}
		if err := s.enqueuePINChangedTx(ctx, tx, user, now); err != nil {
			return err
		}
		changeErr = nil
		return s.auditTx(ctx, tx, actor, "STAFF_PIN_SET", "PIN diganti oleh pemilik akun", user.ID, nil)
	})
	if err != nil {
		return err
	}
	return changeErr
}

func (s *AuthService) issueAccountLinkTx(ctx context.Context, tx *sqlx.Tx, user *domain.User, purpose string, actor *AuthenticatedStaff, now time.Time) error {
	if err := s.emailRepo.ConsumeOpenTokens(ctx, tx, user.StoreID, user.ID, now); err != nil {
		return err
	}
	secret, secretHash, err := newRefreshSecret()
	if err != nil {
		return err
	}
	ttl, template, label, expiresIn := pinResetTTL, mailer.TemplatePINReset, "Buat PIN baru", "30 menit"
	if purpose == domain.AccountTokenActivation {
		ttl, template, label, expiresIn = activationTTL, mailer.TemplateStaffInvite, "Aktifkan akun & buat PIN", "48 jam"
	}
	t := &domain.AccountToken{
		ID: uuid.New(), StoreID: user.StoreID, UserID: user.ID, Purpose: purpose,
		TokenHash: secretHash, ExpiresAt: now.Add(ttl), CreatedAt: now,
	}
	data := mailer.Data{
		RecipientEmail: user.Email, RoleLabel: roleLabels[user.Role], ActionLabel: label,
		ExpiresIn: expiresIn, ExpiresAt: formatLocalTime(t.ExpiresAt), ReplyTo: s.replyTo,
		ActionURL: s.publicURL + pinSetupPath + formatRefreshToken(user.StoreID, t.ID, secret),
	}
	if actor != nil {
		creator := actor.Staff.UserID
		t.CreatedBy = &creator
		data.ActorName = actor.Staff.FullName
	}
	if err := s.emailRepo.InsertAccountToken(ctx, tx, t); err != nil {
		return err
	}
	if s.email == nil {
		return errors.New("email delivery is not configured")
	}
	return s.email.EnqueueTx(ctx, tx, user.StoreID, template, user.Email, user.FullName, t.ID.String(), data)
}

func (s *AuthService) enqueuePINChangedTx(ctx context.Context, tx *sqlx.Tx, user *domain.User, now time.Time) error {
	if s.email == nil {
		return nil
	}
	return s.email.EnqueueTx(ctx, tx, user.StoreID, mailer.TemplatePINChanged, user.Email, user.FullName, user.ID.String(),
		mailer.Data{EventAt: formatLocalTime(now), ReplyTo: s.replyTo})
}

// enqueueLockedNoticeTx emails every active owner when a staff account locks (owner decision N1).
func (s *AuthService) enqueueLockedNoticeTx(ctx context.Context, tx *sqlx.Tx, user *domain.User, lockedUntil, now time.Time) error {
	if s.email == nil {
		return nil
	}
	owners, err := s.repo.ListActiveOwners(ctx, tx, user.StoreID)
	if err != nil {
		return err
	}
	for _, owner := range owners {
		if err := s.email.EnqueueTx(ctx, tx, user.StoreID, mailer.TemplateAccountLocked, owner.Email, owner.FullName, user.ID.String(), mailer.Data{
			StaffName: user.FullName, RoleLabel: roleLabels[user.Role], ExpiresAt: formatLocalTime(lockedUntil),
			EventAt: formatLocalTime(now), ReplyTo: s.replyTo,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *AuthService) auditTx(ctx context.Context, tx *sqlx.Tx, actor *AuthenticatedStaff, action, reason string, target uuid.UUID, meta map[string]any) error {
	metaJSON := []byte("{}")
	if meta != nil {
		metaJSON, _ = json.Marshal(meta)
	}
	entry := &domain.AuditLog{
		StoreID: actor.Staff.Store.ID, ActorName: actor.Staff.FullName, Action: action, Reason: reason,
		ReferenceID: target.String(), Metadata: string(metaJSON),
	}
	if actor.Staff.UserID != uuid.Nil {
		id := actor.Staff.UserID
		entry.ActorID = &id
	}
	return s.repo.InsertAuditLog(ctx, tx, entry)
}

// hashPIN bounds concurrent argon2id work with the same semaphore as login.
func (s *AuthService) hashPIN(pin string) (string, error) {
	select {
	case s.hashSem <- struct{}{}:
		defer func() { <-s.hashSem }()
	default:
		return "", ErrAuthBusy
	}
	return auth.HashPIN(pin)
}
