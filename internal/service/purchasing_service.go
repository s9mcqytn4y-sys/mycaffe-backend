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
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

// Purchasing (K2, PR 11.4, decisions X7 and X9): suppliers, purchase orders with staged receipts, and direct
// purchases without a PO. Every receipt line adds stock (in base units) to an ingredient or a ready-to-serve
// product and updates its moving-average cost. Receipts are append-only; payables follow in PR 11.5.

var (
	ErrSupplierNotFound      = fmt.Errorf("supplier: %w", ErrCatalogNotFound)
	ErrPurchaseOrderNotFound = fmt.Errorf("purchase order: %w", ErrCatalogNotFound)
	// ErrPurchaseOrderState answers 409 through the shared conflict mapping.
	ErrPurchaseOrderState error = &ConflictError{Field: "status", Message: "status PO tidak mengizinkan aksi ini"}
)

type Supplier struct {
	ID          uuid.UUID `json:"id" db:"id"`
	Name        string    `json:"name" db:"name"`
	Contact     string    `json:"contact" db:"contact"`
	PaymentDays int       `json:"payment_days" db:"payment_days"`
	Notes       string    `json:"notes" db:"notes"`
	IsActive    bool      `json:"is_active" db:"is_active"`
}

const supplierColumns = `id, name, contact, payment_days, notes, is_active`

type SupplierInput struct {
	Name        *string `json:"name"`
	Contact     *string `json:"contact"`
	PaymentDays *int    `json:"payment_days"`
	Notes       *string `json:"notes"`
	IsActive    *bool   `json:"is_active"`
}

// StockLineInput buys Quantity purchase units of an ingredient or a ready-to-serve product at UnitPrice each;
// Conversion is how many base units (g, ml, pcs or product units) one purchase unit holds.
type StockLineInput struct {
	IngredientID *uuid.UUID `json:"ingredient_id,omitempty"`
	ProductID    *uuid.UUID `json:"product_id,omitempty"`
	PurchaseUnit string     `json:"purchase_unit"`
	Conversion   int64      `json:"conversion"`
	Quantity     int64      `json:"quantity"`
	UnitPrice    int64      `json:"unit_price"`
}

type PurchaseOrderLine struct {
	ID               uuid.UUID  `json:"id" db:"id"`
	IngredientID     *uuid.UUID `json:"ingredient_id,omitempty" db:"ingredient_id"`
	ProductID        *uuid.UUID `json:"product_id,omitempty" db:"product_id"`
	ItemName         string     `json:"item_name" db:"item_name"`
	PurchaseUnit     string     `json:"purchase_unit" db:"purchase_unit"`
	Conversion       int64      `json:"conversion" db:"conversion"`
	Quantity         int64      `json:"quantity" db:"quantity"`
	UnitPrice        int64      `json:"unit_price" db:"unit_price"`
	ReceivedQuantity int64      `json:"received_quantity" db:"received_quantity"`
}

type PurchaseOrder struct {
	ID           uuid.UUID           `json:"id" db:"id"`
	Number       int                 `json:"number" db:"number"`
	SupplierID   uuid.UUID           `json:"supplier_id" db:"supplier_id"`
	SupplierName string              `json:"supplier_name" db:"supplier_name"`
	Status       string              `json:"status" db:"status"`
	ExpectedDate *string             `json:"expected_date,omitempty" db:"expected_date"`
	Notes        string              `json:"notes" db:"notes"`
	Total        int64               `json:"total" db:"total"`
	CreatedAt    time.Time           `json:"created_at" db:"created_at"`
	Lines        []PurchaseOrderLine `json:"lines,omitempty"`
}

type PurchaseOrderInput struct {
	SupplierID   *uuid.UUID       `json:"supplier_id"`
	ExpectedDate *string          `json:"expected_date"`
	Notes        *string          `json:"notes"`
	Lines        []StockLineInput `json:"lines"`
}

