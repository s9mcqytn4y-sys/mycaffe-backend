package service

import (
	"context"
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

// Outlet cash fund (decision V13): money kept apart from the cashier drawers for daily expenses,
// topped up by the owner. The append-only ledger's signed sum is the balance.

const (
	OutletTopUp      = "top_up"
	OutletExpense    = "expense"
	OutletRefund     = "refund" // written by refunds paid from the outlet fund (PR 8.5)
	OutletToDrawer   = "to_drawer"
	OutletFromDrawer = "from_drawer"
)

var (
	ErrOutletInsufficientFunds  = errors.New("outlet cash balance is too low")
	ErrOutletApprovalRequired   = errors.New("expense exceeds today's outlet limit; owner approval required")
	outletExpenseCategories     = map[string]bool{"ingredients": true, "operational": true, "other": true}
	outletSignedAmount          = `CASE WHEN movement_type IN ('top_up', 'from_drawer') THEN amount ELSE -amount END`
	outletMovementReasonsByType = map[string]string{
		OutletTopUp: "Top-up kas outlet", OutletExpense: "Pengeluaran kas outlet",
		OutletToDrawer: "Kas outlet dipindah ke laci", OutletFromDrawer: "Kas laci disetor ke kas outlet",
	}
)

type OutletMovementInput struct {
	MovementType string     `json:"movement_type"`
	Amount       int64      `json:"amount"`
	Category     string     `json:"category"`
	Note         string     `json:"note"`
	DrawerID     *uuid.UUID `json:"drawer_id,omitempty"`
	ApprovalID   *uuid.UUID `json:"approval_id,omitempty"`
}

type OutletMovement struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	MovementType string     `json:"movement_type" db:"movement_type"`
	Amount       int64      `json:"amount" db:"amount"`
	Category     *string    `json:"category,omitempty" db:"category"`
	Note         string     `json:"note" db:"note"`
	ShiftID      *uuid.UUID `json:"shift_id,omitempty" db:"shift_id"`
	ActorName    string     `json:"actor_name" db:"actor_name"`
	CreatedAt    time.Time  `json:"created_at" db:"created_at"`
}

type OutletCashSummary struct {
	Balance          int64            `json:"balance"`
	ExpensesToday    int64            `json:"expenses_today"`
	DailyLimit       int64            `json:"daily_expense_limit"`
	RemainingToday   int64            `json:"remaining_without_approval"`
	RecentMovements  []OutletMovement `json:"recent_movements"`
	OpenDrawer       bool             `json:"open_drawer,omitempty"`
	RecordedMovement *OutletMovement  `json:"movement,omitempty"`
}

type OutletCashService struct {
	db      *sqlx.DB
	shifts  *postgres.ShiftRepository
	nowFunc func() time.Time
}

func NewOutletCashService(db *sqlx.DB) *OutletCashService {
	return &OutletCashService{db: db, shifts: postgres.NewShiftRepository(), nowFunc: time.Now}
}

// Summary returns the balance, today's spending against the limit and the 50 latest movements.
func (s *OutletCashService) Summary(ctx context.Context, actor *AuthenticatedStaff) (*OutletCashSummary, error) {
	if !actor.Can(rbac.CashMovementCreate) {
		return nil, ErrForbidden
	}
	var out *OutletCashSummary
	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		var err error
		out, err = s.summaryTx(ctx, tx, actor.Staff.Store.ID)
		return err
	})
	return out, err
}

func (s *OutletCashService) summaryTx(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID) (*OutletCashSummary, error) {
	out := &OutletCashSummary{RecentMovements: []OutletMovement{}}
	if err := tx.GetContext(ctx, &out.Balance, `SELECT COALESCE(SUM(`+outletSignedAmount+`), 0) FROM outlet_cash_movements WHERE store_id = $1`, storeID); err != nil {
		return nil, err
	}
	spent, err := outletExpensesTodayTx(ctx, tx, storeID)
	if err != nil {
		return nil, err
	}
	settings, err := postgres.NewSettingsRepository().GetStoreSettings(ctx, tx, storeID)
	if err != nil {
		return nil, err
	}
	out.ExpensesToday, out.DailyLimit = spent, settings.OutletDailyExpenseLimit
	out.RemainingToday = max(0, min(out.DailyLimit-spent, out.Balance))
	err = tx.SelectContext(ctx, &out.RecentMovements, `
		SELECT m.id, m.movement_type, m.amount, m.category, m.note, m.shift_id, COALESCE(u.full_name, '') AS actor_name, m.created_at
		FROM outlet_cash_movements m LEFT JOIN users u ON u.store_id = m.store_id AND u.id = m.actor_id
		WHERE m.store_id = $1 ORDER BY m.created_at DESC, m.id DESC LIMIT 50`, storeID)
	return out, err
}

