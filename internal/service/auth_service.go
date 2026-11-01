package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/auth"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

var (
	// ErrInvalidCredentials is the single generic login failure (unknown store, unknown or
	// inactive user, wrong PIN, locked account) so responses never reveal which part failed.
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrAuthBusy           = errors.New("too many concurrent login attempts")
	ErrInvalidSession     = errors.New("session is invalid or expired")
	ErrSessionStale       = errors.New("session no longer matches the current user state")
	// ErrRefreshRace means the previous refresh generation arrived right after a rotation,
	// e.g. two POS tabs refreshing at once. The client retries with the cookie it now holds.
	ErrRefreshRace = errors.New("refresh token was rotated moments ago; retry")
)

// refreshRaceGrace is how long after a rotation the previous generation is answered with
// ErrRefreshRace instead of being treated as theft. It never yields tokens.
const refreshRaceGrace = 30 * time.Second

// AuthPolicy carries the ADR-003 D3/D4 and owner decision O3 limits.
type AuthPolicy struct {
	AccessTokenTTL     time.Duration
	RefreshTokenTTL    time.Duration
	SessionIdleTimeout time.Duration
	MaxSessions        int
	PINMaxFailures     int
	PINLockout         time.Duration
	// MaxConcurrentHashes bounds parallel argon2id work (19 MiB each) on the small VPS.
	MaxConcurrentHashes int
}

type AuthService struct {
	db      *sqlx.DB
	repo    *postgres.AuthRepository
	secret  string
	policy  AuthPolicy
	nowFunc func() time.Time
	hashSem chan struct{}

	// Account lifecycle email (WithAuthEmail); nil disables invitation/reset links.
	email        *EmailService
	emailRepo    *postgres.EmailRepository
	settingsRepo *postgres.SettingsRepository
	publicURL    string
	replyTo      string
}

type AuthServiceOption func(*AuthService)

func WithAuthClock(clock func() time.Time) AuthServiceOption {
	return func(s *AuthService) {
		if clock != nil {
			s.nowFunc = clock
		}
	}
}

func NewAuthService(db *sqlx.DB, repo *postgres.AuthRepository, jwtSecret string, policy AuthPolicy, opts ...AuthServiceOption) *AuthService {
	if policy.MaxConcurrentHashes <= 0 {
		policy.MaxConcurrentHashes = 4
	}
	s := &AuthService{
		db:           db,
		repo:         repo,
		secret:       jwtSecret,
		policy:       policy,
		nowFunc:      time.Now,
		hashSem:      make(chan struct{}, policy.MaxConcurrentHashes),
		emailRepo:    postgres.NewEmailRepository(),
		settingsRepo: postgres.NewSettingsRepository(),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

type LoginInput struct {
	StoreSlug string
	Email     string
	PIN       string
	UserAgent string
	ClientIP  string
}

type StaffStore struct {
	ID   uuid.UUID `json:"id"`
	Slug string    `json:"slug"`
	Name string    `json:"name"`
}

// StaffIdentity is the OpenAPI StaffProfile (docs/api/openapi.yaml).
type StaffIdentity struct {
	UserID   uuid.UUID       `json:"id"`
	FullName string          `json:"full_name"`
	Role     domain.UserRole `json:"role"`
	Store    StaffStore      `json:"store"`
}

// AuthenticatedStaff is the result of validating an access token against the database.
type AuthenticatedStaff struct {
	Staff             StaffIdentity
	SessionID         uuid.UUID
	SessionExpiresAt  time.Time
	PINChangeRequired bool
	// Permissions is the effective RBAC set (role + store options such as cashier_handles_bar).
	Permissions []rbac.Permission
}

type SessionTokens struct {
	AccessToken      string
	AccessExpiresAt  time.Time
	RefreshToken     string
	RefreshExpiresAt time.Time
	SessionID        uuid.UUID
	Staff            StaffIdentity
	// PINChangeRequired is true after an owner-issued temporary PIN (owner decision E2).
	PINChangeRequired bool
}

// Login verifies store_slug + email + PIN (ADR-003 D1) and opens a persisted session.
func (s *AuthService) Login(ctx context.Context, in LoginInput) (*SessionTokens, error) {
	slug := strings.ToLower(strings.TrimSpace(in.StoreSlug))
	email := strings.TrimSpace(in.Email)

	select {
	case s.hashSem <- struct{}{}:
		defer func() { <-s.hashSem }()
	default:
		return nil, ErrAuthBusy
	}

	if slug == "" || len(slug) > 100 || !isPlausibleEmail(email) || len(in.PIN) != auth.PINLength {
		auth.BurnPINVerification(in.PIN)
		return nil, ErrInvalidCredentials
	}

	storeID, active, err := s.repo.ResolveStoreForLogin(ctx, s.db, slug)
	if errors.Is(err, postgres.ErrStoreNotFound) || (err == nil && !active) {
		auth.BurnPINVerification(in.PIN)
		return nil, ErrInvalidCredentials
	}
	if err != nil {
		return nil, err
	}

	now := s.nowFunc().UTC()
	var tokens *SessionTokens
	loginErr := ErrInvalidCredentials
	err = database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		user, err := s.repo.GetUserByEmailForUpdate(ctx, tx, storeID, email)
		if errors.Is(err, postgres.ErrUserNotFound) {
			auth.BurnPINVerification(in.PIN)
			return nil
		}
		if err != nil {
			return err
		}
		// An invited user without a PIN is treated like an unknown user: no counter, same 401.
		if !user.IsActive || user.PINSetAt == nil || (user.LockedUntil != nil && now.Before(*user.LockedUntil)) {
			auth.BurnPINVerification(in.PIN)
			return nil
		}

		ok, verifyErr := auth.VerifyPIN(in.PIN, user.PinHash)
		if verifyErr != nil || !ok {
			// Failures are committed (tx returns nil) so the counter survives the 401.
			return s.recordFailedPIN(ctx, tx, user, now, in.ClientIP)
		}

		if user.FailedPINAttempts != 0 || user.LockedUntil != nil {
			user.FailedPINAttempts, user.LockedUntil = 0, nil
			if err := s.repo.UpdateUserLockout(ctx, tx, user); err != nil {
				return err
			}
		}
		tokens, err = s.openSession(ctx, tx, user, now, in.UserAgent, in.ClientIP)
		if err != nil {
			return err
		}
		tokens.PINChangeRequired = user.MustChangePIN
		loginErr = nil
		return nil
	})
	if err != nil {
		return nil, err
	}
	if loginErr != nil {
		return nil, loginErr
	}
	return tokens, nil
}

