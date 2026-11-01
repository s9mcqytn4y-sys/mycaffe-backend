package service

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
)

var ErrInvalidSettings = errors.New("invalid store settings")

// StoreSettingsPatch is a partial update; nil fields keep their current value.
type StoreSettingsPatch struct {
	VoidRefundOwnerThreshold *int64  `json:"void_refund_owner_threshold"`
	CashierMaxDiscountBPS    *int64  `json:"cashier_max_discount_bps"`
	CashVarianceTolerance    *int64  `json:"cash_variance_tolerance"`
	CashierHandlesBar        *bool   `json:"cashier_handles_bar"`
	TableIdleCloseMinutes    *int    `json:"table_idle_close_minutes"`
	QROrderingPaused         *bool   `json:"qr_ordering_paused"`
	StaticQRISFallbackMode   *string `json:"static_qris_fallback_mode"`
}

// SettingsService lets owners change operating policies without a developer (owner decision K3).
type SettingsService struct {
	db           *sqlx.DB
	settingsRepo *postgres.SettingsRepository
}

func NewSettingsService(db *sqlx.DB) *SettingsService {
	return &SettingsService{db: db, settingsRepo: postgres.NewSettingsRepository()}
}

// Get is readable by any signed-in staff member of the store: POS screens need the thresholds.
func (s *SettingsService) Get(ctx context.Context, actor *AuthenticatedStaff) (domain.StoreSettings, error) {
	if actor == nil {
		return domain.StoreSettings{}, ErrInvalidSession
	}
	var out domain.StoreSettings
	err := database.WithTenantTx(ctx, s.db, actor.Staff.Store.ID, func(tx *sqlx.Tx) error {
		var err error
		out, err = s.settingsRepo.GetStoreSettings(ctx, tx, actor.Staff.Store.ID)
		return err
	})
	return out, err
}

// Update merges the patch, validates it, saves it, and audits old → new values.
func (s *SettingsService) Update(ctx context.Context, actor *AuthenticatedStaff, patch StoreSettingsPatch) (domain.StoreSettings, error) {
	if !actor.Can(rbac.StoreSettingsManage) {
		return domain.StoreSettings{}, ErrForbidden
	}
	storeID := actor.Staff.Store.ID
	var out domain.StoreSettings
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		before, err := s.settingsRepo.GetStoreSettings(ctx, tx, storeID)
		if err != nil {
			return err
		}
		after := before
		if patch.VoidRefundOwnerThreshold != nil {
			after.VoidRefundOwnerThreshold = *patch.VoidRefundOwnerThreshold
		}
		if patch.CashierMaxDiscountBPS != nil {
			after.CashierMaxDiscountBPS = *patch.CashierMaxDiscountBPS
		}
		if patch.CashVarianceTolerance != nil {
			after.CashVarianceTolerance = *patch.CashVarianceTolerance
		}
		if patch.CashierHandlesBar != nil {
			after.CashierHandlesBar = *patch.CashierHandlesBar
		}
		if patch.TableIdleCloseMinutes != nil {
			after.TableIdleCloseMinutes = *patch.TableIdleCloseMinutes
		}
		if patch.QROrderingPaused != nil {
			after.QROrderingPaused = *patch.QROrderingPaused
		}
		if patch.StaticQRISFallbackMode != nil {
			after.StaticQRISFallbackMode = *patch.StaticQRISFallbackMode
		}
		if err := validateStoreSettings(after); err != nil {
			return err
		}
		if err := s.settingsRepo.UpsertStoreSettings(ctx, tx, after, actor.Staff.UserID); err != nil {
			return err
		}
		meta, _ := json.Marshal(map[string]any{"before": before, "after": after})
		actorID := actor.Staff.UserID
		if err := postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{
			StoreID: storeID, ActorID: &actorID, ActorName: actor.Staff.FullName, Action: "STORE_SETTINGS_CHANGED",
			Reason: "Pengaturan operasional toko diubah owner", ReferenceID: storeID.String(), Metadata: string(meta),
		}); err != nil {
			return err
		}
		out = after
		return nil
	})
	return out, err
}

func validateStoreSettings(s domain.StoreSettings) error {
	switch {
	case s.VoidRefundOwnerThreshold < 0, s.CashVarianceTolerance < 0:
		return errors.Join(ErrInvalidSettings, errors.New("nominal tidak boleh negatif"))
	case s.CashierMaxDiscountBPS < 0 || s.CashierMaxDiscountBPS > 10000:
		return errors.Join(ErrInvalidSettings, errors.New("batas diskon kasir harus 0-10000 bps (0-100%)"))
	case s.TableIdleCloseMinutes < 5 || s.TableIdleCloseMinutes > 240:
		return errors.Join(ErrInvalidSettings, errors.New("waktu tutup meja otomatis harus 5-240 menit"))
	case s.StaticQRISFallbackMode != domain.StaticQRISFallbackOff && s.StaticQRISFallbackMode != domain.StaticQRISFallbackAuto && s.StaticQRISFallbackMode != domain.StaticQRISFallbackForced:
		return errors.Join(ErrInvalidSettings, errors.New("mode QRIS statis harus off, auto, atau forced"))
	}
	return nil
}
