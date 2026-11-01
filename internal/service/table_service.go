package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
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

// Tables and table sessions (iteration 5a; decisions T1, QR1–QR4, QR7).

var (
	ErrTableNotFound        = errors.New("table not found")
	ErrTableSessionOpen     = errors.New("table has an open session")
	ErrTableHasUnpaidOrders = errors.New("table session still has unpaid orders")
	ErrTableInactive        = errors.New("table is inactive")
)

// QR ordering refusal reasons shown to guests (decision QR4 and the emergency pause).
const (
	QRClosedPaused   = "paused"
	QRClosedNoShift  = "store_closed"
	QRClosedInactive = "table_inactive"
)

type TableService struct {
	db       *sqlx.DB
	repo     *postgres.TableRepository
	settings *postgres.SettingsRepository
	orders   *postgres.OrderRepository
	nowFunc  func() time.Time
}

func NewTableService(db *sqlx.DB) *TableService {
	return &TableService{
		db: db, repo: postgres.NewTableRepository(), settings: postgres.NewSettingsRepository(),
		orders: postgres.NewOrderRepository(), nowFunc: time.Now,
	}
}

type TableInput struct {
	TableNumber *string `json:"table_number"`
	Label       *string `json:"label"`
	Section     *string `json:"section"`
	Capacity    *int    `json:"capacity"`
	IsActive    *bool   `json:"is_active"`
}

// newQRToken returns 32 random bytes as base64url (43 characters, decision QR1).
func newQRToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate QR token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ListTables returns the table board. Lazy auto-close (QR7) runs first, so statuses are current.
// The QR token is only shown to staff who may manage QR codes.
func (s *TableService) ListTables(ctx context.Context, actor *AuthenticatedStaff) ([]domain.TableBoardEntry, error) {
	if actor == nil {
		return nil, ErrInvalidSession
	}
	var board []domain.TableBoardEntry
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		if err := s.closeSettled(ctx, tx, actor.Staff.Store.ID); err != nil {
			return err
		}
		var err error
		board, err = s.repo.ListBoard(ctx, tx, actor.Staff.Store.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	if !actor.Can(rbac.TableQRManage) {
		for i := range board {
			board[i].QRToken = ""
		}
	}
	return board, nil
}

func (s *TableService) CreateTable(ctx context.Context, actor *AuthenticatedStaff, in TableInput) (*domain.Table, error) {
	if !actor.Can(rbac.TableManage) {
		return nil, ErrForbidden
	}
	if in.TableNumber == nil {
		return nil, &ValidationError{Fields: map[string]string{"table_number": "wajib diisi"}}
	}
	t := &domain.Table{ID: uuid.New(), StoreID: actor.Staff.Store.ID, Capacity: 4, IsActive: true}
	if err := applyTable(t, in); err != nil {
		return nil, err
	}
	token, err := newQRToken()
	if err != nil {
		return nil, err
	}
	t.QRToken = token
	err = s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		if err := s.repo.InsertTable(ctx, tx, t); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "TABLE_CREATE", "Meja baru dibuat", t.ID, map[string]any{"table_number": t.TableNumber})
	})
	return t, mapTableError(err)
}

func (s *TableService) UpdateTable(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID, in TableInput) (*domain.Table, error) {
	if !actor.Can(rbac.TableManage) {
		return nil, ErrForbidden
	}
	var t *domain.Table
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		var err error
		if t, err = s.repo.GetTable(ctx, tx, actor.Staff.Store.ID, id, true); err != nil {
			return err
		}
		if err := applyTable(t, in); err != nil {
			return err
		}
		if err := s.repo.UpdateTable(ctx, tx, t); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "TABLE_UPDATE", "Data meja diubah", t.ID, map[string]any{
			"table_number": t.TableNumber, "is_active": t.IsActive, "capacity": t.Capacity,
		})
	})
	return t, mapTableError(err)
}

