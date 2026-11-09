package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/sms"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
)

// B2B invoices (v0.11.1 PR 11.9, X18). The cashier charges an order to a business customer; the owner bills
// charged orders on an invoice and records its payments. No e-Faktur.

var (
	ErrBusinessCustomerNotFound = fmt.Errorf("business customer: %w", ErrCatalogNotFound)
	ErrInvoiceNotFound          = fmt.Errorf("invoice: %w", ErrCatalogNotFound)
	// ErrInvoicedPaymentNotRefundable: a charge to an account took no money, so there is nothing to hand back.
	ErrInvoicedPaymentNotRefundable = errors.New("an order charged to an account cannot be refunded")
)

type BusinessCustomer struct {
	ID               uuid.UUID `json:"id" db:"id"`
	Name             string    `json:"name" db:"name"`
	TaxID            string    `json:"tax_id,omitempty" db:"tax_id"`
	Email            string    `json:"email,omitempty" db:"email"`
	Phone            string    `json:"phone,omitempty" db:"phone"`
	PaymentTermsDays int64     `json:"payment_terms_days" db:"payment_terms_days"`
	IsActive         bool      `json:"is_active" db:"is_active"`
}

type BusinessCustomerInput struct {
	Name             *string `json:"name"`
	TaxID            *string `json:"tax_id"`
	Email            *string `json:"email"`
	Phone            *string `json:"phone"`
	PaymentTermsDays *int64  `json:"payment_terms_days"`
	IsActive         *bool   `json:"is_active"`
}

type InvoiceOrder struct {
	ID          uuid.UUID `json:"id" db:"id"`
	OrderNumber string    `json:"order_number" db:"order_number"`
	TotalAmount int64     `json:"total_amount" db:"total_amount"`
	CreatedAt   time.Time `json:"created_at" db:"created_at"`
}

type InvoicePayment struct {
	ID        uuid.UUID `json:"id" db:"id"`
	Amount    int64     `json:"amount" db:"amount"`
	Method    string    `json:"method" db:"method"`
	Reference string    `json:"reference" db:"reference"`
	Note      string    `json:"note" db:"note"`
	PaidAt    time.Time `json:"paid_at" db:"paid_at"`
}

type Invoice struct {
	ID                 uuid.UUID        `json:"id" db:"id"`
	Number             int              `json:"number" db:"number"`
	BusinessCustomerID uuid.UUID        `json:"business_customer_id" db:"business_customer_id"`
	CustomerName       string           `json:"customer_name" db:"customer_name"`
	TaxID              string           `json:"tax_id" db:"tax_id"`
	TotalAmount        int64            `json:"total_amount" db:"total_amount"`
	PaidAmount         int64            `json:"paid_amount" db:"paid_amount"`
	Remaining          int64            `json:"remaining" db:"remaining"`
	DueDate            string           `json:"due_date" db:"due_date"`
	Overdue            bool             `json:"overdue" db:"overdue"`
	Note               string           `json:"note" db:"note"`
	IssuedAt           time.Time        `json:"issued_at" db:"issued_at"`
	Orders             []InvoiceOrder   `json:"orders,omitempty" db:"-"`
	Payments           []InvoicePayment `json:"payments,omitempty" db:"-"`
}

type CreateInvoiceInput struct {
	BusinessCustomerID uuid.UUID   `json:"business_customer_id"`
	OrderIDs           []uuid.UUID `json:"order_ids"`
	Note               string      `json:"note"`
}

type InvoicePaymentInput struct {
	Amount    int64  `json:"amount"`
	Method    string `json:"method"`
	Reference string `json:"reference"`
	Note      string `json:"note"`
}

const businessCustomerColumns = `id, name, tax_id, email, phone, payment_terms_days, is_active`

