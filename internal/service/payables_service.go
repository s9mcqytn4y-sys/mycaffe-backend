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
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
)

// Supplier payables (PR 11.5, decision X7). Every goods receipt is owed to its supplier from the store-local
// receipt date plus the supplier's payment terms; it is paid at once or in instalments from the outlet cash
// fund (an "ingredients" expense, V13), by transfer or in cash. Payments are append-only.

var ErrPayableNotFound = fmt.Errorf("payable: %w", ErrCatalogNotFound)

const (
	PayableOutletCash = "outlet_cash"
	PayableTransfer   = "transfer"
	PayableCash       = "cash"
)

type Payable struct {
	ID                  uuid.UUID  `json:"id" db:"id"`
	GoodsReceiptID      uuid.UUID  `json:"goods_receipt_id" db:"goods_receipt_id"`
	PurchaseOrderNumber *int       `json:"purchase_order_number,omitempty" db:"purchase_order_number"`
	SupplierID          *uuid.UUID `json:"supplier_id,omitempty" db:"supplier_id"`
	SupplierName        string     `json:"supplier_name" db:"supplier_name"`
	Amount              int64      `json:"amount" db:"amount"`
	PaidAmount          int64      `json:"paid_amount" db:"paid_amount"`
	Remaining           int64      `json:"remaining" db:"remaining"`
	DueDate             string     `json:"due_date" db:"due_date"`
	Overdue             bool       `json:"overdue" db:"overdue"`
	ReceivedAt          time.Time  `json:"received_at" db:"received_at"`
}

// PayPayableInput pays Amount (an instalment or the rest) by Method.
type PayPayableInput struct {
	Amount    int64  `json:"amount"`
	Method    string `json:"method"`
	Reference string `json:"reference"`
	Note      string `json:"note"`
}

const payableSelect = `SELECT p.id, p.goods_receipt_id, po.number AS purchase_order_number, p.supplier_id, p.supplier_name, p.amount,
	p.paid_amount, p.amount - p.paid_amount AS remaining, to_char(p.due_date, 'YYYY-MM-DD') AS due_date,
	p.paid_amount < p.amount AND p.due_date < (NOW() AT TIME ZONE COALESCE(NULLIF(st.timezone, ''), 'Asia/Jakarta'))::date AS overdue,
	r.received_at
	FROM payables p
	JOIN goods_receipts r ON r.store_id = p.store_id AND r.id = p.goods_receipt_id
	JOIN stores st ON st.id = p.store_id
	LEFT JOIN purchase_orders po ON po.store_id = r.store_id AND po.id = r.purchase_order_id`

// createPayableTx records what a receipt owes, due after the supplier's terms (0 days without a supplier).
func createPayableTx(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, r *GoodsReceipt) (uuid.UUID, error) {
	id := uuid.New()
	_, err := tx.ExecContext(ctx, `INSERT INTO payables (id, store_id, goods_receipt_id, supplier_id, supplier_name, amount, due_date, created_at)
		SELECT $1, $2, $3, $4, $5, $6,
			($7::timestamptz AT TIME ZONE COALESCE(NULLIF(st.timezone, ''), 'Asia/Jakarta'))::date + COALESCE(s.payment_days, 0), $7
		FROM stores st LEFT JOIN suppliers s ON s.store_id = st.id AND s.id = $4 WHERE st.id = $2`,
		id, storeID, r.ID, r.SupplierID, r.SupplierName, r.TotalAmount, r.ReceivedAt)
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to record payable: %w", err)
	}
	return id, nil
}

