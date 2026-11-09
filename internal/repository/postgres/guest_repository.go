package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

var (
	ErrGuestSessionNotFound = errors.New("guest session not found in tenant context")
	ErrTrackedOrderNotFound = errors.New("tracked order not found")
)

// GuestRepository stores guest sessions and their QR orders (iteration 5b, decision Q6).
type GuestRepository struct{}

func NewGuestRepository() *GuestRepository {
	return &GuestRepository{}
}

// GuestSessionRow is a guest session joined with its table session and table.
type GuestSessionRow struct {
	ID                 uuid.UUID  `db:"id"`
	StoreID            uuid.UUID  `db:"store_id"`
	TableSessionID     uuid.UUID  `db:"table_session_id"`
	TokenHash          []byte     `db:"token_hash"`
	ExpiresAt          time.Time  `db:"expires_at"`
	EndedAt            *time.Time `db:"ended_at"`
	TableSessionStatus string     `db:"table_session_status"`
	TableID            uuid.UUID  `db:"table_id"`
	TableNumber        string     `db:"table_number"`
	TableActive        bool       `db:"table_active"`
}

// InsertGuestSession binds a device to the table session it orders into and to the table it scanned;
// they differ when that table is merged into another table's session (W5).
func (r *GuestRepository) InsertGuestSession(ctx context.Context, tx *sqlx.Tx, id, storeID, tableSessionID, tableID uuid.UUID, tokenHash []byte, createdAt, expiresAt time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO guest_sessions (id, store_id, table_session_id, table_id, token_hash, created_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, id, storeID, tableSessionID, tableID, tokenHash, createdAt, expiresAt)
	if err != nil {
		return fmt.Errorf("failed to create guest session: %w", err)
	}
	return nil
}

// GetGuestSession loads the session with its table context; forUpdate locks the guest and table
// session rows so concurrent orders of one table are checked against the caps one at a time.
func (r *GuestRepository) GetGuestSession(ctx context.Context, tx *sqlx.Tx, storeID, id uuid.UUID, forUpdate bool) (*GuestSessionRow, error) {
	query := `
		SELECT g.id, g.store_id, g.table_session_id, g.token_hash, g.expires_at, g.ended_at,
			ts.status AS table_session_status, t.id AS table_id, t.table_number,
			(t.is_active AND t.archived_at IS NULL) AS table_active
		FROM guest_sessions g
		JOIN table_sessions ts ON ts.store_id = g.store_id AND ts.id = g.table_session_id
		JOIN tables t ON t.store_id = ts.store_id AND t.id = COALESCE(g.table_id, ts.table_id)
		WHERE g.store_id = $1 AND g.id = $2`
	if forUpdate {
		query += ` FOR UPDATE OF g, ts`
	}
	var row GuestSessionRow
	if err := tx.GetContext(ctx, &row, query, storeID, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrGuestSessionNotFound
		}
		return nil, fmt.Errorf("failed to load guest session: %w", err)
	}
	return &row, nil
}

// CountUnpaidQROrders counts orders still awaiting payment for a guest session and for its table session.
func (r *GuestRepository) CountUnpaidQROrders(ctx context.Context, tx *sqlx.Tx, storeID, guestSessionID, tableSessionID uuid.UUID) (int, int, error) {
	var counts struct {
		Guest int `db:"guest"`
		Table int `db:"table_total"`
	}
	err := tx.GetContext(ctx, &counts, `
		SELECT COUNT(*) FILTER (WHERE o.guest_session_id = $2) AS guest, COUNT(*) AS table_total
		FROM orders o
		WHERE o.store_id = $1 AND o.table_session_id = $3 AND `+unpaidOrderFilter, storeID, guestSessionID, tableSessionID)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to count unpaid QR orders: %w", err)
	}
	return counts.Guest, counts.Table, nil
}

// ClearSettled restarts occupancy: a new order means the table is no longer settled (decision T1).
func (r *GuestRepository) ClearSettled(ctx context.Context, tx *sqlx.Tx, storeID, tableSessionID uuid.UUID) error {
	if _, err := tx.ExecContext(ctx, `UPDATE table_sessions SET settled_at = NULL WHERE store_id = $1 AND id = $2`, storeID, tableSessionID); err != nil {
		return fmt.Errorf("failed to clear table session settled_at: %w", err)
	}
	return nil
}

