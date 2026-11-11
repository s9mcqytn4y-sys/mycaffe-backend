package domain

import (
	"time"

	"github.com/google/uuid"
)

type Store struct {
	ID                    uuid.UUID   `json:"id" db:"id"`
	Slug                  string      `json:"slug" db:"slug"`
	Name                  string      `json:"name" db:"name"`
	Address               string      `json:"address" db:"address"`
	Phone                 string      `json:"phone" db:"phone"`
	Timezone              string      `json:"timezone" db:"timezone"`
	IsActive              bool        `json:"is_active" db:"is_active"`
	DeletedAt             *time.Time  `json:"deleted_at,omitempty" db:"deleted_at"`
	OrderPrefix           string      `json:"order_prefix" db:"order_prefix"`
	QueuePrefix           string      `json:"queue_prefix" db:"queue_prefix"`
	DefaultBillingMode    BillingMode `json:"default_billing_mode" db:"default_billing_mode"`
	ShiftMode             string      `json:"shift_mode" db:"shift_mode"`
	EnabledPaymentMethods string      `json:"enabled_payment_methods" db:"enabled_payment_methods"`
	TaxBps                int64       `json:"tax_bps" db:"tax_bps"`
	ServiceBps            int64       `json:"service_bps" db:"service_bps"`
	QrisMdrBps            int64       `json:"qris_mdr_bps" db:"qris_mdr_bps"`
	Currency              string      `json:"currency" db:"currency"`
	CreatedAt             time.Time   `json:"created_at" db:"created_at"`
	UpdatedAt             time.Time   `json:"updated_at" db:"updated_at"`
}

type UserRole string

const (
	RoleOwner   UserRole = "owner"
	RoleCashier UserRole = "cashier"
	RoleBarista UserRole = "barista"
	RoleKitchen UserRole = "kitchen"
)

func ValidUserRoles() []UserRole {
	return []UserRole{RoleOwner, RoleCashier, RoleBarista, RoleKitchen}
}

type User struct {
	ID        uuid.UUID `json:"id" db:"id"`
	StoreID   uuid.UUID `json:"store_id" db:"store_id"`
	Email     string    `json:"email" db:"email"`
	FullName  string    `json:"full_name" db:"full_name"`
	Role      UserRole  `json:"role" db:"role"`
	PinHash   string    `json:"-" db:"pin_hash"`
	IsActive  bool      `json:"is_active" db:"is_active"`
	CreatedAt time.Time `json:"created_at" db:"created_at"`
	UpdatedAt time.Time `json:"updated_at" db:"updated_at"`

	FailedPINAttempts int        `json:"-" db:"failed_pin_attempts"`
	PINSetAt          *time.Time `json:"-" db:"pin_set_at"`
	MustChangePIN     bool       `json:"-" db:"must_change_pin"`
	LockedUntil       *time.Time `json:"-" db:"locked_until"`
}

// AuthSession is a persisted staff login session (auth_sessions). Only SHA-256 hashes of
// refresh secrets are stored; the raw secret exists only in the client cookie.
type AuthSession struct {
	ID                       uuid.UUID  `db:"id"`
	StoreID                  uuid.UUID  `db:"store_id"`
	UserID                   uuid.UUID  `db:"user_id"`
	RefreshTokenHash         string     `db:"refresh_token_hash"`
	PreviousRefreshTokenHash string     `db:"previous_refresh_token_hash"`
	CreatedAt                time.Time  `db:"created_at"`
	LastUsedAt               time.Time  `db:"last_used_at"`
	ExpiresAt                time.Time  `db:"expires_at"`
	RevokedAt                *time.Time `db:"revoked_at"`
	RevokedReason            *string    `db:"revoked_reason"`
	UserAgent                string     `db:"user_agent"`
	ClientIP                 string     `db:"client_ip"`
}

type Category struct {
	ID           uuid.UUID `json:"id" db:"id"`
	StoreID      uuid.UUID `json:"store_id" db:"store_id"`
	Name         string    `json:"name" db:"name"`
	DisplayOrder int       `json:"display_order" db:"display_order"`
	IsActive     bool      `json:"is_active" db:"is_active"`
	CreatedAt    time.Time `json:"created_at" db:"created_at"`
	UpdatedAt    time.Time `json:"updated_at" db:"updated_at"`
}

type Product struct {
	ID            uuid.UUID  `json:"id" db:"id"`
	StoreID       uuid.UUID  `json:"store_id" db:"store_id"`
	CategoryID    *uuid.UUID `json:"category_id,omitempty" db:"category_id"`
	Name          string     `json:"name" db:"name"`
	Description   string     `json:"description" db:"description"`
	BasePrice     int64      `json:"base_price" db:"base_price"`
	ImageURL      *string    `json:"image_url,omitempty" db:"image_url"`
	IsAvailable   bool       `json:"is_available" db:"is_available"`
	TrackStock    bool       `json:"track_stock" db:"track_stock"`
	StockQuantity int        `json:"stock_quantity" db:"stock_quantity"`
	ProductType   string     `json:"product_type" db:"product_type"`
	PrepStation   string     `json:"prep_station" db:"prep_station"`
	DisplayOrder  int        `json:"display_order" db:"display_order"`
	ArchivedAt    *time.Time `json:"archived_at,omitempty" db:"archived_at"`
	CreatedAt     time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at" db:"updated_at"`
}