func (s *AuthService) recordFailedPIN(ctx context.Context, tx *sqlx.Tx, user *domain.User, now time.Time, clientIP string) error {
	user.FailedPINAttempts++
	if user.FailedPINAttempts < s.policy.PINMaxFailures {
		return s.repo.UpdateUserLockout(ctx, tx, user)
	}
	lockedUntil := now.Add(s.policy.PINLockout)
	user.FailedPINAttempts, user.LockedUntil = 0, &lockedUntil
	if err := s.repo.UpdateUserLockout(ctx, tx, user); err != nil {
		return err
	}
	if err := s.enqueueLockedNoticeTx(ctx, tx, user, lockedUntil, now); err != nil {
		return err
	}
	meta, _ := json.Marshal(map[string]any{"locked_until": lockedUntil, "client_ip": clientIP})
	actorID := user.ID
	return s.repo.InsertAuditLog(ctx, tx, &domain.AuditLog{
		StoreID:     user.StoreID,
		ActorID:     &actorID,
		ActorName:   user.FullName,
		Action:      "STAFF_LOGIN_LOCKED",
		Reason:      fmt.Sprintf("Akun dikunci otomatis setelah %d kali PIN salah", s.policy.PINMaxFailures),
		ReferenceID: user.ID.String(),
		Metadata:    string(meta),
	})
}

func (s *AuthService) openSession(ctx context.Context, tx *sqlx.Tx, user *domain.User, now time.Time, userAgent, clientIP string) (*SessionTokens, error) {
	secret, secretHash, err := newRefreshSecret()
	if err != nil {
		return nil, err
	}
	session := &domain.AuthSession{
		ID:               uuid.New(),
		StoreID:          user.StoreID,
		UserID:           user.ID,
		RefreshTokenHash: secretHash,
		CreatedAt:        now,
		ExpiresAt:        now.Add(s.policy.RefreshTokenTTL),
		UserAgent:        truncate(userAgent, 255),
		ClientIP:         truncate(clientIP, 64),
	}
	if err := s.repo.InsertAuthSession(ctx, tx, session); err != nil {
		return nil, err
	}
	if err := s.repo.RevokeSessionsBeyondLimit(ctx, tx, user.StoreID, user.ID, s.policy.MaxSessions, now); err != nil {
		return nil, err
	}
	return s.issueTokens(ctx, tx, user, session, secret, now)
}

