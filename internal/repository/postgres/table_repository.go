package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
)

var (
	ErrTableNotFound        = errors.New("table not found in tenant context")
	ErrTableSessionNotFound = errors.New("table session not found in tenant context")
)

// unpaidOrderFilter: orders of a session that still hold money owed (not cancelled, not settled).
const unpaidOrderFilter = `o.status <> 'cancelled' AND o.payment_status NOT IN ('paid', 'partially_refunded', 'refunded')`

type TableRepository struct{}

func NewTableRepository() *TableRepository {
	return &TableRepository{}
}

const tableColumns = `id, store_id, table_number, label, section, capacity, qr_token, qr_rotated_at,
	is_active, archived_at, created_at, updated_at`

const tableSessionColumns = `id, store_id, table_id, status, opened_at, opened_via, opened_by_user_id,
	settled_at, closed_at, closed_by_user_id, close_reason`

// ResolveTableQR calls the SECURITY DEFINER resolver (decision QR2); it runs before a tenant context
// exists and returns only (store_id, table_id, is_active).
func (r *TableRepository) ResolveTableQR(ctx context.Context, db *sqlx.DB, token string) (uuid.UUID, uuid.UUID, bool, error) {
	var row struct {
		StoreID  uuid.UUID `db:"store_id"`
		TableID  uuid.UUID `db:"table_id"`
		IsActive bool      `db:"is_active"`
	}
	if err := db.GetContext(ctx, &row, `SELECT store_id, table_id, is_active FROM resolve_table_qr($1)`, token); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return uuid.Nil, uuid.Nil, false, ErrTableNotFound
		}
		return uuid.Nil, uuid.Nil, false, fmt.Errorf("failed to resolve table QR: %w", err)
	}
	return row.StoreID, row.TableID, row.IsActive, nil
}

func (r *TableRepository) InsertTable(ctx context.Context, tx *sqlx.Tx, t *domain.Table) error {
	return tx.QueryRowxContext(ctx, `
		INSERT INTO tables (id, store_id, table_number, label, section, capacity, qr_token, is_active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING qr_rotated_at, created_at, updated_at
	`, t.ID, t.StoreID, t.TableNumber, t.Label, t.Section, t.Capacity, t.QRToken, t.IsActive).
		Scan(&t.QRRotatedAt, &t.CreatedAt, &t.UpdatedAt)
}

func (r *TableRepository) GetTable(ctx context.Context, tx *sqlx.Tx, storeID, id uuid.UUID, forUpdate bool) (*domain.Table, error) {
	query := `SELECT ` + tableColumns + ` FROM tables WHERE store_id = $1 AND id = $2 AND archived_at IS NULL`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var t domain.Table
	if err := tx.GetContext(ctx, &t, query, storeID, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTableNotFound
		}
		return nil, fmt.Errorf("failed to load table: %w", err)
	}
	return &t, nil
}

// UpdateTable writes every mutable column, including the QR token and archive time.
func (r *TableRepository) UpdateTable(ctx context.Context, tx *sqlx.Tx, t *domain.Table) error {
	return tx.QueryRowxContext(ctx, `
		UPDATE tables SET table_number = $3, label = $4, section = $5, capacity = $6, qr_token = $7,
			qr_rotated_at = $8, is_active = $9, archived_at = $10, updated_at = NOW()
		WHERE store_id = $1 AND id = $2
		RETURNING updated_at
	`, t.StoreID, t.ID, t.TableNumber, t.Label, t.Section, t.Capacity, t.QRToken, t.QRRotatedAt, t.IsActive, t.ArchivedAt).
		Scan(&t.UpdatedAt)
}

