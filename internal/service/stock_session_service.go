package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
)

// Stock counts and waste (K2, PR 11.6, X8). Staff with inventory.count record a session; one worth more than
// stockSessionApprovalLimit waits for the owner (inventory.manage), who posts or rejects it. Posting applies
// each line's delta, not the counted figure, so sales between counting and approval stay deducted.

// shortcut: one fixed limit for every store, make it a store setting when an owner asks for another figure.
const stockSessionApprovalLimit int64 = 50_000

var ErrStockSessionNotFound = fmt.Errorf("stock session: %w", ErrCatalogNotFound)

type StockSessionLineInput struct {
	IngredientID *uuid.UUID `json:"ingredient_id"`
	ProductID    *uuid.UUID `json:"product_id"`
	Quantity     int64      `json:"quantity"` // counted quantity (count) or quantity thrown away (waste), in base units
	Reason       string     `json:"reason"`
}

type StockSessionInput struct {
	Kind  string                  `json:"kind"`
	Note  string                  `json:"note"`
	Lines []StockSessionLineInput `json:"lines"`
}

type StockSessionLine struct {
	IngredientID     *uuid.UUID `json:"ingredient_id,omitempty" db:"ingredient_id"`
	ProductID        *uuid.UUID `json:"product_id,omitempty" db:"product_id"`
	Name             string     `json:"name" db:"name"`
	Unit             string     `json:"unit" db:"unit"`
	ExpectedQuantity int64      `json:"expected_quantity" db:"expected_quantity"`
	CountedQuantity  *int64     `json:"counted_quantity,omitempty" db:"counted_quantity"`
	Delta            int64      `json:"delta" db:"delta"`
	UnitCostPer1000  int64      `json:"unit_cost_per_1000" db:"unit_cost_per_1000"`
	Reason           string     `json:"reason" db:"reason"`
}

type StockSession struct {
	ID           uuid.UUID          `json:"id" db:"id"`
	Kind         string             `json:"kind" db:"kind"`
	Status       string             `json:"status" db:"status"`
	ValueAmount  int64              `json:"value_amount" db:"value_amount"`
	Note         string             `json:"note" db:"note"`
	CreatedBy    uuid.UUID          `json:"created_by" db:"created_by"`
	CreatedName  string             `json:"created_by_name" db:"created_name"`
	DecidedBy    *uuid.UUID         `json:"decided_by,omitempty" db:"decided_by"`
	DecisionNote string             `json:"decision_note" db:"decision_note"`
	CreatedAt    time.Time          `json:"created_at" db:"created_at"`
	DecidedAt    *time.Time         `json:"decided_at,omitempty" db:"decided_at"`
	Lines        []StockSessionLine `json:"lines" db:"-"`
}

// StockItem is what staff see when counting: names and units only, never the system balance (blind count).
type StockItem struct {
	ID   uuid.UUID `json:"id" db:"id"`
	Kind string    `json:"kind" db:"kind"`
	Name string    `json:"name" db:"name"`
	Unit string    `json:"unit" db:"unit"`
}

func (s *InventoryService) txAs(ctx context.Context, actor *AuthenticatedStaff, perm rbac.Permission, fn func(tx *sqlx.Tx) error) error {
	if actor == nil {
		return ErrInvalidSession
	}
	if !actor.Can(perm) {
		return ErrForbidden
	}
	return database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, fn)
}

// ListStockItems returns active ingredients and stock-tracked ready-to-serve products.
func (s *InventoryService) ListStockItems(ctx context.Context, actor *AuthenticatedStaff) ([]StockItem, error) {
	out := []StockItem{}
	err := s.txAs(ctx, actor, rbac.InventoryCount, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `SELECT id, 'ingredient' AS kind, name, unit FROM ingredients WHERE store_id = $1 AND is_active
			UNION ALL SELECT id, 'product', name, 'pcs' FROM products WHERE store_id = $1 AND archived_at IS NULL
				AND product_type = 'ready_to_serve' AND track_stock
			ORDER BY kind, name`, actor.Staff.Store.ID)
	})
	return out, err
}

func validateStockSession(in StockSessionInput) error {
	f := fieldErrors{}
	f.oneOf("kind", in.Kind, "count", "waste")
	f.text("note", in.Note, 0, 500)
	if len(in.Lines) == 0 || len(in.Lines) > 200 {
		f.add("lines", "isi 1-200 baris")
	}
	seen := map[uuid.UUID]bool{}
	for i, l := range in.Lines {
		p := fmt.Sprintf("lines[%d].", i)
		if (l.IngredientID == nil) == (l.ProductID == nil) {
			f.add(p+"ingredient_id", "pilih salah satu: ingredient_id atau product_id")
			continue
		}
		id := l.IngredientID
		if id == nil {
			id = l.ProductID
		}
		if seen[*id] {
			f.add(p+"ingredient_id", "barang yang sama muncul dua kali")
		}
		seen[*id] = true
		if in.Kind == "waste" {
			f.intRange(p+"quantity", l.Quantity, 1, 100_000_000)
		} else {
			f.intRange(p+"quantity", l.Quantity, 0, 100_000_000)
		}
		f.text(p+"reason", l.Reason, 0, 200)
	}
	return f.err()
}

