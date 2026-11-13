package service_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/auth"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/mailer"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/service"
)

type recordingSender struct {
	mu   sync.Mutex
	sent []mailer.Message
	fail func(mailer.Message) error
}

func (r *recordingSender) Send(_ context.Context, m mailer.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		if err := r.fail(m); err != nil {
			return err
		}
	}
	r.sent = append(r.sent, m)
	return nil
}

// last returns the newest delivered email to `to` (case-insensitive).
func (r *recordingSender) last(to string) (mailer.Message, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.sent) - 1; i >= 0; i-- {
		if strings.EqualFold(r.sent[i].To, to) {
			return r.sent[i], true
		}
	}
	return mailer.Message{}, false
}

func linkToken(t *testing.T, m mailer.Message) string {
	t.Helper()
	_, after, ok := strings.Cut(m.Text, "#token=")
	if !ok {
		t.Fatalf("email %q has no #token= link", m.Subject)
	}
	return strings.Fields(after)[0]
}

func TestStaffLifecycleIntegration(t *testing.T) {
	db := openIntegrationAppDB(t)
	ctx := context.Background()
	storeAID := uuid.MustParse("11111111-1111-4111-8111-111111111111")

	sender := &recordingSender{}
	emailSvc := service.NewEmailService(db, sender)
	svc := service.NewAuthService(db, postgres.NewAuthRepository(), "integration-test-jwt-secret-at-least-32-chars", service.AuthPolicy{
		AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 12 * time.Hour, SessionIdleTimeout: 2 * time.Hour,
		MaxSessions: 3, PINMaxFailures: 5, PINLockout: 15 * time.Minute, MaxConcurrentHashes: 8,
	}, service.WithAuthEmail(emailSvc, "https://pos.kafe.test", "owner@kafe.test"))

	pinHash, _ := auth.HashPIN(testPIN)
	ownerEmail := "owner." + uuid.NewString()[:8] + "@kafe.test"
	if err := database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO users (id, store_id, email, full_name, role, pin_hash, pin_set_at) VALUES ($1, $2, $3, 'Owner Uji', 'owner', $4, NOW())`,
			uuid.New(), storeAID, ownerEmail, pinHash)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	login := func(email, pin string) (*service.SessionTokens, error) {
		return svc.Login(ctx, service.LoginInput{StoreSlug: "central-cafe", Email: email, PIN: pin})
	}
	actorFor := func(t *testing.T, email string) *service.AuthenticatedStaff {
		t.Helper()
		tokens, err := login(email, testPIN)
		if err != nil {
			t.Fatalf("login %s: %v", email, err)
		}
		a, err := svc.Authenticate(ctx, tokens.AccessToken)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	deliver := func(t *testing.T) {
		t.Helper()
		// next_attempt_at comes from the database clock and the worker compares it with the Go clock; a
		// few milliseconds of Docker VM skew leaves mail inserted just now "not yet due". Shift only
		// mail due within that window, never retries scheduled minutes ahead.
		if err := database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE email_outbox SET next_attempt_at = next_attempt_at - INTERVAL '2 seconds'
				WHERE store_id = $1 AND status = 'pending' AND next_attempt_at <= NOW() + INTERVAL '2 seconds'`, storeAID)
			return err
		}); err != nil {
			t.Fatalf("align due mail: %v", err)
		}
		// Claims are oldest first; other tests leave more than one batch of due mail in the shared store.
		if _, err := emailSvc.DeliverDue(ctx, 100000); err != nil {
			t.Fatalf("deliver: %v", err)
		}
	}
	owner := actorFor(t, ownerEmail)

	var invitedEmail string
	t.Run("1_InviteActivationLink_SetsPIN_SingleUse", func(t *testing.T) {
		invitedEmail = "Kasir." + uuid.NewString()[:8] + "@Kafe.Test"
		staff, err := svc.InviteStaff(ctx, owner, service.InviteStaffInput{FullName: "Kasir Baru", Email: invitedEmail, Role: domain.RoleCashier})
		if err != nil {
			t.Fatalf("invite: %v", err)
		}
		if staff.Email != strings.ToLower(invitedEmail) {
			t.Fatalf("email must be normalized, got %q", staff.Email)
		}
		if _, err := svc.InviteStaff(ctx, owner, service.InviteStaffInput{FullName: "Duplikat", Email: strings.ToUpper(invitedEmail), Role: domain.RoleBarista}); !errors.Is(err, postgres.ErrEmailTaken) {
			t.Fatalf("duplicate email (other case) err = %v", err)
		}
		// Not activated: rejected like an unknown user, and the failure counter stays 0.
		if _, err := login(invitedEmail, testPIN); !errors.Is(err, service.ErrInvalidCredentials) {
			t.Fatalf("login before activation: %v", err)
		}
		deliver(t)
		msg, ok := sender.last(strings.ToLower(invitedEmail))
		if !ok || !strings.Contains(msg.Subject, "Undangan") || !strings.Contains(msg.HTML, "https://pos.kafe.test/staff/pin#token=") {
			t.Fatalf("invite email not delivered correctly: %+v", msg.Subject)
		}
		token := linkToken(t, msg)

		if _, err := svc.SetPINWithToken(ctx, token, "123456"); !errors.Is(err, auth.ErrPINTooWeak) {
			t.Fatalf("weak PIN: %v", err)
		}
		res, err := svc.SetPINWithToken(ctx, token, "905172")
		if err != nil || res.StoreSlug != "central-cafe" {
			t.Fatalf("set PIN: %+v %v", res, err)
		}
		if _, err := svc.SetPINWithToken(ctx, token, "905173"); !errors.Is(err, service.ErrInvalidAccountLink) {
			t.Fatalf("token reuse: %v", err)
		}
		if _, err := login(invitedEmail, "905172"); err != nil {
			t.Fatalf("login after activation: %v", err)
		}

		var body string
		_ = database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &body, `SELECT text_body FROM email_outbox WHERE store_id = $1 AND lower(to_email) = lower($2) AND status = 'sent' LIMIT 1`, storeAID, invitedEmail)
		})
		if strings.Contains(body, "#token=") {
			t.Fatal("delivered email bodies must be scrubbed of link secrets")
		}
	})

	t.Run("2_ForgotPIN_NoEnumeration_ResetRevokesSessions", func(t *testing.T) {
		before := len(sender.sent)
		if err := svc.RequestPINReset(ctx, "central-cafe", "nobody@kafe.test"); err != nil {
			t.Fatal(err)
		}
		if err := svc.RequestPINReset(ctx, "no-such-store", invitedEmail); err != nil {
			t.Fatal(err)
		}
		deliver(t)
		if len(sender.sent) != before {
			t.Fatal("unknown store/email must not send anything")
		}

		session, err := login(invitedEmail, "905172")
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.RequestPINReset(ctx, "central-cafe", invitedEmail); err != nil {
			t.Fatal(err)
		}
		deliver(t)
		msg, _ := sender.last(strings.ToLower(invitedEmail))
		if !strings.Contains(msg.Subject, "Atur ulang PIN") {
			t.Fatalf("expected reset email, got %q", msg.Subject)
		}
		sentBefore := len(sender.sent)
		if err := svc.RequestPINReset(ctx, "central-cafe", invitedEmail); err != nil {
			t.Fatal(err)
		}
		deliver(t)
		if len(sender.sent) != sentBefore {
			t.Fatal("a second reset request within the cooldown must not send another email")
		}
		if _, err := svc.SetPINWithToken(ctx, linkToken(t, msg), "738204"); err != nil {
			t.Fatalf("reset: %v", err)
		}
		if _, err := svc.Refresh(ctx, session.RefreshToken, "", ""); !errors.Is(err, service.ErrInvalidSession) {
			t.Fatalf("reset must revoke existing sessions: %v", err)
		}
		deliver(t)
		if msg, _ := sender.last(strings.ToLower(invitedEmail)); !strings.Contains(msg.Subject, "diubah") {
			t.Fatalf("expected PIN-changed notice, got %q", msg.Subject)
		}
	})

	t.Run("3_TemporaryPIN_RequiresChange", func(t *testing.T) {
		staff, err := svc.InviteStaff(ctx, owner, service.InviteStaffInput{FullName: "Dapur Tanpa Email", Email: "dapur." + uuid.NewString()[:8] + "@kafe.test", Role: domain.RoleKitchen})
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.IssueTemporaryPIN(ctx, owner, staff.ID, "618273"); err != nil {
			t.Fatalf("temporary PIN: %v", err)
		}
		tokens, err := login(staff.Email, "618273")
		if err != nil || !tokens.PINChangeRequired {
			t.Fatalf("temporary PIN login must require change: %+v %v", tokens, err)
		}
		kitchen, _ := svc.Authenticate(ctx, tokens.AccessToken)
		if err := svc.ChangePIN(ctx, kitchen, "000001", "582910"); !errors.Is(err, service.ErrWrongCurrentPIN) {
			t.Fatalf("wrong current PIN: %v", err)
		}
		if err := svc.ChangePIN(ctx, kitchen, "618273", "582910"); err != nil {
			t.Fatalf("change PIN: %v", err)
		}
		after, err := login(staff.Email, "582910")
		if err != nil || after.PINChangeRequired {
			t.Fatalf("after change: %+v %v", after, err)
		}
		if _, err := svc.Authenticate(ctx, tokens.AccessToken); err != nil {
			t.Fatalf("the session that changed the PIN must stay signed in: %v", err)
		}
	})

	t.Run("4_NonOwnerForbidden_AndLockNotifiesOwners", func(t *testing.T) {
		cashier := actorFor(t, "cashier.central@mycaffe.local")
		if _, err := svc.InviteStaff(ctx, cashier, service.InviteStaffInput{FullName: "X", Email: "x@kafe.test", Role: domain.RoleCashier}); !errors.Is(err, service.ErrForbidden) {
			t.Fatalf("cashier invite: %v", err)
		}
		staff, _ := svc.InviteStaff(ctx, owner, service.InviteStaffInput{FullName: "Barista Kunci", Email: "kunci." + uuid.NewString()[:8] + "@kafe.test", Role: domain.RoleBarista})
		if err := svc.IssueTemporaryPIN(ctx, owner, staff.ID, "618273"); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 5; i++ {
			_, _ = login(staff.Email, "999999")
		}
		deliver(t)
		msg, ok := sender.last(ownerEmail)
		if !ok || !strings.Contains(msg.Subject, "Barista Kunci") || !strings.Contains(msg.Subject, "terkunci") {
			t.Fatalf("owner must be notified of the lock, got %q", msg.Subject)
		}
	})

	t.Run("5_TransientFailureRetries_PermanentFailureStops", func(t *testing.T) {
		transient := "retry." + uuid.NewString()[:8] + "@kafe.test"
		permanent := "bounce." + uuid.NewString()[:8] + "@kafe.test"
		sender.mu.Lock()
		sender.fail = func(m mailer.Message) error {
			switch m.To {
			case transient:
				return errors.New("smtp dial: connection refused")
			case permanent:
				return mailer.ErrPermanent
			}
			return nil
		}
		sender.mu.Unlock()
		defer func() { sender.mu.Lock(); sender.fail = nil; sender.mu.Unlock() }()

		for _, email := range []string{transient, permanent} {
			if _, err := svc.InviteStaff(ctx, owner, service.InviteStaffInput{FullName: "Uji Email", Email: email, Role: domain.RoleCashier}); err != nil {
				t.Fatal(err)
			}
		}
		deliver(t)
		status := func(email string) (string, int, time.Time) {
			var row struct {
				Status   string    `db:"status"`
				Attempts int       `db:"attempts"`
				Next     time.Time `db:"next_attempt_at"`
			}
			_ = database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
				return tx.GetContext(ctx, &row, `SELECT status, attempts, next_attempt_at FROM email_outbox WHERE store_id = $1 AND to_email = $2`, storeAID, email)
			})
			return row.Status, row.Attempts, row.Next
		}
		if s, n, next := status(transient); s != "pending" || n != 1 || !next.After(time.Now()) {
			t.Fatalf("transient failure must be rescheduled: status=%s attempts=%d next=%v", s, n, next)
		}
		if s, n, _ := status(permanent); s != "failed" || n != 1 {
			t.Fatalf("permanent failure must stop: status=%s attempts=%d", s, n)
		}
	})
}