// CloseSettledSessions lazily closes sessions whose auto-close countdown has passed (decision QR7).
func (r *TableRepository) CloseSettledSessions(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, idleMinutes int, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE table_sessions SET status = 'closed', closed_at = $3, close_reason = 'auto'
		WHERE store_id = $1 AND status = 'open' AND settled_at IS NOT NULL
		  AND settled_at + make_interval(mins => $2) <= $3
	`, storeID, idleMinutes, now)
	if err != nil {
		return fmt.Errorf("failed to auto-close table sessions: %w", err)
	}
	return nil
}

// ListBoard returns active and inactive (not archived) tables with their derived status.
func (r *TableRepository) ListBoard(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID) ([]domain.TableBoardEntry, error) {
	var rows []struct {
		domain.Table
		SessionID      *uuid.UUID `db:"session_id"`
		OpenedAt       *time.Time `db:"session_opened_at"`
		OpenedVia      *string    `db:"session_opened_via"`
		OpenedByUserID *uuid.UUID `db:"session_opened_by"`
		SettledAt      *time.Time `db:"session_settled_at"`
		UnpaidOrders   int        `db:"unpaid_orders"`
		UnpaidAmount   int64      `db:"unpaid_amount"`
	}
	err := tx.SelectContext(ctx, &rows, `
		SELECT t.id, t.store_id, t.table_number, t.label, t.section, t.capacity, t.qr_token, t.qr_rotated_at,
			t.is_active, t.archived_at, t.created_at, t.updated_at,
			s.id AS session_id, s.opened_at AS session_opened_at, s.opened_via AS session_opened_via,
			s.opened_by_user_id AS session_opened_by, s.settled_at AS session_settled_at,
			COUNT(o.id) AS unpaid_orders, COALESCE(SUM(o.total_amount - o.paid_amount), 0) AS unpaid_amount
		FROM tables t
		LEFT JOIN table_sessions s ON s.store_id = t.store_id AND s.table_id = t.id AND s.status = 'open'
		LEFT JOIN orders o ON o.store_id = t.store_id AND o.table_session_id = s.id AND `+unpaidOrderFilter+`
		WHERE t.store_id = $1 AND t.archived_at IS NULL
		GROUP BY t.id, s.id
		ORDER BY lower(t.section), lower(t.table_number)
	`, storeID)
	if err != nil {
		return nil, fmt.Errorf("failed to list tables: %w", err)
	}
	out := make([]domain.TableBoardEntry, len(rows))
	for i, row := range rows {
		e := domain.TableBoardEntry{Table: row.Table, Status: domain.TableStatusAvailable, UnpaidOrders: row.UnpaidOrders, UnpaidAmount: row.UnpaidAmount}
		if row.SessionID != nil {
			e.Status = domain.TableStatusOccupied
			if row.SettledAt != nil {
				e.Status = domain.TableStatusSettled
			}
			e.Session = &domain.TableSession{
				ID: *row.SessionID, TableID: row.ID, Status: domain.TableSessionOpen, OpenedAt: *row.OpenedAt,
				OpenedVia: *row.OpenedVia, OpenedByUserID: row.OpenedByUserID, SettledAt: row.SettledAt,
			}
		}
		out[i] = e
	}
	return out, nil
}

// GetOpenSessionForUpdate returns the table's open session, or nil when the table is free.
func (r *TableRepository) GetOpenSessionForUpdate(ctx context.Context, tx *sqlx.Tx, storeID, tableID uuid.UUID) (*domain.TableSession, error) {
	var s domain.TableSession
	err := tx.GetContext(ctx, &s, `SELECT `+tableSessionColumns+` FROM table_sessions
		WHERE store_id = $1 AND table_id = $2 AND status = 'open' FOR UPDATE`, storeID, tableID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load open table session: %w", err)
	}
	return &s, nil
}

func (r *TableRepository) GetSessionForUpdate(ctx context.Context, tx *sqlx.Tx, storeID, id uuid.UUID) (*domain.TableSession, error) {
	var s domain.TableSession
	err := tx.GetContext(ctx, &s, `SELECT `+tableSessionColumns+` FROM table_sessions WHERE store_id = $1 AND id = $2 FOR UPDATE`, storeID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTableSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load table session: %w", err)
	}
	return &s, nil
}

func (r *TableRepository) InsertSession(ctx context.Context, tx *sqlx.Tx, s *domain.TableSession) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO table_sessions (id, store_id, table_id, status, opened_at, opened_via, opened_by_user_id)
		VALUES ($1, $2, $3, 'open', $4, $5, $6)
	`, s.ID, s.StoreID, s.TableID, s.OpenedAt, s.OpenedVia, s.OpenedByUserID)
	if err != nil {
		return fmt.Errorf("failed to open table session: %w", err)
	}
	return nil
}

func (r *TableRepository) CloseSession(ctx context.Context, tx *sqlx.Tx, s *domain.TableSession) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE table_sessions SET status = 'closed', closed_at = $3, closed_by_user_id = $4, close_reason = $5
		WHERE store_id = $1 AND id = $2
	`, s.StoreID, s.ID, s.ClosedAt, s.ClosedByUserID, s.CloseReason)
	if err != nil {
		return fmt.Errorf("failed to close table session: %w", err)
	}
	return nil
}

func (r *TableRepository) CountUnpaidOrders(ctx context.Context, tx *sqlx.Tx, storeID, sessionID uuid.UUID) (int, error) {
	var n int
	if err := tx.GetContext(ctx, &n, `SELECT COUNT(*) FROM orders o
		WHERE o.store_id = $1 AND o.table_session_id = $2 AND `+unpaidOrderFilter, storeID, sessionID); err != nil {
		return 0, fmt.Errorf("failed to count unpaid table orders: %w", err)
	}
	return n, nil
}

// HasOpenShift reports whether any drawer has an open shift: the store is "open" for QR orders (QR4).
func (r *TableRepository) HasOpenShift(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID) (bool, error) {
	var open bool
	if err := tx.GetContext(ctx, &open, `SELECT EXISTS (SELECT 1 FROM shifts WHERE store_id = $1 AND status = 'open')`, storeID); err != nil {
		return false, fmt.Errorf("failed to check open shifts: %w", err)
	}
	return open, nil
}
