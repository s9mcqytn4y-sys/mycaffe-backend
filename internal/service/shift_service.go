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
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

var (
	ErrDrawerConflict           = errors.New("laci kas sudah memiliki sesi shift aktif atau nama duplikat")
	ErrShiftConflict            = errors.New("sesi shift tidak dalam status terbuka")
	ErrVarianceExceedsTolerance = errors.New("selisih kas melebihi batas toleransi toko")
	ErrInvalidOwnerPIN          = errors.New("PIN Owner tidak valid")
	ErrInsufficientDrawerCash   = errors.New("saldo kas di laci tidak mencukupi untuk mutasi pengeluaran ini")
)

type ShiftService struct {
	db       *sqlx.DB
	repo     *postgres.ShiftRepository
	authRep  *postgres.AuthRepository
	settings *postgres.SettingsRepository
	orderRep *postgres.OrderRepository
	nowFunc  func() time.Time
	pins     *pinGuard
}

func NewShiftService(db *sqlx.DB) *ShiftService {
	s := &ShiftService{
		db:       db,
		repo:     postgres.NewShiftRepository(),
		authRep:  postgres.NewAuthRepository(),
		settings: postgres.NewSettingsRepository(),
		orderRep: postgres.NewOrderRepository(),
		nowFunc:  time.Now,
	}
	s.pins = newPINGuard(db, s.authRep, func() time.Time { return s.nowFunc() })
	return s
}

// ---------- Drawer Inputs & Management ----------

type DrawerInput struct {
	Name     string `json:"name"`
	IsActive *bool  `json:"is_active,omitempty"`
}

func (s *ShiftService) CreateDrawer(ctx context.Context, actor *AuthenticatedStaff, in DrawerInput) (*domain.CashDrawer, error) {
	if !actor.Can(rbac.CashDrawerManage) {
		return nil, ErrForbidden
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, &ValidationError{Fields: map[string]string{"name": "wajib diisi"}}
	}

	now := s.nowFunc().UTC()
	drawer := &domain.CashDrawer{
		ID:        uuid.New(),
		StoreID:   actor.Staff.Store.ID,
		Name:      name,
		IsActive:  true,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if in.IsActive != nil {
		drawer.IsActive = *in.IsActive
	}

	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		if err := s.repo.InsertCashDrawer(ctx, tx, drawer); err != nil {
			if _, ok := postgres.UniqueViolation(err); ok {
				return &ConflictError{Field: "name", Message: "nama laci kas sudah dipakai"}
			}
			return err
		}
		actorID := actor.Staff.UserID
		return postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{
			StoreID:     actor.Staff.Store.ID,
			ActorID:     &actorID,
			ActorName:   actor.Staff.FullName,
			Action:      "DRAWER_CREATE",
			Reason:      fmt.Sprintf("Membuat laci kas baru %q", drawer.Name),
			ReferenceID: drawer.ID.String(),
			CreatedAt:   now,
		})
	})
	if err != nil {
		return nil, err
	}
	return drawer, nil
}

func (s *ShiftService) ListDrawers(ctx context.Context, actor *AuthenticatedStaff) ([]domain.CashDrawer, error) {
	if actor == nil {
		return nil, ErrInvalidSession
	}
	var drawers []domain.CashDrawer
	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		var err error
		drawers, err = s.repo.ListCashDrawers(ctx, tx, actor.Staff.Store.ID)
		return err
	})
	return drawers, err
}

