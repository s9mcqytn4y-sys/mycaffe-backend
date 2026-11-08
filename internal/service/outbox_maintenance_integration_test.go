package service

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/config"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/sms"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/realtime"
)

type recordingPublisher struct {
	fail bool
	got  []string
}

func (p *recordingPublisher) Publish(m *realtime.EventMessage) error {
	if p.fail {
		return errors.New("hub unavailable")
	}
	p.got = append(p.got, m.Type)
	return nil
}

// TestOutboxDeadLetterAndPurge covers P0-08: a store whose oldest event keeps failing dead-letters it
// after relayMaxAttempts so newer events flow again, and the operator purge removes only technical
// rows past retention.
func TestOutboxDeadLetterAndPurge(t *testing.T) {
	appDSN := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	migratorDSN := strings.TrimSpace(os.Getenv("TEST_MIGRATOR_DATABASE_URL"))
	if appDSN == "" || migratorDSN == "" {
		t.Skip("TEST_APP_DATABASE_URL / TEST_MIGRATOR_DATABASE_URL not set; skipping outbox maintenance test")
	}
	appDB, err := database.NewPostgresDB(&config.Config{DatabaseURL: appDSN})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = appDB.Close() })
	migratorDB, err := database.NewPostgresDB(&config.Config{DatabaseURL: migratorDSN})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migratorDB.Close() })
	ctx := context.Background()

	slug := "outbox-" + uuid.NewString()[:8]
	storeID, ownerID, err := BootstrapStore(ctx, migratorDB, BootstrapStoreInput{
		Slug: slug, StoreName: "Kafe Outbox", OwnerName: "Pemilik Outbox", OwnerEmail: "owner@" + slug + ".test", PIN: "731904",
	})
	if err != nil {
		t.Fatal(err)
	}
	// BootstrapStore writes its own events; mark them done so the test controls the queue.
	if _, err := migratorDB.ExecContext(ctx, `UPDATE outbox_events SET processed_at = NOW() WHERE store_id = $1 AND processed_at IS NULL`, storeID); err != nil {
		t.Fatal(err)
	}
	emit := func(eventType string) uuid.UUID {
		t.Helper()
		id := uuid.New()
		if err := database.WithTenantTx(ctx, appDB, storeID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO outbox_events (id, store_id, event_type, aggregate_type, aggregate_id, payload)
				VALUES ($1, $2, $3, 'test', $4, '{}')`, id, storeID, eventType, id.String())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	svc := NewRealtimeService(appDB)

	t.Run("DeadLetterUnblocksTheStore", func(t *testing.T) {
		type eventRow struct {
			Retries int        `db:"retry_count"`
			Dead    *time.Time `db:"dead_at"`
			Done    *time.Time `db:"processed_at"`
			LastErr *string    `db:"last_error"`
		}
		load := func(id uuid.UUID) eventRow {
			var row eventRow
			if err := migratorDB.GetContext(ctx, &row, `SELECT retry_count, dead_at, processed_at, last_error FROM outbox_events WHERE id = $1`, id); err != nil {
				t.Fatal(err)
			}
			return row
		}
		// Relay tests in other packages run RelayOnce over every store at the same time and may publish
		// the event first; such a round is retried. The event starts one failure short of the limit.
		failing := &recordingPublisher{fail: true}
		var poison uuid.UUID
		var row eventRow
		for attempt := 0; attempt < 5 && row.Dead == nil; attempt++ {
			poison = emit("test.poison")
			if _, err := migratorDB.ExecContext(ctx, `UPDATE outbox_events SET retry_count = $2 WHERE id = $1`, poison, relayMaxAttempts-1); err != nil {
				t.Fatal(err)
			}
			err := svc.relayStore(ctx, failing, storeID)
			if err == nil {
				continue // another relay published it first
			}
			svc.recordRelayFailure(ctx, storeID, err)
			row = load(poison)
		}
		if row.Retries != relayMaxAttempts || row.Dead == nil || row.Done == nil || row.LastErr == nil || *row.LastErr != "hub unavailable" {
			t.Fatalf("poison event after %d failures: %+v", relayMaxAttempts, row)
		}
		after := emit("test.after")
		if err := svc.relayStore(ctx, &recordingPublisher{}, storeID); err != nil {
			t.Fatal(err)
		}
		if load(after).Done == nil {
			t.Fatal("a newer event must flow again once the poison event is dead-lettered")
		}
	})

	t.Run("PurgeKeepsFreshAndEvidenceRows", func(t *testing.T) {
		oldEvent, freshEvent, pending := emit("test.old"), emit("test.fresh"), emit("test.pending")
		if _, err := migratorDB.ExecContext(ctx, `UPDATE outbox_events SET processed_at = NOW() - interval '8 days' WHERE id = $1`, oldEvent); err != nil {
			t.Fatal(err)
		}
		if _, err := migratorDB.ExecContext(ctx, `UPDATE outbox_events SET processed_at = NOW() WHERE id = $1`, freshEvent); err != nil {
			t.Fatal(err)
		}
		oldSession, liveSession := uuid.New(), uuid.New()
		if _, err := migratorDB.ExecContext(ctx, `INSERT INTO auth_sessions (id, store_id, user_id, refresh_token_hash, expires_at, created_at)
			VALUES ($1, $3, $4, $5, NOW() - interval '31 days', NOW() - interval '40 days'),
			       ($2, $3, $4, $6, NOW() + interval '1 day', NOW())`,
			oldSession, liveSession, storeID, ownerID, strings.Repeat("a", 64), strings.Repeat("b", 64)); err != nil {
			t.Fatal(err)
		}
		var auditBefore int
		_ = migratorDB.GetContext(ctx, &auditBefore, `SELECT COUNT(*) FROM audit_logs WHERE store_id = $1`, storeID)

		counts, err := PurgeTechnicalRows(ctx, migratorDB, time.Now().UTC())
		if err != nil {
			t.Fatalf("purge: %v (%v)", err, counts)
		}
		exists := func(table string, id uuid.UUID) bool {
			var n int
			_ = migratorDB.GetContext(ctx, &n, `SELECT COUNT(*) FROM `+table+` WHERE id = $1`, id)
			return n == 1
		}
		if exists("outbox_events", oldEvent) || !exists("outbox_events", freshEvent) || !exists("outbox_events", pending) {
			t.Fatal("outbox purge must drop only events processed more than 7 days ago")
		}
		if exists("auth_sessions", oldSession) || !exists("auth_sessions", liveSession) {
			t.Fatal("session purge must drop only sessions expired more than 30 days ago")
		}
		var auditAfter int
		_ = migratorDB.GetContext(ctx, &auditAfter, `SELECT COUNT(*) FROM audit_logs WHERE store_id = $1`, storeID)
		if auditAfter != auditBefore || counts["outbox_events"] < 1 || counts["auth_sessions"] < 1 {
			t.Fatalf("audit rows must survive and counts must be reported: %v", counts)
		}
	})
}

// TestMessageOutcomes covers the SMS queue (V5): a sent or finally failed message has its body
// scrubbed (codes are secrets), a transient failure is retried on the short schedule.
func TestMessageOutcomes(t *testing.T) {
	appDSN := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	migratorDSN := strings.TrimSpace(os.Getenv("TEST_MIGRATOR_DATABASE_URL"))
	if appDSN == "" || migratorDSN == "" {
		t.Skip("TEST_APP_DATABASE_URL / TEST_MIGRATOR_DATABASE_URL not set; skipping message outcome test")
	}
	appDB, err := database.NewPostgresDB(&config.Config{DatabaseURL: appDSN})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = appDB.Close() })
	migratorDB, err := database.NewPostgresDB(&config.Config{DatabaseURL: migratorDSN})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migratorDB.Close() })
	ctx := context.Background()
	slug := "msg-" + uuid.NewString()[:8]
	storeID, _, err := BootstrapStore(ctx, migratorDB, BootstrapStoreInput{Slug: slug, StoreName: "Kafe Pesan", OwnerName: "Pemilik", OwnerEmail: "o@" + slug + ".test", PIN: "731904"})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewMessageService(appDB, recordingSMS{})
	queue := func() queuedMessage {
		t.Helper()
		var m queuedMessage
		if err := database.WithTenantTx(ctx, appDB, storeID, func(tx *sqlx.Tx) error {
			if err := svc.EnqueueSMSTx(ctx, tx, storeID, "+6281234567890", "test", "Kode 123456", "ref"); err != nil {
				return err
			}
			return tx.GetContext(ctx, &m, `UPDATE message_outbox SET attempts = 1, status = 'sending' WHERE store_id = $1 AND id =
				(SELECT id FROM message_outbox WHERE store_id = $1 ORDER BY created_at DESC LIMIT 1) RETURNING id, store_id, to_phone, template, body, attempts`, storeID)
		}); err != nil {
			t.Fatal(err)
		}
		return m
	}
	state := func(id uuid.UUID) (status, body string) {
		var row struct {
			Status string `db:"status"`
			Body   string `db:"body"`
		}
		_ = migratorDB.GetContext(ctx, &row, `SELECT status, body FROM message_outbox WHERE id = $1`, id)
		return row.Status, row.Body
	}

	sent := queue()
	if err := svc.recordOutcome(ctx, sent, nil); err != nil {
		t.Fatal(err)
	}
	if status, body := state(sent.ID); status != "sent" || body != messageScrubbed {
		t.Fatalf("sent message: %s %q", status, body)
	}
	retry := queue()
	if err := svc.recordOutcome(ctx, retry, errors.New("timeout")); err != nil {
		t.Fatal(err)
	}
	if status, body := state(retry.ID); status != "pending" || body != "Kode 123456" {
		t.Fatalf("transient failure must retry with the body kept: %s %q", status, body)
	}
	final := queue()
	final.Attempts = len(messageRetrySchedule) + 1
	if err := svc.recordOutcome(ctx, final, errors.New("timeout")); err != nil {
		t.Fatal(err)
	}
	if status, body := state(final.ID); status != "failed" || body != messageScrubbed {
		t.Fatalf("final failure must scrub: %s %q", status, body)
	}
}

type recordingSMS struct{}

func (recordingSMS) Send(context.Context, sms.Message) error { return nil }