// SubmitStockSession records a count or waste session; it posts at once when the actor owns the inventory or
// its value is within the limit, and waits for the owner otherwise.
func (s *InventoryService) SubmitStockSession(ctx context.Context, actor *AuthenticatedStaff, in StockSessionInput) (*StockSession, error) {
	if err := validateStockSession(in); err != nil {
		return nil, err
	}
	var out *StockSession
	err := s.txAs(ctx, actor, rbac.InventoryCount, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID
		now := s.nowFunc().UTC()
		sess := StockSession{ID: uuid.New(), Kind: in.Kind, Note: strings.TrimSpace(in.Note), CreatedBy: actor.Staff.UserID, CreatedAt: now}
		for i, l := range in.Lines {
			line, err := stockLineTx(ctx, tx, storeID, in.Kind, l)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return &ValidationError{Fields: map[string]string{fmt.Sprintf("lines[%d]", i): "bahan nonaktif atau produk tanpa stok di toko ini"}}
				}
				return err
			}
			sess.ValueAmount += roundHalfUpDiv(abs64(line.Delta)*line.UnitCostPer1000, 1000)
			sess.Lines = append(sess.Lines, line)
		}
		sess.Status = "pending"
		if actor.Can(rbac.InventoryManage) || sess.ValueAmount <= stockSessionApprovalLimit {
			sess.Status = "posted"
		}
		if sess.Status == "posted" {
			sess.DecidedBy, sess.DecidedAt = &sess.CreatedBy, &now
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO stock_sessions (id, store_id, kind, status, value_amount, note, created_by, created_at, decided_by, decided_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`, sess.ID, storeID, sess.Kind, sess.Status, sess.ValueAmount, sess.Note, sess.CreatedBy, now,
			sess.DecidedBy, sess.DecidedAt); err != nil {
			return fmt.Errorf("failed to record stock session: %w", err)
		}
		for _, l := range sess.Lines {
			if _, err := tx.ExecContext(ctx, `INSERT INTO stock_session_lines (store_id, stock_session_id, ingredient_id, product_id,
				expected_quantity, counted_quantity, delta, unit_cost_per_1000, reason) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
				storeID, sess.ID, l.IngredientID, l.ProductID, l.ExpectedQuantity, l.CountedQuantity, l.Delta, l.UnitCostPer1000, l.Reason); err != nil {
				return fmt.Errorf("failed to record stock session line: %w", err)
			}
		}
		if sess.Status == "posted" {
			if err := postStockSessionTx(ctx, tx, storeID, &sess, actor.Staff.UserID, now); err != nil {
				return err
			}
		}
		meta := map[string]any{"kind": sess.Kind, "status": sess.Status, "value_amount": sess.ValueAmount, "lines": len(sess.Lines)}
		if err := s.audit(ctx, tx, actor, "STOCK_SESSION_SUBMITTED", "Sesi "+stockKindLabel(sess.Kind)+" dicatat", sess.ID, meta, now); err != nil {
			return err
		}
		var err error
		out, err = loadStockSessionTx(ctx, tx, storeID, sess.ID)
		return err
	})
	return out, err
}

// stockLineTx reads the item's balance and cost now and works out the line's delta.
func stockLineTx(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, kind string, l StockSessionLineInput) (StockSessionLine, error) {
	var cur struct {
		Stock int64 `db:"stock_quantity"`
		Cost  int64 `db:"cost"`
	}
	var err error
	if l.IngredientID != nil {
		err = tx.GetContext(ctx, &cur, `SELECT stock_quantity, cost_per_1000 AS cost FROM ingredients WHERE store_id = $1 AND id = $2 AND is_active FOR UPDATE`,
			storeID, *l.IngredientID)
	} else {
		err = tx.GetContext(ctx, &cur, `SELECT stock_quantity, unit_cost * 1000 AS cost FROM products WHERE store_id = $1 AND id = $2
			AND archived_at IS NULL AND product_type = 'ready_to_serve' AND track_stock FOR UPDATE`, storeID, *l.ProductID)
	}
	line := StockSessionLine{IngredientID: l.IngredientID, ProductID: l.ProductID, ExpectedQuantity: cur.Stock, UnitCostPer1000: cur.Cost,
		Reason: strings.TrimSpace(l.Reason)}
	if kind == "count" {
		counted := l.Quantity
		line.CountedQuantity, line.Delta = &counted, counted-cur.Stock
	} else {
		line.Delta = -l.Quantity
	}
	return line, err
}