// GuestOrderSummary is what a guest sees of its own orders.
type GuestOrderSummary struct {
	ID            uuid.UUID  `json:"id" db:"id"`
	OrderNumber   string     `json:"order_number" db:"order_number"`
	QueueNumber   string     `json:"queue_number" db:"queue_number"`
	Status        string     `json:"status" db:"status"`
	PaymentStatus string     `json:"payment_status" db:"payment_status"`
	TotalAmount   int64      `json:"total_amount" db:"total_amount"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty" db:"expires_at"`
	CreatedAt     time.Time  `json:"created_at" db:"created_at"`
}

func (r *GuestRepository) ListGuestOrders(ctx context.Context, tx *sqlx.Tx, storeID, guestSessionID uuid.UUID) ([]GuestOrderSummary, error) {
	out := []GuestOrderSummary{}
	err := tx.SelectContext(ctx, &out, `
		SELECT id, order_number, queue_number, status, payment_status, total_amount, expires_at, created_at
		FROM orders WHERE store_id = $1 AND guest_session_id = $2
		ORDER BY created_at DESC LIMIT 50
	`, storeID, guestSessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to list guest orders: %w", err)
	}
	return out, nil
}

// OrderGuestSession returns the guest session an order belongs to (nil for staff orders).
func (r *GuestRepository) OrderGuestSession(ctx context.Context, tx *sqlx.Tx, storeID, orderID uuid.UUID) (*uuid.UUID, error) {
	var id *uuid.UUID
	err := tx.GetContext(ctx, &id, `SELECT guest_session_id FROM orders WHERE store_id = $1 AND id = $2`, storeID, orderID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrOrderNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load order owner: %w", err)
	}
	return id, nil
}

// TrackedOrder is the public tracker view returned by track_order_by_token (decision QR6).
type TrackedOrder struct {
	StoreName     string    `json:"store_name" db:"store_name"`
	OrderNumber   string    `json:"order_number" db:"order_number"`
	QueueNumber   string    `json:"queue_number" db:"queue_number"`
	Status        string    `json:"status" db:"status"`
	PaymentStatus string    `json:"payment_status" db:"payment_status"`
	UpdatedAt     time.Time `json:"updated_at" db:"updated_at"`
}

// TrackOrder calls the SECURITY DEFINER tracker; it runs without a tenant context.
func (r *GuestRepository) TrackOrder(ctx context.Context, db *sqlx.DB, tokenHash []byte) (*TrackedOrder, error) {
	var out TrackedOrder
	err := db.GetContext(ctx, &out, `
		SELECT store_name, order_number, queue_number, status, payment_status, updated_at
		FROM track_order_by_token($1)`, tokenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTrackedOrderNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to track order: %w", err)
	}
	return &out, nil
}

// SplitShare is a per-person payment link resolved from its token hash.
type SplitShare struct {
	StoreID   uuid.UUID `db:"store_id"`
	SplitID   uuid.UUID `db:"split_id"`
	OrderID   uuid.UUID `db:"order_id"`
	ExpiresAt time.Time `db:"expires_at"`
}

// SplitShareView is everything a link holder may see.
type SplitShareView struct {
	StoreName string `json:"store_name" db:"store_name"`
	Label     string `json:"label" db:"label"`
	Amount    int64  `json:"amount" db:"amount"`
	Status    string `json:"status" db:"status"`
}

func (r *GuestRepository) InsertSplitShare(ctx context.Context, tx *sqlx.Tx, storeID, orderID, splitID uuid.UUID, tokenHash []byte, expiresAt time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO guest_split_shares (store_id, order_id, split_id, token_hash, expires_at)
		VALUES ($1, $2, $3, $4, $5)`, storeID, orderID, splitID, tokenHash, expiresAt)
	if err != nil {
		return fmt.Errorf("failed to insert split share: %w", err)
	}
	return nil
}

// ResolveSplitShare returns nil when no link has this hash.
func (r *GuestRepository) ResolveSplitShare(ctx context.Context, db *sqlx.DB, tokenHash []byte) (*SplitShare, error) {
	var out SplitShare
	err := db.GetContext(ctx, &out, `SELECT store_id, split_id, order_id, expires_at FROM resolve_split_share($1)`, tokenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to resolve split share: %w", err)
	}
	return &out, nil
}

func (r *GuestRepository) GetSplitShareView(ctx context.Context, tx *sqlx.Tx, storeID, splitID uuid.UUID) (*SplitShareView, error) {
	var out SplitShareView
	err := tx.GetContext(ctx, &out, `
		SELECT st.name AS store_name, ps.label, ps.total_amount AS amount, ps.status
		FROM payment_splits ps JOIN stores st ON st.id = ps.store_id
		WHERE ps.store_id = $1 AND ps.id = $2`, storeID, splitID)
	if err != nil {
		return nil, fmt.Errorf("failed to load split share: %w", err)
	}
	return &out, nil
}