// ArchiveTable retires a table; refused while a session is open so no guest loses their table.
func (s *TableService) ArchiveTable(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID) error {
	if !actor.Can(rbac.TableManage) {
		return ErrForbidden
	}
	return mapTableError(s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		t, err := s.repo.GetTable(ctx, tx, actor.Staff.Store.ID, id, true)
		if err != nil {
			return err
		}
		open, err := s.repo.GetOpenSessionForUpdate(ctx, tx, t.StoreID, t.ID)
		if err != nil {
			return err
		}
		if open != nil {
			return ErrTableSessionOpen
		}
		now := s.nowFunc().UTC()
		t.ArchivedAt, t.IsActive = &now, false
		if err := s.repo.UpdateTable(ctx, tx, t); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "TABLE_UPDATE", "Meja diarsipkan", t.ID, map[string]any{"table_number": t.TableNumber, "archived": true})
	}))
}

// RotateQR issues a new token; the printed sticker with the old token stops resolving at once (QR1).
func (s *TableService) RotateQR(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID) (*domain.Table, error) {
	if !actor.Can(rbac.TableQRManage) {
		return nil, ErrForbidden
	}
	token, err := newQRToken()
	if err != nil {
		return nil, err
	}
	var t *domain.Table
	err = s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		var err error
		if t, err = s.repo.GetTable(ctx, tx, actor.Staff.Store.ID, id, true); err != nil {
			return err
		}
		t.QRToken, t.QRRotatedAt = token, s.nowFunc().UTC()
		if err := s.repo.UpdateTable(ctx, tx, t); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "TABLE_QR_ROTATE", "QR meja diganti", t.ID, map[string]any{"table_number": t.TableNumber})
	})
	return t, mapTableError(err)
}

// OpenSession seats guests at a table from the POS; it returns the already open session if any.
func (s *TableService) OpenSession(ctx context.Context, actor *AuthenticatedStaff, tableID uuid.UUID) (*domain.TableSession, error) {
	if !actor.Can(rbac.TabManage) {
		return nil, ErrForbidden
	}
	var session *domain.TableSession
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		if err := s.closeSettled(ctx, tx, actor.Staff.Store.ID); err != nil {
			return err
		}
		t, err := s.repo.GetTable(ctx, tx, actor.Staff.Store.ID, tableID, true)
		if err != nil {
			return err
		}
		if !t.IsActive {
			return ErrTableInactive
		}
		if session, err = s.repo.GetOpenSessionForUpdate(ctx, tx, t.StoreID, t.ID); err != nil || session != nil {
			return err
		}
		userID := actor.Staff.UserID
		session = &domain.TableSession{
			ID: uuid.New(), StoreID: t.StoreID, TableID: t.ID, Status: domain.TableSessionOpen,
			OpenedAt: s.nowFunc().UTC(), OpenedVia: "staff", OpenedByUserID: &userID,
		}
		return s.repo.InsertSession(ctx, tx, session)
	})
	return session, mapTableError(err)
}

// CloseSession is the cashier's "Kosongkan meja"; refused while an order of the session is unpaid.
func (s *TableService) CloseSession(ctx context.Context, actor *AuthenticatedStaff, sessionID uuid.UUID) (*domain.TableSession, error) {
	if !actor.Can(rbac.TabManage) {
		return nil, ErrForbidden
	}
	var session *domain.TableSession
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		var err error
		if session, err = s.repo.GetSessionForUpdate(ctx, tx, actor.Staff.Store.ID, sessionID); err != nil {
			return err
		}
		if session.Status == domain.TableSessionClosed {
			return nil // idempotent replay
		}
		unpaid, err := s.repo.CountUnpaidOrders(ctx, tx, session.StoreID, session.ID)
		if err != nil {
			return err
		}
		if unpaid > 0 {
			return ErrTableHasUnpaidOrders
		}
		now, userID, reason := s.nowFunc().UTC(), actor.Staff.UserID, "cashier"
		session.Status, session.ClosedAt, session.ClosedByUserID, session.CloseReason = domain.TableSessionClosed, &now, &userID, &reason
		if err := s.repo.CloseSession(ctx, tx, session); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "TABLE_SESSION_CLOSE", "Meja dikosongkan kasir", session.ID, map[string]any{"table_id": session.TableID})
	})
	return session, mapTableError(err)
}

// PublicTable is what a scanned QR reveals: enough to show the menu header, nothing internal.
type PublicTable struct {
	StoreName    string `json:"store_name"`
	StoreSlug    string `json:"store_slug"`
	TableNumber  string `json:"table_number"`
	Label        string `json:"label"`
	OrderingOpen bool   `json:"ordering_open"`
	ClosedReason string `json:"closed_reason,omitempty"`
}