// outletExpensesTodayTx sums outlet expenses since midnight in the store time zone.
func outletExpensesTodayTx(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID) (int64, error) {
	rng, err := parseRange(ctx, tx, storeID, "", "")
	if err != nil {
		return 0, err
	}
	var spent int64
	err = tx.GetContext(ctx, &spent, `SELECT COALESCE(SUM(amount), 0) FROM outlet_cash_movements
		WHERE store_id = $1 AND movement_type = 'expense' AND created_at >= $2 AND created_at < $3`, storeID, rng.start, rng.end)
	return spent, err
}

// Record writes one movement. Top-ups are owner-only; a cashier expense beyond today's limit needs an
// approved ADR-007 request for exactly this amount and category; drawer transfers move real cash through
// the open shift of the drawer, so its expected cash stays right.
func (s *OutletCashService) Record(ctx context.Context, actor *AuthenticatedStaff, in OutletMovementInput) (*OutletCashSummary, error) {
	in.Note, in.Category = strings.TrimSpace(in.Note), strings.TrimSpace(in.Category)
	fields := map[string]string{}
	if _, ok := outletMovementReasonsByType[in.MovementType]; !ok {
		fields["movement_type"] = "harus top_up, expense, to_drawer atau from_drawer"
	}
	if in.Amount <= 0 || in.Amount > 100_000_000 {
		fields["amount"] = "harus 1 - Rp100.000.000"
	}
	if in.Note == "" || len(in.Note) > 500 {
		fields["note"] = "keterangan wajib diisi (maks 500 karakter)"
	}
	if in.MovementType == OutletExpense && !outletExpenseCategories[in.Category] {
		fields["category"] = "harus ingredients, operational atau other"
	}
	if len(fields) > 0 {
		return nil, &ValidationError{Fields: fields}
	}
	if in.MovementType != OutletExpense {
		in.Category = ""
	}
	need := rbac.CashMovementCreate
	if in.MovementType == OutletTopUp {
		need = rbac.CashDrawerManage // owner puts money into the fund
	}
	if !actor.Can(need) {
		return nil, ErrForbidden
	}

	storeID, now := actor.Staff.Store.ID, s.nowFunc().UTC()
	var out *OutletCashSummary
	var movementID uuid.UUID
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		var err error
		if movementID, err = s.recordTx(ctx, tx, actor, in, now); err != nil {
			return err
		}
		out, err = s.summaryTx(ctx, tx, storeID)
		return err
	})
	if err != nil {
		return nil, err
	}
	for i := range out.RecentMovements {
		if out.RecentMovements[i].ID == movementID {
			out.RecordedMovement = &out.RecentMovements[i]
		}
	}
	out.OpenDrawer = in.MovementType == OutletToDrawer || in.MovementType == OutletFromDrawer
	return out, nil
}