func (s *AuthService) issueTokens(ctx context.Context, tx *sqlx.Tx, user *domain.User, session *domain.AuthSession, secret string, now time.Time) (*SessionTokens, error) {
	staff, err := s.staffIdentity(ctx, tx, user)
	if err != nil {
		return nil, err
	}
	access, accessExp, err := auth.IssueStaffAccessToken(s.secret, user.StoreID, user.ID, session.ID, string(user.Role), now, s.policy.AccessTokenTTL)
	if err != nil {
		return nil, err
	}
	return &SessionTokens{
		AccessToken:      access,
		AccessExpiresAt:  accessExp,
		RefreshToken:     formatRefreshToken(session.StoreID, session.ID, secret),
		RefreshExpiresAt: session.ExpiresAt,
		SessionID:        session.ID,
		Staff:            staff,
	}, nil
}

func (s *AuthService) staffIdentity(ctx context.Context, tx *sqlx.Tx, user *domain.User) (StaffIdentity, error) {
	slug, name, err := s.repo.GetStoreProfile(ctx, tx, user.StoreID)
	if err != nil {
		return StaffIdentity{}, err
	}
	return StaffIdentity{
		UserID: user.ID, FullName: user.FullName, Role: user.Role,
		Store: StaffStore{ID: user.StoreID, Slug: slug, Name: name},
	}, nil
}

// Refresh rotates the refresh secret (ADR-003 D4). Presenting the previous generation is
// treated as theft: the whole session is revoked. An unknown secret is only rejected, so a
// forged token cannot be used to revoke someone else's session.
func (s *AuthService) Refresh(ctx context.Context, refreshToken, userAgent, clientIP string) (*SessionTokens, error) {
	storeID, sessionID, secret, ok := parseRefreshToken(refreshToken)
	if !ok {
		return nil, ErrInvalidSession
	}
	presented := hashRefreshSecret(secret)
	now := s.nowFunc().UTC()

	var tokens *SessionTokens
	refreshErr := ErrInvalidSession
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		session, err := s.repo.GetAuthSessionForUpdate(ctx, tx, storeID, sessionID)
		if errors.Is(err, postgres.ErrAuthSessionNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if session.RevokedAt != nil {
			return nil
		}
		switch {
		case constantTimeEqual(presented, session.RefreshTokenHash):
		case session.PreviousRefreshTokenHash != "" && constantTimeEqual(presented, session.PreviousRefreshTokenHash):
			if now.Sub(session.LastUsedAt) < refreshRaceGrace {
				refreshErr = ErrRefreshRace
				return nil
			}
			return s.revokeWithAudit(ctx, tx, session, "refresh_reuse", now, clientIP)
		default:
			return nil
		}

		if !now.Before(session.ExpiresAt) {
			return nil
		}
		if s.policy.SessionIdleTimeout > 0 && !now.Before(session.LastUsedAt.Add(s.policy.SessionIdleTimeout)) {
			return s.repo.RevokeAuthSession(ctx, tx, storeID, session.ID, "idle_timeout", now)
		}
		user, err := s.repo.GetUserByID(ctx, tx, storeID, session.UserID)
		if err != nil {
			return err
		}
		if !user.IsActive {
			return s.repo.RevokeAuthSession(ctx, tx, storeID, session.ID, "user_deactivated", now)
		}
		if user.LockedUntil != nil && now.Before(*user.LockedUntil) {
			return nil
		}

		newSecret, newHash, err := newRefreshSecret()
		if err != nil {
			return err
		}
		if err := s.repo.RotateAuthSession(ctx, tx, session, newHash, now); err != nil {
			return err
		}
		tokens, err = s.issueTokens(ctx, tx, user, session, newSecret, now)
		if err != nil {
			return err
		}
		refreshErr = nil
		return nil
	})
	if err != nil {
		return nil, err
	}
	if refreshErr != nil {
		return nil, refreshErr
	}
	return tokens, nil
}

func (s *AuthService) revokeWithAudit(ctx context.Context, tx *sqlx.Tx, session *domain.AuthSession, reason string, now time.Time, clientIP string) error {
	if err := s.repo.RevokeAuthSession(ctx, tx, session.StoreID, session.ID, reason, now); err != nil {
		return err
	}
	user, err := s.repo.GetUserByID(ctx, tx, session.StoreID, session.UserID)
	if err != nil {
		return err
	}
	meta, _ := json.Marshal(map[string]any{"session_id": session.ID, "reason": reason, "client_ip": clientIP})
	actorID := user.ID
	return s.repo.InsertAuditLog(ctx, tx, &domain.AuditLog{
		StoreID:     session.StoreID,
		ActorID:     &actorID,
		ActorName:   user.FullName,
		Action:      "STAFF_SESSION_REVOKED",
		Reason:      "Sesi dicabut: refresh token lama dipakai ulang (indikasi pencurian token)",
		ReferenceID: session.ID.String(),
		Metadata:    string(meta),
	})
}