const invoiceSelect = `SELECT i.id, i.number, i.business_customer_id, i.customer_name, i.tax_id, i.total_amount, i.paid_amount,
	i.total_amount - i.paid_amount AS remaining, to_char(i.due_date, 'YYYY-MM-DD') AS due_date,
	i.paid_amount < i.total_amount AND i.due_date < (NOW() AT TIME ZONE COALESCE(NULLIF(st.timezone, ''), 'Asia/Jakarta'))::date AS overdue,
	i.note, i.issued_at
	FROM invoices i JOIN stores st ON st.id = i.store_id`

type InvoiceService struct {
	db      *sqlx.DB
	nowFunc func() time.Time
}

func NewInvoiceService(db *sqlx.DB) *InvoiceService {
	return &InvoiceService{db: db, nowFunc: time.Now}
}

func (s *InvoiceService) tx(ctx context.Context, actor *AuthenticatedStaff, perm rbac.Permission, fn func(tx *sqlx.Tx) error) error {
	if actor == nil {
		return ErrInvalidSession
	}
	if !actor.Can(perm) {
		return ErrForbidden
	}
	return database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, fn)
}

func (s *InvoiceService) audit(ctx context.Context, tx *sqlx.Tx, actor *AuthenticatedStaff, action, reason string, ref uuid.UUID, meta map[string]any, now time.Time) error {
	payload, _ := json.Marshal(meta)
	actorID := actor.Staff.UserID
	return postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{StoreID: actor.Staff.Store.ID, ActorID: &actorID, ActorName: actor.Staff.FullName,
		Action: action, Reason: truncate(strings.TrimSpace(reason), 500), ReferenceID: ref.String(), Metadata: string(payload), CreatedAt: now})
}

func applyBusinessCustomer(c *BusinessCustomer, in BusinessCustomerInput) error {
	f := fieldErrors{}
	if in.Name != nil {
		c.Name = f.text("name", *in.Name, 1, 100)
	}
	if in.TaxID != nil {
		c.TaxID = f.text("tax_id", *in.TaxID, 0, 30)
	}
	if in.Email != nil {
		c.Email = f.text("email", *in.Email, 0, 200)
		if _, err := mail.ParseAddress(c.Email); c.Email != "" && err != nil {
			f.add("email", "format email tidak valid")
		}
	}
	if in.Phone != nil {
		c.Phone = strings.TrimSpace(*in.Phone)
		if c.Phone != "" {
			phone, err := sms.NormalizePhone(c.Phone)
			if err != nil {
				f.add("phone", "nomor HP tidak valid (contoh 081234567890)")
			}
			c.Phone = phone
		}
	}
	if in.PaymentTermsDays != nil {
		f.intRange("payment_terms_days", *in.PaymentTermsDays, 0, 120)
		c.PaymentTermsDays = *in.PaymentTermsDays
	}
	if in.IsActive != nil {
		c.IsActive = *in.IsActive
	}
	if c.Name == "" {
		f.add("name", "wajib diisi")
	}
	return f.err()
}

func businessCustomerConflict(err error) error {
	if _, ok := postgres.UniqueViolation(err); ok {
		return &ConflictError{Field: "name", Message: "nama pelanggan bisnis sudah dipakai"}
	}
	return err
}

// ListBusinessCustomers returns every customer with contacts for the owner.
func (s *InvoiceService) ListBusinessCustomers(ctx context.Context, actor *AuthenticatedStaff) ([]BusinessCustomer, error) {
	out := []BusinessCustomer{}
	err := s.tx(ctx, actor, rbac.InvoiceManage, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `SELECT `+businessCustomerColumns+` FROM business_customers WHERE store_id = $1 ORDER BY is_active DESC, name`, actor.Staff.Store.ID)
	})
	return out, err
}

// ListChargeableCustomers is the cashier's picker: names and terms only, no contact details.
func (s *InvoiceService) ListChargeableCustomers(ctx context.Context, actor *AuthenticatedStaff) ([]BusinessCustomer, error) {
	out := []BusinessCustomer{}
	err := s.tx(ctx, actor, rbac.PaymentTenderDirect, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `SELECT id, name, payment_terms_days, is_active FROM business_customers
			WHERE store_id = $1 AND is_active ORDER BY name`, actor.Staff.Store.ID)
	})
	return out, err
}

