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
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
)

// Remote owner approval (ADR-007): a cashier asks, the owner approves on their phone with their own
// PIN, and the cashier's void/refund/stock override/shift close consumes the approval once.

const (
	ApprovalVoid          = "void"
	ApprovalRefund        = "refund"
	ApprovalStockOverride = "stock_override"
	ApprovalShiftVariance = "shift_variance"
	ApprovalOutletExpense = "outlet_expense"
	ApprovalDiscount      = "discount"

	approvalWindow      = 2 * time.Minute // to decide, then again to use an approval (RA3)
	approvalMaxPending  = 3
	approvalStatusPend  = "pending"
	approvalStatusOK    = "approved"
	approvalStatusStale = "expired" // derived, never stored
)

var (
	ErrApprovalNotFound   = errors.New("approval request not found")
	ErrApprovalNotNeeded  = errors.New("this action does not need owner approval")
	ErrApprovalLimit      = errors.New("too many pending approval requests")
	ErrApprovalNotPending = errors.New("approval request is no longer pending")
	ErrApprovalExpired    = errors.New("approval request expired")
	ErrApprovalInvalid    = errors.New("approval does not match this action or can no longer be used")
	ErrApprovalSelf       = errors.New("an owner cannot decide their own request")
)

var approvalPermission = map[string]rbac.Permission{
	ApprovalVoid: rbac.OrderVoid, ApprovalRefund: rbac.PaymentRefund,
	ApprovalStockOverride: rbac.StockOverride, ApprovalShiftVariance: rbac.ShiftClose,
	ApprovalOutletExpense: rbac.CashMovementCreate, ApprovalDiscount: rbac.OrderDiscountApply,
}

type CreateApprovalInput struct {
	Action      string          `json:"action"`
	ReferenceID uuid.UUID       `json:"reference_id"`
	Params      json.RawMessage `json:"params"`
	Reason      string          `json:"reason"`
}

type approvalParams struct {
	NewQuantity *int   `json:"new_quantity,omitempty"`
	CountedCash *int64 `json:"counted_cash,omitempty"`
	// Outlet expense (V13): the approval covers exactly this amount and category.
	Amount   *int64 `json:"amount,omitempty"`
	Category string `json:"category,omitempty"`
}

type Approval struct {
	ID              uuid.UUID       `json:"id" db:"id"`
	Action          string          `json:"action" db:"action"`
	ReferenceID     uuid.UUID       `json:"reference_id" db:"reference_id"`
	ReferenceLabel  string          `json:"reference_label" db:"reference_label"`
	Amount          int64           `json:"amount" db:"amount"`
	Params          json.RawMessage `json:"params" db:"params"`
	Reason          string          `json:"reason" db:"reason"`
	Status          string          `json:"status" db:"status"`
	RequestedBy     uuid.UUID       `json:"-" db:"requested_by"`
	RequestedByName string          `json:"requested_by_name" db:"requested_by_name"`
	DecidedByName   string          `json:"decided_by_name,omitempty" db:"decided_by_name"`
	DecisionNote    string          `json:"decision_note,omitempty" db:"decision_note"`
	ExpiresAt       time.Time       `json:"expires_at" db:"expires_at"`
	DecidedAt       *time.Time      `json:"decided_at,omitempty" db:"decided_at"`
	CreatedAt       time.Time       `json:"created_at" db:"created_at"`
}

// approvalClaim is what a write path offers instead of an owner PIN.
type approvalClaim struct {
	ID     *uuid.UUID
	Action string
	Ref    uuid.UUID
	Params approvalParams
	used   bool
}

// usedID is the approval to record on the action's audit row: only one that was actually consumed.
func (c *approvalClaim) usedID() *uuid.UUID {
	if c.used {
		return c.ID
	}
	return nil
}

type ApprovalService struct {
	db      *sqlx.DB
	ops     *OperationalService
	shifts  *postgres.ShiftRepository
	nowFunc func() time.Time
}

// NewApprovalService shares the operational service's PIN guard, so owner PIN limits (HF5) span both.
func NewApprovalService(db *sqlx.DB, ops *OperationalService) *ApprovalService {
	return &ApprovalService{db: db, ops: ops, shifts: postgres.NewShiftRepository(), nowFunc: time.Now}
}

