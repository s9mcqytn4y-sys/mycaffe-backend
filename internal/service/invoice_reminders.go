package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/mailer"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/metrics"
)

// WhatsAppInvoiceTemplate is the outbox template and the approved WhatsApp template of invoice reminders.
const WhatsAppInvoiceTemplate = "tagihan_invoice"

// invoiceReminderStartHour keeps reminders inside business hours of the store (08:00 onwards).
const invoiceReminderStartHour = 8

var invoiceReminderStages = map[string]string{
	"d_minus_3": "akan jatuh tempo 3 hari lagi",
	"due":       "jatuh tempo hari ini",
	"d_plus_7":  "sudah lewat 7 hari dari jatuh tempo",
}

// InvoiceReminderService sends the automatic reminders of X18 through the e-mail and WhatsApp outboxes.
type InvoiceReminderService struct {
	db       *sqlx.DB
	email    *EmailService
	messages *MessageService
	nowFunc  func() time.Time
}

func NewInvoiceReminderService(db *sqlx.DB, email *EmailService, messages *MessageService) *InvoiceReminderService {
	return &InvoiceReminderService{db: db, email: email, messages: messages, nowFunc: time.Now}
}

type dueInvoiceReminder struct {
	ID        uuid.UUID `db:"id"`
	Number    int       `db:"number"`
	Remaining int64     `db:"remaining"`
	DueDate   string    `db:"due_date"`
	Stage     string    `db:"stage"`
	Name      string    `db:"name"`
	Email     string    `db:"email"`
	Phone     string    `db:"phone"`
}

// SendDue queues the reminders due at now in every store with open invoices. A stage is sent on its exact day,
// once; shortcut: a day the worker is down all day is skipped, add catch-up if that ever costs a reminder.
func (s *InvoiceReminderService) SendDue(ctx context.Context, now time.Time) (int, error) {
	var stores []uuid.UUID
	if err := s.db.SelectContext(ctx, &stores, `SELECT store_id FROM list_stores_with_open_invoices()`); err != nil {
		return 0, fmt.Errorf("failed to list stores with open invoices: %w", err)
	}
	sent := 0
	for _, storeID := range stores {
		n, err := s.sendForStore(ctx, storeID, now)
		if err != nil {
			log.Printf("[INVOICE REMINDER] store_id=%s error: %v", storeID, err)
		}
		sent += n
	}
	return sent, nil
}

func (s *InvoiceReminderService) sendForStore(ctx context.Context, storeID uuid.UUID, now time.Time) (int, error) {
	var storeName, tz string
	var due []dueInvoiceReminder
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		if err := tx.QueryRowxContext(ctx, `SELECT name, timezone FROM stores WHERE id = $1`, storeID).Scan(&storeName, &tz); err != nil {
			return err
		}
		loc, err := time.LoadLocation(tz)
		if err != nil {
			loc = time.FixedZone("WIB", 7*3600)
		}
		local := now.In(loc)
		if local.Hour() < invoiceReminderStartHour {
			return nil
		}
		return tx.SelectContext(ctx, &due, `SELECT i.id, i.number, i.total_amount - i.paid_amount AS remaining, to_char(i.due_date, 'DD/MM/YYYY') AS due_date,
			CASE i.due_date - $2::date WHEN 3 THEN 'd_minus_3' WHEN 0 THEN 'due' ELSE 'd_plus_7' END AS stage, i.customer_name AS name, c.email, c.phone
			FROM invoices i JOIN business_customers c ON c.store_id = i.store_id AND c.id = i.business_customer_id
			WHERE i.store_id = $1 AND i.paid_amount < i.total_amount AND i.due_date IN ($2::date + 3, $2::date, $2::date - 7)
			AND NOT EXISTS (SELECT 1 FROM invoice_reminders r WHERE r.store_id = i.store_id AND r.invoice_id = i.id
				AND r.stage = CASE i.due_date - $2::date WHEN 3 THEN 'd_minus_3' WHEN 0 THEN 'due' ELSE 'd_plus_7' END)
			ORDER BY i.number`, storeID, local.Format(time.DateOnly))
	})
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, r := range due {
		// One transaction per reminder: a bad address never holds back the others.
		if err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
			return s.queueReminderTx(ctx, tx, storeID, storeName, r, now)
		}); err != nil {
			log.Printf("[INVOICE REMINDER] invoice_id=%s stage=%s error: %v", r.ID, r.Stage, err)
			continue
		}
		sent++
	}
	return sent, nil
}

func (s *InvoiceReminderService) queueReminderTx(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, storeName string, r dueInvoiceReminder, now time.Time) error {
	var channels []string
	useEmail, useWhatsApp := r.Email != "" && s.email != nil, r.Phone != "" && s.messages.WhatsAppEnabled()
	if useEmail {
		channels = append(channels, "email")
	}
	if useWhatsApp {
		channels = append(channels, "whatsapp")
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO invoice_reminders (store_id, invoice_id, stage, channels, created_at) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (store_id, invoice_id, stage) DO NOTHING`, storeID, r.ID, r.Stage, strings.Join(channels, ","), now)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	number, status, amount := invoiceDocumentNumber(r.Number), invoiceReminderStages[r.Stage], rupiah(r.Remaining)
	if useEmail {
		if err := s.email.EnqueueTx(ctx, tx, storeID, mailer.TemplateInvoiceReminder, r.Email, r.Name, r.ID.String(), mailer.Data{
			InvoiceNumber: number, TotalLabel: amount, DueDate: r.DueDate, DueStatus: status}); err != nil {
			return err
		}
	}
	if useWhatsApp {
		// Template parameters, one per line: {{1}} store, {{2}} invoice, {{3}} amount, {{4}} status, {{5}} due date.
		body := strings.Join([]string{storeName, number, amount, status, r.DueDate}, "\n")
		if err := s.messages.EnqueueWhatsAppTx(ctx, tx, storeID, r.Phone, WhatsAppInvoiceTemplate, body, r.ID.String()); err != nil {
			return err
		}
	}
	return nil
}

// StartWorker runs SendDue until ctx is cancelled.
func (s *InvoiceReminderService) StartWorker(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.SendDue(ctx, s.nowFunc().UTC()); err != nil {
				if !errors.Is(err, context.Canceled) {
					log.Printf("[INVOICE REMINDER] cycle error: %v", err)
				}
			} else {
				metrics.WorkerSucceeded("invoice_reminder")
			}
		}
	}
}