// ReceiveLineInput receives Quantity purchase units of a PO line; UnitPrice corrects the PO price.
type ReceiveLineInput struct {
	PurchaseOrderLineID uuid.UUID `json:"purchase_order_line_id"`
	Quantity            int64     `json:"quantity"`
	UnitPrice           *int64    `json:"unit_price,omitempty"`
}

type GoodsReceipt struct {
	ID              uuid.UUID  `json:"id" db:"id"`
	PurchaseOrderID *uuid.UUID `json:"purchase_order_id,omitempty" db:"purchase_order_id"`
	SupplierID      *uuid.UUID `json:"supplier_id,omitempty" db:"supplier_id"`
	SupplierName    string     `json:"supplier_name" db:"supplier_name"`
	TotalAmount     int64      `json:"total_amount" db:"total_amount"`
	Note            string     `json:"note" db:"note"`
	ReceivedAt      time.Time  `json:"received_at" db:"received_at"`
}

type DirectPurchaseInput struct {
	SupplierID   *uuid.UUID       `json:"supplier_id"`
	SupplierName string           `json:"supplier_name"`
	Note         string           `json:"note"`
	Lines        []StockLineInput `json:"lines"`
	// PayWith settles the purchase at once (outlet_cash, transfer or cash); empty leaves a payable (PR 11.5).
	PayWith          string `json:"pay_with"`
	PaymentReference string `json:"payment_reference"`
}

// ---------- Suppliers ----------

func applySupplier(s *Supplier, in SupplierInput) error {
	f := fieldErrors{}
	if in.Name != nil {
		s.Name = f.text("name", *in.Name, 1, 100)
	}
	if in.Contact != nil {
		s.Contact = f.text("contact", *in.Contact, 0, 150)
	}
	if in.PaymentDays != nil {
		s.PaymentDays = *in.PaymentDays
		f.intRange("payment_days", int64(s.PaymentDays), 0, 180)
	}
	if in.Notes != nil {
		s.Notes = f.text("notes", *in.Notes, 0, 500)
	}
	if in.IsActive != nil {
		s.IsActive = *in.IsActive
	}
	if s.Name == "" {
		f.add("name", "wajib diisi")
	}
	return f.err()
}

func supplierConflict(err error) error {
	if c, ok := postgres.UniqueViolation(err); ok && c == "uq_suppliers_store_name_active" {
		return &ConflictError{Field: "name", Message: "nama pemasok sudah dipakai"}
	}
	return err
}

func (s *InventoryService) ListSuppliers(ctx context.Context, actor *AuthenticatedStaff) ([]Supplier, error) {
	out := []Supplier{}
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `SELECT `+supplierColumns+` FROM suppliers WHERE store_id = $1 ORDER BY is_active DESC, lower(name)`, actor.Staff.Store.ID)
	})
	return out, err
}

func (s *InventoryService) CreateSupplier(ctx context.Context, actor *AuthenticatedStaff, in SupplierInput) (*Supplier, error) {
	sup := &Supplier{ID: uuid.New(), IsActive: true}
	if err := applySupplier(sup, in); err != nil {
		return nil, err
	}
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO suppliers (id, store_id, name, contact, payment_days, notes) VALUES ($1, $2, $3, $4, $5, $6)`,
			sup.ID, actor.Staff.Store.ID, sup.Name, sup.Contact, sup.PaymentDays, sup.Notes); err != nil {
			return supplierConflict(err)
		}
		return s.audit(ctx, tx, actor, "SUPPLIER_CHANGED", "Pemasok dibuat: "+sup.Name, sup.ID, map[string]any{"after": sup}, s.nowFunc().UTC())
	})
	if err != nil {
		return nil, err
	}
	return sup, nil
}

func (s *InventoryService) UpdateSupplier(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID, in SupplierInput) (*Supplier, error) {
	var sup Supplier
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		err := tx.GetContext(ctx, &sup, `SELECT `+supplierColumns+` FROM suppliers WHERE store_id = $1 AND id = $2 FOR UPDATE`, actor.Staff.Store.ID, id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSupplierNotFound
		}
		if err != nil {
			return err
		}
		before := sup
		if err := applySupplier(&sup, in); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE suppliers SET name = $3, contact = $4, payment_days = $5, notes = $6, is_active = $7, updated_at = NOW()
			WHERE store_id = $1 AND id = $2`, actor.Staff.Store.ID, id, sup.Name, sup.Contact, sup.PaymentDays, sup.Notes, sup.IsActive); err != nil {
			return supplierConflict(err)
		}
		return s.audit(ctx, tx, actor, "SUPPLIER_CHANGED", "Pemasok diubah: "+sup.Name, id, map[string]any{"before": before, "after": sup}, s.nowFunc().UTC())
	})
	if err != nil {
		return nil, err
	}
	return &sup, nil
}