func (s *InvoiceService) CreateBusinessCustomer(ctx context.Context, actor *AuthenticatedStaff, in BusinessCustomerInput) (*BusinessCustomer, error) {
	c := BusinessCustomer{ID: uuid.New(), PaymentTermsDays: 14, IsActive: true}
	if err := applyBusinessCustomer(&c, in); err != nil {
		return nil, err
	}
	err := s.tx(ctx, actor, rbac.InvoiceManage, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO business_customers (id, store_id, name, tax_id, email, phone, payment_terms_days, is_active)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, c.ID, actor.Staff.Store.ID, c.Name, c.TaxID, c.Email, c.Phone, c.PaymentTermsDays, c.IsActive); err != nil {
			return businessCustomerConflict(err)
		}
		return s.audit(ctx, tx, actor, "BUSINESS_CUSTOMER_CHANGED", "Pelanggan bisnis dibuat: "+c.Name, c.ID, map[string]any{"after": c}, s.nowFunc().UTC())
	})
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *InvoiceService) UpdateBusinessCustomer(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID, in BusinessCustomerInput) (*BusinessCustomer, error) {
	var c BusinessCustomer
	err := s.tx(ctx, actor, rbac.InvoiceManage, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID
		if err := tx.GetContext(ctx, &c, `SELECT `+businessCustomerColumns+` FROM business_customers WHERE store_id = $1 AND id = $2 FOR UPDATE`, storeID, id); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrBusinessCustomerNotFound
			}
			return err
		}
		before := c
		if err := applyBusinessCustomer(&c, in); err != nil {
			return err
		}
		now := s.nowFunc().UTC()
		if _, err := tx.ExecContext(ctx, `UPDATE business_customers SET name = $3, tax_id = $4, email = $5, phone = $6, payment_terms_days = $7,
			is_active = $8, updated_at = $9 WHERE store_id = $1 AND id = $2`, storeID, id, c.Name, c.TaxID, c.Email, c.Phone, c.PaymentTermsDays, c.IsActive, now); err != nil {
			return businessCustomerConflict(err)
		}
		return s.audit(ctx, tx, actor, "BUSINESS_CUSTOMER_CHANGED", "Pelanggan bisnis diubah: "+c.Name, id, map[string]any{"before": before, "after": c}, now)
	})
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// chargeToAccountTx records which business customer owes a pay-first order paid with the invoice method.
func chargeToAccountTx(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID, order *domain.Order, customerID *uuid.UUID, partial bool) error {
	if customerID == nil || partial || order.BillingMode != domain.BillingModePayFirst {
		return fmt.Errorf("%w: invoice charges a whole pay-first order to a business_customer_id", ErrUnsupportedPaymentMethod)
	}
	res, err := tx.ExecContext(ctx, `UPDATE orders o SET business_customer_id = $3 FROM business_customers c
		WHERE o.store_id = $1 AND o.id = $2 AND c.store_id = o.store_id AND c.id = $3 AND c.is_active`, storeID, order.ID, *customerID)
	if err != nil {
		return fmt.Errorf("failed to charge order to account: %w", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return fmt.Errorf("%w: business customer is not active in this store", ErrUnsupportedPaymentMethod)
	}
	return nil
}

// ListUninvoicedOrders returns orders charged to accounts and not billed yet, oldest first.
func (s *InvoiceService) ListUninvoicedOrders(ctx context.Context, actor *AuthenticatedStaff, customerID *uuid.UUID) ([]InvoiceOrder, error) {
	out := []InvoiceOrder{}
	err := s.tx(ctx, actor, rbac.InvoiceManage, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, `SELECT id, order_number, total_amount, created_at FROM orders
			WHERE store_id = $1 AND business_customer_id IS NOT NULL AND invoice_id IS NULL AND payment_status = 'paid'
			AND ($2::uuid IS NULL OR business_customer_id = $2) ORDER BY created_at LIMIT 500`, actor.Staff.Store.ID, customerID)
	})
	return out, err
}

