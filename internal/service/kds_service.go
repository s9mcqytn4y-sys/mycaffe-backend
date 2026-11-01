package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

// Kitchen Display System tickets (iteration 5d): one ticket per (order or round) x station; order
// and round status are derived from their tickets.

const (
	KDSQueued    = "queued"
	KDSPreparing = "preparing"
	KDSReady     = "ready"
	KDSServed    = "served"
	KDSCancelled = "cancelled"
)

var (
	ErrKDSTicketNotFound = errors.New("kds ticket not found")
	ErrKDSStaleStatus    = errors.New("kds ticket status changed on another screen")
	ErrKDSTransition     = errors.New("kds ticket transition not allowed")
)

// kdsTransitions lists the allowed moves; ready -> preparing is a recall. Cancel happens only by void.
var kdsTransitions = map[string][]string{
	KDSQueued:    {KDSPreparing},
	KDSPreparing: {KDSReady},
	KDSReady:     {KDSServed, KDSPreparing},
}

// createKDSTicketsTx opens one ticket per station for the order's lines of roundID (nil = lines
// outside any round). It is idempotent and returns how many tickets the lines need in total.
func createKDSTicketsTx(ctx context.Context, tx *sqlx.Tx, orders *postgres.OrderRepository, order *domain.Order, roundID *uuid.UUID, latePayment bool, now time.Time) (int, error) {
	var created []struct {
		ID      uuid.UUID `db:"id"`
		Station string    `db:"station"`
	}
	if err := tx.SelectContext(ctx, &created, `
		INSERT INTO kds_tickets (store_id, order_id, round_id, station, late_payment, created_at, updated_at)
		SELECT DISTINCT oi.store_id, oi.order_id, oi.round_id, oi.snapshot_prep_station, $4::boolean, $5::timestamptz, $5::timestamptz
		FROM order_items oi
		WHERE oi.store_id = $1 AND oi.order_id = $2 AND oi.round_id IS NOT DISTINCT FROM $3::uuid
		  AND oi.snapshot_prep_station IN ('bar', 'kitchen')
		ON CONFLICT ON CONSTRAINT uq_kds_tickets_unit DO NOTHING
		RETURNING id, station
	`, order.StoreID, order.ID, roundID, latePayment, now); err != nil {
		return 0, fmt.Errorf("failed to create kds tickets: %w", err)
	}
	for _, t := range created {
		payload, _ := json.Marshal(map[string]any{
			"ticket_id": t.ID, "order_id": order.ID, "order_number": order.OrderNumber, "queue_number": order.QueueNumber,
			"table_number": order.TableNumber, "round_id": roundID, "station": t.Station, "status": KDSQueued, "late_payment": latePayment,
		})
		if err := orders.InsertOutboxEvent(ctx, tx, &domain.OutboxEvent{
			ID: uuid.New(), StoreID: order.StoreID, EventType: "kds.ticket_created", AggregateType: "order",
			AggregateID: order.ID.String(), Payload: string(payload),
		}); err != nil {
			return 0, err
		}
	}
	var total int
	err := tx.GetContext(ctx, &total, `SELECT COUNT(*) FROM kds_tickets WHERE store_id = $1 AND order_id = $2 AND round_id IS NOT DISTINCT FROM $3`,
		order.StoreID, order.ID, roundID)
	return total, err
}

// deriveKDSStatus folds ticket statuses (cancelled ignored) into the order/round status:
// all served -> served; all ready or served -> ready; any work started -> preparing; else queued.
func deriveKDSStatus(statuses []string) string {
	counts := map[string]int{}
	live := 0
	for _, s := range statuses {
		if s != KDSCancelled {
			counts[s]++
			live++
		}
	}
	switch {
	case live == 0:
		return ""
	case counts[KDSServed] == live:
		return KDSServed
	case counts[KDSServed]+counts[KDSReady] == live:
		return KDSReady
	case counts[KDSQueued] < live:
		return KDSPreparing
	}
	return KDSQueued
}

