package service

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
)

// Receipts (B1, decision R1). Every amount comes from the order, item, option and payment snapshots;
// nothing is recalculated from the catalog or the current store rates.

const receiptLinkTTL = 90 * 24 * time.Hour

const (
	ReceiptKindSale   = "sale"
	ReceiptKindRefund = "refund"
	ReceiptKindVoid   = "void"
)

var (
	ErrReceiptNotFound     = errors.New("receipt not found")
	ErrReceiptNotAvailable = errors.New("receipt of this kind is not available for the order")
	ErrInvalidReceiptLink  = errors.New("receipt link is invalid or expired")
)

type Receipt struct {
	Kind        string           `json:"kind"`
	Store       ReceiptStore     `json:"store"`
	OrderID     uuid.UUID        `json:"order_id"`
	OrderNumber string           `json:"order_number"`
	QueueNumber string           `json:"queue_number"`
	TableNumber string           `json:"table_number,omitempty"`
	OrderType   string           `json:"order_type"`
	Status      string           `json:"status"`
	PaymentStat string           `json:"payment_status"`
	IssuedAt    time.Time        `json:"issued_at"`
	LocalTime   string           `json:"local_time"`
	StaffName   string           `json:"staff_name,omitempty"`
	Lines       []ReceiptLine    `json:"lines"`
	Subtotal    int64            `json:"subtotal_amount"`
	Discount    int64            `json:"discount_amount"`
	Tax         int64            `json:"tax_amount"`
	Service     int64            `json:"service_charge_amount"`
	Total       int64            `json:"total_amount"`
	Paid        int64            `json:"paid_amount"`
	Refunded    int64            `json:"refunded_amount"`
	Payments    []ReceiptPayment `json:"payments"`
	Refund      *ReceiptRefund   `json:"refund,omitempty"`
	VoidReason  string           `json:"void_reason,omitempty"`
}

type ReceiptStore struct {
	Name    string `json:"name"`
	Address string `json:"address,omitempty"`
	Phone   string `json:"phone,omitempty"`
}

type ReceiptLine struct {
	itemID    uuid.UUID
	Name      string   `json:"name"`
	Variant   string   `json:"variant,omitempty"`
	Options   []string `json:"options,omitempty"`
	Notes     string   `json:"notes,omitempty"`
	Quantity  int      `json:"quantity"`
	UnitPrice int64    `json:"unit_price"`
	Amount    int64    `json:"amount"`
}

type ReceiptPayment struct {
	SplitLabel   string `json:"split_label,omitempty" db:"split_label"`
	Method       string `json:"method" db:"method"`
	Amount       int64  `json:"amount" db:"amount"`
	CashTendered int64  `json:"cash_tendered_amount,omitempty" db:"cash_tendered_amount"`
	Change       int64  `json:"change_amount,omitempty" db:"change_amount"`
	Rounding     int64  `json:"rounding_amount,omitempty" db:"rounding_amount"`
	Status       string `json:"status" db:"status"`
}

type ReceiptRefund struct {
	PaymentID     uuid.UUID `json:"payment_id"`
	Method        string    `json:"method"`
	Amount        int64     `json:"amount"`
	Reason        string    `json:"reason"`
	StockReturned bool      `json:"stock_returned"`
}

