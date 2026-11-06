package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

// Owner review of offline submissions (ADR-005 OF9). Accept re-processes the stored order with the
// device-side checks waived (expired verifier, limits, revoked device) by the owner's decision; pricing,
// stock and the shift still apply. Reject closes it with a reason. Both are audited.

type OfflineSubmission struct {
	ID             uuid.UUID       `json:"id" db:"id"`
	DevicePrefix   string          `json:"device_prefix" db:"device_prefix"`
	ClientOrderID  uuid.UUID       `json:"client_order_id" db:"client_order_id"`
	Status         string          `json:"status" db:"status"`
	Reason         *string         `json:"reason,omitempty" db:"reason"`
	OrderID        *uuid.UUID      `json:"order_id,omitempty" db:"order_id"`
	ResolvedAt     *time.Time      `json:"resolved_at,omitempty" db:"resolved_at"`
	ResolutionNote *string         `json:"resolution_note,omitempty" db:"resolution_note"`
	CreatedAt      time.Time       `json:"created_at" db:"created_at"`
	Payload        json.RawMessage `json:"payload" db:"payload"`
}

type ResolveSubmissionInput struct {
	Decision string `json:"decision"` // accept | reject
	Note     string `json:"note"`
}

// ListSubmissions returns submissions by status (default rejected = waiting for the owner), newest first.
func (s *OfflineSyncService) ListSubmissions(ctx context.Context, actor *AuthenticatedStaff, status string) ([]OfflineSubmission, error) {
	if err := requireDeviceManage(actor); err != nil {
		return nil, err
	}
	if status == "" {
		status = "rejected"
	}
	f := fieldErrors{}
	f.oneOf("status", status, "accepted", "rejected", "resolved_accepted", "resolved_rejected")
	if err := f.err(); err != nil {
		return nil, err
	}
	out := []OfflineSubmission{}
	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `SELECT s.id, d.prefix AS device_prefix, s.client_order_id, s.status, s.reason, s.order_id,
				s.resolved_at, s.resolution_note, s.created_at, s.payload
			FROM offline_order_submissions s JOIN devices d ON d.store_id = s.store_id AND d.id = s.device_id
			WHERE s.store_id = $1 AND s.status = $2 ORDER BY s.created_at DESC LIMIT 200`, actor.Staff.Store.ID, status)
	})
	return out, err
}

// ResolveSubmission accepts or rejects a rejected submission. An accept that still fails (unknown
// product, closed shift, stock) answers 422 with the reason and leaves the submission waiting.
func (s *OfflineSyncService) ResolveSubmission(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID, in ResolveSubmissionInput) (*OfflineSyncResult, error) {
	if err := requireDeviceManage(actor); err != nil {
		return nil, err
	}
	note := strings.TrimSpace(in.Note)
	f := fieldErrors{}
	f.oneOf("decision", in.Decision, "accept", "reject")
	if in.Decision == "reject" && note == "" {
		f.add("note", "alasan penolakan wajib diisi")
	}
	if len(note) > 500 {
		f.add("note", "maksimal 500 karakter")
	}
	if err := f.err(); err != nil {
		return nil, err
	}
	storeID := actor.Staff.Store.ID
	var res *OfflineSyncResult
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		var row struct {
			DeviceID uuid.UUID `db:"device_id"`
			Status   string    `db:"status"`
			Payload  []byte    `db:"payload"`
		}
		err := tx.GetContext(ctx, &row, `SELECT device_id, status, payload FROM offline_order_submissions WHERE store_id = $1 AND id = $2 FOR UPDATE`, storeID, id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSubmissionNotFound
		}
		if err != nil {
			return err
		}
		if row.Status != "rejected" {
			return ErrSubmissionResolved
		}
		var o OfflineOrderInput
		if err := json.Unmarshal(row.Payload, &o); err != nil {
			return fmt.Errorf("stored offline payload is unreadable: %w", err)
		}
		now := s.nowFunc().UTC()
		status := "resolved_rejected"
		var orderID *uuid.UUID
		res = &OfflineSyncResult{ClientOrderID: o.ClientOrderID, Status: status}
		if in.Decision == "accept" {
			order, reason, err := s.placeWithSavepoint(ctx, tx, storeID, row.DeviceID, o, now)
			if err != nil {
				return err
			}
			if order == nil {
				return &ValidationError{Fields: map[string]string{"decision": "belum bisa diterima: " + reason}}
			}
			status, orderID = "resolved_accepted", &order.ID
			res = &OfflineSyncResult{ClientOrderID: o.ClientOrderID, Status: status, OrderID: orderID, OrderNumber: order.OrderNumber, QueueNumber: order.QueueNumber}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE offline_order_submissions SET status = $3, order_id = $4, resolved_by = $5, resolved_at = $6,
			resolution_note = NULLIF($7, '') WHERE store_id = $1 AND id = $2`, storeID, id, status, orderID, actor.Staff.UserID, now, note); err != nil {
			return err
		}
		actorID := actor.Staff.UserID
		meta, _ := json.Marshal(map[string]any{"submission_id": id, "decision": in.Decision, "order_id": orderID, "client_order_id": o.ClientOrderID})
		return postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{StoreID: storeID, ActorID: &actorID, ActorName: actor.Staff.FullName,
			Action: "OFFLINE_ORDER_RESOLVED", Reason: truncate("Pesanan offline "+o.OfflineQueueNumber+" diputuskan owner: "+in.Decision+" "+note, 500),
			ReferenceID: o.ClientOrderID.String(), Metadata: string(meta), CreatedAt: now})
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}