// ---------- Stock lines ----------

func validateStockLines(lines []StockLineInput, field string) error {
	f := fieldErrors{}
	if len(lines) == 0 || len(lines) > 100 {
		f.add(field, "isi 1-100 baris")
	}
	for i, l := range lines {
		p := fmt.Sprintf("%s[%d].", field, i)
		if (l.IngredientID == nil) == (l.ProductID == nil) {
			f.add(p+"ingredient_id", "pilih salah satu: ingredient_id atau product_id")
		}
		f.text(p+"purchase_unit", l.PurchaseUnit, 1, 30)
		f.intRange(p+"conversion", l.Conversion, 1, 1_000_000)
		f.intRange(p+"quantity", l.Quantity, 1, 1_000_000)
		f.intRange(p+"unit_price", l.UnitPrice, 0, maxItemPrice)
	}
	return f.err()
}

// checkStockItemsTx refuses lines whose ingredient is not active or whose product is not a live
// ready-to-serve product of the store (X9).
func checkStockItemsTx(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, lines []StockLineInput) error {
	for i, l := range lines {
		var ok bool
		var err error
		if l.IngredientID != nil {
			err = tx.GetContext(ctx, &ok, `SELECT EXISTS (SELECT 1 FROM ingredients WHERE store_id = $1 AND id = $2 AND is_active)`, storeID, *l.IngredientID)
		} else {
			err = tx.GetContext(ctx, &ok, `SELECT EXISTS (SELECT 1 FROM products WHERE store_id = $1 AND id = $2 AND archived_at IS NULL
				AND product_type = 'ready_to_serve')`, storeID, *l.ProductID)
		}
		if err != nil {
			return err
		}
		if !ok {
			return &ValidationError{Fields: map[string]string{fmt.Sprintf("lines[%d]", i): "bahan nonaktif atau bukan produk siap saji toko ini"}}
		}
	}
	return nil
}

