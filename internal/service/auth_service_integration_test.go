package service_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/auth"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

const testPIN = "482913"

func TestAuthServiceIntegration_EndToEnd(t *testing.T) {
	db := openIntegrationAppDB(t)
	ctx := context.Background()

	storeAID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	storeBID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	pinHash, err := auth.HashPIN(testPIN)
	if err != nil {
		t.Fatal(err)
	}

	var clockMu sync.Mutex
	currentNow := time.Now().UTC()
	clock := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return currentNow }
	advance := func(d time.Duration) { clockMu.Lock(); currentNow = currentNow.Add(d); clockMu.Unlock() }

	policy := service.AuthPolicy{
		AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 12 * time.Hour, SessionIdleTimeout: 2 * time.Hour,
		MaxSessions: 3, PINMaxFailures: 5, PINLockout: 15 * time.Minute, MaxConcurrentHashes: 16,
	}
	repo := postgres.NewAuthRepository()
	svc := service.NewAuthService(db, repo, "integration-test-jwt-secret-at-least-32-chars", policy, service.WithAuthClock(clock))

	// newStaff inserts a fresh user so tests never mutate the seeded staff used by other suites.
	newStaff := func(t *testing.T, storeID uuid.UUID, role string) (uuid.UUID, string) {
		t.Helper()
		id := uuid.New()
		email := "Auth." + id.String()[:8] + "@MyCaffe.Test"
		if err := database.WithTenantTx(ctx, db, storeID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO users (id, store_id, email, full_name, role, pin_hash, pin_set_at) VALUES ($1, $2, $3, $4, $5, $6, NOW())`,
				id, storeID, email, "Staf Uji "+role, role, pinHash)
			return err
		}); err != nil {
			t.Fatalf("insert staff: %v", err)
		}
		return id, email
	}
	login := func(email string, pin string) (*service.SessionTokens, error) {
		return svc.Login(ctx, service.LoginInput{StoreSlug: "Central-Cafe ", Email: email, PIN: pin, UserAgent: "go-test", ClientIP: "203.0.113.7"})
	}
	auditCount := func(t *testing.T, storeID uuid.UUID, action, ref string) int {
		t.Helper()
		var n int
		if err := database.WithTenantTx(ctx, db, storeID, func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &n, `SELECT COUNT(*) FROM audit_logs WHERE store_id = $1 AND action = $2 AND reference_id = $3`, storeID, action, ref)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	sessionRevokedReason := func(t *testing.T, storeID, sessionID uuid.UUID) string {
		t.Helper()
		var reason *string
		if err := database.WithTenantTx(ctx, db, storeID, func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &reason, `SELECT revoked_reason FROM auth_sessions WHERE store_id = $1 AND id = $2`, storeID, sessionID)
		}); err != nil {
			t.Fatal(err)
		}
		if reason == nil {
			return ""
		}
		return *reason
	}

	t.Run("1_LoginCaseInsensitiveEmail_IssuesTokens_AndAuthenticate", func(t *testing.T) {
		userID, email := newStaff(t, storeAID, "cashier")
		tokens, err := login(strings.ToLower(email), testPIN)
		if err != nil {
			t.Fatalf("login failed: %v", err)
		}
		if tokens.Staff.UserID != userID || tokens.Staff.Store.ID != storeAID || tokens.Staff.Role != "cashier" {
			t.Fatalf("unexpected identity: %+v", tokens.Staff)
		}
		if !strings.HasPrefix(tokens.RefreshToken, storeAID.String()+"."+tokens.SessionID.String()+".") {
			t.Fatalf("refresh token format: %s", tokens.RefreshToken)
		}
		authed, err := svc.Authenticate(ctx, tokens.AccessToken)
		if err != nil || authed.Staff.UserID != userID || authed.SessionID != tokens.SessionID || authed.Staff.Store.Slug != "central-cafe" {
			t.Fatalf("authenticate: %+v err=%v", authed, err)
		}
		var storedHash string
		_ = database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &storedHash, `SELECT refresh_token_hash FROM auth_sessions WHERE store_id = $1 AND id = $2`, storeAID, tokens.SessionID)
		})
		if strings.Contains(tokens.RefreshToken, storedHash) || len(storedHash) != 64 {
			t.Fatal("refresh secret must be stored only as a SHA-256 hash")
		}
	})

	t.Run("2_GenericFailures_UnknownStoreUserWrongPIN", func(t *testing.T) {
		_, email := newStaff(t, storeAID, "barista")
		for name, in := range map[string]service.LoginInput{
			"unknown_store": {StoreSlug: "no-such-store", Email: email, PIN: testPIN},
			"unknown_email": {StoreSlug: "central-cafe", Email: "nobody@mycaffe.test", PIN: testPIN},
			"wrong_pin":     {StoreSlug: "central-cafe", Email: email, PIN: "482914"},
			"other_tenant":  {StoreSlug: "express-counter", Email: email, PIN: testPIN},
			"bad_email":     {StoreSlug: "central-cafe", Email: "not-an-email", PIN: testPIN},
		} {
			if _, err := svc.Login(ctx, in); !errors.Is(err, service.ErrInvalidCredentials) {
				t.Errorf("%s: err = %v, want ErrInvalidCredentials", name, err)
			}
		}
	})

	t.Run("3_LockoutAfterFiveFailures_ThenUnlocksAfterWindow", func(t *testing.T) {
		userID, email := newStaff(t, storeAID, "cashier")
		for i := 0; i < 5; i++ {
			if _, err := login(email, "999999"); !errors.Is(err, service.ErrInvalidCredentials) {
				t.Fatalf("attempt %d: %v", i, err)
			}
		}
		if _, err := login(email, testPIN); !errors.Is(err, service.ErrInvalidCredentials) {
			t.Fatalf("correct PIN must be rejected while locked, got %v", err)
		}
		if n := auditCount(t, storeAID, "STAFF_LOGIN_LOCKED", userID.String()); n != 1 {
			t.Fatalf("expected 1 STAFF_LOGIN_LOCKED audit row, got %d", n)
		}
		advance(16 * time.Minute)
		if _, err := login(email, testPIN); err != nil {
			t.Fatalf("login after lockout window: %v", err)
		}
	})

	t.Run("4_ConcurrentWrongPINs_LockExactlyOnce", func(t *testing.T) {
		userID, email := newStaff(t, storeAID, "kitchen")
		var wg sync.WaitGroup
		for i := 0; i < 12; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = login(email, "999999")
			}()
		}
		wg.Wait()
		// 12 serialized failures with threshold 5: lock at the 5th, later attempts hit the lock.
		if n := auditCount(t, storeAID, "STAFF_LOGIN_LOCKED", userID.String()); n != 1 {
			t.Fatalf("row lock must serialize attempts: expected exactly 1 lock, got %d", n)
		}
	})

	t.Run("5_RefreshRotation_AndReuseRevokesSession", func(t *testing.T) {
		userID, email := newStaff(t, storeAID, "cashier")
		first, err := login(email, testPIN)
		if err != nil {
			t.Fatal(err)
		}
		second, err := svc.Refresh(ctx, first.RefreshToken, "go-test", "203.0.113.7")
		if err != nil {
			t.Fatalf("refresh: %v", err)
		}
		if second.RefreshToken == first.RefreshToken || second.SessionID != first.SessionID {
			t.Fatal("refresh must rotate the secret within the same session")
		}
		// Within the grace window the previous generation is a tab race: rejected, session kept.
		if _, err := svc.Refresh(ctx, first.RefreshToken, "go-test-tab-2", "203.0.113.7"); !errors.Is(err, service.ErrRefreshRace) {
			t.Fatalf("concurrent-tab refresh: %v, want ErrRefreshRace", err)
		}
		if got := sessionRevokedReason(t, storeAID, first.SessionID); got != "" {
			t.Fatalf("race must not revoke the session, got %q", got)
		}
		advance(time.Minute)
		// After the grace window, replaying the rotated token is theft: the whole session dies.
		if _, err := svc.Refresh(ctx, first.RefreshToken, "attacker", "198.51.100.9"); !errors.Is(err, service.ErrInvalidSession) {
			t.Fatalf("reuse: %v", err)
		}
		if _, err := svc.Refresh(ctx, second.RefreshToken, "go-test", "203.0.113.7"); !errors.Is(err, service.ErrInvalidSession) {
			t.Fatalf("token after reuse must be dead: %v", err)
		}
		if got := sessionRevokedReason(t, storeAID, first.SessionID); got != "refresh_reuse" {
			t.Fatalf("revoked_reason = %q", got)
		}
		if n := auditCount(t, storeAID, "STAFF_SESSION_REVOKED", first.SessionID.String()); n != 1 {
			t.Fatalf("expected STAFF_SESSION_REVOKED audit, got %d", n)
		}
		if _, err := svc.Authenticate(ctx, second.AccessToken); !errors.Is(err, service.ErrInvalidSession) {
			t.Fatalf("access token of revoked session must fail: %v", err)
		}
		_ = userID
	})

	t.Run("6_ForgedSecret_IsRejectedWithoutRevoking", func(t *testing.T) {
		_, email := newStaff(t, storeAID, "cashier")
		tokens, err := login(email, testPIN)
		if err != nil {
			t.Fatal(err)
		}
		forged := storeAID.String() + "." + tokens.SessionID.String() + "." + strings.Repeat("A", 43)
		if _, err := svc.Refresh(ctx, forged, "attacker", "198.51.100.9"); !errors.Is(err, service.ErrInvalidSession) {
			t.Fatalf("forged: %v", err)
		}
		if _, err := svc.Refresh(ctx, tokens.RefreshToken, "go-test", "203.0.113.7"); err != nil {
			t.Fatalf("legitimate session must survive a forged attempt: %v", err)
		}
	})

	t.Run("7_SessionCapOfThree_RevokesOldest", func(t *testing.T) {
		_, email := newStaff(t, storeAID, "barista")
		var sessions []*service.SessionTokens
		for i := 0; i < 4; i++ {
			advance(time.Second)
			s, err := login(email, testPIN)
			if err != nil {
				t.Fatal(err)
			}
			sessions = append(sessions, s)
		}
		if got := sessionRevokedReason(t, storeAID, sessions[0].SessionID); got != "session_limit" {
			t.Fatalf("oldest session revoked_reason = %q, want session_limit", got)
		}
		for _, s := range sessions[1:] {
			if got := sessionRevokedReason(t, storeAID, s.SessionID); got != "" {
				t.Fatalf("newer session unexpectedly revoked: %q", got)
			}
		}
	})

	t.Run("8_IdleTimeout_And_Logout", func(t *testing.T) {
		_, email := newStaff(t, storeAID, "cashier")
		idle, err := login(email, testPIN)
		if err != nil {
			t.Fatal(err)
		}
		advance(2*time.Hour + time.Minute)
		if _, err := svc.Refresh(ctx, idle.RefreshToken, "go-test", ""); !errors.Is(err, service.ErrInvalidSession) {
			t.Fatalf("idle refresh: %v", err)
		}
		if got := sessionRevokedReason(t, storeAID, idle.SessionID); got != "idle_timeout" {
			t.Fatalf("revoked_reason = %q", got)
		}

		active, err := login(email, testPIN)
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.Logout(ctx, active.RefreshToken); err != nil {
			t.Fatal(err)
		}
		if got := sessionRevokedReason(t, storeAID, active.SessionID); got != "logout" {
			t.Fatalf("revoked_reason = %q", got)
		}
		if err := svc.Logout(ctx, active.RefreshToken); err != nil {
			t.Fatalf("logout must be idempotent: %v", err)
		}
	})

	t.Run("9_CrossTenant_TokenCannotCrossStores", func(t *testing.T) {
		_, email := newStaff(t, storeAID, "cashier")
		tokens, err := login(email, testPIN)
		if err != nil {
			t.Fatal(err)
		}
		swapped := strings.Replace(tokens.RefreshToken, storeAID.String(), storeBID.String(), 1)
		if _, err := svc.Refresh(ctx, swapped, "attacker", ""); !errors.Is(err, service.ErrInvalidSession) {
			t.Fatalf("store-swapped refresh token: %v", err)
		}
		// Under store B's RLS context the store A session must be invisible.
		var visible int
		_ = database.WithTenantTx(ctx, db, storeBID, func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &visible, `SELECT COUNT(*) FROM auth_sessions WHERE id = $1`, tokens.SessionID)
		})
		if visible != 0 {
			t.Fatal("RLS leak: store B can see a store A session")
		}
	})

	t.Run("10_RoleChangeIsStale_DeactivationKillsSession", func(t *testing.T) {
		userID, email := newStaff(t, storeAID, "cashier")
		tokens, err := login(email, testPIN)
		if err != nil {
			t.Fatal(err)
		}
		setUser := func(query string) {
			if err := database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
				_, err := tx.ExecContext(ctx, query, storeAID, userID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
		setUser(`UPDATE users SET role = 'barista' WHERE store_id = $1 AND id = $2`)
		if _, err := svc.Authenticate(ctx, tokens.AccessToken); !errors.Is(err, service.ErrSessionStale) {
			t.Fatalf("role change: %v", err)
		}
		refreshed, err := svc.Refresh(ctx, tokens.RefreshToken, "go-test", "")
		if err != nil || refreshed.Staff.Role != "barista" {
			t.Fatalf("refresh must pick up the database role: %+v %v", refreshed, err)
		}
		setUser(`UPDATE users SET is_active = false WHERE store_id = $1 AND id = $2`)
		if _, err := svc.Authenticate(ctx, refreshed.AccessToken); !errors.Is(err, service.ErrInvalidSession) {
			t.Fatalf("deactivated user access: %v", err)
		}
		if _, err := svc.Refresh(ctx, refreshed.RefreshToken, "go-test", ""); !errors.Is(err, service.ErrInvalidSession) {
			t.Fatalf("deactivated user refresh: %v", err)
		}
	})
}

// TestStaffAdminIntegration exercises the operator CLI paths with the migrator connection.
func TestStaffAdminIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_MIGRATOR_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_MIGRATOR_DATABASE_URL not set; skipping operator CLI integration test")
	}
	migratorDB, err := database.NewPostgresDB(&config.Config{DatabaseURL: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migratorDB.Close() })
	appDB := openIntegrationAppDB(t)
	ctx := context.Background()

	slug := "uji-" + uuid.NewString()[:8]
	if _, _, err := service.BootstrapStore(ctx, migratorDB, service.BootstrapStoreInput{
		Slug: slug, StoreName: "Kafe Uji", OwnerName: "Pemilik Uji", OwnerEmail: "pemilik@" + slug + ".test", PIN: "123456",
	}); !errors.Is(err, auth.ErrPINTooWeak) {
		t.Fatalf("weak PIN must be rejected: %v", err)
	}
	storeID, ownerID, err := service.BootstrapStore(ctx, migratorDB, service.BootstrapStoreInput{
		Slug: slug, StoreName: "Kafe Uji", OwnerName: "Pemilik Uji", OwnerEmail: "pemilik@" + slug + ".test", PIN: testPIN,
	})
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if _, _, err := service.BootstrapStore(ctx, migratorDB, service.BootstrapStoreInput{
		Slug: slug, StoreName: "Duplikat", OwnerName: "X", OwnerEmail: "x@" + slug + ".test", PIN: testPIN,
	}); err == nil {
		t.Fatal("duplicate slug must fail")
	}

	svc := service.NewAuthService(appDB, postgres.NewAuthRepository(), "integration-test-jwt-secret-at-least-32-chars", service.AuthPolicy{
		AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 12 * time.Hour, SessionIdleTimeout: 2 * time.Hour,
		MaxSessions: 3, PINMaxFailures: 5, PINLockout: 15 * time.Minute,
	})
	tokens, err := svc.Login(ctx, service.LoginInput{StoreSlug: slug, Email: "pemilik@" + slug + ".test", PIN: testPIN})
	if err != nil || tokens.Staff.UserID != ownerID || tokens.Staff.Store.ID != storeID || tokens.Staff.Role != "owner" {
		t.Fatalf("bootstrapped owner login: %+v %v", tokens, err)
	}

	if err := service.ResetOwnerPIN(ctx, migratorDB, slug, "pemilik@"+slug+".test", "905172"); err != nil {
		t.Fatalf("reset owner PIN: %v", err)
	}
	if _, err := svc.Refresh(ctx, tokens.RefreshToken, "", ""); !errors.Is(err, service.ErrInvalidSession) {
		t.Fatalf("PIN reset must revoke existing sessions: %v", err)
	}
	if _, err := svc.Login(ctx, service.LoginInput{StoreSlug: slug, Email: "pemilik@" + slug + ".test", PIN: "905172"}); err != nil {
		t.Fatalf("login with reset PIN: %v", err)
	}
}
