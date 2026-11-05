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
	settled_at, closed_at, closed_by_user_id, close_reason, ends_at, warned_at, expired_at, extended_minutes`

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
		WITH closed AS (
			UPDATE table_sessions SET status = 'closed', closed_at = $3, close_reason = 'auto'
			WHERE store_id = $1 AND status = 'open' AND settled_at IS NOT NULL
			  AND settled_at + make_interval(mins => $2) <= $3
			RETURNING id
		)
		UPDATE table_session_links SET unlinked_at = $3
		WHERE store_id = $1 AND unlinked_at IS NULL AND primary_session_id IN (SELECT id FROM closed)
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
		SessionTableID *uuid.UUID `db:"session_table_id"`
		EndsAt         *time.Time `db:"session_ends_at"`
		WarnedAt       *time.Time `db:"session_warned_at"`
		ExpiredAt      *time.Time `db:"session_expired_at"`
		Extended       *int       `db:"session_extended_minutes"`
		UnpaidOrders   int        `db:"unpaid_orders"`
		UnpaidAmount   int64      `db:"unpaid_amount"`
	}
	err := tx.SelectContext(ctx, &rows, `
		SELECT t.id, t.store_id, t.table_number, t.label, t.section, t.capacity, t.qr_token, t.qr_rotated_at,
			t.is_active, t.archived_at, t.created_at, t.updated_at,
			s.id AS session_id, s.opened_at AS session_opened_at, s.opened_via AS session_opened_via,
			s.opened_by_user_id AS session_opened_by, s.settled_at AS session_settled_at, s.table_id AS session_table_id,
			s.ends_at AS session_ends_at, s.warned_at AS session_warned_at, s.expired_at AS session_expired_at,
			s.extended_minutes AS session_extended_minutes,
			COUNT(o.id) AS unpaid_orders, COALESCE(SUM(o.total_amount - o.paid_amount), 0) AS unpaid_amount
		FROM tables t
		LEFT JOIN table_session_links l ON l.store_id = t.store_id AND l.table_id = t.id AND l.unlinked_at IS NULL
		LEFT JOIN table_sessions s ON s.store_id = t.store_id AND s.status = 'open'
			AND (s.table_id = t.id OR s.id = l.primary_session_id)
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
				ID: *row.SessionID, TableID: *row.SessionTableID, Status: domain.TableSessionOpen, OpenedAt: *row.OpenedAt,
				OpenedVia: *row.OpenedVia, OpenedByUserID: row.OpenedByUserID, SettledAt: row.SettledAt,
				EndsAt: row.EndsAt, WarnedAt: row.WarnedAt, ExpiredAt: row.ExpiredAt, ExtendedMinutes: *row.Extended,
			}
			if *row.SessionTableID != row.ID {
				e.MergedIntoTableID = row.SessionTableID
			}
		}
		out[i] = e
	}
	return out, nil
}