// Create records a pending request after checking that the action really needs the owner (RA1, RA6).
func (s *ApprovalService) Create(ctx context.Context, actor *AuthenticatedStaff, in CreateApprovalInput) (*Approval, error) {
	perm, known := approvalPermission[in.Action]
	reason := strings.TrimSpace(in.Reason)
	fields := map[string]string{}
	if !known {
		fields["action"] = "harus void, refund, stock_override, shift_variance, outlet_expense atau discount"
	}
	if in.Action == ApprovalOutletExpense {
		in.ReferenceID = actor.Staff.Store.ID // the fund belongs to the store, not to one record
	}
	if in.ReferenceID == uuid.Nil {
		fields["reference_id"] = "wajib diisi"
	}
	if reason == "" {
		fields["reason"] = "alasan wajib diisi"
	}
	var params approvalParams
	if len(in.Params) > 0 && string(in.Params) != "null" {
		if err := json.Unmarshal(in.Params, &params); err != nil {
			fields["params"] = "format tidak valid"
		}
	}
	if in.Action == ApprovalStockOverride && (params.NewQuantity == nil || *params.NewQuantity < 0) {
		fields["params.new_quantity"] = "wajib diisi, tidak boleh negatif"
	}
	if in.Action == ApprovalOutletExpense && (params.Amount == nil || *params.Amount <= 0 || !outletExpenseCategories[params.Category]) {
		fields["params"] = "amount (> 0) dan category (ingredients, operational, other) wajib diisi"
	}
	if in.Action == ApprovalDiscount && (params.Amount == nil || *params.Amount <= 0) {
		fields["params.amount"] = "nominal diskon (rupiah, > 0) wajib diisi"
	}
	if in.Action == ApprovalShiftVariance && (params.CountedCash == nil || *params.CountedCash < 0) {
		fields["params.counted_cash"] = "wajib diisi, tidak boleh negatif"
	}
	if len(fields) > 0 {
		return nil, &ValidationError{Fields: fields}
	}
	if !actor.Can(perm) || actor.Staff.Role == domain.RoleOwner {
		return nil, ErrForbidden // owners use their PIN directly
	}

	storeID, now := actor.Staff.Store.ID, s.nowFunc().UTC()
	id := uuid.New()
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		var pending int
		if err := tx.GetContext(ctx, &pending, `SELECT COUNT(*) FROM approval_requests
			WHERE store_id = $1 AND requested_by = $2 AND status = 'pending' AND expires_at > $3`, storeID, actor.Staff.UserID, now); err != nil {
			return err
		}
		if pending >= approvalMaxPending {
			return ErrApprovalLimit
		}
		amount, needed, err := s.requiredAmount(ctx, tx, actor, in.Action, in.ReferenceID, params, now)
		if err != nil {
			return err
		}
		if !needed {
			return ErrApprovalNotNeeded
		}
		stored, _ := json.Marshal(params)
		if _, err := tx.ExecContext(ctx, `INSERT INTO approval_requests
			(id, store_id, action, reference_id, params, amount, reason, requested_by, expires_at, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			id, storeID, in.Action, in.ReferenceID, stored, amount, reason, actor.Staff.UserID, now.Add(approvalWindow), now); err != nil {
			return fmt.Errorf("failed to insert approval request: %w", err)
		}
		return s.recordTx(ctx, tx, actor, id, "APPROVAL_REQUESTED", "approval.requested", reason, now)
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, actor, id)
}

// requiredAmount recomputes the amount the owner sees and whether the cashier needs the owner at all.
// The same rules run again when the approval is consumed.
func (s *ApprovalService) requiredAmount(ctx context.Context, tx *sqlx.Tx, actor *AuthenticatedStaff, action string, ref uuid.UUID, p approvalParams, now time.Time) (int64, bool, error) {
	storeID := actor.Staff.Store.ID
	settings, err := s.ops.settings.GetStoreSettings(ctx, tx, storeID)
	if err != nil {
		return 0, false, err
	}
	threshold := settings.VoidRefundOwnerThreshold
	if threshold <= 0 {
		threshold = 100000
	}
	switch action {
	case ApprovalVoid:
		var total int64
		err := tx.GetContext(ctx, &total, `SELECT total_amount FROM orders WHERE store_id = $1 AND id = $2 AND status <> 'cancelled'`, storeID, ref)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, ErrApprovalNotFound
		}
		return total, total > threshold, err
	case ApprovalRefund:
		payment, err := s.ops.paymentRepo.GetPaymentByIDForUpdate(ctx, tx, storeID, ref)
		if errors.Is(err, postgres.ErrPaymentNotFound) || (err == nil && payment.Status != domain.PaymentStatusPaid) {
			return 0, false, ErrApprovalNotFound
		}
		if err != nil {
			return 0, false, err
		}
		ownerWindow, err := s.ops.checkRefundWindow(ctx, tx, actor, payment, now)
		if err != nil {
			return 0, false, err
		}
		return payment.Amount, payment.Amount > threshold || ownerWindow, nil
	case ApprovalStockOverride:
		var exists bool
		if err := tx.GetContext(ctx, &exists, `SELECT EXISTS (SELECT 1 FROM products WHERE store_id = $1 AND id = $2)`, storeID, ref); err != nil {
			return 0, false, err
		}
		if !exists {
			return 0, false, ErrApprovalNotFound
		}
		return int64(*p.NewQuantity), true, nil // decision O4: always the owner
	case ApprovalShiftVariance:
		shift, err := s.shifts.GetShiftForUpdate(ctx, tx, storeID, ref)
		if errors.Is(err, postgres.ErrShiftNotFound) || (err == nil && shift.Status != domain.ShiftStatusOpen) {
			return 0, false, ErrApprovalNotFound
		}
		if err != nil {
			return 0, false, err
		}
		summary, err := s.shifts.AggregateCashTotalsByShift(ctx, tx, storeID, shift.ID)
		if err != nil {
			return 0, false, err
		}
		variance := *p.CountedCash - summary.ExpectedCash(shift.OpeningFloat)
		tolerance := settings.CashVarianceTolerance
		if tolerance <= 0 {
			tolerance = 5000
		}
		return variance, variance > tolerance || -variance > tolerance, nil
	case ApprovalDiscount:
		var subtotal int64
		err := tx.GetContext(ctx, &subtotal, `SELECT subtotal_amount FROM orders WHERE store_id = $1 AND id = $2
			AND status = 'pending_payment' AND billing_mode = 'pay_first' AND paid_amount = 0 AND discount_amount = 0`, storeID, ref)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && *p.Amount > subtotal) {
			return 0, false, ErrApprovalNotFound
		}
		if err != nil {
			return 0, false, err
		}
		return *p.Amount, discountNeedsOwner(*p.Amount, subtotal, settings.CashierMaxDiscountBPS), nil
	case ApprovalOutletExpense:
		spent, err := outletExpensesTodayTx(ctx, tx, storeID)
		if err != nil {
			return 0, false, err
		}
		return *p.Amount, spent+*p.Amount > settings.OutletDailyExpenseLimit, nil
	}
	return 0, false, ErrApprovalInvalid
}

const approvalSelect = `
	SELECT a.id, a.action, a.reference_id, a.amount, a.params, a.reason, a.status, a.requested_by,
	       COALESCE(a.decision_note, '') AS decision_note, a.expires_at, a.decided_at, a.created_at,
	       COALESCE(o.order_number, po.order_number, pr.name, d.name, CASE WHEN a.action = 'outlet_expense' THEN 'Kas outlet' END, '') AS reference_label,
	       COALESCE(ru.full_name, '') AS requested_by_name, COALESCE(du.full_name, '') AS decided_by_name
	FROM approval_requests a
	LEFT JOIN orders o ON a.action IN ('void', 'discount') AND o.store_id = a.store_id AND o.id = a.reference_id
	LEFT JOIN payments p ON a.action = 'refund' AND p.store_id = a.store_id AND p.id = a.reference_id
	LEFT JOIN orders po ON po.store_id = p.store_id AND po.id = p.order_id
	LEFT JOIN products pr ON a.action = 'stock_override' AND pr.store_id = a.store_id AND pr.id = a.reference_id
	LEFT JOIN shifts sh ON a.action = 'shift_variance' AND sh.store_id = a.store_id AND sh.id = a.reference_id
	LEFT JOIN cash_drawers d ON d.store_id = sh.store_id AND d.id = sh.drawer_id
	LEFT JOIN users ru ON ru.store_id = a.store_id AND ru.id = a.requested_by
	LEFT JOIN users du ON du.store_id = a.store_id AND du.id = a.decided_by
	WHERE a.store_id = $1`

// Get returns one request to its requester or to an owner.
func (s *ApprovalService) Get(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID) (*Approval, error) {
	var a Approval
	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		return tx.GetContext(ctx, &a, approvalSelect+` AND a.id = $2`, actor.Staff.Store.ID, id)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrApprovalNotFound
	}
	if err != nil {
		return nil, err
	}
	if a.RequestedBy != actor.Staff.UserID && !actor.Can(rbac.ApprovalDecide) {
		return nil, ErrApprovalNotFound
	}
	s.derive(&a)
	return &a, nil
}

// ListPending returns the unexpired pending requests, newest first, for the owner's phone.
func (s *ApprovalService) ListPending(ctx context.Context, actor *AuthenticatedStaff) ([]Approval, error) {
	if !actor.Can(rbac.ApprovalDecide) {
		return nil, ErrForbidden
	}
	out := []Approval{}
	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &out, approvalSelect+` AND a.status = 'pending' AND a.expires_at > $2
			ORDER BY a.created_at DESC LIMIT 50`, actor.Staff.Store.ID, s.nowFunc().UTC())
	})
	return out, err
}

func (s *ApprovalService) derive(a *Approval) {
	if (a.Status == approvalStatusPend || a.Status == approvalStatusOK) && !s.nowFunc().Before(a.ExpiresAt) {
		a.Status = approvalStatusStale
	}
}

// Approve checks the deciding owner's own PIN (RA2) and opens the 2-minute window to use it.
func (s *ApprovalService) Approve(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID, pin string) (*Approval, error) {
	if !actor.Can(rbac.ApprovalDecide) {
		return nil, ErrForbidden
	}
	pin = strings.TrimSpace(pin)
	if pin == "" {
		return nil, &ValidationError{Fields: map[string]string{"pin": "PIN Owner wajib diisi"}}
	}
	err := s.ops.pins.withPINTx(ctx, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		now, err := s.lockPending(ctx, tx, actor, id)
		if err != nil {
			return err
		}
		ownerID, err := s.ops.pins.verifyOwner(ctx, tx, actor, pin)
		if err != nil {
			return err
		}
		if ownerID != actor.Staff.UserID {
			return ErrInvalidOwnerPIN // another owner's PIN: the decider confirms with their own
		}
		if _, err := tx.ExecContext(ctx, `UPDATE approval_requests SET status = 'approved', decided_by = $3, decided_at = $4, expires_at = $5
			WHERE store_id = $1 AND id = $2`, actor.Staff.Store.ID, id, actor.Staff.UserID, now, now.Add(approvalWindow)); err != nil {
			return fmt.Errorf("failed to approve request: %w", err)
		}
		return s.recordTx(ctx, tx, actor, id, "APPROVAL_APPROVED", "approval.decided", "Permintaan persetujuan disetujui owner", now)
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, actor, id)
}

// Reject closes a pending request with an optional note shown to the cashier.
func (s *ApprovalService) Reject(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID, note string) (*Approval, error) {
	if !actor.Can(rbac.ApprovalDecide) {
		return nil, ErrForbidden
	}
	note = strings.TrimSpace(note)
	if len(note) > 500 {
		return nil, &ValidationError{Fields: map[string]string{"note": "maksimal 500 karakter"}}
	}
	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		now, err := s.lockPending(ctx, tx, actor, id)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE approval_requests SET status = 'rejected', decided_by = $3, decided_at = $4, decision_note = NULLIF($5, '')
			WHERE store_id = $1 AND id = $2`, actor.Staff.Store.ID, id, actor.Staff.UserID, now, note); err != nil {
			return fmt.Errorf("failed to reject request: %w", err)
		}
		reason := "Permintaan persetujuan ditolak owner"
		if note != "" {
			reason += ": " + note
		}
		return s.recordTx(ctx, tx, actor, id, "APPROVAL_REJECTED", "approval.decided", reason, now)
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, actor, id)
}