type ProductModifierGroup struct {
	StoreID          uuid.UUID `json:"store_id" db:"store_id"`
	ProductID        uuid.UUID `json:"product_id" db:"product_id"`
	GroupID          uuid.UUID `json:"group_id" db:"group_id"`
	DisplayOrder     int       `json:"display_order" db:"display_order"`
	OverrideRequired *bool     `json:"override_required,omitempty" db:"override_required"`
	CreatedAt        time.Time `json:"created_at" db:"created_at"`
}

type OptionModifierGroup struct {
	StoreID        uuid.UUID `json:"store_id" db:"store_id"`
	ParentOptionID uuid.UUID `json:"parent_option_id" db:"parent_option_id"`
	ChildGroupID   uuid.UUID `json:"child_group_id" db:"child_group_id"`
	DisplayOrder   int       `json:"display_order" db:"display_order"`
	CreatedAt      time.Time `json:"created_at" db:"created_at"`
}

type StoreDailySequence struct {
	StoreID      uuid.UUID `json:"store_id" db:"store_id"`
	BusinessDate time.Time `json:"business_date" db:"business_date"`
	LastSeq      int       `json:"last_seq" db:"last_seq"`
	UpdatedAt    time.Time `json:"updated_at" db:"updated_at"`
}

type OutboxEvent struct {
	ID            uuid.UUID  `json:"id" db:"id"`
	StoreID       uuid.UUID  `json:"store_id" db:"store_id"`
	EventType     string     `json:"event_type" db:"event_type"`
	AggregateType string     `json:"aggregate_type" db:"aggregate_type"`
	AggregateID   string     `json:"aggregate_id" db:"aggregate_id"`
	Payload       string     `json:"payload" db:"payload"`
	CreatedAt     time.Time  `json:"created_at" db:"created_at"`
	ProcessedAt   *time.Time `json:"processed_at,omitempty" db:"processed_at"`
	RetryCount    int        `json:"retry_count" db:"retry_count"`
	LastError     *string    `json:"last_error,omitempty" db:"last_error"`
}

type AuditLog struct {
	ID          uuid.UUID  `json:"id" db:"id"`
	StoreID     uuid.UUID  `json:"store_id" db:"store_id"`
	ActorID     *uuid.UUID `json:"actor_id,omitempty" db:"actor_id"`
	ActorName   string     `json:"actor_name" db:"actor_name"`
	Action      string     `json:"action" db:"action"`
	Reason      string     `json:"reason" db:"reason"`
	ReferenceID string     `json:"reference_id,omitempty" db:"reference_id"`
	Metadata    string     `json:"metadata" db:"metadata"`
	CreatedAt   time.Time  `json:"created_at" db:"created_at"`
}

// EmailOutbox is a queued transactional email (email_outbox), delivered by the email worker.
type EmailOutbox struct {
	ID            uuid.UUID `db:"id"`
	StoreID       uuid.UUID `db:"store_id"`
	Template      string    `db:"template"`
	ToEmail       string    `db:"to_email"`
	ToName        string    `db:"to_name"`
	Subject       string    `db:"subject"`
	HTMLBody      string    `db:"html_body"`
	TextBody      string    `db:"text_body"`
	ReplyTo       string    `db:"reply_to"`
	ReferenceID   string    `db:"reference_id"`
	Status        string    `db:"status"`
	Attempts      int       `db:"attempts"`
	NextAttemptAt time.Time `db:"next_attempt_at"`
	LastError     string    `db:"last_error"`
	CreatedAt     time.Time `db:"created_at"`
}

// Account token purposes (account_tokens.purpose).
const (
	AccountTokenActivation = "activation"
	AccountTokenPINReset   = "pin_reset"
)

// AccountToken is a single-use activation or PIN-reset link. Only the SHA-256 of the secret is stored.
type AccountToken struct {
	ID        uuid.UUID  `db:"id"`
	StoreID   uuid.UUID  `db:"store_id"`
	UserID    uuid.UUID  `db:"user_id"`
	Purpose   string     `db:"purpose"`
	TokenHash string     `db:"token_hash"`
	ExpiresAt time.Time  `db:"expires_at"`
	UsedAt    *time.Time `db:"used_at"`
	CreatedBy *uuid.UUID `db:"created_by"`
	CreatedAt time.Time  `db:"created_at"`
}

// Static QRIS fallback modes (owner decision K8).
const (
	StaticQRISFallbackOff    = "off"
	StaticQRISFallbackAuto   = "auto"
	StaticQRISFallbackForced = "forced"
)

