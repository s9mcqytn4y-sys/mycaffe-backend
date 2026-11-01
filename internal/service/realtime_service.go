package service

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/realtime"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

// Outbox relay and WebSocket tickets (iteration 5c; decisions N1, QR2; risk R2).

const (
	wsTicketTTL       = 30 * time.Second
	maxReplayEvents   = 500
	relayBatchSize    = 100
	relayPollInterval = 2 * time.Second
)

var ErrInvalidWSTicket = errors.New("websocket ticket is invalid, used or expired")

// Publisher is the realtime hub as the relay sees it.
type Publisher interface {
	Publish(msg *realtime.EventMessage) error
}

type RealtimeService struct {
	db      *sqlx.DB
	guests  *postgres.GuestRepository
	nowFunc func() time.Time
}

func NewRealtimeService(db *sqlx.DB) *RealtimeService {
	return &RealtimeService{db: db, guests: postgres.NewGuestRepository(), nowFunc: time.Now}
}

// WSTicket is returned to the client, which opens GET /ws?ticket=...&last_seq=N within 30 s.
type WSTicket struct {
	Ticket    string    `json:"ticket"`
	ExpiresAt time.Time `json:"expires_at"`
	Channels  []string  `json:"channels"`
}

// staffChannels derives what a staff member may watch from RBAC: the POS stream needs order reads,
// each KDS station stream needs that station's KDS read permission (C3 toggle included).
func staffChannels(actor *AuthenticatedStaff) []string {
	var out []string
	if actor.Can(rbac.OrderReadStore) {
		out = append(out, realtime.ChannelPOS)
	}
	if actor.Can(rbac.KDSBaristaRead) {
		out = append(out, realtime.ChannelKDSBar)
	}
	if actor.Can(rbac.KDSKitchenRead) {
		out = append(out, realtime.ChannelKDSKitchen)
	}
	return out
}

func (s *RealtimeService) IssueStaffTicket(ctx context.Context, actor *AuthenticatedStaff) (*WSTicket, error) {
	if actor == nil {
		return nil, ErrInvalidSession
	}
	channels := staffChannels(actor)
	if len(channels) == 0 {
		return nil, ErrForbidden
	}
	userID, sessionID := actor.Staff.UserID, actor.SessionID
	return s.issue(ctx, actor.Staff.Store.ID, channels, &userID, &sessionID, nil)
}

func (s *RealtimeService) IssueGuestTicket(ctx context.Context, g *GuestIdentity) (*WSTicket, error) {
	if g == nil {
		return nil, ErrGuestSessionInvalid
	}
	return s.issue(ctx, g.StoreID, []string{realtime.ChannelGuestPrefix + g.SessionID.String()}, nil, nil, &g.SessionID)
}

