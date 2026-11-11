package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
)

type SettingsRepository struct{}

func NewSettingsRepository() *SettingsRepository {
	return &SettingsRepository{}
}

const storeSettingsColumns = `store_id, void_refund_owner_threshold, cashier_max_discount_bps, cash_variance_tolerance,
	cashier_handles_bar, table_idle_close_minutes, qr_ordering_paused, static_qris_fallback_mode,
	refund_window_cashier_days, refund_window_owner_days, cash_rounding_step,
	outlet_daily_expense_limit, default_opening_float, cashier_sees_expected_cash, gateway_partial_refunds,
	table_time_limit_minutes, table_timer_warning_minutes, qr_service_mode`

// GetStoreSettings returns the store's settings, or the defaults when no row exists yet.
func (r *SettingsRepository) GetStoreSettings(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID) (domain.StoreSettings, error) {
	if tx == nil {
		return domain.StoreSettings{}, ErrNilTransaction
	}
	var s domain.StoreSettings
	err := tx.GetContext(ctx, &s, `SELECT `+storeSettingsColumns+` FROM store_settings WHERE store_id = $1`, storeID)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.DefaultStoreSettings(storeID), nil
	}
	if err != nil {
		return domain.StoreSettings{}, fmt.Errorf("failed to load store settings: %w", err)
	}
	return s, nil
}

// UpsertStoreSettings writes the full settings row (the caller merges a patch onto the current values).
func (r *SettingsRepository) UpsertStoreSettings(ctx context.Context, tx *sqlx.Tx, s domain.StoreSettings, updatedBy uuid.UUID) error {
	if tx == nil {
		return ErrNilTransaction
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO store_settings (`+storeSettingsColumns+`, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
		ON CONFLICT (store_id) DO UPDATE SET
			void_refund_owner_threshold = EXCLUDED.void_refund_owner_threshold,
			cashier_max_discount_bps = EXCLUDED.cashier_max_discount_bps,
			cash_variance_tolerance = EXCLUDED.cash_variance_tolerance,
			cashier_handles_bar = EXCLUDED.cashier_handles_bar,
			table_idle_close_minutes = EXCLUDED.table_idle_close_minutes,
			qr_ordering_paused = EXCLUDED.qr_ordering_paused,
			static_qris_fallback_mode = EXCLUDED.static_qris_fallback_mode,
			refund_window_cashier_days = EXCLUDED.refund_window_cashier_days,
			refund_window_owner_days = EXCLUDED.refund_window_owner_days,
			cash_rounding_step = EXCLUDED.cash_rounding_step,
			outlet_daily_expense_limit = EXCLUDED.outlet_daily_expense_limit,
			default_opening_float = EXCLUDED.default_opening_float,
			cashier_sees_expected_cash = EXCLUDED.cashier_sees_expected_cash,
			gateway_partial_refunds = EXCLUDED.gateway_partial_refunds,
			table_time_limit_minutes = EXCLUDED.table_time_limit_minutes,
			table_timer_warning_minutes = EXCLUDED.table_timer_warning_minutes,
			qr_service_mode = EXCLUDED.qr_service_mode,
			updated_by = EXCLUDED.updated_by
	`, s.StoreID, s.VoidRefundOwnerThreshold, s.CashierMaxDiscountBPS, s.CashVarianceTolerance,
		s.CashierHandlesBar, s.TableIdleCloseMinutes, s.QROrderingPaused, s.StaticQRISFallbackMode,
		s.RefundWindowCashierDays, s.RefundWindowOwnerDays, s.CashRoundingStep,
		s.OutletDailyExpenseLimit, s.DefaultOpeningFloat, s.CashierSeesExpectedCash, s.GatewayPartialRefunds,
		s.TableTimeLimitMinutes, s.TableTimerWarningMinutes, s.QRServiceMode, updatedBy)
	if err != nil {
		return fmt.Errorf("failed to save store settings: %w", err)
	}
	return nil
}