// ReceiptLink is a digital receipt token; Token is shown once and stored only as its SHA-256.
type ReceiptLink struct {
	Token     string    `json:"token"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

type ReceiptService struct {
	db        *sqlx.DB
	publicURL string
	nowFunc   func() time.Time
}

// NewReceiptService builds digital receipt URLs as <publicURL>/receipts/<token>.
func NewReceiptService(db *sqlx.DB, publicURL string) *ReceiptService {
	return &ReceiptService{db: db, publicURL: strings.TrimRight(publicURL, "/"), nowFunc: time.Now}
}

// Build returns a receipt of one kind: sale (any order), refund (paymentID of a refunded payment) or
// void (a cancelled order with a VOID_ORDER audit).
func (s *ReceiptService) Build(ctx context.Context, storeID, orderID uuid.UUID, kind string, paymentID *uuid.UUID) (*Receipt, error) {
	var r *Receipt
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		var err error
		r, err = s.buildTx(ctx, tx, storeID, orderID, kind, paymentID)
		return err
	})
	return r, err
}

// IssueLink creates a 90-day digital receipt token for an order.
func (s *ReceiptService) IssueLink(ctx context.Context, actor *AuthenticatedStaff, orderID uuid.UUID) (*ReceiptLink, error) {
	secret, hash, err := randomToken()
	if err != nil {
		return nil, err
	}
	storeID, id, now := actor.Staff.Store.ID, uuid.New(), s.nowFunc().UTC()
	expires := now.Add(receiptLinkTTL)
	err = database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO receipt_tokens (id, store_id, order_id, token_hash, created_by, created_at, expires_at)
			SELECT $1, $2, o.id, $4, $5, $6, $7 FROM orders o WHERE o.store_id = $2 AND o.id = $3`,
			id, storeID, orderID, hash, actor.Staff.UserID, now, expires)
		if err != nil {
			return fmt.Errorf("failed to issue receipt link: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrReceiptNotFound
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	token := storeID.String() + "." + id.String() + "." + secret
	return &ReceiptLink{Token: token, URL: s.LinkURL(token), ExpiresAt: expires}, nil
}

func (s *ReceiptService) LinkURL(token string) string { return s.publicURL + "/receipts/" + token }

// Public resolves a digital receipt token (store.id.secret; the store only routes, the secret
// authorizes) to the sale receipt, plus the refund state the order has today.
func (s *ReceiptService) Public(ctx context.Context, token string) (*Receipt, error) {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) != 3 {
		return nil, ErrInvalidReceiptLink
	}
	storeID, err1 := uuid.Parse(parts[0])
	tokenID, err2 := uuid.Parse(parts[1])
	if err1 != nil || err2 != nil || storeID == uuid.Nil {
		return nil, ErrInvalidReceiptLink
	}
	sum := sha256.Sum256([]byte(parts[2]))
	var r *Receipt
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		var row struct {
			OrderID   uuid.UUID `db:"order_id"`
			TokenHash []byte    `db:"token_hash"`
			ExpiresAt time.Time `db:"expires_at"`
		}
		err := tx.GetContext(ctx, &row, `SELECT order_id, token_hash, expires_at FROM receipt_tokens WHERE store_id = $1 AND id = $2`, storeID, tokenID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvalidReceiptLink
		}
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare(row.TokenHash, sum[:]) != 1 || !s.nowFunc().Before(row.ExpiresAt) {
			return ErrInvalidReceiptLink
		}
		r, err = s.buildTx(ctx, tx, storeID, row.OrderID, ReceiptKindSale, nil)
		return err
	})
	if err != nil {
		return nil, err
	}
	r.StaffName = "" // staff names stay off the public page
	return r, nil
}

func (s *ReceiptService) buildTx(ctx context.Context, tx *sqlx.Tx, storeID, orderID uuid.UUID, kind string, paymentID *uuid.UUID) (*Receipt, error) {
	var head struct {
		StoreName   string         `db:"store_name"`
		Address     sql.NullString `db:"address"`
		Phone       sql.NullString `db:"phone"`
		Timezone    string         `db:"timezone"`
		OrderNumber string         `db:"order_number"`
		QueueNumber string         `db:"queue_number"`
		TableNumber sql.NullString `db:"table_number"`
		OrderType   string         `db:"order_type"`
		Status      string         `db:"status"`
		PaymentStat string         `db:"payment_status"`
		Subtotal    int64          `db:"subtotal_amount"`
		Discount    int64          `db:"discount_amount"`
		Tax         int64          `db:"tax_amount"`
		Service     int64          `db:"service_charge_amount"`
		Total       int64          `db:"total_amount"`
		Paid        int64          `db:"paid_amount"`
		Refunded    int64          `db:"refunded_amount"`
		CreatedAt   time.Time      `db:"created_at"`
		StaffName   sql.NullString `db:"staff_name"`
	}
	err := tx.GetContext(ctx, &head, `
		SELECT s.name AS store_name, s.address, s.phone, s.timezone,
			o.order_number, o.queue_number, o.table_number, o.order_type, o.status, o.payment_status,
			o.subtotal_amount, o.discount_amount, o.tax_amount, o.service_charge_amount, o.total_amount,
			o.paid_amount, o.refunded_amount, o.created_at, u.full_name AS staff_name
		FROM orders o
		JOIN stores s ON s.id = o.store_id
		LEFT JOIN users u ON u.store_id = o.store_id AND u.id = o.created_by_user_id
		WHERE o.store_id = $1 AND o.id = $2`, storeID, orderID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrReceiptNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load receipt order: %w", err)
	}
	r := &Receipt{
		Kind: kind, OrderID: orderID, Store: ReceiptStore{Name: head.StoreName, Address: head.Address.String, Phone: head.Phone.String},
		OrderNumber: head.OrderNumber, QueueNumber: head.QueueNumber, TableNumber: head.TableNumber.String,
		OrderType: head.OrderType, Status: head.Status, PaymentStat: head.PaymentStat, IssuedAt: head.CreatedAt,
		StaffName: head.StaffName.String, Subtotal: head.Subtotal, Discount: head.Discount, Tax: head.Tax,
		Service: head.Service, Total: head.Total, Paid: head.Paid, Refunded: head.Refunded,
	}
	if r.Lines, err = receiptLinesTx(ctx, tx, storeID, orderID); err != nil {
		return nil, err
	}
	if r.Payments, err = receiptPaymentsTx(ctx, tx, storeID, orderID); err != nil {
		return nil, err
	}

	switch kind {
	case ReceiptKindSale:
	case ReceiptKindRefund:
		if paymentID == nil {
			return nil, ErrReceiptNotAvailable
		}
		if err := s.attachRefundTx(ctx, tx, storeID, orderID, *paymentID, r); err != nil {
			return nil, err
		}
	case ReceiptKindVoid:
		var audit struct {
			Reason    string    `db:"reason"`
			ActorName string    `db:"actor_name"`
			CreatedAt time.Time `db:"created_at"`
		}
		// VOID_ORDER audits reference the order number, not the id.
		err := tx.GetContext(ctx, &audit, `SELECT reason, COALESCE(actor_name, '') AS actor_name, created_at FROM audit_logs
			WHERE store_id = $1 AND action = 'VOID_ORDER' AND reference_id = $2 ORDER BY created_at DESC LIMIT 1`, storeID, head.OrderNumber)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrReceiptNotAvailable
		}
		if err != nil {
			return nil, fmt.Errorf("failed to load void audit: %w", err)
		}
		r.VoidReason, r.StaffName, r.IssuedAt = audit.Reason, audit.ActorName, audit.CreatedAt
	default:
		return nil, ErrReceiptNotAvailable
	}
	r.LocalTime = formatStoreTime(r.IssuedAt, head.Timezone)
	return r, nil
}

