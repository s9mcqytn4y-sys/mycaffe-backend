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
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/realtime"
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
		poison := emit("test.poison")
		failing := &recordingPublisher{fail: true}
		for i := 0; i < relayMaxAttempts; i++ {
			if err := svc.relayStore(ctx, failing, storeID); err == nil {
				t.Fatal("failing publisher must fail the store batch")
			} else {
				svc.recordRelayFailure(ctx, storeID, err)
			}
		}
		var row struct {
			Retries int        `db:"retry_count"`
			Dead    *time.Time `db:"dead_at"`
			Done    *time.Time `db:"processed_at"`
			LastErr *string    `db:"last_error"`
		}
		if err := migratorDB.GetContext(ctx, &row, `SELECT retry_count, dead_at, processed_at, last_error FROM outbox_events WHERE id = $1`, poison); err != nil {
			t.Fatal(err)
		}
		if row.Retries != relayMaxAttempts || row.Dead == nil || row.Done == nil || row.LastErr == nil || *row.LastErr != "hub unavailable" {
			t.Fatalf("poison event after %d failures: %+v", relayMaxAttempts, row)
		}
		emit("test.after")
		ok := &recordingPublisher{}
		if err := svc.relayStore(ctx, ok, storeID); err != nil {
			t.Fatal(err)
		}
		if len(ok.got) != 1 || ok.got[0] != "test.after" {
			t.Fatalf("only the newer event must be relayed, got %v", ok.got)
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