// GetOpenSessionForUpdate returns the session a table orders into: its own open session, or the
// primary session it is merged into (W5). nil when the table is free.
func (r *TableRepository) GetOpenSessionForUpdate(ctx context.Context, tx *sqlx.Tx, storeID, tableID uuid.UUID) (*domain.TableSession, error) {
	var s domain.TableSession
	err := tx.GetContext(ctx, &s, `SELECT `+tableSessionColumns+` FROM table_sessions
		WHERE store_id = $1 AND status = 'open' AND (table_id = $2 OR id = (
			SELECT primary_session_id FROM table_session_links
			WHERE store_id = $1 AND table_id = $2 AND unlinked_at IS NULL))
		ORDER BY (table_id = $2) DESC LIMIT 1 FOR UPDATE`, storeID, tableID)
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

// InsertSession opens a session; under a store time limit it starts the table timer (W6).
func (r *TableRepository) InsertSession(ctx context.Context, tx *sqlx.Tx, s *domain.TableSession) error {
	err := tx.GetContext(ctx, &s.EndsAt, `
		INSERT INTO table_sessions (id, store_id, table_id, status, opened_at, opened_via, opened_by_user_id, ends_at)
		SELECT $1::uuid, $2::uuid, $3::uuid, 'open', $4::timestamptz, $5::varchar, $6::uuid,
			CASE WHEN ss.table_time_limit_minutes > 0 THEN $4 + make_interval(mins => ss.table_time_limit_minutes) END
		FROM (SELECT 1) one LEFT JOIN store_settings ss ON ss.store_id = $2
		RETURNING ends_at
	`, s.ID, s.StoreID, s.TableID, s.OpenedAt, s.OpenedVia, s.OpenedByUserID)
	if err != nil {
		return fmt.Errorf("failed to open table session: %w", err)
	}
	return nil
}

// CloseSession closes the session and frees the tables merged into it.
func (r *TableRepository) CloseSession(ctx context.Context, tx *sqlx.Tx, s *domain.TableSession) error {
	_, err := tx.ExecContext(ctx, `
		WITH closed AS (
			UPDATE table_sessions SET status = 'closed', closed_at = $3, closed_by_user_id = $4, close_reason = $5
			WHERE store_id = $1 AND id = $2
		)
		UPDATE table_session_links SET unlinked_at = $3, unlinked_by_user_id = $4
		WHERE store_id = $1 AND primary_session_id = $2 AND unlinked_at IS NULL
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

// MoveSession seats the whole session at another table; its orders placed at the old table follow.
func (r *TableRepository) MoveSession(ctx context.Context, tx *sqlx.Tx, storeID, sessionID uuid.UUID, from, to *domain.Table) error {
	if _, err := tx.ExecContext(ctx, `UPDATE table_sessions SET table_id = $3 WHERE store_id = $1 AND id = $2`,
		storeID, sessionID, to.ID); err != nil {
		return fmt.Errorf("failed to move table session: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE orders SET table_id = $4, table_number = $5
		WHERE store_id = $1 AND table_session_id = $2 AND table_id = $3`,
		storeID, sessionID, from.ID, to.ID, to.TableNumber); err != nil {
		return fmt.Errorf("failed to move table orders: %w", err)
	}
	// Guests seated at the old table move with it; guests of merged tables keep their own table.
	if _, err := tx.ExecContext(ctx, `UPDATE guest_sessions SET table_id = $4
		WHERE store_id = $1 AND table_session_id = $2 AND COALESCE(table_id, $3) = $3`,
		storeID, sessionID, from.ID, to.ID); err != nil {
		return fmt.Errorf("failed to move table guests: %w", err)
	}
	return nil
}

// MergeSession folds the secondary session into the primary one (W5): orders, guest sessions and
// links move, the secondary closes with reason "merged" and its table is linked to the primary.
func (r *TableRepository) MergeSession(ctx context.Context, tx *sqlx.Tx, primary, secondary *domain.TableSession, userID uuid.UUID, now time.Time) error {
	// One statement: every step sees the same snapshot and touches different rows. The primary may
	// have been settled; the merged orders reopen it until refreshTableSettledTx runs again.
	_, err := tx.ExecContext(ctx, `
		WITH moved_orders AS (
			UPDATE orders SET table_session_id = $2 WHERE store_id = $1 AND table_session_id = $3
		), moved_guests AS (
			UPDATE guest_sessions SET table_session_id = $2,
				table_id = COALESCE(table_id, (SELECT table_id FROM table_sessions WHERE store_id = $1 AND id = $3))
			WHERE store_id = $1 AND table_session_id = $3
		), moved_links AS (
			UPDATE table_session_links SET primary_session_id = $2
			WHERE store_id = $1 AND primary_session_id = $3 AND unlinked_at IS NULL
		), reopened AS (
			UPDATE table_sessions SET settled_at = NULL WHERE store_id = $1 AND id = $2
		), closed AS (
			UPDATE table_sessions SET status = 'closed', closed_at = $4, closed_by_user_id = $5, close_reason = 'merged'
			WHERE store_id = $1 AND id = $3
			RETURNING table_id
		)
		INSERT INTO table_session_links (store_id, primary_session_id, table_id, linked_at, linked_by_user_id)
		SELECT $1, $2, table_id, $4, $5 FROM closed
	`, primary.StoreID, primary.ID, secondary.ID, now, userID)
	if err != nil {
		return fmt.Errorf("failed to merge table sessions: %w", err)
	}
	return nil
}

// UnlinkTable splits a merged table off its primary session; false when it was not linked to it.
func (r *TableRepository) UnlinkTable(ctx context.Context, tx *sqlx.Tx, storeID, primaryID, tableID, userID uuid.UUID, now time.Time) (bool, error) {
	res, err := tx.ExecContext(ctx, `UPDATE table_session_links SET unlinked_at = $4, unlinked_by_user_id = $5
		WHERE store_id = $1 AND primary_session_id = $2 AND table_id = $3 AND unlinked_at IS NULL`,
		storeID, primaryID, tableID, now, userID)
	if err != nil {
		return false, fmt.Errorf("failed to unlink table: %w", err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// TimerEvent is one session whose table timer crossed its warning or end (W6).
type TimerEvent struct {
	SessionID   uuid.UUID `db:"id"`
	TableID     uuid.UUID `db:"table_id"`
	TableNumber string    `db:"table_number"`
	EndsAt      time.Time `db:"ends_at"`
}

// ListStoresWithDueTableTimers calls the narrow SECURITY DEFINER discovery function.
func (r *TableRepository) ListStoresWithDueTableTimers(ctx context.Context, db *sqlx.DB) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	if err := db.SelectContext(ctx, &ids, `SELECT store_id FROM list_stores_with_due_table_timers()`); err != nil {
		return nil, fmt.Errorf("failed to list stores with due table timers: %w", err)
	}
	return ids, nil
}

// MarkTimers stamps expired sessions first, then sessions entering the warning window. Each row is
// stamped once: the WHERE re-checks after a concurrent worker's lock, so only one worker returns it.
func (r *TableRepository) MarkTimers(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, warningMinutes int, now time.Time) (expired, warned []TimerEvent, err error) {
	if err = tx.SelectContext(ctx, &expired, `
		WITH stamped AS (
			UPDATE table_sessions SET expired_at = $2, warned_at = COALESCE(warned_at, $2)
			WHERE store_id = $1 AND status = 'open' AND ends_at IS NOT NULL AND expired_at IS NULL AND ends_at <= $2
			RETURNING id, table_id, ends_at
		)
		SELECT s.id, s.table_id, t.table_number, s.ends_at FROM stamped s JOIN tables t ON t.store_id = $1 AND t.id = s.table_id
	`, storeID, now); err != nil {
		return nil, nil, fmt.Errorf("failed to mark expired table timers: %w", err)
	}
	if err = tx.SelectContext(ctx, &warned, `
		WITH stamped AS (
			UPDATE table_sessions SET warned_at = $2
			WHERE store_id = $1 AND status = 'open' AND ends_at IS NOT NULL AND warned_at IS NULL
			  AND ends_at > $2 AND ends_at - make_interval(mins => $3) <= $2
			RETURNING id, table_id, ends_at
		)
		SELECT s.id, s.table_id, t.table_number, s.ends_at FROM stamped s JOIN tables t ON t.store_id = $1 AND t.id = s.table_id
	`, storeID, now, warningMinutes); err != nil {
		return nil, nil, fmt.Errorf("failed to mark warned table timers: %w", err)
	}
	return expired, warned, nil
}

// ExtendTimer moves ends_at to max(ends_at, now) + minutes and re-arms the warning and expiry.
func (r *TableRepository) ExtendTimer(ctx context.Context, tx *sqlx.Tx, s *domain.TableSession, minutes int, now time.Time) error {
	return tx.QueryRowxContext(ctx, `
		UPDATE table_sessions SET ends_at = GREATEST(ends_at, $3) + make_interval(mins => $4),
			warned_at = NULL, expired_at = NULL, extended_minutes = extended_minutes + $4
		WHERE store_id = $1 AND id = $2
		RETURNING ends_at, warned_at, expired_at, extended_minutes
	`, s.StoreID, s.ID, now, minutes).Scan(&s.EndsAt, &s.WarnedAt, &s.ExpiredAt, &s.ExtendedMinutes)
}