// Logout revokes the session the refresh token belongs to. It is idempotent and never
// reveals whether the token was valid.
func (s *AuthService) Logout(ctx context.Context, refreshToken string) error {
	storeID, sessionID, secret, ok := parseRefreshToken(refreshToken)
	if !ok {
		return nil
	}
	presented := hashRefreshSecret(secret)
	now := s.nowFunc().UTC()
	return database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		session, err := s.repo.GetAuthSessionForUpdate(ctx, tx, storeID, sessionID)
		if errors.Is(err, postgres.ErrAuthSessionNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if session.RevokedAt != nil || !constantTimeEqual(presented, session.RefreshTokenHash) {
			return nil
		}
		return s.repo.RevokeAuthSession(ctx, tx, storeID, sessionID, "logout", now)
	})
}

// Authenticate validates a staff access token and revalidates it against the database
// (ADR-003 D4): the session must be live and the user active, unlocked, and still in the
// role the token claims. The database role is authoritative.
func (s *AuthService) Authenticate(ctx context.Context, accessToken string) (*AuthenticatedStaff, error) {
	claims, err := auth.ValidateStaffAccessToken(accessToken, s.secret)
	if err != nil {
		return nil, ErrInvalidSession
	}
	storeID := uuid.MustParse(claims.StoreID)
	userID := uuid.MustParse(claims.Subject)
	sessionID := uuid.MustParse(claims.SessionID)
	now := s.nowFunc().UTC()

	var result *AuthenticatedStaff
	err = database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		session, err := s.repo.GetAuthSession(ctx, tx, storeID, sessionID)
		if err != nil {
			return err
		}
		if session.UserID != userID || session.RevokedAt != nil || !now.Before(session.ExpiresAt) {
			return ErrInvalidSession
		}
		user, err := s.repo.GetUserByID(ctx, tx, storeID, userID)
		if err != nil {
			return err
		}
		if !user.IsActive || (user.LockedUntil != nil && now.Before(*user.LockedUntil)) {
			return ErrInvalidSession
		}
		if string(user.Role) != claims.Role {
			return ErrSessionStale
		}
		staff, err := s.staffIdentity(ctx, tx, user)
		if err != nil {
			return err
		}
		settings, err := s.settingsRepo.GetStoreSettings(ctx, tx, storeID)
		if err != nil {
			return err
		}
		result = &AuthenticatedStaff{
			Staff: staff, SessionID: sessionID, SessionExpiresAt: session.ExpiresAt, PINChangeRequired: user.MustChangePIN,
			Permissions: rbac.Permissions(user.Role, rbac.Options{CashierHandlesBar: settings.CashierHandlesBar}),
		}
		return nil
	})
	switch {
	case err == nil:
		return result, nil
	case errors.Is(err, ErrSessionStale):
		return nil, ErrSessionStale
	case errors.Is(err, ErrInvalidSession), errors.Is(err, postgres.ErrAuthSessionNotFound), errors.Is(err, postgres.ErrUserNotFound):
		return nil, ErrInvalidSession
	default:
		return nil, err // infrastructure failure: let the handler answer 503, not 401
	}
}

// Refresh tokens are "<store_id>.<session_id>.<secret>". store_id is not secret: it is needed to
// open the tenant transaction under RLS. Only SHA-256(secret) is stored.
func formatRefreshToken(storeID, sessionID uuid.UUID, secret string) string {
	return storeID.String() + "." + sessionID.String() + "." + secret
}

func parseRefreshToken(token string) (uuid.UUID, uuid.UUID, string, bool) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 || len(parts[2]) != 43 {
		return uuid.Nil, uuid.Nil, "", false
	}
	storeID, err1 := uuid.Parse(parts[0])
	sessionID, err2 := uuid.Parse(parts[1])
	if err1 != nil || err2 != nil || storeID == uuid.Nil || sessionID == uuid.Nil {
		return uuid.Nil, uuid.Nil, "", false
	}
	return storeID, sessionID, parts[2], true
}

func newRefreshSecret() (string, string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("failed to generate refresh token: %w", err)
	}
	secret := base64.RawURLEncoding.EncodeToString(buf)
	return secret, hashRefreshSecret(secret), nil
}

func hashRefreshSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func isPlausibleEmail(email string) bool {
	if email == "" || len(email) > 255 {
		return false
	}
	addr, err := mail.ParseAddress(email)
	return err == nil && addr.Address == email
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Can reports whether the authenticated staff member holds a permission (defence in depth for services).
func (a *AuthenticatedStaff) Can(p rbac.Permission) bool {
	return a != nil && slices.Contains(a.Permissions, p)
}
