package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/mailer"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/metrics"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
)

// emailRetrySchedule is the wait after each failed attempt; after the last entry the email is
// marked failed (6 attempts over roughly 10.5 hours, owner decision E5).
var emailRetrySchedule = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 8 * time.Hour}

const emailLease = 10 * time.Minute

// EmailService renders templates into the durable email_outbox and delivers them in the background.
type EmailService struct {
	db       *sqlx.DB
	repo     *postgres.EmailRepository
	authRepo *postgres.AuthRepository
	sender   mailer.Sender
	nowFunc  func() time.Time
}

func NewEmailService(db *sqlx.DB, sender mailer.Sender) *EmailService {
	return &EmailService{
		db:       db,
		repo:     postgres.NewEmailRepository(),
		authRepo: postgres.NewAuthRepository(),
		sender:   sender,
		nowFunc:  time.Now,
	}
}

// EnqueueTx renders a template and queues it inside the caller's tenant transaction, so the
// email exists only if the business change commits.
func (s *EmailService) EnqueueTx(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, template, toEmail, toName, referenceID string, data mailer.Data) error {
	if s == nil {
		return nil
	}
	slug, name, err := s.authRepo.GetStoreProfile(ctx, tx, storeID)
	if err != nil {
		return err
	}
	data.StoreName, data.StoreSlug, data.RecipientName = name, slug, toName
	if data.Year == 0 {
		data.Year = s.nowFunc().Year()
	}
	rendered, err := mailer.Render(template, data)
	if err != nil {
		return err
	}
	return s.repo.EnqueueEmail(ctx, tx, &domain.EmailOutbox{
		StoreID: storeID, Template: template, ToEmail: toEmail, ToName: toName,
		Subject: rendered.Subject, HTMLBody: rendered.HTML, TextBody: rendered.Text,
		ReplyTo: data.ReplyTo, ReferenceID: referenceID,
	})
}

// DeliverDue sends every due email once: discover stores, claim a batch per store under RLS,
// send outside the transaction, then record the outcome.
func (s *EmailService) DeliverDue(ctx context.Context, batchLimit int) (sent int, err error) {
	storeIDs, err := s.repo.ListStoresWithDueEmail(ctx, s.db)
	if err != nil {
		return 0, err
	}
	for _, storeID := range storeIDs {
		var claimed []domain.EmailOutbox
		if err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
			var err error
			claimed, err = s.repo.ClaimDueEmails(ctx, tx, storeID, s.nowFunc().UTC(), emailLease, batchLimit)
			return err
		}); err != nil {
			log.Printf("[EMAIL WORKER] store_id=%s claim error: %v", storeID, err)
			continue
		}
		for _, e := range claimed {
			sendErr := s.sender.Send(ctx, mailer.Message{
				To: e.ToEmail, ToName: e.ToName, ReplyTo: e.ReplyTo, Subject: e.Subject, HTML: e.HTMLBody, Text: e.TextBody,
			})
			if err := s.recordOutcome(ctx, e, sendErr); err != nil {
				log.Printf("[EMAIL WORKER] store_id=%s email_id=%s outcome error: %v", storeID, e.ID, err)
				continue
			}
			if sendErr == nil {
				sent++
			}
		}
	}
	return sent, nil
}

func (s *EmailService) recordOutcome(ctx context.Context, e domain.EmailOutbox, sendErr error) error {
	now := s.nowFunc().UTC()
	return database.WithTenantTx(ctx, s.db, e.StoreID, func(tx *sqlx.Tx) error {
		switch {
		case sendErr == nil:
			return s.repo.MarkEmailSent(ctx, tx, e.StoreID, e.ID, now)
		case errors.Is(sendErr, mailer.ErrPermanent) || e.Attempts > len(emailRetrySchedule):
			log.Printf("[EMAIL WORKER] email_id=%s template=%s failed permanently after %d attempt(s): %v", e.ID, e.Template, e.Attempts, sendErr)
			metrics.EmailSendFailures.WithLabelValues("true").Inc()
			return s.repo.MarkEmailFailed(ctx, tx, e.StoreID, e.ID, sendErr.Error())
		default:
			metrics.EmailSendFailures.WithLabelValues("false").Inc()
			next := now.Add(emailRetrySchedule[e.Attempts-1])
			return s.repo.MarkEmailRetry(ctx, tx, e.StoreID, e.ID, next, sendErr.Error())
		}
	})
}

// StartWorker polls the outbox until ctx is cancelled.
func (s *EmailService) StartWorker(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.DeliverDue(ctx, 20); err != nil {
				if !errors.Is(err, context.Canceled) {
					log.Printf("[EMAIL WORKER] cycle error: %v", err)
				}
			} else {
				metrics.WorkerSucceeded("email")
			}
		}
	}
}

// formatLocalTime renders times for Indonesian email copy, e.g. "2 Nov 2026 10:00 WIB".
func formatLocalTime(t time.Time) string {
	months := [...]string{"Jan", "Feb", "Mar", "Apr", "Mei", "Jun", "Jul", "Agu", "Sep", "Okt", "Nov", "Des"}
	// shortcut: fixed WIB (UTC+7); use the store timezone once stores outside Java/Sumatra onboard.
	local := t.In(time.FixedZone("WIB", 7*3600))
	return fmt.Sprintf("%d %s %d %02d:%02d WIB", local.Day(), months[local.Month()-1], local.Year(), local.Hour(), local.Minute())
}