// postStockLineTx writes one receipt line, adds its base units to stock and blends its cost into the
// moving average. Negative balances carry no weight in the average (they were sold below zero, X5).
func postStockLineTx(ctx context.Context, tx *sqlx.Tx, storeID, receiptID uuid.UUID, poLineID *uuid.UUID, l StockLineInput, actorID uuid.UUID, now time.Time) (int64, error) {
	base := l.Quantity * l.Conversion
	total := l.Quantity * l.UnitPrice
	if l.IngredientID != nil {
		var cur struct {
			Stock int64 `db:"stock_quantity"`
			Cost  int64 `db:"cost_per_1000"`
		}
		if err := tx.GetContext(ctx, &cur, `SELECT stock_quantity, cost_per_1000 FROM ingredients WHERE store_id = $1 AND id = $2 FOR UPDATE`, storeID, *l.IngredientID); err != nil {
			return 0, err
		}
		lineCost := roundHalfUpDiv(total*1000, base)
		held := max(cur.Stock, 0)
		avg := roundHalfUpDiv(held*cur.Cost+base*lineCost, held+base)
		if _, err := tx.ExecContext(ctx, `UPDATE ingredients SET stock_quantity = stock_quantity + $3, cost_per_1000 = $4, updated_at = $5
			WHERE store_id = $1 AND id = $2`, storeID, *l.IngredientID, base, avg, now); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO ingredient_movements (store_id, ingredient_id, kind, quantity, cost_per_1000, actor_id, note, created_at)
			VALUES ($1, $2, 'purchase', $3, $4, $5, 'Penerimaan barang', $6)`, storeID, *l.IngredientID, base, lineCost, actorID, now); err != nil {
			return 0, err
		}
	} else {
		var cur struct {
			Stock int64 `db:"stock_quantity"`
			Cost  int64 `db:"unit_cost"`
			Track bool  `db:"track_stock"`
		}
		if err := tx.GetContext(ctx, &cur, `SELECT stock_quantity, unit_cost, track_stock FROM products WHERE store_id = $1 AND id = $2 FOR UPDATE`, storeID, *l.ProductID); err != nil {
			return 0, err
		}
		held := int64(0)
		if cur.Track {
			held = max(cur.Stock, 0)
		}
		avg := roundHalfUpDiv(held*cur.Cost+total, held+base)
		if _, err := tx.ExecContext(ctx, `UPDATE products SET stock_quantity = stock_quantity + CASE WHEN track_stock THEN $3 ELSE 0 END,
			unit_cost = $4, updated_at = $5 WHERE store_id = $1 AND id = $2`, storeID, *l.ProductID, base, avg, now); err != nil {
			return 0, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO goods_receipt_lines (store_id, goods_receipt_id, purchase_order_line_id, ingredient_id, product_id,
		purchase_unit, conversion, quantity, unit_price, line_total) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		storeID, receiptID, poLineID, l.IngredientID, l.ProductID, strings.TrimSpace(l.PurchaseUnit), l.Conversion, l.Quantity, l.UnitPrice, total); err != nil {
		return 0, fmt.Errorf("failed to record receipt line: %w", err)
	}
	return total, nil
}

func insertReceiptTx(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, r *GoodsReceipt, actorID uuid.UUID) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO goods_receipts (id, store_id, purchase_order_id, supplier_id, supplier_name, total_amount, note, received_by, received_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, r.ID, storeID, r.PurchaseOrderID, r.SupplierID, r.SupplierName, r.TotalAmount, r.Note, actorID, r.ReceivedAt)
	return err
}

// ---------- Direct purchases ----------

// DirectPurchase records goods bought without a PO (market run): stock and moving-average cost at once.
func (s *InventoryService) DirectPurchase(ctx context.Context, actor *AuthenticatedStaff, in DirectPurchaseInput) (*GoodsReceipt, error) {
	if err := validateStockLines(in.Lines, "lines"); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.SupplierName)
	if in.SupplierID == nil && name == "" {
		return nil, &ValidationError{Fields: map[string]string{"supplier_name": "isi pemasok atau nama tempat beli"}}
	}
	if in.PayWith != "" {
		f := fieldErrors{}
		f.oneOf("pay_with", in.PayWith, PayableOutletCash, PayableTransfer, PayableCash)
		if err := f.err(); err != nil {
			return nil, err
		}
	}
	now := s.nowFunc().UTC()
	r := &GoodsReceipt{ID: uuid.New(), SupplierID: in.SupplierID, SupplierName: truncate(name, 100), Note: truncate(strings.TrimSpace(in.Note), 500), ReceivedAt: now}
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID
		if in.SupplierID != nil {
			if err := tx.GetContext(ctx, &r.SupplierName, `SELECT name FROM suppliers WHERE store_id = $1 AND id = $2 AND is_active`, storeID, *in.SupplierID); errors.Is(err, sql.ErrNoRows) {
				return ErrSupplierNotFound
			} else if err != nil {
				return err
			}
		}
		if err := checkStockItemsTx(ctx, tx, storeID, in.Lines); err != nil {
			return err
		}
		for _, l := range in.Lines {
			r.TotalAmount += l.Quantity * l.UnitPrice
		}
		if err := insertReceiptTx(ctx, tx, storeID, r, actor.Staff.UserID); err != nil {
			return err
		}
		payableID, err := createPayableTx(ctx, tx, storeID, r)
		if err != nil {
			return err
		}
		for _, l := range in.Lines {
			if _, err := postStockLineTx(ctx, tx, storeID, r.ID, nil, l, actor.Staff.UserID, now); err != nil {
				return err
			}
		}
		if err := s.audit(ctx, tx, actor, "GOODS_RECEIVED", "Pembelian langsung dari "+r.SupplierName, r.ID, map[string]any{"total": r.TotalAmount, "lines": in.Lines}, now); err != nil {
			return err
		}
		if in.PayWith == "" || r.TotalAmount == 0 {
			return nil
		}
		return s.payPayableTx(ctx, tx, actor, payableID, PayPayableInput{Amount: r.TotalAmount, Method: in.PayWith,
			Reference: in.PaymentReference, Note: "Pembelian langsung"}, now)
	})
	if err != nil {
		return nil, err
	}
	return r, nil
}

// ---------- Purchase orders ----------

const poSelect = `SELECT po.id, po.number, po.supplier_id, s.name AS supplier_name, po.status, to_char(po.expected_date, 'YYYY-MM-DD') AS expected_date,
	po.notes, po.created_at, COALESCE((SELECT SUM(l.quantity * l.unit_price) FROM purchase_order_lines l
		WHERE l.store_id = po.store_id AND l.purchase_order_id = po.id), 0) AS total
	FROM purchase_orders po JOIN suppliers s ON s.store_id = po.store_id AND s.id = po.supplier_id`

func (s *InventoryService) loadPurchaseOrderTx(ctx context.Context, tx *sqlx.Tx, storeID, id uuid.UUID, lock bool) (*PurchaseOrder, error) {
	if lock {
		if _, err := tx.ExecContext(ctx, `SELECT 1 FROM purchase_orders WHERE store_id = $1 AND id = $2 FOR UPDATE`, storeID, id); err != nil {
			return nil, err
		}
	}
	var po PurchaseOrder
	err := tx.GetContext(ctx, &po, poSelect+` WHERE po.store_id = $1 AND po.id = $2`, storeID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPurchaseOrderNotFound
	}
	if err != nil {
		return nil, err
	}
	po.Lines = []PurchaseOrderLine{}
	err = tx.SelectContext(ctx, &po.Lines, `SELECT l.id, l.ingredient_id, l.product_id, COALESCE(i.name, p.name) AS item_name, l.purchase_unit,
			l.conversion, l.quantity, l.unit_price, l.received_quantity
		FROM purchase_order_lines l
		LEFT JOIN ingredients i ON i.store_id = l.store_id AND i.id = l.ingredient_id
		LEFT JOIN products p ON p.store_id = l.store_id AND p.id = l.product_id
		WHERE l.store_id = $1 AND l.purchase_order_id = $2 ORDER BY item_name, l.id`, storeID, id)
	return &po, err
}

func (s *InventoryService) ListPurchaseOrders(ctx context.Context, actor *AuthenticatedStaff, status string) ([]PurchaseOrder, error) {
	out := []PurchaseOrder{}
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, poSelect+` WHERE po.store_id = $1 AND ($2 = '' OR po.status = $2) ORDER BY po.number DESC LIMIT 200`,
			actor.Staff.Store.ID, status)
	})
	return out, err
}

func (s *InventoryService) GetPurchaseOrder(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID) (*PurchaseOrder, error) {
	var po *PurchaseOrder
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		var err error
		po, err = s.loadPurchaseOrderTx(ctx, tx, actor.Staff.Store.ID, id, false)
		return err
	})
	return po, err
}

func validatePurchaseOrderInput(in PurchaseOrderInput, create bool) error {
	f := fieldErrors{}
	if create && in.SupplierID == nil {
		f.add("supplier_id", "wajib diisi")
	}
	if in.ExpectedDate != nil && *in.ExpectedDate != "" {
		if _, err := time.Parse("2006-01-02", *in.ExpectedDate); err != nil {
			f.add("expected_date", "format YYYY-MM-DD")
		}
	}
	if in.Notes != nil {
		f.text("notes", *in.Notes, 0, 500)
	}
	if err := f.err(); err != nil {
		return err
	}
	if create || in.Lines != nil {
		return validateStockLines(in.Lines, "lines")
	}
	return nil
}

func (s *InventoryService) replacePOLinesTx(ctx context.Context, tx *sqlx.Tx, storeID, poID uuid.UUID, lines []StockLineInput) error {
	if err := checkStockItemsTx(ctx, tx, storeID, lines); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM purchase_order_lines WHERE store_id = $1 AND purchase_order_id = $2`, storeID, poID); err != nil {
		return err
	}
	for _, l := range lines {
		if _, err := tx.ExecContext(ctx, `INSERT INTO purchase_order_lines (store_id, purchase_order_id, ingredient_id, product_id, purchase_unit,
			conversion, quantity, unit_price) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, storeID, poID, l.IngredientID, l.ProductID,
			strings.TrimSpace(l.PurchaseUnit), l.Conversion, l.Quantity, l.UnitPrice); err != nil {
			return fmt.Errorf("failed to save purchase order line: %w", err)
		}
	}
	return nil
}

func (s *InventoryService) checkSupplierTx(ctx context.Context, tx *sqlx.Tx, storeID, id uuid.UUID) error {
	var ok bool
	if err := tx.GetContext(ctx, &ok, `SELECT EXISTS (SELECT 1 FROM suppliers WHERE store_id = $1 AND id = $2 AND is_active)`, storeID, id); err != nil {
		return err
	}
	if !ok {
		return ErrSupplierNotFound
	}
	return nil
}

func (s *InventoryService) CreatePurchaseOrder(ctx context.Context, actor *AuthenticatedStaff, in PurchaseOrderInput) (*PurchaseOrder, error) {
	if err := validatePurchaseOrderInput(in, true); err != nil {
		return nil, err
	}
	id := uuid.New()
	var po *PurchaseOrder
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID
		if err := s.checkSupplierTx(ctx, tx, storeID, *in.SupplierID); err != nil {
			return err
		}
		// PO numbers count per store; the owner rarely creates two at once, the unique key settles a race.
		if _, err := tx.ExecContext(ctx, `INSERT INTO purchase_orders (id, store_id, number, supplier_id, expected_date, notes, created_by)
			VALUES ($1, $2, (SELECT COALESCE(MAX(number), 0) + 1 FROM purchase_orders WHERE store_id = $2), $3, NULLIF($4, '')::date, $5, $6)`,
			id, storeID, *in.SupplierID, deref(in.ExpectedDate), strings.TrimSpace(deref(in.Notes)), actor.Staff.UserID); err != nil {
			if _, ok := postgres.UniqueViolation(err); ok {
				return &ConflictError{Field: "number", Message: "nomor PO bentrok, coba lagi"}
			}
			return fmt.Errorf("failed to create purchase order: %w", err)
		}
		if err := s.replacePOLinesTx(ctx, tx, storeID, id, in.Lines); err != nil {
			return err
		}
		var err error
		if po, err = s.loadPurchaseOrderTx(ctx, tx, storeID, id, false); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "PURCHASE_ORDER_CHANGED", fmt.Sprintf("PO #%d dibuat", po.Number), id, map[string]any{"after": po}, s.nowFunc().UTC())
	})
	if err != nil {
		return nil, err
	}
	return po, nil
}

// UpdatePurchaseOrder edits a draft PO; lines, when given, replace all lines.
func (s *InventoryService) UpdatePurchaseOrder(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID, in PurchaseOrderInput) (*PurchaseOrder, error) {
	if err := validatePurchaseOrderInput(in, false); err != nil {
		return nil, err
	}
	var po *PurchaseOrder
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID
		before, err := s.loadPurchaseOrderTx(ctx, tx, storeID, id, true)
		if err != nil {
			return err
		}
		if before.Status != "draft" {
			return ErrPurchaseOrderState
		}
		if in.SupplierID != nil {
			if err := s.checkSupplierTx(ctx, tx, storeID, *in.SupplierID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE purchase_orders SET supplier_id = COALESCE($3, supplier_id),
			expected_date = CASE WHEN $4::text IS NULL THEN expected_date ELSE NULLIF($4, '')::date END,
			notes = COALESCE($5, notes), updated_at = NOW() WHERE store_id = $1 AND id = $2`,
			storeID, id, in.SupplierID, in.ExpectedDate, in.Notes); err != nil {
			return err
		}
		if in.Lines != nil {
			if err := s.replacePOLinesTx(ctx, tx, storeID, id, in.Lines); err != nil {
				return err
			}
		}
		if po, err = s.loadPurchaseOrderTx(ctx, tx, storeID, id, false); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "PURCHASE_ORDER_CHANGED", fmt.Sprintf("PO #%d diubah", po.Number), id, map[string]any{"before": before, "after": po}, s.nowFunc().UTC())
	})
	if err != nil {
		return nil, err
	}
	return po, nil
}