func (s *ReceiptService) attachRefundTx(ctx context.Context, tx *sqlx.Tx, storeID, orderID, paymentID uuid.UUID, r *Receipt) error {
	var p struct {
		Method   string        `db:"method"`
		Refunded int64         `db:"refunded_amount"`
		Status   string        `db:"status"`
		SplitID  uuid.NullUUID `db:"split_id"`
	}
	err := tx.GetContext(ctx, &p, `SELECT method, refunded_amount, status, split_id FROM payments WHERE store_id = $1 AND order_id = $2 AND id = $3`,
		storeID, orderID, paymentID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrReceiptNotFound
	}
	if err != nil {
		return fmt.Errorf("failed to load refunded payment: %w", err)
	}
	var audit struct {
		Reason    string    `db:"reason"`
		ActorName string    `db:"actor_name"`
		Metadata  []byte    `db:"metadata"`
		CreatedAt time.Time `db:"created_at"`
	}
	err = tx.GetContext(ctx, &audit, `SELECT reason, COALESCE(actor_name, '') AS actor_name, metadata, created_at FROM audit_logs
		WHERE store_id = $1 AND action = 'REFUND' AND reference_id = $2 ORDER BY created_at DESC LIMIT 1`, storeID, paymentID.String())
	if errors.Is(err, sql.ErrNoRows) || p.Refunded == 0 {
		return ErrReceiptNotAvailable
	}
	if err != nil {
		return fmt.Errorf("failed to load refund audit: %w", err)
	}
	var meta struct {
		RestoreStock bool `json:"restore_stock"`
	}
	_ = json.Unmarshal(audit.Metadata, &meta)
	r.Refund = &ReceiptRefund{PaymentID: paymentID, Method: p.Method, Amount: p.Refunded, Reason: audit.Reason, StockReturned: meta.RestoreStock}
	r.StaffName, r.IssuedAt = audit.ActorName, audit.CreatedAt
	// A by-item split refund lists only that split's lines; other refunds keep every line.
	if p.SplitID.Valid {
		var rows []struct {
			OrderItemID uuid.UUID `db:"order_item_id"`
			Quantity    int       `db:"quantity"`
		}
		if err := tx.SelectContext(ctx, &rows, `SELECT order_item_id, quantity FROM payment_split_items WHERE store_id = $1 AND split_id = $2`,
			storeID, p.SplitID.UUID); err != nil {
			return fmt.Errorf("failed to load split items: %w", err)
		}
		if len(rows) > 0 {
			qty := make(map[uuid.UUID]int, len(rows))
			for _, row := range rows {
				qty[row.OrderItemID] += row.Quantity
			}
			r.Lines = splitLines(r.Lines, qty)
		}
	}
	return nil
}