// StoreSettings are owner-managed operating policies (store_settings). Amounts are integer Rupiah.
type StoreSettings struct {
	StoreID                  uuid.UUID `json:"-" db:"store_id"`
	VoidRefundOwnerThreshold int64     `json:"void_refund_owner_threshold" db:"void_refund_owner_threshold"`
	CashierMaxDiscountBPS    int64     `json:"cashier_max_discount_bps" db:"cashier_max_discount_bps"`
	CashVarianceTolerance    int64     `json:"cash_variance_tolerance" db:"cash_variance_tolerance"`
	CashierHandlesBar        bool      `json:"cashier_handles_bar" db:"cashier_handles_bar"`
	TableIdleCloseMinutes    int       `json:"table_idle_close_minutes" db:"table_idle_close_minutes"`
	QROrderingPaused         bool      `json:"qr_ordering_paused" db:"qr_ordering_paused"`
	StaticQRISFallbackMode   string    `json:"static_qris_fallback_mode" db:"static_qris_fallback_mode"`
	// RefundWindow*Days limit refunds to payments paid at most this many business days ago (RF1-e; 0 = same day).
	RefundWindowCashierDays int `json:"refund_window_cashier_days" db:"refund_window_cashier_days"`
	RefundWindowOwnerDays   int `json:"refund_window_owner_days" db:"refund_window_owner_days"`
	// CashRoundingStep rounds a closing cash tender to 0 (off), 100 or 500 rupiah (decision V11).
	CashRoundingStep int `json:"cash_rounding_step" db:"cash_rounding_step"`
	// Outlet cash and cashier settings (decision V13).
	OutletDailyExpenseLimit int64 `json:"outlet_daily_expense_limit" db:"outlet_daily_expense_limit"`
	DefaultOpeningFloat     int64 `json:"default_opening_float" db:"default_opening_float"`
	CashierSeesExpectedCash bool  `json:"cashier_sees_expected_cash" db:"cashier_sees_expected_cash"`
	// GatewayPartialRefunds allows partial Midtrans refunds once proven in the sandbox (RF1-b).
	GatewayPartialRefunds bool `json:"gateway_partial_refunds" db:"gateway_partial_refunds"`
	// Table timer (decision W6): 0 turns it off; warnings only, orders are never refused.
	TableTimeLimitMinutes    int `json:"table_time_limit_minutes" db:"table_time_limit_minutes"`
	TableTimerWarningMinutes int `json:"table_timer_warning_minutes" db:"table_timer_warning_minutes"`
	// QRServiceMode says how a QR order reaches the guest: picked up at the bar or brought to the table.
	QRServiceMode string `json:"qr_service_mode" db:"qr_service_mode"`
}

// QR service modes (PRD v3 Q3).
const (
	QRServicePickup = "pickup"
	QRServiceTable  = "table"
)

// DefaultStoreSettings mirrors the column defaults of migration 000007 and later additions.
func DefaultStoreSettings(storeID uuid.UUID) StoreSettings {
	return StoreSettings{
		StoreID: storeID, VoidRefundOwnerThreshold: 100000, CashierMaxDiscountBPS: 1000, CashVarianceTolerance: 5000,
		TableIdleCloseMinutes: 30, StaticQRISFallbackMode: StaticQRISFallbackAuto,
		RefundWindowCashierDays: 0, RefundWindowOwnerDays: 7, TableTimerWarningMinutes: 10,
		QRServiceMode: QRServicePickup,
	}
}

// Product types and preparation stations (migration 000008, owner decision K2).
const (
	ProductTypePrepared     = "prepared"
	ProductTypeReadyToServe = "ready_to_serve"
	// ProductTypeBundle is a fixed-price bundle of components (X4, PR 11.2); it is never stocked or prepared itself.
	ProductTypeBundle = "bundle"

	PrepStationBar     = "bar"
	PrepStationKitchen = "kitchen"
	PrepStationNone    = "none"
)

// ProductVariant is a sellable form of a product with its own SKU and price (owner decision K1),
// e.g. Caffe Latte "Reguler" / "Large". Every active product has exactly one default variant.
type ProductVariant struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	StoreID      uuid.UUID  `json:"-" db:"store_id"`
	ProductID    uuid.UUID  `json:"product_id" db:"product_id"`
	Name         string     `json:"name" db:"name"`
	SKU          string     `json:"sku" db:"sku"`
	Price        int64      `json:"price" db:"price"`
	IsDefault    bool       `json:"is_default" db:"is_default"`
	IsAvailable  bool       `json:"is_available" db:"is_available"`
	DisplayOrder int        `json:"display_order" db:"display_order"`
	ArchivedAt   *time.Time `json:"archived_at,omitempty" db:"archived_at"`
	CreatedAt    time.Time  `json:"created_at" db:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at" db:"updated_at"`
}