// TransitionPurchaseOrder sends a draft, cancels a draft or a sent PO without receipts, or closes a
// partially received one (stop waiting for the rest).
func (s *InventoryService) TransitionPurchaseOrder(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID, action string) (*PurchaseOrder, error) {
	allowed := map[string]struct {
		from      []string
		to, label string
	}{
		"send":   {[]string{"draft"}, "sent", "dikirim"},
		"cancel": {[]string{"draft", "sent"}, "cancelled", "dibatalkan"},
		"close":  {[]string{"partial"}, "closed", "ditutup"},
	}
	t, ok := allowed[action]
	if !ok {
		return nil, ErrCatalogNotFound
	}
	var po *PurchaseOrder
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID
		cur, err := s.loadPurchaseOrderTx(ctx, tx, storeID, id, true)
		if err != nil {
			return err
		}
		valid := false
		for _, f := range t.from {
			valid = valid || cur.Status == f
		}
		if !valid {
			return ErrPurchaseOrderState
		}
		if _, err := tx.ExecContext(ctx, `UPDATE purchase_orders SET status = $3, updated_at = NOW() WHERE store_id = $1 AND id = $2`, storeID, id, t.to); err != nil {
			return err
		}
		cur.Status, po = t.to, cur
		return s.audit(ctx, tx, actor, "PURCHASE_ORDER_CHANGED", fmt.Sprintf("PO #%d %s", cur.Number, t.label), id, map[string]any{"status": t.to}, s.nowFunc().UTC())
	})
	if err != nil {
		return nil, err
	}
	return po, nil
}

