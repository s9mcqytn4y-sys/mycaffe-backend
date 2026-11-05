package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/metrics"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/sms"
)

// SMS/WhatsApp delivery queue (decision V5), same durable pattern as email_outbox: enqueue inside the
// business transaction, deliver in the background, scrub the body once it is sent or failed.

// messageRetrySchedule is short on purpose: OTP messages are worthless after their 5-minute expiry.
var messageRetrySchedule = []time.Duration{15 * time.Second, 30 * time.Second, time.Minute}

const (
	messageLease    = time.Minute
	messageScrubbed = "[dihapus setelah pengiriman]"
)

var ErrSMSUnavailable = errors.New("sms delivery is not configured")

type MessageService struct {
	db      *sqlx.DB
	sender  sms.Sender // nil = disabled
	nowFunc func() time.Time
}

// NewMessageService works without a sender (SMS_DRIVER=disabled): Enabled reports false and flows that
// need SMS answer ErrSMSUnavailable.
func NewMessageService(db *sqlx.DB, sender sms.Sender) *MessageService {
	return &MessageService{db: db, sender: sender, nowFunc: time.Now}
}

func (s *MessageService) Enabled() bool { return s != nil && s.sender != nil }

// EnqueueSMSTx queues one SMS in the caller's tenant transaction.
func (s *MessageService) EnqueueSMSTx(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, toPhone, template, body, referenceID string) error {
	if !s.Enabled() {
		return ErrSMSUnavailable
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO message_outbox (store_id, channel, to_phone, template, body, reference_id, next_attempt_at)
		VALUES ($1, 'sms', $2, $3, $4, $5, $6)`, storeID, toPhone, template, body, referenceID, s.nowFunc().UTC())
	if err != nil {
		return fmt.Errorf("failed to enqueue sms: %w", err)
	}
	return nil
}

type queuedMessage struct {
	ID       uuid.UUID `db:"id"`
	StoreID  uuid.UUID `db:"store_id"`
	ToPhone  string    `db:"to_phone"`
	Template string    `db:"template"`
	Body     string    `db:"body"`
	Attempts int       `db:"attempts"`
}

// DeliverDue claims due messages per store (SKIP LOCKED), sends outside the transaction and records
// each outcome.
func (s *MessageService) DeliverDue(ctx context.Context, batchLimit int) (sent int, err error) {
	if !s.Enabled() {
		return 0, nil
	}
	var stores []uuid.UUID
	if err := s.db.SelectContext(ctx, &stores, `SELECT store_id FROM list_stores_with_due_message()`); err != nil {
		return 0, fmt.Errorf("failed to list stores with due messages: %w", err)
	}
	for _, storeID := range stores {
		var claimed []queuedMessage
		now := s.nowFunc().UTC()
		if err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
			return tx.SelectContext(ctx, &claimed, `
				UPDATE message_outbox m SET status = 'sending', attempts = m.attempts + 1, locked_until = $3
				WHERE m.id IN (
					SELECT id FROM message_outbox WHERE store_id = $1
					  AND ((status = 'pending' AND next_attempt_at <= $2) OR (status = 'sending' AND locked_until <= $2))
					ORDER BY next_attempt_at LIMIT $4 FOR UPDATE SKIP LOCKED)
				RETURNING id, store_id, to_phone, template, body, attempts`, storeID, now, now.Add(messageLease), batchLimit)
		}); err != nil {
			log.Printf("[SMS WORKER] store_id=%s claim error: %v", storeID, err)
			continue
		}
		for _, m := range claimed {
			sendErr := s.sender.Send(ctx, sms.Message{To: m.ToPhone, Body: m.Body})
			if err := s.recordOutcome(ctx, m, sendErr); err != nil {
				log.Printf("[SMS WORKER] message_id=%s outcome error: %v", m.ID, err)
				continue
			}
			if sendErr == nil {
				sent++
			}
		}
	}
	return sent, nil
}

func (s *MessageService) recordOutcome(ctx context.Context, m queuedMessage, sendErr error) error {
	now := s.nowFunc().UTC()
	return database.WithTenantTx(ctx, s.db, m.StoreID, func(tx *sqlx.Tx) error {
		var err error
		switch {
		case sendErr == nil:
			_, err = tx.ExecContext(ctx, `UPDATE message_outbox SET status = 'sent', sent_at = $3, locked_until = NULL, last_error = '', body = $4
				WHERE store_id = $1 AND id = $2`, m.StoreID, m.ID, now, messageScrubbed)
		case errors.Is(sendErr, sms.ErrPermanent) || m.Attempts > len(messageRetrySchedule):
			log.Printf("[SMS WORKER] message_id=%s template=%s failed after %d attempt(s): %v", m.ID, m.Template, m.Attempts, sendErr)
			_, err = tx.ExecContext(ctx, `UPDATE message_outbox SET status = 'failed', locked_until = NULL, last_error = left($3, 500), body = $4
				WHERE store_id = $1 AND id = $2`, m.StoreID, m.ID, sendErr.Error(), messageScrubbed)
		default:
			_, err = tx.ExecContext(ctx, `UPDATE message_outbox SET status = 'pending', next_attempt_at = $3, locked_until = NULL, last_error = left($4, 500)
				WHERE store_id = $1 AND id = $2`, m.StoreID, m.ID, now.Add(messageRetrySchedule[m.Attempts-1]), sendErr.Error())
		}
		return err
	})
}

// StartWorker polls the queue until ctx is cancelled.
func (s *MessageService) StartWorker(ctx context.Context, interval time.Duration) {
	if !s.Enabled() {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.DeliverDue(ctx, 20); err != nil {
				if !errors.Is(err, context.Canceled) {
					log.Printf("[SMS WORKER] cycle error: %v", err)
				}
			} else {
				metrics.WorkerSucceeded("sms")
			}
		}
	}
}
