package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
)

// Order read model for the POS (v0.7 G-1/G-2): a keyset-paginated list and a detail view with
// server-computed allowed_actions, so the frontend never re-implements the order FSM.

const (
	orderListDefaultLimit = 50
	orderListMaxLimit     = 100
)

var (
	ErrOrderNotFound   = errors.New("order not found")
	orderSearchPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,30}$`)
)

type OrderListFilter struct {
	Status        string
	PaymentStatus string
	BillingMode   string
	From, To      string
	ShiftID       string
	TableID       string
	Query         string
	Limit         string
	After         string
}

type OrderSummary struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	OrderNumber    string     `json:"order_number" db:"order_number"`
	QueueNumber    string     `json:"queue_number" db:"queue_number"`
	OrderType      string     `json:"order_type" db:"order_type"`
	BillingMode    string     `json:"billing_mode" db:"billing_mode"`
	Status         string     `json:"status" db:"status"`
	PaymentStatus  string     `json:"payment_status" db:"payment_status"`
	TableNumber    string     `json:"table_number" db:"table_number"`
	CustomerName   string     `json:"customer_name" db:"customer_name"`
	TotalAmount    int64      `json:"total_amount" db:"total_amount"`
	PaidAmount     int64      `json:"paid_amount" db:"paid_amount"`
	RefundedAmount int64      `json:"refunded_amount" db:"refunded_amount"`
	ItemCount      int64      `json:"item_count" db:"item_count"`
	ShiftID        *uuid.UUID `json:"shift_id,omitempty" db:"shift_id"`
	CreatedAt      time.Time  `json:"created_at" db:"created_at"`
}

type OrderList struct {
	Items      []OrderSummary `json:"items"`
	NextCursor *string        `json:"next_cursor"`
}

type OrderPaymentView struct {
	ID             uuid.UUID  `json:"id" db:"id"`
	SplitID        *uuid.UUID `json:"split_id,omitempty" db:"split_id"`
	Method         string     `json:"method" db:"method"`
	Status         string     `json:"status" db:"status"`
	Amount         int64      `json:"amount" db:"amount"`
	RefundedAmount int64      `json:"refunded_amount" db:"refunded_amount"`
	PaidAt         *time.Time `json:"paid_at,omitempty" db:"paid_at"`
	CreatedAt      time.Time  `json:"created_at" db:"created_at"`
}

type OrderRefundView struct {
	ID                uuid.UUID `json:"id" db:"id"`
	PaymentID         uuid.UUID `json:"payment_id" db:"payment_id"`
	Amount            int64     `json:"amount" db:"amount"`
	Method            string    `json:"method" db:"method"`
	BankName          string    `json:"bank_name,omitempty" db:"bank_name"`
	AccountLast4      string    `json:"account_last4,omitempty" db:"account_last4"`
	TransferReference string    `json:"transfer_reference,omitempty" db:"transfer_reference"`
	Reason            string    `json:"reason" db:"reason"`
	ActorName         string    `json:"actor_name" db:"actor_name"`
	CreatedAt         time.Time `json:"created_at" db:"created_at"`
}

type OrderDetail struct {
	Order          *domain.Order         `json:"order"`
	Rounds         []domain.OrderRound   `json:"rounds"`
	Splits         []domain.PaymentSplit `json:"splits"`
	Payments       []OrderPaymentView    `json:"payments"`
	Refunds        []OrderRefundView     `json:"refunds"`
	AllowedActions []string              `json:"allowed_actions"`
}

type OrderReadService struct {
	db     *sqlx.DB
	orders *postgres.OrderRepository
	tabs   *postgres.TabRepository
}

func NewOrderReadService(db *sqlx.DB) *OrderReadService {
	return &OrderReadService{db: db, orders: postgres.NewOrderRepository(), tabs: postgres.NewTabRepository()}
}

// List handles GET /pos/orders: newest first, keyset cursor over (created_at, id).
func (s *OrderReadService) List(ctx context.Context, actor *AuthenticatedStaff, f OrderListFilter) (*OrderList, error) {
	if !actor.Can(rbac.OrderReadStore) {
		return nil, ErrForbidden
	}
	fields := map[string]string{}
	statuses := enumList(f.Status, domain.ValidOrderStatuses(), "status", fields)
	payStatuses := enumList(f.PaymentStatus, domain.ValidOrderPaymentStatuses(), "payment_status", fields)
	modes := enumList(f.BillingMode, domain.ValidBillingModes(), "billing_mode", fields)
	shiftID := optionalUUID(f.ShiftID, "shift_id", fields)
	tableID := optionalUUID(f.TableID, "table_id", fields)
	if f.Query != "" && !orderSearchPattern.MatchString(f.Query) {
		fields["q"] = "hanya huruf, angka, dan tanda hubung (maks 30)"
	}
	limit := orderListDefaultLimit
	if f.Limit != "" {
		n, err := strconv.Atoi(f.Limit)
		if err != nil || n < 1 || n > orderListMaxLimit {
			fields["limit"] = fmt.Sprintf("angka 1–%d", orderListMaxLimit)
		}
		limit = n
	}
	afterAt, afterID, ok := decodeOrderCursor(f.After)
	if !ok {
		fields["after"] = "cursor tidak valid"
	}
	if len(fields) > 0 {
		return nil, &ValidationError{Fields: fields}
	}

	storeID := actor.Staff.Store.ID
	out := &OrderList{Items: []OrderSummary{}}
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		rng, err := parseRange(ctx, tx, storeID, f.From, f.To)
		if err != nil {
			return err
		}
		q := `
			SELECT o.id, o.order_number, o.queue_number, o.order_type, o.billing_mode, o.status, o.payment_status,
			       COALESCE(o.table_number, '') AS table_number, COALESCE(o.customer_name, '') AS customer_name,
			       o.total_amount, o.paid_amount, o.refunded_amount, o.shift_id, o.created_at,
			       COALESCE((SELECT SUM(oi.quantity) FROM order_items oi WHERE oi.store_id = o.store_id AND oi.order_id = o.id), 0) AS item_count
			FROM orders o
			WHERE o.store_id = $1 AND o.created_at >= $2 AND o.created_at < $3`
		args := []any{storeID, rng.start, rng.end}
		add := func(cond string, v any) {
			args = append(args, v)
			q += fmt.Sprintf(" AND "+cond, len(args))
		}
		if len(statuses) > 0 {
			add("o.status = ANY($%d)", statuses)
		}
		if len(payStatuses) > 0 {
			add("o.payment_status = ANY($%d)", payStatuses)
		}
		if len(modes) > 0 {
			add("o.billing_mode = ANY($%d)", modes)
		}
		if shiftID != nil {
			add("o.shift_id = $%d", *shiftID)
		}
		if tableID != nil {
			add("o.table_id = $%d", *tableID)
		}
		if f.Query != "" {
			add("(o.order_number ILIKE $%[1]d || '%%' OR o.queue_number ILIKE $%[1]d || '%%')", f.Query)
		}
		if afterID != uuid.Nil {
			args = append(args, afterAt, afterID)
			q += fmt.Sprintf(" AND (o.created_at, o.id) < ($%d, $%d)", len(args)-1, len(args))
		}
		args = append(args, limit+1)
		q += fmt.Sprintf(" ORDER BY o.created_at DESC, o.id DESC LIMIT $%d", len(args))
		return tx.SelectContext(ctx, &out.Items, q, args...)
	})
	if err != nil {
		return nil, err
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		last := out.Items[limit-1]
		c := base64.RawURLEncoding.EncodeToString([]byte(last.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + last.ID.String()))
		out.NextCursor = &c
	}
	return out, nil
}

// Get handles GET /pos/orders/:id.
func (s *OrderReadService) Get(ctx context.Context, actor *AuthenticatedStaff, orderID uuid.UUID) (*OrderDetail, error) {
	if !actor.Can(rbac.OrderReadStore) {
		return nil, ErrForbidden
	}
	storeID := actor.Staff.Store.ID
	out := &OrderDetail{Rounds: []domain.OrderRound{}, Payments: []OrderPaymentView{}, Refunds: []OrderRefundView{}}
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		order, splits, err := s.orders.GetOrderByIDReadOnly(ctx, tx, storeID, orderID)
		if errors.Is(err, postgres.ErrOrderNotFound) {
			return ErrOrderNotFound
		}
		if err != nil {
			return err
		}
		order.IdempotencyKey, order.RequestHash = "", ""
		out.Order, out.Splits = order, splits
		if out.Splits == nil {
			out.Splits = []domain.PaymentSplit{}
		}
		if order.BillingMode == domain.BillingModeOpenTab {
			rounds, err := s.tabs.ListRoundsByOrder(ctx, tx, storeID, orderID)
			if err != nil {
				return fmt.Errorf("failed to load rounds: %w", err)
			}
			for i := range rounds {
				rounds[i].IdempotencyKey, rounds[i].RequestHash = "", ""
			}
			out.Rounds = append(out.Rounds, rounds...)
		}
		if err := tx.SelectContext(ctx, &out.Payments, `
			SELECT id, split_id, method, status, amount, refunded_amount, paid_at, created_at
			FROM payments WHERE store_id = $1 AND order_id = $2 ORDER BY created_at, id`, storeID, orderID); err != nil {
			return fmt.Errorf("failed to load payments: %w", err)
		}
		return tx.SelectContext(ctx, &out.Refunds, `
			SELECT r.id, r.payment_id, r.amount, r.method, COALESCE(r.bank_name, '') AS bank_name,
			       COALESCE(r.account_last4, '') AS account_last4, COALESCE(r.transfer_reference, '') AS transfer_reference,
			       r.reason, COALESCE(u.full_name, '') AS actor_name, r.created_at
			FROM payment_refunds r LEFT JOIN users u ON u.store_id = r.store_id AND u.id = r.actor_id
			WHERE r.store_id = $1 AND r.order_id = $2 ORDER BY r.created_at, r.id`, storeID, orderID)
	})
	if err != nil {
		return nil, err
	}
	out.AllowedActions = allowedOrderActions(actor, out.Order)
	return out, nil
}

// allowedOrderActions mirrors the state checks of the write paths (void, refund, payment, tab).
// Owner-PIN thresholds are still enforced when the action runs; this only decides which buttons show.
func allowedOrderActions(actor *AuthenticatedStaff, o *domain.Order) []string {
	acts := []string{}
	tabOpen := o.BillingMode == domain.BillingModeOpenTab && o.Status == domain.OrderStatusOpen
	switch {
	case tabOpen:
		if actor.Can(rbac.TabManage) {
			acts = append(acts, "add_round", "close_tab")
		}
	case o.BillingMode == domain.BillingModeOpenTab && o.Status == domain.OrderStatusPendingPayment && o.PaidAmount == 0:
		if actor.Can(rbac.TabManage) {
			acts = append(acts, "reopen_tab")
		}
	}
	payable := slices.Contains([]domain.OrderPaymentStatus{domain.OrderPaymentStatusUnpaid, domain.OrderPaymentStatusFailed,
		domain.OrderPaymentStatusExpired, domain.OrderPaymentStatusPartiallyPaid}, o.PaymentStatus)
	if payable && !tabOpen && o.Status != domain.OrderStatusCancelled &&
		(actor.Can(rbac.PaymentTenderDirect) || actor.Can(rbac.PaymentInitiateGateway)) {
		acts = append(acts, "pay")
	}
	voidable := slices.Contains([]domain.OrderPaymentStatus{domain.OrderPaymentStatusUnpaid, domain.OrderPaymentStatusFailed,
		domain.OrderPaymentStatusExpired}, o.PaymentStatus)
	if voidable && o.Status != domain.OrderStatusCancelled && actor.Can(rbac.OrderVoid) {
		acts = append(acts, "void")
	}
	if (o.PaymentStatus == domain.OrderPaymentStatusPaid || o.PaymentStatus == domain.OrderPaymentStatusPartiallyRefunded) && actor.Can(rbac.PaymentRefund) {
		acts = append(acts, "refund")
	}
	if o.PaidAmount > 0 || o.Status == domain.OrderStatusCancelled {
		acts = append(acts, "print_receipt")
	}
	return acts
}

func enumList[T ~string](raw string, valid []T, field string, fields map[string]string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	for _, v := range strings.Split(raw, ",") {
		v = strings.TrimSpace(v)
		if !slices.Contains(valid, T(v)) {
			fields[field] = fmt.Sprintf("nilai tidak dikenal: %q", v)
			return nil
		}
		out = append(out, v)
	}
	return out
}

func optionalUUID(raw, field string, fields map[string]string) *uuid.UUID {
	if raw == "" {
		return nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		fields[field] = "UUID tidak valid"
		return nil
	}
	return &id
}

// decodeOrderCursor reads "<RFC3339Nano>|<uuid>"; an empty cursor is the first page.
func decodeOrderCursor(raw string) (time.Time, uuid.UUID, bool) {
	if raw == "" {
		return time.Time{}, uuid.Nil, true
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return time.Time{}, uuid.Nil, false
	}
	at, id, found := strings.Cut(string(b), "|")
	t, err1 := time.Parse(time.RFC3339Nano, at)
	u, err2 := uuid.Parse(id)
	if !found || err1 != nil || err2 != nil || u == uuid.Nil {
		return time.Time{}, uuid.Nil, false
	}
	return t, u, true
}