// ReceivePurchaseOrder posts a (possibly partial) delivery of a sent PO at the PO price or a corrected one.
func (s *InventoryService) ReceivePurchaseOrder(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID, lines []ReceiveLineInput, note string) (*GoodsReceipt, error) {
	f := fieldErrors{}
	if len(lines) == 0 || len(lines) > 100 {
		f.add("lines", "isi 1-100 baris")
	}
	for i, l := range lines {
		f.intRange(fmt.Sprintf("lines[%d].quantity", i), l.Quantity, 1, 1_000_000)
		if l.UnitPrice != nil {
			f.intRange(fmt.Sprintf("lines[%d].unit_price", i), *l.UnitPrice, 0, maxItemPrice)
		}
	}
	if err := f.err(); err != nil {
		return nil, err
	}
	now := s.nowFunc().UTC()
	var r *GoodsReceipt
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID
		po, err := s.loadPurchaseOrderTx(ctx, tx, storeID, id, true)
		if err != nil {
			return err
		}
		if po.Status != "sent" && po.Status != "partial" {
			return ErrPurchaseOrderState
		}
		byID := make(map[uuid.UUID]PurchaseOrderLine, len(po.Lines))
		for _, l := range po.Lines {
			byID[l.ID] = l
		}
		r = &GoodsReceipt{ID: uuid.New(), PurchaseOrderID: &po.ID, SupplierID: &po.SupplierID, SupplierName: po.SupplierName,
			Note: truncate(strings.TrimSpace(note), 500), ReceivedAt: now}
		// Validate and price every line first: the receipt is written once, with its total (append-only).
		type posting struct {
			poLine uuid.UUID
			line   StockLineInput
		}
		postings := make([]posting, 0, len(lines))
		for i, in := range lines {
			pl, ok := byID[in.PurchaseOrderLineID]
			if !ok {
				return &ValidationError{Fields: map[string]string{fmt.Sprintf("lines[%d].purchase_order_line_id", i): "bukan baris PO ini"}}
			}
			if in.Quantity > pl.Quantity-pl.ReceivedQuantity {
				return &ValidationError{Fields: map[string]string{fmt.Sprintf("lines[%d].quantity", i): fmt.Sprintf("sisa yang belum diterima %d", pl.Quantity-pl.ReceivedQuantity)}}
			}
			price := pl.UnitPrice
			if in.UnitPrice != nil {
				price = *in.UnitPrice
			}
			pl.ReceivedQuantity += in.Quantity
			byID[pl.ID] = pl
			r.TotalAmount += in.Quantity * price
			postings = append(postings, posting{poLine: pl.ID, line: StockLineInput{IngredientID: pl.IngredientID, ProductID: pl.ProductID,
				PurchaseUnit: pl.PurchaseUnit, Conversion: pl.Conversion, Quantity: in.Quantity, UnitPrice: price}})
		}
		if err := insertReceiptTx(ctx, tx, storeID, r, actor.Staff.UserID); err != nil {
			return err
		}
		if _, err := createPayableTx(ctx, tx, storeID, r); err != nil {
			return err
		}
		for _, p := range postings {
			lineID := p.poLine
			if _, err := postStockLineTx(ctx, tx, storeID, r.ID, &lineID, p.line, actor.Staff.UserID, now); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE purchase_order_lines SET received_quantity = $3 WHERE store_id = $1 AND id = $2`,
				storeID, p.poLine, byID[p.poLine].ReceivedQuantity); err != nil {
				return err
			}
		}
		status := "received"
		for _, l := range byID {
			if l.ReceivedQuantity < l.Quantity {
				status = "partial"
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE purchase_orders SET status = $3, updated_at = $4 WHERE store_id = $1 AND id = $2`, storeID, po.ID, status, now); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "GOODS_RECEIVED", fmt.Sprintf("Penerimaan PO #%d", po.Number), r.ID, map[string]any{"purchase_order_id": po.ID,
			"total": r.TotalAmount, "status": status, "lines": lines}, now)
	})
	if err != nil {
		return nil, err
	}
	return r, nil
}

// ListReceipts lists receipts (PO deliveries and direct purchases), newest first, optionally of one PO.
func (s *InventoryService) ListReceipts(ctx context.Context, actor *AuthenticatedStaff, poID *uuid.UUID) ([]GoodsReceipt, error) {
	out := []GoodsReceipt{}
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `SELECT id, purchase_order_id, supplier_id, supplier_name, total_amount, note, received_at FROM goods_receipts
			WHERE store_id = $1 AND ($2::uuid IS NULL OR purchase_order_id = $2) ORDER BY received_at DESC LIMIT 200`, actor.Staff.Store.ID, poID)
	})
	return out, err
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