func (s *ShiftService) UpdateDrawer(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID, in DrawerInput) (*domain.CashDrawer, error) {
	if !actor.Can(rbac.CashDrawerManage) {
		return nil, ErrForbidden
	}
	var drawer *domain.CashDrawer
	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		var err error
		drawer, err = s.repo.GetCashDrawer(ctx, tx, actor.Staff.Store.ID, id)
		if err != nil {
			if errors.Is(err, postgres.ErrDrawerNotFound) {
				return ErrCatalogNotFound
			}
			return err
		}
		if strings.TrimSpace(in.Name) != "" {
			drawer.Name = strings.TrimSpace(in.Name)
		}
		if in.IsActive != nil {
			drawer.IsActive = *in.IsActive
		}
		drawer.UpdatedAt = s.nowFunc().UTC()
		if err := s.repo.UpdateCashDrawer(ctx, tx, drawer); err != nil {
			if _, ok := postgres.UniqueViolation(err); ok {
				return &ConflictError{Field: "name", Message: "nama laci kas sudah dipakai"}
			}
			return err
		}
		actorID := actor.Staff.UserID
		return postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{
			StoreID:     actor.Staff.Store.ID,
			ActorID:     &actorID,
			ActorName:   actor.Staff.FullName,
			Action:      "DRAWER_UPDATE",
			Reason:      fmt.Sprintf("Memperbarui laci kas %q", drawer.Name),
			ReferenceID: drawer.ID.String(),
			CreatedAt:   drawer.UpdatedAt,
		})
	})
	if err != nil {
		return nil, err
	}
	return drawer, nil
}

// ---------- Shift Lifecycle ----------

type OpenShiftInput struct {
	DrawerID     *uuid.UUID `json:"drawer_id,omitempty"`
	OpeningFloat int64      `json:"opening_float"`
	Notes        *string    `json:"notes,omitempty"`
}

type OpenShiftResult struct {
	Shift      *domain.Shift `json:"shift"`
	OpenDrawer bool          `json:"open_drawer"`
}

func (s *ShiftService) OpenShift(ctx context.Context, actor *AuthenticatedStaff, in OpenShiftInput) (*OpenShiftResult, error) {
	if !actor.Can(rbac.ShiftOpen) {
		return nil, ErrForbidden
	}
	if in.OpeningFloat < 0 {
		return nil, &ValidationError{Fields: map[string]string{"opening_float": "modal awal kas tidak boleh negatif"}}
	}

	var result *OpenShiftResult
	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		drawerID := uuid.Nil
		if in.DrawerID != nil && *in.DrawerID != uuid.Nil {
			drawerID = *in.DrawerID
			if _, err := s.repo.GetCashDrawer(ctx, tx, actor.Staff.Store.ID, drawerID); err != nil {
				return err
			}
		} else {
			defaultDrawer, err := s.repo.GetDefaultCashDrawer(ctx, tx, actor.Staff.Store.ID)
			if err != nil {
				if errors.Is(err, postgres.ErrDrawerNotFound) {
					// Auto-create default drawer jika toko belum memiliki laci kas
					now := s.nowFunc().UTC()
					defaultDrawer = &domain.CashDrawer{
						ID:        uuid.New(),
						StoreID:   actor.Staff.Store.ID,
						Name:      "Kasir Utama",
						IsActive:  true,
						CreatedAt: now,
						UpdatedAt: now,
					}
					if err := s.repo.InsertCashDrawer(ctx, tx, defaultDrawer); err != nil {
						return err
					}
				} else {
					return err
				}
			}
			drawerID = defaultDrawer.ID
		}

		// Validasi apakah laci sudah memiliki open shift aktif
		activeShift, err := s.repo.GetOpenShiftByDrawer(ctx, tx, actor.Staff.Store.ID, drawerID)
		if err == nil && activeShift != nil {
			return &ConflictError{Field: "drawer_id", Message: "laci kas masih memiliki sesi shift yang sedang aktif"}
		} else if err != nil && !errors.Is(err, postgres.ErrNoOpenShift) {
			return err
		}

		now := s.nowFunc().UTC()
		shift := &domain.Shift{
			ID:           uuid.New(),
			StoreID:      actor.Staff.Store.ID,
			DrawerID:     drawerID,
			OpenedBy:     actor.Staff.UserID,
			OpeningFloat: in.OpeningFloat,
			OpenedAt:     now,
			Status:       domain.ShiftStatusOpen,
			Notes:        in.Notes,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := s.repo.InsertShift(ctx, tx, shift); err != nil {
			if _, ok := postgres.UniqueViolation(err); ok {
				return &ConflictError{Field: "drawer_id", Message: "laci kas masih memiliki sesi shift yang sedang aktif"}
			}
			return err
		}

		// Jika modal awal > 0, catat mutasi opening_float
		if in.OpeningFloat > 0 {
			openingMovement := &domain.CashMovement{
				ID:           uuid.New(),
				StoreID:      actor.Staff.Store.ID,
				ShiftID:      shift.ID,
				MovementType: domain.CashMovementOpeningFloat,
				Amount:       in.OpeningFloat,
				ActorID:      actor.Staff.UserID,
				CreatedAt:    now,
			}
			if err := s.repo.InsertCashMovement(ctx, tx, openingMovement); err != nil {
				return err
			}
		}

		actorID := actor.Staff.UserID
		auditPayload, _ := json.Marshal(map[string]any{
			"shift_id":      shift.ID,
			"drawer_id":     drawerID,
			"opening_float": in.OpeningFloat,
		})
		if err := postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{
			StoreID:     actor.Staff.Store.ID,
			ActorID:     &actorID,
			ActorName:   actor.Staff.FullName,
			Action:      "SHIFT_OPEN",
			Reason:      fmt.Sprintf("Sesi shift dibuka dengan modal awal Rp%d", in.OpeningFloat),
			ReferenceID: shift.ID.String(),
			Metadata:    string(auditPayload),
			CreatedAt:   now,
		}); err != nil {
			return err
		}

		result = &OpenShiftResult{
			Shift:      shift,
			OpenDrawer: true,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *ShiftService) GetCurrentShift(ctx context.Context, actor *AuthenticatedStaff, drawerID *uuid.UUID) (*domain.Shift, error) {
	if actor == nil {
		return nil, ErrInvalidSession
	}
	var shift *domain.Shift
	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		targetDrawerID := uuid.Nil
		if drawerID != nil && *drawerID != uuid.Nil {
			targetDrawerID = *drawerID
		} else {
			defaultDrawer, err := s.repo.GetDefaultCashDrawer(ctx, tx, actor.Staff.Store.ID)
			if err != nil {
				return err
			}
			targetDrawerID = defaultDrawer.ID
		}

		var err error
		shift, err = s.repo.GetOpenShiftByDrawer(ctx, tx, actor.Staff.Store.ID, targetDrawerID)
		return err
	})
	if err != nil {
		if errors.Is(err, postgres.ErrNoOpenShift) || errors.Is(err, postgres.ErrDrawerNotFound) {
			return nil, ErrShiftConflict
		}
		return nil, err
	}
	return shift, nil
}