// refreshTableSettledTx sets table_sessions.settled_at once every order of the session is paid and
// done (served, completed or cancelled), which starts the lazy auto-close countdown (T1, QR7).
func refreshTableSettledTx(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, tableSessionID *uuid.UUID, now time.Time) error {
	if tableSessionID == nil {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE table_sessions ts SET settled_at = $3
		WHERE ts.store_id = $1 AND ts.id = $2 AND ts.status = 'open' AND ts.settled_at IS NULL
		  AND EXISTS (SELECT 1 FROM orders o WHERE o.store_id = ts.store_id AND o.table_session_id = ts.id)
		  AND NOT EXISTS (
			SELECT 1 FROM orders o
			WHERE o.store_id = ts.store_id AND o.table_session_id = ts.id
			  AND (o.status NOT IN ('served', 'completed', 'cancelled')
			       OR (o.status <> 'cancelled' AND o.payment_status NOT IN ('paid', 'partially_refunded', 'refunded'))))
	`, storeID, *tableSessionID, now)
	if err != nil {
		return fmt.Errorf("failed to refresh table session settled_at: %w", err)
	}
	return nil
}

type KDSService struct {
	db      *sqlx.DB
	orders  *postgres.OrderRepository
	nowFunc func() time.Time
}

func NewKDSService(db *sqlx.DB) *KDSService {
	return &KDSService{db: db, orders: postgres.NewOrderRepository(), nowFunc: time.Now}
}

type KDSTicketLine struct {
	Name     string `json:"name" db:"snapshot_name"`
	Variant  string `json:"variant,omitempty" db:"snapshot_variant_name"`
	Quantity int    `json:"quantity" db:"quantity"`
	Options  string `json:"options" db:"selected_options"`
	Notes    string `json:"notes,omitempty" db:"notes"`
}

type KDSTicket struct {
	ID          uuid.UUID       `json:"id" db:"id"`
	OrderID     uuid.UUID       `json:"order_id" db:"order_id"`
	RoundID     *uuid.UUID      `json:"round_id,omitempty" db:"round_id"`
	RoundNumber *int            `json:"round_number,omitempty" db:"round_number"`
	OrderNumber string          `json:"order_number" db:"order_number"`
	QueueNumber string          `json:"queue_number" db:"queue_number"`
	TableNumber string          `json:"table_number" db:"table_number"`
	Station     string          `json:"station" db:"station"`
	Status      string          `json:"status" db:"status"`
	LatePayment bool            `json:"late_payment" db:"late_payment"`
	CreatedAt   time.Time       `json:"created_at" db:"created_at"`
	StartedAt   *time.Time      `json:"started_at,omitempty" db:"started_at"`
	ReadyAt     *time.Time      `json:"ready_at,omitempty" db:"ready_at"`
	ServedAt    *time.Time      `json:"served_at,omitempty" db:"served_at"`
	Lines       []KDSTicketLine `json:"lines" db:"-"`
}

const kdsTicketSelect = `
	SELECT k.id, k.order_id, k.round_id, r.round_number, o.order_number, o.queue_number, o.table_number,
		k.station, k.status, k.late_payment, k.created_at, k.started_at, k.ready_at, k.served_at
	FROM kds_tickets k
	JOIN orders o ON o.store_id = k.store_id AND o.id = k.order_id
	LEFT JOIN order_rounds r ON r.store_id = k.store_id AND r.id = k.round_id`

func canSeeStation(actor *AuthenticatedStaff, station string, update bool) bool {
	switch {
	case station == domain.PrepStationBar && update:
		return actor.Can(rbac.KDSBaristaUpdate)
	case station == domain.PrepStationBar:
		return actor.Can(rbac.KDSBaristaRead)
	case station == domain.PrepStationKitchen && update:
		return actor.Can(rbac.KDSKitchenUpdate)
	case station == domain.PrepStationKitchen:
		return actor.Can(rbac.KDSKitchenRead)
	}
	return false
}

func (s *KDSService) loadLines(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, t *KDSTicket) error {
	t.Lines = []KDSTicketLine{}
	return tx.SelectContext(ctx, &t.Lines, `
		SELECT snapshot_name, snapshot_variant_name, quantity, selected_options::text AS selected_options, COALESCE(notes, '') AS notes
		FROM order_items
		WHERE store_id = $1 AND order_id = $2 AND round_id IS NOT DISTINCT FROM $3 AND snapshot_prep_station = $4
		ORDER BY created_at, id`, storeID, t.OrderID, t.RoundID, t.Station)
}

// ListActive returns a station's open tickets (queued, preparing, ready), oldest first.
func (s *KDSService) ListActive(ctx context.Context, actor *AuthenticatedStaff, station string) ([]KDSTicket, error) {
	if actor == nil {
		return nil, ErrInvalidSession
	}
	if station != domain.PrepStationBar && station != domain.PrepStationKitchen {
		return nil, &ValidationError{Fields: map[string]string{"station": "harus bar atau kitchen"}}
	}
	if !canSeeStation(actor, station, false) {
		return nil, ErrForbidden
	}
	out := []KDSTicket{}
	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		if err := tx.SelectContext(ctx, &out, kdsTicketSelect+`
			WHERE k.store_id = $1 AND k.station = $2 AND k.status IN ('queued', 'preparing', 'ready')
			ORDER BY k.created_at LIMIT 200`, actor.Staff.Store.ID, station); err != nil {
			return err
		}
		for i := range out {
			if err := s.loadLines(ctx, tx, actor.Staff.Store.ID, &out[i]); err != nil {
				return err
			}
		}
		return nil
	})
	return out, err
}

type KDSTransitionInput struct {
	To             string `json:"to"`
	ExpectedStatus string `json:"expected_status"`
}

// Transition moves one ticket with optimistic concurrency (expected_status), then derives the round
// or order status, refreshes the table's settled state and emits kds.ticket_updated.
func (s *KDSService) Transition(ctx context.Context, actor *AuthenticatedStaff, ticketID uuid.UUID, in KDSTransitionInput) (*KDSTicket, error) {
	if actor == nil {
		return nil, ErrInvalidSession
	}
	f := fieldErrors{}
	f.oneOf("to", in.To, KDSPreparing, KDSReady, KDSServed)
	f.oneOf("expected_status", in.ExpectedStatus, KDSQueued, KDSPreparing, KDSReady)
	if err := f.err(); err != nil {
		return nil, err
	}
	storeID := actor.Staff.Store.ID
	var out *KDSTicket
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		var t KDSTicket
		err := tx.GetContext(ctx, &t, kdsTicketSelect+` WHERE k.store_id = $1 AND k.id = $2 FOR UPDATE OF k`, storeID, ticketID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrKDSTicketNotFound
		}
		if err != nil {
			return err
		}
		if !canSeeStation(actor, t.Station, true) {
			return ErrForbidden
		}
		if t.Status != in.ExpectedStatus {
			return ErrKDSStaleStatus
		}
		allowed := false
		for _, to := range kdsTransitions[t.Status] {
			allowed = allowed || to == in.To
		}
		if !allowed {
			return ErrKDSTransition
		}
		now := s.nowFunc().UTC()
		if _, err := tx.ExecContext(ctx, `
			UPDATE kds_tickets SET status = $3::text, updated_at = $4, updated_by_user_id = $5,
				started_at = CASE WHEN $3::text = 'preparing' THEN COALESCE(started_at, $4) ELSE started_at END,
				ready_at = CASE WHEN $3::text = 'ready' THEN $4 WHEN $3::text = 'preparing' THEN NULL ELSE ready_at END,
				served_at = CASE WHEN $3::text = 'served' THEN $4 ELSE served_at END
			WHERE store_id = $1 AND id = $2`, storeID, ticketID, in.To, now, actor.Staff.UserID); err != nil {
			return err
		}
		if err := tx.GetContext(ctx, &t, kdsTicketSelect+` WHERE k.store_id = $1 AND k.id = $2`, storeID, ticketID); err != nil {
			return err
		}

		var statuses []string
		if err := tx.SelectContext(ctx, &statuses, `SELECT status FROM kds_tickets
			WHERE store_id = $1 AND order_id = $2 AND round_id IS NOT DISTINCT FROM $3`, storeID, t.OrderID, t.RoundID); err != nil {
			return err
		}
		derived := deriveKDSStatus(statuses)
		if t.RoundID != nil {
			// A tab stays "open" for billing; its kitchen progress lives on the round.
			if _, err := tx.ExecContext(ctx, `UPDATE order_rounds SET status = $3, updated_at = $4 WHERE store_id = $1 AND id = $2`,
				storeID, *t.RoundID, derived, now); err != nil {
				return err
			}
		} else if _, err := tx.ExecContext(ctx, `UPDATE orders SET status = $3, updated_at = $4
			WHERE store_id = $1 AND id = $2 AND status IN ('queued', 'preparing', 'ready', 'served')`,
			storeID, t.OrderID, derived, now); err != nil {
			return err
		}

		var order struct {
			TableSessionID *uuid.UUID `db:"table_session_id"`
			Status         string     `db:"status"`
		}
		if err := tx.GetContext(ctx, &order, `SELECT table_session_id, status FROM orders WHERE store_id = $1 AND id = $2`, storeID, t.OrderID); err != nil {
			return err
		}
		if err := refreshTableSettledTx(ctx, tx, storeID, order.TableSessionID, now); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{
			"ticket_id": t.ID, "order_id": t.OrderID, "round_id": t.RoundID, "station": t.Station, "status": t.Status,
			"derived_status": derived, "order_status": order.Status, "order_number": t.OrderNumber, "queue_number": t.QueueNumber,
		})
		if err := s.orders.InsertOutboxEvent(ctx, tx, &domain.OutboxEvent{
			ID: uuid.New(), StoreID: storeID, EventType: "kds.ticket_updated", AggregateType: "order",
			AggregateID: t.OrderID.String(), Payload: string(payload),
		}); err != nil {
			return err
		}
		if err := s.loadLines(ctx, tx, storeID, &t); err != nil {
			return err
		}
		out = &t
		return nil
	})
	return out, err
}

// releaseUnstartedStockTx is the void stock rule (decision P4): lines whose KDS ticket is still
// queued (or that need no preparation) go back to stock; lines a station already started are waste.
// It returns the wasted order_item ids and marks the order's stock released either way.
func releaseUnstartedStockTx(ctx context.Context, tx *sqlx.Tx, orders *postgres.OrderRepository, order *domain.Order) ([]uuid.UUID, error) {
	wasted := []uuid.UUID{}
	if err := tx.SelectContext(ctx, &wasted, `
		SELECT oi.id FROM order_items oi
		JOIN kds_tickets k ON k.store_id = oi.store_id AND k.order_id = oi.order_id
			AND k.round_id IS NOT DISTINCT FROM oi.round_id AND k.station = oi.snapshot_prep_station
		WHERE oi.store_id = $1 AND oi.order_id = $2 AND k.status IN ('preparing', 'ready', 'served')
	`, order.StoreID, order.ID); err != nil {
		return nil, fmt.Errorf("failed to find started kds lines: %w", err)
	}
	if len(wasted) == 0 {
		return wasted, releaseOrderStockTx(ctx, tx, orders, order)
	}
	if order.StockStatus != domain.OrderStockStatusReserved && order.StockStatus != domain.OrderStockStatusCommitted {
		return wasted, nil
	}
	lineQty := make(map[uuid.UUID]int, len(order.Items))
	for _, item := range order.Items {
		lineQty[item.ID] = item.Quantity
	}
	for _, id := range wasted {
		delete(lineQty, id)
	}
	products, options := computeItemStockQuantities(order, lineQty)
	if err := restoreStockTx(ctx, tx, orders, order.StoreID, products, options); err != nil {
		return nil, err
	}
	order.StockStatus = domain.OrderStockStatusReleased
	return wasted, nil
}