func (s *ApprovalService) lockPending(ctx context.Context, tx *sqlx.Tx, actor *AuthenticatedStaff, id uuid.UUID) (time.Time, error) {
	var row struct {
		Status      string    `db:"status"`
		RequestedBy uuid.UUID `db:"requested_by"`
		ExpiresAt   time.Time `db:"expires_at"`
	}
	err := tx.GetContext(ctx, &row, `SELECT status, requested_by, expires_at FROM approval_requests WHERE store_id = $1 AND id = $2 FOR UPDATE`,
		actor.Staff.Store.ID, id)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrApprovalNotFound
	}
	if err != nil {
		return time.Time{}, err
	}
	now := s.nowFunc().UTC()
	switch {
	case row.RequestedBy == actor.Staff.UserID:
		return now, ErrApprovalSelf
	case row.Status != approvalStatusPend:
		return now, ErrApprovalNotPending
	case !now.Before(row.ExpiresAt):
		return now, ErrApprovalExpired
	}
	return now, nil
}

func (s *ApprovalService) recordTx(ctx context.Context, tx *sqlx.Tx, actor *AuthenticatedStaff, id uuid.UUID, audit, event, reason string, now time.Time) error {
	actorID := actor.Staff.UserID
	meta, _ := json.Marshal(map[string]any{"approval_id": id})
	if err := postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{StoreID: actor.Staff.Store.ID, ActorID: &actorID, ActorName: actor.Staff.FullName,
		Action: audit, Reason: reason, ReferenceID: id.String(), Metadata: string(meta), CreatedAt: now}); err != nil {
		return err
	}
	var status string
	if err := tx.GetContext(ctx, &status, `SELECT status FROM approval_requests WHERE store_id = $1 AND id = $2`, actor.Staff.Store.ID, id); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"approval_id": id, "status": status})
	return postgres.NewOrderRepository().InsertOutboxEvent(ctx, tx, &domain.OutboxEvent{ID: uuid.New(), StoreID: actor.Staff.Store.ID,
		EventType: event, AggregateType: "approval", AggregateID: id.String(), Payload: string(payload)})
}