// CreateInvoice bills charged orders of one customer; the due date is today in the store time zone plus the terms.
func (s *InvoiceService) CreateInvoice(ctx context.Context, actor *AuthenticatedStaff, in CreateInvoiceInput) (*Invoice, error) {
	f := fieldErrors{}
	in.Note = f.text("note", in.Note, 0, 500)
	unique := uniqueUUIDs(in.OrderIDs)
	if len(in.OrderIDs) == 0 || len(in.OrderIDs) > 200 || len(unique) != len(in.OrderIDs) {
		f.add("order_ids", "isi 1-200 pesanan tanpa duplikat")
	}
	if err := f.err(); err != nil {
		return nil, err
	}
	var out *Invoice
	err := s.tx(ctx, actor, rbac.InvoiceManage, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID
		now := s.nowFunc().UTC()
		ids := make([]string, 0, len(unique))
		for id := range unique {
			ids = append(ids, id.String())
		}
		var totals []int64
		if err := tx.SelectContext(ctx, &totals, `SELECT total_amount FROM orders WHERE store_id = $1 AND id = ANY($2::uuid[]) AND business_customer_id = $3
			AND invoice_id IS NULL AND payment_status = 'paid' FOR UPDATE`, storeID, ids, in.BusinessCustomerID); err != nil {
			return err
		}
		if len(totals) != len(ids) {
			return &ValidationError{Fields: map[string]string{"order_ids": "ada pesanan yang bukan tagihan pelanggan ini atau sudah masuk invoice"}}
		}
		var total int64
		for _, t := range totals {
			total += t
		}
		id := uuid.New()
		// Invoice numbers count per store; the unique key settles a race, as for purchase orders.
		if _, err := tx.ExecContext(ctx, `INSERT INTO invoices (id, store_id, number, business_customer_id, customer_name, tax_id, total_amount, due_date, note, created_by, issued_at)
			SELECT $1, $2, (SELECT COALESCE(MAX(number), 0) + 1 FROM invoices WHERE store_id = $2), c.id, c.name, c.tax_id, $4,
				($5::timestamptz AT TIME ZONE COALESCE(NULLIF(st.timezone, ''), 'Asia/Jakarta'))::date + c.payment_terms_days, $6, $7, $5
			FROM business_customers c JOIN stores st ON st.id = c.store_id WHERE c.store_id = $2 AND c.id = $3`,
			id, storeID, in.BusinessCustomerID, total, now, in.Note, actor.Staff.UserID); err != nil {
			if _, ok := postgres.UniqueViolation(err); ok {
				return &ConflictError{Field: "number", Message: "nomor invoice bentrok, coba lagi"}
			}
			return fmt.Errorf("failed to create invoice: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE orders SET invoice_id = $3 WHERE store_id = $1 AND id = ANY($2::uuid[])`, storeID, ids, id); err != nil {
			return fmt.Errorf("failed to bill orders: %w", err)
		}
		var err error
		if out, err = loadInvoiceTx(ctx, tx, storeID, id, ""); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "INVOICE_ISSUED", "Invoice "+invoiceDocumentNumber(out.Number)+" diterbitkan", id,
			map[string]any{"total_amount": total, "orders": len(ids), "due_date": out.DueDate}, now)
	})
	return out, err
}

func loadInvoiceTx(ctx context.Context, tx *sqlx.Tx, storeID, id uuid.UUID, lock string) (*Invoice, error) {
	var inv Invoice
	err := tx.GetContext(ctx, &inv, invoiceSelect+` WHERE i.store_id = $1 AND i.id = $2 `+lock, storeID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvoiceNotFound
	}
	if err != nil {
		return nil, err
	}
	inv.Orders, inv.Payments = []InvoiceOrder{}, []InvoicePayment{}
	if err := tx.SelectContext(ctx, &inv.Orders, `SELECT id, order_number, total_amount, created_at FROM orders
		WHERE store_id = $1 AND invoice_id = $2 ORDER BY created_at`, storeID, id); err != nil {
		return nil, err
	}
	err = tx.SelectContext(ctx, &inv.Payments, `SELECT id, amount, method, reference, note, paid_at FROM invoice_payments
		WHERE store_id = $1 AND invoice_id = $2 ORDER BY paid_at`, storeID, id)
	return &inv, err
}