func (s *RealtimeService) issue(ctx context.Context, storeID uuid.UUID, channels []string, userID, authSessionID, guestSessionID *uuid.UUID) (*WSTicket, error) {
	secret, hash, err := randomToken()
	if err != nil {
		return nil, err
	}
	id, now := uuid.New(), s.nowFunc().UTC()
	expires := now.Add(wsTicketTTL)
	err = database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO ws_tickets (id, store_id, token_hash, user_id, auth_session_id, guest_session_id, channels, created_at, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7::text[], $8, $9)
		`, id, storeID, hash, userID, authSessionID, guestSessionID, channels, now, expires)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("failed to issue websocket ticket: %w", err)
	}
	return &WSTicket{Ticket: storeID.String() + "." + id.String() + "." + secret, ExpiresAt: expires, Channels: channels}, nil
}

// Redeem consumes a ticket once and re-checks its subject: the staff session must still be live
// and the user active, or the guest session still valid.
func (s *RealtimeService) Redeem(ctx context.Context, ticket string) (uuid.UUID, []string, error) {
	parts := strings.SplitN(ticket, ".", 3)
	if len(parts) != 3 {
		return uuid.Nil, nil, ErrInvalidWSTicket
	}
	storeID, err1 := uuid.Parse(parts[0])
	ticketID, err2 := uuid.Parse(parts[1])
	if err1 != nil || err2 != nil || storeID == uuid.Nil {
		return uuid.Nil, nil, ErrInvalidWSTicket
	}
	sum := sha256.Sum256([]byte(parts[2]))
	var channels []string
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		var row struct {
			TokenHash      []byte     `db:"token_hash"`
			AuthSessionID  *uuid.UUID `db:"auth_session_id"`
			UserID         *uuid.UUID `db:"user_id"`
			GuestSessionID *uuid.UUID `db:"guest_session_id"`
			Channels       string     `db:"channels"`
			ExpiresAt      time.Time  `db:"expires_at"`
			UsedAt         *time.Time `db:"used_at"`
		}
		err := tx.GetContext(ctx, &row, `SELECT token_hash, auth_session_id, user_id, guest_session_id, array_to_string(channels, ',') AS channels, expires_at, used_at
			FROM ws_tickets WHERE store_id = $1 AND id = $2 FOR UPDATE`, storeID, ticketID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvalidWSTicket
		}
		if err != nil {
			return err
		}
		now := s.nowFunc().UTC()
		if subtle.ConstantTimeCompare(row.TokenHash, sum[:]) != 1 || row.UsedAt != nil || !now.Before(row.ExpiresAt) {
			return ErrInvalidWSTicket
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ws_tickets SET used_at = $3 WHERE store_id = $1 AND id = $2`, storeID, ticketID, now); err != nil {
			return err
		}
		if row.GuestSessionID != nil {
			g, err := s.guests.GetGuestSession(ctx, tx, storeID, *row.GuestSessionID, false)
			if err != nil || g.EndedAt != nil || !now.Before(g.ExpiresAt) || g.TableSessionStatus != "open" {
				return ErrInvalidWSTicket
			}
		} else {
			var live bool
			if err := tx.GetContext(ctx, &live, `
				SELECT EXISTS (
					SELECT 1 FROM auth_sessions a JOIN users u ON u.store_id = a.store_id AND u.id = a.user_id
					WHERE a.store_id = $1 AND a.id = $2 AND a.user_id = $3
					  AND a.revoked_at IS NULL AND a.expires_at > $4 AND u.is_active)`,
				storeID, row.AuthSessionID, row.UserID, now); err != nil {
				return err
			}
			if !live {
				return ErrInvalidWSTicket
			}
		}
		channels = strings.Split(row.Channels, ",")
		return nil
	})
	if err != nil {
		// A rejected ticket is still marked used only if the transaction committed; on rejection it rolled back,
		// which is fine: an invalid ticket stays invalid.
		return uuid.Nil, nil, err
	}
	return storeID, channels, nil
}

// outboxRow is an outbox event joined with the order it is about (all aggregates are orders today).
type outboxRow struct {
	ID             uuid.UUID       `db:"id"`
	StoreID        uuid.UUID       `db:"store_id"`
	StoreSeq       int64           `db:"store_seq"`
	EventType      string          `db:"event_type"`
	Payload        json.RawMessage `db:"payload"`
	GuestSessionID *uuid.UUID      `db:"guest_session_id"`
	OrderID        *uuid.UUID      `db:"order_id"`
	OrderNumber    *string         `db:"order_number"`
	QueueNumber    *string         `db:"queue_number"`
	Status         *string         `db:"status"`
	PaymentStatus  *string         `db:"payment_status"`
}

const outboxSelect = `
	SELECT e.id, e.store_id, e.store_seq, e.event_type, e.payload,
		o.guest_session_id, o.id AS order_id, o.order_number, o.queue_number, o.status, o.payment_status
	FROM outbox_events e
	LEFT JOIN orders o ON e.aggregate_type = 'order' AND o.store_id = e.store_id AND o.id::text = e.aggregate_id`

// kdsEvents reach the station screens; everything reaches the POS channel.
var kdsEvents = map[string]bool{
	"order.paid": true, "order.round_submitted": true, "order.cancelled": true, "order.expired": true,
}

// messages turns one event into what each audience may see: staff get the full payload; the guest
// who owns the order gets only its public status (never totals of other guests or staff data).
func (r outboxRow) messages() []*realtime.EventMessage {
	store := r.StoreID.String()
	staff := []string{realtime.ChannelPOS}
	if kdsEvents[r.EventType] || strings.HasPrefix(r.EventType, "kds.") {
		staff = append(staff, realtime.ChannelKDSBar, realtime.ChannelKDSKitchen)
	}
	out := []*realtime.EventMessage{{Seq: r.StoreSeq, Type: r.EventType, StoreID: store, Payload: r.Payload, Channels: staff}}
	if r.GuestSessionID != nil && (strings.HasPrefix(r.EventType, "order.") || strings.HasPrefix(r.EventType, "payment.")) {
		out = append(out, &realtime.EventMessage{
			Seq: r.StoreSeq, Type: r.EventType, StoreID: store,
			Channels: []string{realtime.ChannelGuestPrefix + r.GuestSessionID.String()},
			Payload: map[string]any{
				"order_id": r.OrderID, "order_number": r.OrderNumber, "queue_number": r.QueueNumber,
				"status": r.Status, "payment_status": r.PaymentStatus,
			},
		})
	}
	return out
}