// consumeApproval spends an approved request inside the action's own transaction (RA3, RA4). It
// returns the approving owner, or ErrApprovalInvalid when anything differs from what was approved.
func consumeApproval(ctx context.Context, tx *sqlx.Tx, actor *AuthenticatedStaff, c *approvalClaim, amount int64, now time.Time) (uuid.UUID, error) {
	var row struct {
		Action      string          `db:"action"`
		ReferenceID uuid.UUID       `db:"reference_id"`
		Amount      int64           `db:"amount"`
		Params      json.RawMessage `db:"params"`
		Status      string          `db:"status"`
		RequestedBy uuid.UUID       `db:"requested_by"`
		DecidedBy   *uuid.UUID      `db:"decided_by"`
		ExpiresAt   time.Time       `db:"expires_at"`
	}
	err := tx.GetContext(ctx, &row, `SELECT action, reference_id, amount, params, status, requested_by, decided_by, expires_at
		FROM approval_requests WHERE store_id = $1 AND id = $2 FOR UPDATE`, actor.Staff.Store.ID, *c.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return uuid.Nil, ErrApprovalInvalid
	}
	if err != nil {
		return uuid.Nil, err
	}
	var approved approvalParams
	_ = json.Unmarshal(row.Params, &approved)
	paramsMatch := (c.Params.NewQuantity == nil || (approved.NewQuantity != nil && *approved.NewQuantity == *c.Params.NewQuantity)) &&
		(c.Params.CountedCash == nil || (approved.CountedCash != nil && *approved.CountedCash == *c.Params.CountedCash)) &&
		(c.Params.Amount == nil || (approved.Amount != nil && *approved.Amount == *c.Params.Amount)) &&
		c.Params.Category == approved.Category
	if row.Status != approvalStatusOK || row.DecidedBy == nil || !now.Before(row.ExpiresAt) || row.RequestedBy != actor.Staff.UserID ||
		row.Action != c.Action || row.ReferenceID != c.Ref || row.Amount != amount || !paramsMatch {
		return uuid.Nil, ErrApprovalInvalid
	}
	if _, err := tx.ExecContext(ctx, `UPDATE approval_requests SET status = 'consumed', consumed_at = $3 WHERE store_id = $1 AND id = $2`,
		actor.Staff.Store.ID, *c.ID, now); err != nil {
		return uuid.Nil, fmt.Errorf("failed to consume approval: %w", err)
	}
	c.used = true
	return *row.DecidedBy, nil
}