// postStockSessionTx applies every line's delta to the stock balance; ingredients also get a movement.
func postStockSessionTx(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, sess *StockSession, actorID uuid.UUID, now time.Time) error {
	note := truncate("Sesi "+stockKindLabel(sess.Kind)+" "+sess.ID.String()[:8], 200)
	for _, l := range sess.Lines {
		if l.Delta == 0 {
			continue
		}
		if l.IngredientID != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE ingredients SET stock_quantity = stock_quantity + $3, updated_at = $4 WHERE store_id = $1 AND id = $2`,
				storeID, *l.IngredientID, l.Delta, now); err != nil {
				return fmt.Errorf("failed to adjust ingredient stock: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO ingredient_movements (store_id, ingredient_id, kind, quantity, cost_per_1000, actor_id, note, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, storeID, *l.IngredientID, sess.Kind, l.Delta, l.UnitCostPer1000, actorID, note, now); err != nil {
				return fmt.Errorf("failed to record ingredient movement: %w", err)
			}
		} else if _, err := tx.ExecContext(ctx, `UPDATE products SET stock_quantity = stock_quantity + $3, updated_at = $4
			WHERE store_id = $1 AND id = $2 AND track_stock`, storeID, *l.ProductID, l.Delta, now); err != nil {
			return fmt.Errorf("failed to adjust product stock: %w", err)
		}
	}
	return nil
}

// DecideStockSession lets the owner approve (post) or reject a pending session.
func (s *InventoryService) DecideStockSession(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID, approve bool, note string) (*StockSession, error) {
	f := fieldErrors{}
	note = f.text("note", note, 0, 500)
	if err := f.err(); err != nil {
		return nil, err
	}
	var out *StockSession
	err := s.txAs(ctx, actor, rbac.InventoryManage, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID
		now := s.nowFunc().UTC()
		sess, err := loadStockSessionTx(ctx, tx, storeID, id, "FOR UPDATE OF ss")
		if err != nil {
			return err
		}
		if sess.Status != "pending" {
			return &ConflictError{Field: "status", Message: "sesi stok sudah diputuskan"}
		}
		status, action := "rejected", "STOCK_SESSION_REJECTED"
		if approve {
			status, action = "posted", "STOCK_SESSION_APPROVED"
			if err := postStockSessionTx(ctx, tx, storeID, sess, actor.Staff.UserID, now); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE stock_sessions SET status = $3, decided_by = $4, decided_at = $5, decision_note = $6
			WHERE store_id = $1 AND id = $2`, storeID, id, status, actor.Staff.UserID, now, note); err != nil {
			return err
		}
		meta := map[string]any{"kind": sess.Kind, "value_amount": sess.ValueAmount}
		if err := s.audit(ctx, tx, actor, action, note, id, meta, now); err != nil {
			return err
		}
		out, err = loadStockSessionTx(ctx, tx, storeID, id)
		return err
	})
	return out, err
}

// ListStockSessions lists sessions, newest first; status filters (pending is the owner's approval queue).
func (s *InventoryService) ListStockSessions(ctx context.Context, actor *AuthenticatedStaff, status string) ([]StockSession, error) {
	if status != "" {
		f := fieldErrors{}
		f.oneOf("status", status, "posted", "pending", "rejected")
		if err := f.err(); err != nil {
			return nil, err
		}
	}
	out := []StockSession{}
	err := s.txAs(ctx, actor, rbac.InventoryManage, func(tx *sqlx.Tx) error {
		var ids []uuid.UUID
		if err := tx.SelectContext(ctx, &ids, `SELECT id FROM stock_sessions WHERE store_id = $1 AND ($2 = '' OR status = $2)
			ORDER BY created_at DESC LIMIT 100`, actor.Staff.Store.ID, status); err != nil {
			return err
		}
		for _, id := range ids {
			sess, err := loadStockSessionTx(ctx, tx, actor.Staff.Store.ID, id)
			if err != nil {
				return err
			}
			out = append(out, *sess)
		}
		return nil
	})
	return out, err
}

func loadStockSessionTx(ctx context.Context, tx *sqlx.Tx, storeID, id uuid.UUID, lock ...string) (*StockSession, error) {
	var sess StockSession
	err := tx.GetContext(ctx, &sess, `SELECT ss.id, ss.kind, ss.status, ss.value_amount, ss.note, ss.created_by, u.full_name AS created_name,
		ss.decided_by, ss.decision_note, ss.created_at, ss.decided_at
		FROM stock_sessions ss JOIN users u ON u.store_id = ss.store_id AND u.id = ss.created_by
		WHERE ss.store_id = $1 AND ss.id = $2 `+strings.Join(lock, ""), storeID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrStockSessionNotFound
	}
	if err != nil {
		return nil, err
	}
	sess.Lines = []StockSessionLine{}
	err = tx.SelectContext(ctx, &sess.Lines, `SELECT l.ingredient_id, l.product_id, COALESCE(i.name, p.name) AS name, COALESCE(i.unit, 'pcs') AS unit,
		l.expected_quantity, l.counted_quantity, l.delta, l.unit_cost_per_1000, l.reason
		FROM stock_session_lines l
		LEFT JOIN ingredients i ON i.store_id = l.store_id AND i.id = l.ingredient_id
		LEFT JOIN products p ON p.store_id = l.store_id AND p.id = l.product_id
		WHERE l.store_id = $1 AND l.stock_session_id = $2 ORDER BY name`, storeID, id)
	return &sess, err
}

func stockKindLabel(kind string) string {
	if kind == "waste" {
		return "barang terbuang"
	}
	return "hitung stok"
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