// ResolveQR maps a scanned token to its table. Unknown tokens and archived tables or stores are
// indistinguishable (404); a paused store, a store without an open shift or an inactive table still
// resolve but report ordering_open = false.
func (s *TableService) ResolveQR(ctx context.Context, token string) (*PublicTable, error) {
	if len(token) < 22 || len(token) > 64 {
		return nil, ErrTableNotFound
	}
	storeID, tableID, active, err := s.repo.ResolveTableQR(ctx, s.db, token)
	if err != nil {
		return nil, mapTableError(err)
	}
	var out *PublicTable
	err = database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		store, err := s.orders.GetStoreForOrder(ctx, tx, storeID)
		if err != nil {
			return err
		}
		t, err := s.repo.GetTable(ctx, tx, storeID, tableID, false)
		if err != nil {
			return err
		}
		settings, err := s.settings.GetStoreSettings(ctx, tx, storeID)
		if err != nil {
			return err
		}
		if err := s.repo.CloseSettledSessions(ctx, tx, storeID, settings.TableIdleCloseMinutes, s.nowFunc().UTC()); err != nil {
			return err
		}
		out = &PublicTable{StoreName: store.Name, StoreSlug: store.Slug, TableNumber: t.TableNumber, Label: t.Label}
		switch {
		case !active:
			out.ClosedReason = QRClosedInactive
		case settings.QROrderingPaused:
			out.ClosedReason = QRClosedPaused
		default:
			open, err := s.repo.HasOpenShift(ctx, tx, storeID)
			if err != nil {
				return err
			}
			if !open {
				out.ClosedReason = QRClosedNoShift
			}
		}
		out.OrderingOpen = out.ClosedReason == ""
		return nil
	})
	if err != nil {
		return nil, mapTableError(err)
	}
	return out, nil
}

func (s *TableService) closeSettled(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID) error {
	settings, err := s.settings.GetStoreSettings(ctx, tx, storeID)
	if err != nil {
		return err
	}
	return s.repo.CloseSettledSessions(ctx, tx, storeID, settings.TableIdleCloseMinutes, s.nowFunc().UTC())
}

func (s *TableService) audit(ctx context.Context, tx *sqlx.Tx, actor *AuthenticatedStaff, action, reason string, ref uuid.UUID, meta map[string]any) error {
	payload, _ := json.Marshal(meta)
	actorID := actor.Staff.UserID
	return postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{
		StoreID: actor.Staff.Store.ID, ActorID: &actorID, ActorName: actor.Staff.FullName, Action: action,
		Reason: reason, ReferenceID: ref.String(), Metadata: string(payload), CreatedAt: s.nowFunc().UTC(),
	})
}

func (s *TableService) tx(ctx context.Context, actor *AuthenticatedStaff, fn func(tx *sqlx.Tx) error) error {
	if actor == nil {
		return ErrInvalidSession
	}
	return database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, fn)
}

func applyTable(t *domain.Table, in TableInput) error {
	f := fieldErrors{}
	if in.TableNumber != nil {
		t.TableNumber = f.text("table_number", *in.TableNumber, 1, 50)
	}
	if in.Label != nil {
		t.Label = f.text("label", *in.Label, 0, 100)
	}
	if in.Section != nil {
		t.Section = f.text("section", *in.Section, 0, 50)
	}
	if in.Capacity != nil {
		f.intRange("capacity", int64(*in.Capacity), 1, 100)
		t.Capacity = *in.Capacity
	}
	if in.IsActive != nil {
		t.IsActive = *in.IsActive
	}
	return f.err()
}

func mapTableError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, postgres.ErrTableNotFound), errors.Is(err, postgres.ErrTableSessionNotFound), errors.Is(err, postgres.ErrStoreNotFound):
		return ErrTableNotFound
	}
	if constraint, ok := postgres.UniqueViolation(err); ok {
		switch constraint {
		case "uq_tables_store_number_active":
			return &ConflictError{Field: "table_number", Message: "nomor meja sudah dipakai"}
		case "uq_table_sessions_one_open":
			return ErrTableSessionOpen
		}
		return &ConflictError{Message: "data duplikat"}
	}
	return err
}