// splitLines keeps the lines covered by a by-item split, with the split's quantity at the snapshot unit price.
func splitLines(lines []ReceiptLine, qty map[uuid.UUID]int) []ReceiptLine {
	out := make([]ReceiptLine, 0, len(qty))
	for _, l := range lines {
		if q := qty[l.itemID]; q > 0 {
			l.Quantity, l.Amount = q, l.UnitPrice*int64(q)
			out = append(out, l)
		}
	}
	return out
}

func receiptLinesTx(ctx context.Context, tx *sqlx.Tx, storeID, orderID uuid.UUID) ([]ReceiptLine, error) {
	var items []struct {
		ID        uuid.UUID `db:"id"`
		Name      string    `db:"snapshot_name"`
		Variant   string    `db:"snapshot_variant_name"`
		Notes     string    `db:"notes"`
		Quantity  int       `db:"quantity"`
		UnitPrice int64     `db:"snapshot_price"`
		Amount    int64     `db:"subtotal_amount"`
	}
	if err := tx.SelectContext(ctx, &items, `
		SELECT id, snapshot_name, COALESCE(snapshot_variant_name, '') AS snapshot_variant_name, COALESCE(notes, '') AS notes,
			quantity, snapshot_price, subtotal_amount
		FROM order_items WHERE store_id = $1 AND order_id = $2 ORDER BY created_at, id`, storeID, orderID); err != nil {
		return nil, fmt.Errorf("failed to load receipt lines: %w", err)
	}
	var opts []struct {
		ItemID   uuid.UUID `db:"order_item_id"`
		Child    bool      `db:"child"`
		Name     string    `db:"snapshot_option_name"`
		Quantity int       `db:"quantity"`
	}
	if err := tx.SelectContext(ctx, &opts, `
		SELECT oio.order_item_id, oio.parent_order_item_option_id IS NOT NULL AS child, oio.snapshot_option_name, oio.quantity
		FROM order_item_options oio JOIN order_items oi ON oi.store_id = oio.store_id AND oi.id = oio.order_item_id
		WHERE oio.store_id = $1 AND oi.order_id = $2
		ORDER BY oio.parent_order_item_option_id NULLS FIRST, oio.created_at, oio.id`, storeID, orderID); err != nil {
		return nil, fmt.Errorf("failed to load receipt options: %w", err)
	}
	byItem := map[uuid.UUID][]string{}
	for _, o := range opts {
		label := o.Name
		if o.Quantity > 1 {
			label = fmt.Sprintf("%dx %s", o.Quantity, o.Name)
		}
		if o.Child {
			label = "- " + label
		}
		byItem[o.ItemID] = append(byItem[o.ItemID], label)
	}
	lines := make([]ReceiptLine, len(items))
	for i, it := range items {
		lines[i] = ReceiptLine{itemID: it.ID, Name: it.Name, Variant: it.Variant, Options: byItem[it.ID], Notes: it.Notes,
			Quantity: it.Quantity, UnitPrice: it.UnitPrice, Amount: it.Amount}
	}
	return lines, nil
}

// receiptPaymentsTx lists the payments that took money (paid or later refunded), split by split.
func receiptPaymentsTx(ctx context.Context, tx *sqlx.Tx, storeID, orderID uuid.UUID) ([]ReceiptPayment, error) {
	payments := []ReceiptPayment{}
	err := tx.SelectContext(ctx, &payments, `
		SELECT COALESCE(sp.label, '') AS split_label, p.method, p.amount, p.cash_tendered_amount,
			p.change_given_amount AS change_amount, p.rounding_amount, p.status
		FROM payments p LEFT JOIN payment_splits sp ON sp.store_id = p.store_id AND sp.id = p.split_id
		WHERE p.store_id = $1 AND p.order_id = $2 AND p.status IN ('paid', 'partially_refunded', 'refunded')
		ORDER BY sp.split_number NULLS FIRST, p.paid_at, p.created_at`, storeID, orderID)
	if err != nil {
		return nil, fmt.Errorf("failed to load receipt payments: %w", err)
	}
	return payments, nil
}

// formatStoreTime renders a timestamp in the store's time zone (WIB when the zone is unknown).
func formatStoreTime(t time.Time, tz string) string {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.FixedZone("WIB", 7*3600)
	}
	return t.In(loc).Format("02/01/2006 15:04")
}