// recordTx writes one validated movement inside an open tenant transaction (also used to pay supplier
// payables from the fund, PR 11.5) and returns its id.
func (s *OutletCashService) recordTx(ctx context.Context, tx *sqlx.Tx, actor *AuthenticatedStaff, in OutletMovementInput, now time.Time) (uuid.UUID, error) {
	storeID := actor.Staff.Store.ID
	var movementID uuid.UUID
	err := func() error {
		// One writer per store at a time: the balance and daily-limit checks below must see every movement.
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('outlet_cash:' || $1::text))`, storeID); err != nil {
			return err
		}
		summary, err := s.summaryTx(ctx, tx, storeID)
		if err != nil {
			return err
		}
		if (in.MovementType == OutletExpense || in.MovementType == OutletToDrawer) && in.Amount > summary.Balance {
			return ErrOutletInsufficientFunds
		}

		var approvedBy, approvalID *uuid.UUID
		if in.MovementType == OutletExpense && actor.Staff.Role != domain.RoleOwner && summary.ExpensesToday+in.Amount > summary.DailyLimit {
			if in.ApprovalID == nil {
				return ErrOutletApprovalRequired
			}
			amount := in.Amount
			owner, err := consumeApproval(ctx, tx, actor, &approvalClaim{ID: in.ApprovalID, Action: ApprovalOutletExpense, Ref: storeID,
				Params: approvalParams{Amount: &amount, Category: in.Category}}, in.Amount, now)
			if err != nil {
				return err
			}
			approvedBy, approvalID = &owner, in.ApprovalID
		}

		var shiftID, cashMovementID *uuid.UUID
		if in.MovementType == OutletToDrawer || in.MovementType == OutletFromDrawer {
			sID, cmID, err := s.moveThroughDrawerTx(ctx, tx, actor, in, now)
			if err != nil {
				return err
			}
			shiftID, cashMovementID = &sID, &cmID
		}

		movementID = uuid.New()
		var category *string
		if in.Category != "" {
			category = &in.Category
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO outlet_cash_movements
			(id, store_id, movement_type, amount, category, note, shift_id, cash_movement_id, approval_id, actor_id, approved_by, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			movementID, storeID, in.MovementType, in.Amount, category, in.Note, shiftID, cashMovementID, approvalID, actor.Staff.UserID, approvedBy, now); err != nil {
			return fmt.Errorf("failed to record outlet cash movement: %w", err)
		}
		actorID := actor.Staff.UserID
		meta, _ := json.Marshal(map[string]any{"movement_id": movementID, "movement_type": in.MovementType, "amount": in.Amount,
			"category": in.Category, "approval_id": approvalID, "approved_by": approvedBy, "shift_id": shiftID})
		if err := postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{StoreID: storeID, ActorID: &actorID, ActorName: actor.Staff.FullName,
			Action: "OUTLET_CASH_MOVEMENT", Reason: fmt.Sprintf("%s Rp%d: %s", outletMovementReasonsByType[in.MovementType], in.Amount, in.Note),
			ReferenceID: movementID.String(), Metadata: string(meta), CreatedAt: now}); err != nil {
			return err
		}
		return nil
	}()
	return movementID, err
}

// moveThroughDrawerTx books the drawer side of a transfer in the drawer's open shift: cash_in when the
// fund pays into the drawer, paid_out (never more than the drawer holds) when the drawer pays the fund.
func (s *OutletCashService) moveThroughDrawerTx(ctx context.Context, tx *sqlx.Tx, actor *AuthenticatedStaff, in OutletMovementInput, now time.Time) (uuid.UUID, uuid.UUID, error) {
	storeID := actor.Staff.Store.ID
	drawerID := uuid.Nil
	if in.DrawerID != nil {
		drawerID = *in.DrawerID
	} else {
		d, err := s.shifts.GetDefaultCashDrawer(ctx, tx, storeID)
		if err != nil {
			return uuid.Nil, uuid.Nil, err
		}
		drawerID = d.ID
	}
	shift, err := s.shifts.GetOpenShiftByDrawer(ctx, tx, storeID, drawerID)
	if errors.Is(err, postgres.ErrShiftNotFound) || errors.Is(err, postgres.ErrNoOpenShift) || errors.Is(err, postgres.ErrDrawerNotFound) {
		return uuid.Nil, uuid.Nil, ErrNoOpenShift
	}
	if err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	kind := domain.CashMovementCashIn
	if in.MovementType == OutletFromDrawer {
		kind = domain.CashMovementPaidOut
		summary, err := s.shifts.AggregateCashTotalsByShift(ctx, tx, storeID, shift.ID)
		if err != nil {
			return uuid.Nil, uuid.Nil, err
		}
		if in.Amount > summary.ExpectedCash(shift.OpeningFloat) {
			return uuid.Nil, uuid.Nil, ErrInsufficientDrawerCash
		}
	}
	reason := outletMovementReasonsByType[in.MovementType] + ": " + in.Note
	m := &domain.CashMovement{ID: uuid.New(), StoreID: storeID, ShiftID: shift.ID, MovementType: kind, Amount: in.Amount,
		ActorID: actor.Staff.UserID, Reason: &reason, CreatedAt: now}
	if err := s.shifts.InsertCashMovement(ctx, tx, m); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return shift.ID, m.ID, nil
}