// ListInvoices lists invoices, open ones by due date first; status is open, paid or empty for all.
func (s *InvoiceService) ListInvoices(ctx context.Context, actor *AuthenticatedStaff, status string) ([]Invoice, error) {
	if status != "" {
		f := fieldErrors{}
		f.oneOf("status", status, "open", "paid")
		if err := f.err(); err != nil {
			return nil, err
		}
	}
	out := []Invoice{}
	err := s.tx(ctx, actor, rbac.InvoiceManage, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, invoiceSelect+` WHERE i.store_id = $1 AND ($2::text = '' OR ($2::text = 'open') = (i.paid_amount < i.total_amount))
			ORDER BY i.paid_amount = i.total_amount, i.due_date, i.number LIMIT 200`, actor.Staff.Store.ID, status)
	})
	return out, err
}

func (s *InvoiceService) GetInvoice(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID) (*Invoice, error) {
	var out *Invoice
	err := s.tx(ctx, actor, rbac.InvoiceManage, func(tx *sqlx.Tx) error {
		var err error
		out, err = loadInvoiceTx(ctx, tx, actor.Staff.Store.ID, id, "")
		return err
	})
	return out, err
}

// InvoicePDF renders the invoice and returns the PDF with its file name.
func (s *InvoiceService) InvoicePDF(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID) ([]byte, string, error) {
	inv, err := s.GetInvoice(ctx, actor, id)
	if err != nil {
		return nil, "", err
	}
	return invoicePDF(actor.Staff.Store.Name, inv), invoiceDocumentNumber(inv.Number) + ".pdf", nil
}

// PayInvoice records an instalment or the rest of an invoice; it never exceeds what is still owed.
func (s *InvoiceService) PayInvoice(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID, in InvoicePaymentInput) (*Invoice, error) {
	f := fieldErrors{}
	f.intRange("amount", in.Amount, 1, 1<<53)
	f.oneOf("method", in.Method, "transfer", "cash", "qris")
	in.Reference = f.text("reference", in.Reference, 0, 100)
	in.Note = f.text("note", in.Note, 0, 500)
	if err := f.err(); err != nil {
		return nil, err
	}
	var out *Invoice
	err := s.tx(ctx, actor, rbac.InvoiceManage, func(tx *sqlx.Tx) error {
		storeID := actor.Staff.Store.ID
		now := s.nowFunc().UTC()
		inv, err := loadInvoiceTx(ctx, tx, storeID, id, "FOR UPDATE OF i")
		if err != nil {
			return err
		}
		if in.Amount > inv.Remaining {
			return &ValidationError{Fields: map[string]string{"amount": fmt.Sprintf("melebihi sisa tagihan %s", rupiah(inv.Remaining))}}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO invoice_payments (store_id, invoice_id, amount, method, reference, note, recorded_by, paid_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, storeID, id, in.Amount, in.Method, in.Reference, in.Note, actor.Staff.UserID, now); err != nil {
			return fmt.Errorf("failed to record invoice payment: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE invoices SET paid_amount = paid_amount + $3 WHERE store_id = $1 AND id = $2`, storeID, id, in.Amount); err != nil {
			return err
		}
		if out, err = loadInvoiceTx(ctx, tx, storeID, id, ""); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor, "INVOICE_PAYMENT_RECORDED", "Pembayaran invoice "+invoiceDocumentNumber(out.Number), id,
			map[string]any{"amount": in.Amount, "method": in.Method, "reference": in.Reference, "remaining": out.Remaining}, now)
	})
	return out, err
}