// Replay returns the events after lastSeq that the given channels may see, or a single
// resync_required message when more than 500 were missed (the client reloads over REST).
func (s *RealtimeService) Replay(ctx context.Context, storeID uuid.UUID, channels []string, lastSeq int64) ([]*realtime.EventMessage, error) {
	if lastSeq < 0 {
		return nil, nil
	}
	var rows []outboxRow
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows, outboxSelect+`
			WHERE e.store_id = $1 AND e.store_seq > $2 ORDER BY e.store_seq LIMIT $3`, storeID, lastSeq, maxReplayEvents+1)
	})
	if err != nil {
		return nil, err
	}
	if len(rows) > maxReplayEvents {
		return []*realtime.EventMessage{{Type: "resync_required", StoreID: storeID.String(), Payload: map[string]any{"last_seq": lastSeq}}}, nil
	}
	want := map[string]bool{}
	for _, ch := range channels {
		want[ch] = true
	}
	var out []*realtime.EventMessage
	for _, row := range rows {
		for _, m := range row.messages() {
			for _, ch := range m.Channels {
				if want[ch] {
					out = append(out, m)
					break
				}
			}
		}
	}
	return out, nil
}

// RelayOnce publishes pending events of every store in sequence order and marks them processed.
// Delivery is at-least-once: a crash after publishing re-sends, and clients deduplicate by seq.
func (s *RealtimeService) RelayOnce(ctx context.Context, hub Publisher) error {
	var stores []uuid.UUID
	if err := s.db.SelectContext(ctx, &stores, `SELECT store_id FROM list_stores_with_pending_outbox($1)`, relayBatchSize); err != nil {
		return fmt.Errorf("failed to list stores with pending events: %w", err)
	}
	for _, storeID := range stores {
		err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
			var rows []outboxRow
			if err := tx.SelectContext(ctx, &rows, outboxSelect+`
				WHERE e.store_id = $1 AND e.processed_at IS NULL
				ORDER BY e.store_seq LIMIT $2
				FOR UPDATE OF e SKIP LOCKED`, storeID, relayBatchSize); err != nil {
				return err
			}
			ids := make([]string, 0, len(rows))
			for _, row := range rows {
				for _, m := range row.messages() {
					if err := hub.Publish(m); err != nil {
						return err
					}
				}
				ids = append(ids, row.ID.String())
			}
			if len(ids) == 0 {
				return nil
			}
			_, err := tx.ExecContext(ctx, `UPDATE outbox_events SET processed_at = $3 WHERE store_id = $1 AND id = ANY($2::uuid[])`,
				storeID, ids, s.nowFunc().UTC())
			return err
		})
		if err != nil {
			return fmt.Errorf("relay store %s: %w", storeID, err)
		}
	}
	return nil
}

// RunRelay wakes on LISTEN outbox (fired by the sequence trigger) and polls every 2 s as a fallback.
func (s *RealtimeService) RunRelay(ctx context.Context, hub Publisher) {
	notify := make(chan struct{}, 1)
	go s.listen(ctx, notify)
	ticker := time.NewTicker(relayPollInterval)
	defer ticker.Stop()
	for {
		if err := s.RelayOnce(ctx, hub); err != nil && ctx.Err() == nil {
			log.Printf("[RELAY] %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-notify:
		}
	}
}

// listen holds one pooled connection for LISTEN; on any error it backs off and polling covers the gap.
func (s *RealtimeService) listen(ctx context.Context, notify chan<- struct{}) {
	for ctx.Err() == nil {
		err := func() error {
			conn, err := s.db.Conn(ctx)
			if err != nil {
				return err
			}
			defer conn.Close()
			return conn.Raw(func(driverConn any) error {
				pgxConn := driverConn.(*stdlib.Conn).Conn()
				if _, err := pgxConn.Exec(ctx, "LISTEN outbox"); err != nil {
					return err
				}
				for {
					if _, err := pgxConn.WaitForNotification(ctx); err != nil {
						return err
					}
					select {
					case notify <- struct{}{}:
					default:
					}
				}
			})
		}()
		if ctx.Err() != nil {
			return
		}
		log.Printf("[RELAY] LISTEN connection lost, polling only for 10s: %v", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
	}
}