// ---------- Cash Movements ----------

type CreateCashMovementInput struct {
	ShiftID      *uuid.UUID              `json:"shift_id,omitempty"`
	DrawerID     *uuid.UUID              `json:"drawer_id,omitempty"`
	MovementType domain.CashMovementType `json:"movement_type"`
	Amount       int64                   `json:"amount"`
	Reason       string                  `json:"reason"`
}

type CreateCashMovementResult struct {
	Movement   *domain.CashMovement `json:"movement"`
	OpenDrawer bool                 `json:"open_drawer"`
}

func (s *ShiftService) CreateCashMovement(ctx context.Context, actor *AuthenticatedStaff, in CreateCashMovementInput) (*CreateCashMovementResult, error) {
	if !actor.Can(rbac.CashMovementCreate) {
		return nil, ErrForbidden
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return nil, &ValidationError{Fields: map[string]string{"reason": "alasan mutasi kas wajib diisi"}}
	}
	if in.Amount < 0 {
		return nil, &ValidationError{Fields: map[string]string{"amount": "nominal mutasi kas tidak boleh negatif"}}
	}

	switch in.MovementType {
	case domain.CashMovementPaidOut, domain.CashMovementCashIn, domain.CashMovementTip, domain.CashMovementStaffMeal:
		if in.Amount <= 0 {
			return nil, &ValidationError{Fields: map[string]string{"amount": "nominal mutasi harus lebih besar dari nol"}}
		}
	case domain.CashMovementNoSale:
		in.Amount = 0
	default:
		return nil, &ValidationError{Fields: map[string]string{"movement_type": "tipe mutasi manual tidak valid"}}
	}

	var result *CreateCashMovementResult
	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		var shift *domain.Shift
		var err error

		if in.ShiftID != nil && *in.ShiftID != uuid.Nil {
			shift, err = s.repo.GetShiftForUpdate(ctx, tx, actor.Staff.Store.ID, *in.ShiftID)
		} else {
			targetDrawerID := uuid.Nil
			if in.DrawerID != nil && *in.DrawerID != uuid.Nil {
				targetDrawerID = *in.DrawerID
			} else {
				defaultDrawer, err := s.repo.GetDefaultCashDrawer(ctx, tx, actor.Staff.Store.ID)
				if err != nil {
					return err
				}
				targetDrawerID = defaultDrawer.ID
			}
			shift, err = s.repo.GetOpenShiftByDrawer(ctx, tx, actor.Staff.Store.ID, targetDrawerID)
		}
		if err != nil {
			if errors.Is(err, postgres.ErrShiftNotFound) || errors.Is(err, postgres.ErrNoOpenShift) {
				return ErrShiftConflict
			}
			return err
		}
		if shift.Status != domain.ShiftStatusOpen {
			return ErrShiftConflict
		}

		// Validasi kas fisik di laci tidak boleh minus untuk mutasi keluar
		summary, err := s.repo.AggregateCashTotalsByShift(ctx, tx, actor.Staff.Store.ID, shift.ID)
		if err != nil {
			return err
		}
		expectedCash := summary.ExpectedCash(shift.OpeningFloat)

		if (in.MovementType == domain.CashMovementPaidOut || in.MovementType == domain.CashMovementStaffMeal) && in.Amount > expectedCash {
			return ErrInsufficientDrawerCash
		}

		now := s.nowFunc().UTC()
		m := &domain.CashMovement{
			ID:           uuid.New(),
			StoreID:      actor.Staff.Store.ID,
			ShiftID:      shift.ID,
			MovementType: in.MovementType,
			Amount:       in.Amount,
			ActorID:      actor.Staff.UserID,
			Reason:       &reason,
			CreatedAt:    now,
		}
		if err := s.repo.InsertCashMovement(ctx, tx, m); err != nil {
			return err
		}

		actorID := actor.Staff.UserID
		auditPayload, _ := json.Marshal(map[string]any{
			"shift_id":      shift.ID,
			"movement_id":   m.ID,
			"movement_type": in.MovementType,
			"amount":        in.Amount,
			"reason":        reason,
		})
		if err := postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{
			StoreID:     actor.Staff.Store.ID,
			ActorID:     &actorID,
			ActorName:   actor.Staff.FullName,
			Action:      "CASH_MOVEMENT",
			Reason:      fmt.Sprintf("Mutasi kas %s sebesar Rp%d: %s", in.MovementType, in.Amount, reason),
			ReferenceID: m.ID.String(),
			Metadata:    string(auditPayload),
			CreatedAt:   now,
		}); err != nil {
			return err
		}

		result = &CreateCashMovementResult{
			Movement:   m,
			OpenDrawer: true,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ---------- Shift Closing & Reconciliation ----------

type CloseShiftInput struct {
	CountedCash    int64   `json:"counted_cash"`
	VarianceReason *string `json:"variance_reason,omitempty"`
	OwnerPIN       *string `json:"owner_pin,omitempty"`
	Notes          *string `json:"notes,omitempty"`
}

type CloseShiftResult struct {
	Report     *domain.ZReport `json:"report"`
	OpenDrawer bool            `json:"open_drawer"`
}

func (s *ShiftService) CloseShift(ctx context.Context, actor *AuthenticatedStaff, shiftID uuid.UUID, in CloseShiftInput) (*CloseShiftResult, error) {
	if !actor.Can(rbac.ShiftClose) {
		return nil, ErrForbidden
	}
	if in.CountedCash < 0 {
		return nil, &ValidationError{Fields: map[string]string{"counted_cash": "hitungan fisik kas tidak boleh negatif"}}
	}

	var result *CloseShiftResult
	err := s.pins.withPINTx(ctx, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		shift, err := s.repo.GetShiftForUpdate(ctx, tx, actor.Staff.Store.ID, shiftID)
		if err != nil {
			if errors.Is(err, postgres.ErrShiftNotFound) {
				return ErrShiftConflict
			}
			return err
		}
		if shift.Status != domain.ShiftStatusOpen {
			return ErrShiftConflict
		}

		summary, err := s.repo.AggregateCashTotalsByShift(ctx, tx, actor.Staff.Store.ID, shift.ID)
		if err != nil {
			return err
		}
		expectedCash := summary.ExpectedCash(shift.OpeningFloat)
		variance := in.CountedCash - expectedCash

		// Ambil store settings untuk toleransi selisih kas
		settings, err := s.settings.GetStoreSettings(ctx, tx, actor.Staff.Store.ID)
		if err != nil {
			return err
		}
		tolerance := settings.CashVarianceTolerance
		if tolerance <= 0 {
			tolerance = 5000 // default Rp 5.000
		}

		absVariance := variance
		if absVariance < 0 {
			absVariance = -absVariance
		}

		var ownerOverrideID *uuid.UUID
		if absVariance > tolerance {
			pinProvided := in.OwnerPIN != nil && strings.TrimSpace(*in.OwnerPIN) != ""
			reasonProvided := in.VarianceReason != nil && strings.TrimSpace(*in.VarianceReason) != ""

			if !pinProvided || !reasonProvided {
				return &ValidationError{
					Fields: map[string]string{
						"variance": fmt.Sprintf("Selisih kas (Rp%d) melebihi batas toleransi toko (Rp%d). Wajib menyertakan PIN Owner dan alasan selisih.", variance, tolerance),
					},
				}
			}

			// Verifikasi PIN Owner terhadap akun aktif pemilik toko
			verifiedOwnerID, err := s.pins.verifyOwner(ctx, tx, actor, strings.TrimSpace(*in.OwnerPIN))
			if err != nil {
				return err
			}
			ownerOverrideID = &verifiedOwnerID
		}

		now := s.nowFunc().UTC()
		closedByID := actor.Staff.UserID
		shift.Status = domain.ShiftStatusClosed
		shift.ClosedBy = &closedByID
		shift.ClosedAt = &now
		shift.ExpectedCash = &expectedCash
		shift.CountedCash = &in.CountedCash
		shift.Variance = &variance
		shift.VarianceReason = in.VarianceReason
		shift.OwnerOverrideBy = ownerOverrideID
		if in.Notes != nil {
			shift.Notes = in.Notes
		}
		shift.UpdatedAt = now

		if err := s.repo.UpdateShift(ctx, tx, shift); err != nil {
			return err
		}

		auditPayload, _ := json.Marshal(map[string]any{
			"shift_id":          shift.ID,
			"expected_cash":     expectedCash,
			"counted_cash":      in.CountedCash,
			"variance":          variance,
			"owner_override_by": ownerOverrideID,
		})
		if err := postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{
			StoreID:     actor.Staff.Store.ID,
			ActorID:     &closedByID,
			ActorName:   actor.Staff.FullName,
			Action:      "SHIFT_CLOSE",
			Reason:      fmt.Sprintf("Shift ditutup: Expected Rp%d, Counted Rp%d, Variance Rp%d", expectedCash, in.CountedCash, variance),
			ReferenceID: shift.ID.String(),
			Metadata:    string(auditPayload),
			CreatedAt:   now,
		}); err != nil {
			return err
		}

		// Buat Z Report
		drawer, _ := s.repo.GetCashDrawer(ctx, tx, actor.Staff.Store.ID, shift.DrawerID)
		drawerName := "Laci Kas"
		if drawer != nil {
			drawerName = drawer.Name
		}
		openedByName, _ := s.repo.GetUserFullName(ctx, tx, actor.Staff.Store.ID, shift.OpenedBy)
		closedByName := actor.Staff.FullName
		var overrideByName *string
		if ownerOverrideID != nil {
			name, _ := s.repo.GetUserFullName(ctx, tx, actor.Staff.Store.ID, *ownerOverrideID)
			overrideByName = &name
		}

		unsettledCount, unsettledTotal, unsettledList, _ := s.populateUnsettledOpenTabs(ctx, tx, actor.Staff.Store.ID)

		report := &domain.ZReport{
			ShiftID:                      shift.ID,
			StoreID:                      shift.StoreID,
			DrawerID:                     shift.DrawerID,
			DrawerName:                   drawerName,
			OpenedBy:                     shift.OpenedBy,
			OpenedByName:                 openedByName,
			OpenedAt:                     shift.OpenedAt,
			ClosedBy:                     shift.ClosedBy,
			ClosedByName:                 &closedByName,
			ClosedAt:                     shift.ClosedAt,
			Status:                       shift.Status,
			Summary:                      *summary,
			ExpectedCash:                 expectedCash,
			CountedCash:                  in.CountedCash,
			Variance:                     variance,
			VarianceReason:               shift.VarianceReason,
			OwnerOverrideBy:              shift.OwnerOverrideBy,
			OverrideByName:               overrideByName,
			Notes:                        shift.Notes,
			UnsettledOpenTabsCount:       unsettledCount,
			UnsettledOpenTabsTotalAmount: unsettledTotal,
			UnsettledOpenTabs:            unsettledList,
		}

		result = &CloseShiftResult{
			Report:     report,
			OpenDrawer: true,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

type ForceCloseShiftInput struct {
	Reason      string  `json:"reason"`
	CountedCash *int64  `json:"counted_cash,omitempty"`
	Notes       *string `json:"notes,omitempty"`
}

func (s *ShiftService) ForceCloseShift(ctx context.Context, actor *AuthenticatedStaff, shiftID uuid.UUID, in ForceCloseShiftInput) (*domain.ZReport, error) {
	if !actor.Can(rbac.ShiftForceClose) {
		return nil, ErrForbidden
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return nil, &ValidationError{Fields: map[string]string{"reason": "alasan force-close shift wajib diisi"}}
	}

	var report *domain.ZReport
	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		shift, err := s.repo.GetShiftForUpdate(ctx, tx, actor.Staff.Store.ID, shiftID)
		if err != nil {
			if errors.Is(err, postgres.ErrShiftNotFound) {
				return ErrShiftConflict
			}
			return err
		}
		if shift.Status != domain.ShiftStatusOpen {
			return ErrShiftConflict
		}

		summary, err := s.repo.AggregateCashTotalsByShift(ctx, tx, actor.Staff.Store.ID, shift.ID)
		if err != nil {
			return err
		}
		expectedCash := summary.ExpectedCash(shift.OpeningFloat)

		counted := expectedCash
		if in.CountedCash != nil && *in.CountedCash >= 0 {
			counted = *in.CountedCash
		}
		variance := counted - expectedCash

		now := s.nowFunc().UTC()
		closedByID := actor.Staff.UserID
		shift.Status = domain.ShiftStatusForceClosed
		shift.ClosedBy = &closedByID
		shift.ClosedAt = &now
		shift.ExpectedCash = &expectedCash
		shift.CountedCash = &counted
		shift.Variance = &variance
		shift.VarianceReason = &reason
		shift.OwnerOverrideBy = &closedByID
		if in.Notes != nil {
			shift.Notes = in.Notes
		}
		shift.UpdatedAt = now

		if err := s.repo.UpdateShift(ctx, tx, shift); err != nil {
			return err
		}

		auditPayload, _ := json.Marshal(map[string]any{
			"shift_id":      shift.ID,
			"force_reason":  reason,
			"expected_cash": expectedCash,
			"counted_cash":  counted,
			"variance":      variance,
		})
		if err := postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{
			StoreID:     actor.Staff.Store.ID,
			ActorID:     &closedByID,
			ActorName:   actor.Staff.FullName,
			Action:      "SHIFT_FORCE_CLOSE",
			Reason:      fmt.Sprintf("Force close oleh owner: %s", reason),
			ReferenceID: shift.ID.String(),
			Metadata:    string(auditPayload),
			CreatedAt:   now,
		}); err != nil {
			return err
		}

		drawer, _ := s.repo.GetCashDrawer(ctx, tx, actor.Staff.Store.ID, shift.DrawerID)
		drawerName := "Laci Kas"
		if drawer != nil {
			drawerName = drawer.Name
		}
		openedByName, _ := s.repo.GetUserFullName(ctx, tx, actor.Staff.Store.ID, shift.OpenedBy)
		closedByName := actor.Staff.FullName

		unsettledCount, unsettledTotal, unsettledList, _ := s.populateUnsettledOpenTabs(ctx, tx, actor.Staff.Store.ID)

		report = &domain.ZReport{
			ShiftID:                      shift.ID,
			StoreID:                      shift.StoreID,
			DrawerID:                     shift.DrawerID,
			DrawerName:                   drawerName,
			OpenedBy:                     shift.OpenedBy,
			OpenedByName:                 openedByName,
			OpenedAt:                     shift.OpenedAt,
			ClosedBy:                     shift.ClosedBy,
			ClosedByName:                 &closedByName,
			ClosedAt:                     shift.ClosedAt,
			Status:                       shift.Status,
			Summary:                      *summary,
			ExpectedCash:                 expectedCash,
			CountedCash:                  counted,
			Variance:                     variance,
			VarianceReason:               shift.VarianceReason,
			OwnerOverrideBy:              shift.OwnerOverrideBy,
			OverrideByName:               &closedByName,
			Notes:                        shift.Notes,
			UnsettledOpenTabsCount:       unsettledCount,
			UnsettledOpenTabsTotalAmount: unsettledTotal,
			UnsettledOpenTabs:            unsettledList,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return report, nil
}

// ---------- Reports (X & Z) ----------

func (s *ShiftService) GetXReport(ctx context.Context, actor *AuthenticatedStaff, shiftID uuid.UUID) (*domain.XReport, error) {
	if actor == nil {
		return nil, ErrInvalidSession
	}
	var report *domain.XReport
	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		shift, err := s.repo.GetShiftByID(ctx, tx, actor.Staff.Store.ID, shiftID)
		if err != nil {
			return err
		}

		summary, err := s.repo.AggregateCashTotalsByShift(ctx, tx, actor.Staff.Store.ID, shift.ID)
		if err != nil {
			return err
		}
		expectedCash := summary.ExpectedCash(shift.OpeningFloat)

		drawer, _ := s.repo.GetCashDrawer(ctx, tx, actor.Staff.Store.ID, shift.DrawerID)
		drawerName := "Laci Kas"
		if drawer != nil {
			drawerName = drawer.Name
		}
		openedByName, _ := s.repo.GetUserFullName(ctx, tx, actor.Staff.Store.ID, shift.OpenedBy)

		unsettledCount, unsettledTotal, unsettledList, _ := s.populateUnsettledOpenTabs(ctx, tx, actor.Staff.Store.ID)

		report = &domain.XReport{
			ShiftID:                      shift.ID,
			StoreID:                      shift.StoreID,
			DrawerID:                     shift.DrawerID,
			DrawerName:                   drawerName,
			OpenedBy:                     shift.OpenedBy,
			OpenedByName:                 openedByName,
			OpenedAt:                     shift.OpenedAt,
			GeneratedAt:                  s.nowFunc().UTC(),
			Status:                       shift.Status,
			Summary:                      *summary,
			UnsettledOpenTabsCount:       unsettledCount,
			UnsettledOpenTabsTotalAmount: unsettledTotal,
			UnsettledOpenTabs:            unsettledList,
		}

		// Blind count security: jika kasir mengakses X report saat shift masih 'open', redact expected_cash
		isOwner := actor.Staff.Role == domain.RoleOwner
		if shift.Status == domain.ShiftStatusOpen && !isOwner {
			report.ExpectedCash = nil
			report.ExpectedRedacted = true
		} else {
			report.ExpectedCash = &expectedCash
			report.ExpectedRedacted = false
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return report, nil
}

func (s *ShiftService) GetZReport(ctx context.Context, actor *AuthenticatedStaff, shiftID uuid.UUID) (*domain.ZReport, error) {
	if actor == nil {
		return nil, ErrInvalidSession
	}
	var report *domain.ZReport
	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		shift, err := s.repo.GetShiftByID(ctx, tx, actor.Staff.Store.ID, shiftID)
		if err != nil {
			return err
		}
		if shift.Status == domain.ShiftStatusOpen {
			return &ConflictError{Field: "status", Message: "Z Report hanya tersedia untuk shift yang telah ditutup"}
		}

		summary, err := s.repo.AggregateCashTotalsByShift(ctx, tx, actor.Staff.Store.ID, shift.ID)
		if err != nil {
			return err
		}

		drawer, _ := s.repo.GetCashDrawer(ctx, tx, actor.Staff.Store.ID, shift.DrawerID)
		drawerName := "Laci Kas"
		if drawer != nil {
			drawerName = drawer.Name
		}
		openedByName, _ := s.repo.GetUserFullName(ctx, tx, actor.Staff.Store.ID, shift.OpenedBy)
		var closedByName *string
		if shift.ClosedBy != nil {
			name, _ := s.repo.GetUserFullName(ctx, tx, actor.Staff.Store.ID, *shift.ClosedBy)
			closedByName = &name
		}
		var overrideByName *string
		if shift.OwnerOverrideBy != nil {
			name, _ := s.repo.GetUserFullName(ctx, tx, actor.Staff.Store.ID, *shift.OwnerOverrideBy)
			overrideByName = &name
		}

		expCash := int64(0)
		if shift.ExpectedCash != nil {
			expCash = *shift.ExpectedCash
		}
		cntCash := int64(0)
		if shift.CountedCash != nil {
			cntCash = *shift.CountedCash
		}
		varCash := int64(0)
		if shift.Variance != nil {
			varCash = *shift.Variance
		}

		unsettledCount, unsettledTotal, unsettledList, _ := s.populateUnsettledOpenTabs(ctx, tx, actor.Staff.Store.ID)

		report = &domain.ZReport{
			ShiftID:                      shift.ID,
			StoreID:                      shift.StoreID,
			DrawerID:                     shift.DrawerID,
			DrawerName:                   drawerName,
			OpenedBy:                     shift.OpenedBy,
			OpenedByName:                 openedByName,
			OpenedAt:                     shift.OpenedAt,
			ClosedBy:                     shift.ClosedBy,
			ClosedByName:                 closedByName,
			ClosedAt:                     shift.ClosedAt,
			Status:                       shift.Status,
			Summary:                      *summary,
			ExpectedCash:                 expCash,
			CountedCash:                  cntCash,
			Variance:                     varCash,
			VarianceReason:               shift.VarianceReason,
			OwnerOverrideBy:              shift.OwnerOverrideBy,
			OverrideByName:               overrideByName,
			Notes:                        shift.Notes,
			UnsettledOpenTabsCount:       unsettledCount,
			UnsettledOpenTabsTotalAmount: unsettledTotal,
			UnsettledOpenTabs:            unsettledList,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return report, nil
}

func (s *ShiftService) populateUnsettledOpenTabs(
	ctx context.Context,
	tx *sqlx.Tx,
	storeID uuid.UUID,
) (int, int64, []domain.UnsettledTabSummary, error) {
	unsettled, err := s.repo.GetUnsettledOpenTabs(ctx, tx, storeID, s.nowFunc().UTC())
	if err != nil {
		return 0, 0, nil, err
	}
	var total int64
	for _, t := range unsettled {
		total += t.TotalAmount
	}
	return len(unsettled), total, unsettled, nil
}