// ListPayables lists payables, open ones by due date first (status open|paid, empty = all).
func (s *InventoryService) ListPayables(ctx context.Context, actor *AuthenticatedStaff, status string) ([]Payable, error) {
	f := fieldErrors{}
	if status != "" {
		f.oneOf("status", status, "open", "paid")
	}
	if err := f.err(); err != nil {
		return nil, err
	}
	out := []Payable{}
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, payableSelect+` WHERE p.store_id = $1
			AND ($2 = '' OR ($2 = 'open' AND p.paid_amount < p.amount) OR ($2 = 'paid' AND p.paid_amount = p.amount))
			ORDER BY p.paid_amount = p.amount, p.due_date, r.received_at LIMIT 200`, actor.Staff.Store.ID, status)
	})
	return out, err
}

func validatePayment(in PayPayableInput) error {
	f := fieldErrors{}
	f.oneOf("method", in.Method, PayableOutletCash, PayableTransfer, PayableCash)
	f.intRange("amount", in.Amount, 1, 10_000_000_000)
	f.text("reference", in.Reference, 0, 100)
	f.text("note", in.Note, 0, 500)
	return f.err()
}

// PayPayable pays an instalment or the rest of a payable.
func (s *InventoryService) PayPayable(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID, in PayPayableInput) (*Payable, error) {
	if err := validatePayment(in); err != nil {
		return nil, err
	}
	var out Payable
	err := s.tx(ctx, actor, func(tx *sqlx.Tx) error {
		if err := s.payPayableTx(ctx, tx, actor, id, in, s.nowFunc().UTC()); err != nil {
			return err
		}
		return tx.GetContext(ctx, &out, payableSelect+` WHERE p.store_id = $1 AND p.id = $2`, actor.Staff.Store.ID, id)
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *InventoryService) payPayableTx(ctx context.Context, tx *sqlx.Tx, actor *AuthenticatedStaff, id uuid.UUID, in PayPayableInput, now time.Time) error {
	storeID := actor.Staff.Store.ID
	var p struct {
		Supplier string `db:"supplier_name"`
		Amount   int64  `db:"amount"`
		Paid     int64  `db:"paid_amount"`
	}
	err := tx.GetContext(ctx, &p, `SELECT supplier_name, amount, paid_amount FROM payables WHERE store_id = $1 AND id = $2 FOR UPDATE`, storeID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPayableNotFound
	}
	if err != nil {
		return err
	}
	if in.Amount > p.Amount-p.Paid {
		return &ValidationError{Fields: map[string]string{"amount": fmt.Sprintf("sisa hutang Rp%d", p.Amount-p.Paid)}}
	}
	note := strings.TrimSpace(in.Note)
	var movementID *uuid.UUID
	if in.Method == PayableOutletCash {
		mid, err := s.outlet.recordTx(ctx, tx, actor, OutletMovementInput{MovementType: OutletExpense, Amount: in.Amount, Category: "ingredients",
			Note: truncate("Bayar hutang "+p.Supplier+" "+note, 500)}, now)
		if err != nil {
			return err
		}
		movementID = &mid
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO payable_payments (store_id, payable_id, amount, method, reference, note, outlet_cash_movement_id, actor_id, paid_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, storeID, id, in.Amount, in.Method, strings.TrimSpace(in.Reference), note, movementID, actor.Staff.UserID, now); err != nil {
		return fmt.Errorf("failed to record payable payment: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE payables SET paid_amount = paid_amount + $3, updated_at = $4 WHERE store_id = $1 AND id = $2`,
		storeID, id, in.Amount, now); err != nil {
		return err
	}
	meta, _ := json.Marshal(map[string]any{"payable_id": id, "amount": in.Amount, "method": in.Method, "reference": in.Reference,
		"remaining": p.Amount - p.Paid - in.Amount, "outlet_cash_movement_id": movementID})
	actorID := actor.Staff.UserID
	return postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{StoreID: storeID, ActorID: &actorID, ActorName: actor.Staff.FullName,
		Action: "PAYABLE_PAID", Reason: truncate(fmt.Sprintf("Bayar hutang %s Rp%d (%s)", p.Supplier, in.Amount, in.Method), 500),
		ReferenceID: id.String(), Metadata: string(meta), CreatedAt: now})
}
