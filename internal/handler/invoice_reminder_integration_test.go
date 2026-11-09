package handler_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/config"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/sms"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/service"
)

type nopWhatsApp struct{}

func (nopWhatsApp) Send(context.Context, sms.Message) error { return nil }

// TestInvoiceRemindersIntegration covers v0.11.1 PR 11.10 (X18): D-3, due date and D+7 reminders go out once
// by e-mail and WhatsApp, inside business hours, and never for other days or paid invoices.
func TestInvoiceRemindersIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping invoice reminder integration test")
	}
	db, err := database.NewPostgresDB(&config.Config{DatabaseURL: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	storeID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	inStore := func(fn func(tx *sqlx.Tx) error) {
		t.Helper()
		if err := database.WithTenantTx(ctx, db, storeID, fn); err != nil {
			t.Fatalf("tenant tx failed: %v", err)
		}
	}

	var ownerID uuid.UUID
	customerID := uuid.New()
	base := int(time.Now().UnixNano()%1_000_000) + 1_000_000
	inStore(func(tx *sqlx.Tx) error {
		if err := tx.GetContext(ctx, &ownerID, `SELECT id FROM users WHERE store_id = $1 AND role = 'owner' LIMIT 1`, storeID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO business_customers (id, store_id, name, email, phone) VALUES ($1, $2, $3, 'ap@example.com', '+6281234567890')`,
			customerID, storeID, "PT Pengingat "+customerID.String()[:6])
		return err
	})
	invoice := func(offset int, due string, paid int64) uuid.UUID {
		t.Helper()
		id := uuid.New()
		inStore(func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO invoices (id, store_id, number, business_customer_id, customer_name, total_amount, paid_amount, due_date, created_by, issued_at)
				VALUES ($1, $2, $3, $4, 'PT Pengingat', 100000, $5, $6::date, $7, NOW())`, id, storeID, base+offset, customerID, paid, due, ownerID)
			return err
		})
		return id
	}
	dMinus3, dueToday, dPlus7 := invoice(0, "2030-01-13", 40000), invoice(1, "2030-01-10", 0), invoice(2, "2030-01-03", 0)
	otherDay, paid := invoice(3, "2030-01-11", 0), invoice(4, "2030-01-10", 100000)

	messages := service.NewMessageService(db, nil)
	messages.SetWhatsApp(nopWhatsApp{})
	svc := service.NewInvoiceReminderService(db, service.NewEmailService(db, nil), messages)
	wib := time.FixedZone("WIB", 7*3600)

	count := func(table string, id uuid.UUID) (n int) {
		t.Helper()
		inStore(func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &n, fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE store_id = $1 AND reference_id = $2`, table), storeID, id.String())
		})
		return n
	}

	if _, err := svc.SendDue(ctx, time.Date(2030, 1, 10, 5, 0, 0, 0, wib).UTC()); err != nil {
		t.Fatal(err)
	}
	if count("email_outbox", dueToday) != 0 {
		t.Fatal("no reminders before business hours")
	}

	for i := 0; i < 2; i++ { // the second run proves a stage goes out once
		if _, err := svc.SendDue(ctx, time.Date(2030, 1, 10, 10, 0, 0, 0, wib).UTC()); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []uuid.UUID{dMinus3, dueToday, dPlus7} {
		if e, w := count("email_outbox", id), count("message_outbox", id); e != 1 || w != 1 {
			t.Fatalf("invoice %s: expected one e-mail and one WhatsApp, got %d and %d", id, e, w)
		}
	}
	for _, id := range []uuid.UUID{otherDay, paid} {
		if e, w := count("email_outbox", id), count("message_outbox", id); e != 0 || w != 0 {
			t.Fatalf("invoice %s must get no reminder, got %d and %d", id, e, w)
		}
	}
	var template, body string
	var stage string
	inStore(func(tx *sqlx.Tx) error {
		if err := tx.QueryRowxContext(ctx, `SELECT template, body FROM message_outbox WHERE store_id = $1 AND reference_id = $2`, storeID, dMinus3.String()).Scan(&template, &body); err != nil {
			return err
		}
		return tx.GetContext(ctx, &stage, `SELECT stage FROM invoice_reminders WHERE store_id = $1 AND invoice_id = $2`, storeID, dMinus3)
	})
	// The reminder asks for what is left: 100.000 - 40.000.
	if template != service.WhatsAppInvoiceTemplate || stage != "d_minus_3" || len(strings.Split(body, "\n")) != 5 ||
		!strings.Contains(body, "60.000") || !strings.Contains(body, "13/01/2030") {
		t.Fatalf("unexpected reminder: %s %s %q", template, stage, body)
	}
}
