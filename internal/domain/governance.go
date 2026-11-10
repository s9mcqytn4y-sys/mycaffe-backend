package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// LegalDocumentType aligns 1:1 with chk_legal_documents_type.
type LegalDocumentType string

const (
	LegalDocTermsOfService LegalDocumentType = "terms_of_service"
	LegalDocPrivacyPDP     LegalDocumentType = "privacy_policy_pdp"
	LegalDocDPATenant      LegalDocumentType = "dpa_tenant"
	LegalDocRefundPolicy   LegalDocumentType = "refund_policy"
	LegalDocGuestPrivacy   LegalDocumentType = "guest_privacy_notice"
	LegalDocGuestTerms     LegalDocumentType = "guest_order_terms"
)

// BusinessEntityType aligns 1:1 with chk_onboarding_entity_type and chk_store_kyb_entity_type.
type BusinessEntityType string

const (
	EntityPerorangan   BusinessEntityType = "perorangan"
	EntityCV           BusinessEntityType = "cv"
	EntityPT           BusinessEntityType = "pt"
	EntityPTPerorangan BusinessEntityType = "pt_perorangan"
	EntityKoperasi     BusinessEntityType = "koperasi"
)

// BillingCycle aligns 1:1 with chk_onboarding_billing_cycle and chk_store_kyb_cycle.
type BillingCycle string

const (
	BillingCycleTrial   BillingCycle = "trial"
	BillingCycleMonthly BillingCycle = "monthly"
	BillingCycleAnnual  BillingCycle = "annual"
)

// OnboardingStatus aligns 1:1 with chk_onboarding_status.
type OnboardingStatus string

const (
	OnboardingPendingEmailOTP OnboardingStatus = "pending_email_otp"
	OnboardingSubmitted       OnboardingStatus = "submitted"
	OnboardingUnderReview     OnboardingStatus = "under_review"
	OnboardingApproved        OnboardingStatus = "approved"
	OnboardingRejected        OnboardingStatus = "rejected"
)

// KYBStatus aligns 1:1 with chk_store_kyb_status and chk_registry_kyb_status.
type KYBStatus string

const (
	KYBUnverified    KYBStatus = "unverified"
	KYBPendingReview KYBStatus = "pending_review"
	KYBVerified      KYBStatus = "verified"
	KYBRejected      KYBStatus = "rejected"
	KYBSuspended     KYBStatus = "suspended"
)

// SubscriptionStatus aligns 1:1 with chk_store_kyb_sub_status and chk_registry_sub_status.
type SubscriptionStatus string

const (
	SubscriptionTrialing  SubscriptionStatus = "trialing"
	SubscriptionActive    SubscriptionStatus = "active"
	SubscriptionPastDue   SubscriptionStatus = "past_due"
	SubscriptionSuspended SubscriptionStatus = "suspended"
	SubscriptionCleared   SubscriptionStatus = "cleared"
)

// PlatformInvoiceStatus aligns 1:1 with chk_platform_invoices_status.
type PlatformInvoiceStatus string

const (
	PlatformInvoicePending   PlatformInvoiceStatus = "pending"
	PlatformInvoicePaid      PlatformInvoiceStatus = "paid"
	PlatformInvoiceExpired   PlatformInvoiceStatus = "expired"
	PlatformInvoiceCancelled PlatformInvoiceStatus = "cancelled"
	PlatformInvoiceRefunded  PlatformInvoiceStatus = "refunded"
)

// ClearanceStatus aligns 1:1 with chk_clearance_status.
type ClearanceStatus string

const (
	ClearanceCoolingOff        ClearanceStatus = "cooling_off"
	ClearanceCancelled         ClearanceStatus = "cancelled"
	ClearancePIIAnonymizedKept ClearanceStatus = "pii_anonymized_ledger_retained"
)

type LegalDocument struct {
	ID              uuid.UUID         `json:"id" db:"id"`
	DocType         LegalDocumentType `json:"doc_type" db:"doc_type"`
	Version         string            `json:"version" db:"version"`
	Locale          string            `json:"locale" db:"locale"`
	Title           string            `json:"title" db:"title"`
	ContentMarkdown string            `json:"content_markdown" db:"content_markdown"`
	ContentSHA256   string            `json:"content_sha256" db:"content_sha256"`
	IsActive        bool              `json:"is_active" db:"is_active"`
	EffectiveAt     time.Time         `json:"effective_at" db:"effective_at"`
	CreatedAt       time.Time         `json:"created_at" db:"created_at"`
}

type SubscriptionPlan struct {
	Code                     string    `json:"code" db:"code"`
	NameID                   string    `json:"name_id" db:"name_id"`
	NameEN                   string    `json:"name_en" db:"name_en"`
	MonthlyPriceIDR          int64     `json:"monthly_price_idr" db:"monthly_price_idr"`
	AnnualPriceIDR           int64     `json:"annual_price_idr" db:"annual_price_idr"`
	CommissionBPS            int       `json:"commission_bps" db:"commission_bps"`
	MaxTables                int       `json:"max_tables" db:"max_tables"`
	MaxPrinters              int       `json:"max_printers" db:"max_printers"`
	SupportsBYOKMidtrans     bool      `json:"supports_byok_midtrans" db:"supports_byok_midtrans"`
	SupportsKDSQueueTV       bool      `json:"supports_kds_queue_tv" db:"supports_kds_queue_tv"`
	SupportsWhiteLabel       bool      `json:"supports_whitelabel" db:"supports_whitelabel"`
	SupportsBYONotifications bool      `json:"supports_byo_notifications" db:"supports_byo_notifications"`
	IsActive                 bool      `json:"is_active" db:"is_active"`
	CreatedAt                time.Time `json:"created_at" db:"created_at"`
	UpdatedAt                time.Time `json:"updated_at" db:"updated_at"`
}

type TenantOnboardingApplication struct {
	ID                 uuid.UUID          `json:"id" db:"id"`
	IdempotencyKey     string             `json:"idempotency_key" db:"idempotency_key"`
	RequestedSlug      string             `json:"requested_slug" db:"requested_slug"`
	BusinessName       string             `json:"business_name" db:"business_name"`
	BusinessEntityType BusinessEntityType `json:"business_entity_type" db:"business_entity_type"`
	OwnerName          string             `json:"owner_name" db:"owner_name"`
	OwnerEmail         string             `json:"owner_email" db:"owner_email"`
	OwnerPhone         string             `json:"owner_phone" db:"owner_phone"`
	CityRegion         string             `json:"city_region" db:"city_region"`
	SelectedPlanCode   string             `json:"selected_plan_code" db:"selected_plan_code"`
	BillingCycle       BillingCycle       `json:"billing_cycle" db:"billing_cycle"`
	NIBMasked          string             `json:"nib_masked" db:"nib_masked"`
	NPWPMasked         string             `json:"npwp_masked" db:"npwp_masked"`
	AcceptedTOSSHA256  string             `json:"accepted_tos_sha256" db:"accepted_tos_sha256"`
	AcceptedPDPSHA256  string             `json:"accepted_pdp_sha256" db:"accepted_pdp_sha256"`
	Status             OnboardingStatus   `json:"status" db:"status"`
	EmailOTPExpiresAt  *time.Time         `json:"otp_expires_at,omitempty" db:"email_otp_expires_at"`
	EmailOTPSentAt     *time.Time         `json:"otp_sent_at,omitempty" db:"email_otp_sent_at"`
	EmailOTPAttempts   int                `json:"email_otp_attempts" db:"email_otp_attempts"`
	EmailVerifiedAt    *time.Time         `json:"email_verified_at,omitempty" db:"email_verified_at"`
	ProvisionedStoreID *uuid.UUID         `json:"provisioned_store_id,omitempty" db:"provisioned_store_id"`
	ReviewNotes        string             `json:"review_notes" db:"review_notes"`
	ReviewedAt         *time.Time         `json:"reviewed_at,omitempty" db:"reviewed_at"`
	CreatedAt          time.Time          `json:"created_at" db:"created_at"`
	UpdatedAt          time.Time          `json:"updated_at" db:"updated_at"`
}

type PlatformTenantSummary struct {
	StoreID             uuid.UUID          `json:"store_id" db:"store_id"`
	Slug                string             `json:"slug" db:"slug"`
	DisplayName         string             `json:"display_name" db:"display_name"`
	PlanCode            string             `json:"plan_code" db:"plan_code"`
	SubscriptionStatus  SubscriptionStatus `json:"subscription_status" db:"subscription_status"`
	KYBStatus           KYBStatus          `json:"kyb_status" db:"kyb_status"`
	BYOKMidtransEnabled bool               `json:"byok_midtrans_enabled" db:"byok_midtrans_enabled"`
	ActiveNodesCount    int                `json:"active_nodes_count" db:"active_nodes_count"`
	UpdatedAt           time.Time          `json:"updated_at" db:"updated_at"`
}

type TenantLegalAcceptance struct {
	ID               uuid.UUID `json:"id" db:"id"`
	StoreID          uuid.UUID `json:"store_id" db:"store_id"`
	LegalDocumentID  uuid.UUID `json:"legal_document_id" db:"legal_document_id"`
	AcceptedByUserID uuid.UUID `json:"accepted_by_user_id" db:"accepted_by_user_id"`
	DocumentSHA256   string    `json:"document_sha256" db:"document_sha256"`
	AcceptedIP       string    `json:"accepted_ip" db:"accepted_ip"`
	UserAgent        string    `json:"user_agent" db:"user_agent"`
	AcceptedAt       time.Time `json:"accepted_at" db:"accepted_at"`
}

type StoreKYBProfile struct {
	StoreID             uuid.UUID          `json:"store_id" db:"store_id"`
	EntityType          BusinessEntityType `json:"entity_type" db:"entity_type"`
	LegalBusinessName   string             `json:"legal_business_name" db:"legal_business_name"`
	NIBMasked           string             `json:"nib_masked" db:"nib_masked"`
	NPWPMasked          string             `json:"npwp_masked" db:"npwp_masked"`
	PSERegistrationNo   string             `json:"pse_registration_no" db:"pse_registration_no"`
	KYBStatus           KYBStatus          `json:"kyb_status" db:"kyb_status"`
	PlanCode            string             `json:"plan_code" db:"plan_code"`
	BillingCycle        BillingCycle       `json:"billing_cycle" db:"billing_cycle"`
	SubscriptionStatus  SubscriptionStatus `json:"subscription_status" db:"subscription_status"`
	CurrentPeriodEndsAt time.Time          `json:"current_period_ends_at" db:"current_period_ends_at"`
	VerifiedAt          *time.Time         `json:"verified_at,omitempty" db:"verified_at"`
	ReviewNotes         string             `json:"review_notes" db:"review_notes"`
	UpdatedAt           time.Time          `json:"updated_at" db:"updated_at"`
}

type PlatformInvoice struct {
	ID                  uuid.UUID             `json:"id" db:"id"`
	StoreID             uuid.UUID             `json:"store_id" db:"store_id"`
	InvoiceNumber       string                `json:"invoice_number" db:"invoice_number"`
	PlanCode            string                `json:"plan_code" db:"plan_code"`
	BillingCycle        BillingCycle          `json:"billing_cycle" db:"billing_cycle"`
	AmountIDR           int64                 `json:"amount_idr" db:"amount_idr"`
	Status              PlatformInvoiceStatus `json:"status" db:"status"`
	ProviderEnvironment string                `json:"provider_environment" db:"provider_environment"`
	ProviderOrderID     *string               `json:"provider_order_id,omitempty" db:"provider_order_id"`
	ProviderQRString    string                `json:"provider_qr_string,omitempty" db:"provider_qr_string"`
	ProviderVANumber    string                `json:"provider_va_number,omitempty" db:"provider_va_number"`
	PaidAt              *time.Time            `json:"paid_at,omitempty" db:"paid_at"`
	DueAt               time.Time             `json:"due_at" db:"due_at"`
	CreatedAt           time.Time             `json:"created_at" db:"created_at"`
	UpdatedAt           time.Time             `json:"updated_at" db:"updated_at"`
}

type TenantClearanceRequest struct {
	ID                   uuid.UUID       `json:"id" db:"id"`
	StoreID              uuid.UUID       `json:"store_id" db:"store_id"`
	RequestedByUserID    uuid.UUID       `json:"requested_by_user_id" db:"requested_by_user_id"`
	Reason               string          `json:"reason" db:"reason"`
	Status               ClearanceStatus `json:"status" db:"status"`
	CoolingOffEndsAt     time.Time       `json:"cooling_off_ends_at" db:"cooling_off_ends_at"`
	LedgerRetentionUntil time.Time       `json:"ledger_retention_until" db:"ledger_retention_until"`
	ExecutedAt           *time.Time      `json:"executed_at,omitempty" db:"executed_at"`
	CreatedAt            time.Time       `json:"created_at" db:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at" db:"updated_at"`
}

type StorePaymentCredential struct {
	StoreID                   uuid.UUID  `json:"store_id" db:"store_id"`
	Provider                  string     `json:"provider" db:"provider"`
	Environment               string     `json:"environment" db:"environment"`
	MerchantID                string     `json:"merchant_id" db:"merchant_id"`
	ClientKey                 string     `json:"client_key" db:"client_key"`
	ServerKeyCiphertext       []byte     `json:"-" db:"server_key_ciphertext"`
	ServerKeyMasked           string     `json:"server_key_masked" db:"server_key_masked"`
	KeyVersion                int16      `json:"key_version" db:"key_version"`
	ValidationStatus          string     `json:"validation_status" db:"validation_status"`
	IsEnabled                 bool       `json:"is_enabled" db:"is_enabled"`
	FallbackStaticQRISEnabled bool       `json:"fallback_static_qris_enabled" db:"fallback_static_qris_enabled"`
	LastVerifiedAt            *time.Time `json:"last_verified_at,omitempty" db:"last_verified_at"`
	UpdatedByUserID           *uuid.UUID `json:"updated_by_user_id,omitempty" db:"updated_by_user_id"`
	UpdatedAt                 time.Time  `json:"updated_at" db:"updated_at"`
}

type StoreCMSConfig struct {
	StoreID              uuid.UUID       `json:"store_id" db:"store_id"`
	DefaultLocale        string          `json:"default_locale" db:"default_locale"`
	SupportedLocales     json.RawMessage `json:"supported_locales" db:"supported_locales"`
	HeroTitleID          string          `json:"hero_title_id" db:"hero_title_id"`
	HeroTitleEN          string          `json:"hero_title_en" db:"hero_title_en"`
	HeroSubtitleID       string          `json:"hero_subtitle_id" db:"hero_subtitle_id"`
	HeroSubtitleEN       string          `json:"hero_subtitle_en" db:"hero_subtitle_en"`
	HeroBannerURL        string          `json:"hero_banner_url" db:"hero_banner_url"`
	AnnouncementBannerID string          `json:"announcement_banner_id" db:"announcement_banner_id"`
	AnnouncementBannerEN string          `json:"announcement_banner_en" db:"announcement_banner_en"`
	PrimaryHex           string          `json:"primary_hex" db:"primary_hex"`
	AccentHex            string          `json:"accent_hex" db:"accent_hex"`
	HighContrastMode     bool            `json:"high_contrast_mode" db:"high_contrast_mode"`
	HidePoweredByBadge   bool            `json:"hide_powered_by_badge" db:"hide_powered_by_badge"`
	CustomDomain         string          `json:"custom_domain" db:"custom_domain"`
	OperatingHours       json.RawMessage `json:"operating_hours" db:"operating_hours"`
	PublicRefundPolicy   string          `json:"public_refund_policy" db:"public_refund_policy"`
	PublicContactPhone   string          `json:"public_contact_phone" db:"public_contact_phone"`
	PublicContactEmail   string          `json:"public_contact_email" db:"public_contact_email"`
	UpdatedAt            time.Time       `json:"updated_at" db:"updated_at"`
}

type StoreNotificationConfig struct {
	StoreID                 uuid.UUID `json:"store_id" db:"store_id"`
	EmailMode               string    `json:"email_mode" db:"email_mode"`
	SMTPHost                string    `json:"smtp_host" db:"smtp_host"`
	SMTPPort                int       `json:"smtp_port" db:"smtp_port"`
	SMTPTLSMode             string    `json:"smtp_tls_mode" db:"smtp_tls_mode"`
	SMTPUsername            string    `json:"smtp_username" db:"smtp_username"`
	SMTPPasswordCiphertext  []byte    `json:"-" db:"smtp_password_ciphertext"`
	SMTPPasswordMasked      string    `json:"smtp_password_masked" db:"smtp_password_masked"`
	SMTPFromEmail           string    `json:"smtp_from_email" db:"smtp_from_email"`
	SMTPFromName            string    `json:"smtp_from_name" db:"smtp_from_name"`
	WhatsAppMode            string    `json:"whatsapp_mode" db:"whatsapp_mode"`
	WhatsAppEndpointURL     string    `json:"whatsapp_endpoint_url" db:"whatsapp_endpoint_url"`
	WhatsAppPhoneNumberID   string    `json:"whatsapp_phone_number_id" db:"whatsapp_phone_number_id"`
	WhatsAppTokenCiphertext []byte    `json:"-" db:"whatsapp_token_ciphertext"`
	WhatsAppTokenMasked     string    `json:"whatsapp_token_masked" db:"whatsapp_token_masked"`
	UpdatedAt               time.Time `json:"updated_at" db:"updated_at"`
}

type StoreNetworkNode struct {
	ID                 uuid.UUID       `json:"id" db:"id"`
	StoreID            uuid.UUID       `json:"store_id" db:"store_id"`
	NodeName           string          `json:"node_name" db:"node_name"`
	NodeType           string          `json:"node_type" db:"node_type"`
	ConnectionProtocol string          `json:"connection_protocol" db:"connection_protocol"`
	LocalIP            string          `json:"local_ip" db:"local_ip"`
	LocalPort          int             `json:"local_port" db:"local_port"`
	MACAddress         string          `json:"mac_address" db:"mac_address"`
	PaperWidthMM       int16           `json:"paper_width_mm" db:"paper_width_mm"`
	DrawerKickEnabled  bool            `json:"drawer_kick_enabled" db:"drawer_kick_enabled"`
	HealthStatus       string          `json:"health_status" db:"health_status"`
	LastLatencyMS      int             `json:"last_latency_ms" db:"last_latency_ms"`
	LastHeartbeatAt    *time.Time      `json:"last_heartbeat_at,omitempty" db:"last_heartbeat_at"`
	Metadata           json.RawMessage `json:"metadata" db:"metadata"`
	IsActive           bool            `json:"is_active" db:"is_active"`
	CreatedAt          time.Time       `json:"created_at" db:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at" db:"updated_at"`
}

type NetworkSimulationHop struct {
	Hop      string `json:"hop"`
	Protocol string `json:"protocol"`
	Status   string `json:"status"`
	Latency  int    `json:"latency_ms"`
	Note     string `json:"note"`
}

type NetworkSimulationReport struct {
	StoreID        uuid.UUID              `json:"store_id"`
	Scenario       string                 `json:"scenario"`
	OverallStatus  string                 `json:"overall_status"`
	DBLatencyMS    int                    `json:"db_latency_ms"`
	ActiveNodes    int                    `json:"active_nodes"`
	BYOKActive     bool                   `json:"byok_midtrans_active"`
	FallbackMode   string                 `json:"fallback_mode"`
	SimulatedHops  []NetworkSimulationHop `json:"simulated_hops"`
	Recommendation string                 `json:"recommendation"`
	SimulatedAt    time.Time              `json:"simulated_at"`
}

type MidtransReadinessCheck struct {
	Code   string `json:"code"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail"`
}

type MidtransBusinessReviewReadiness struct {
	PlatformEnvironment   string                   `json:"platform_environment"`
	DefaultStoreEnv       string                   `json:"default_store_env"`
	ReadyForLiveCutover   bool                     `json:"ready_for_live_cutover"`
	ScorePercent          int                      `json:"score_percent"`
	Checks                []MidtransReadinessCheck `json:"checks"`
	EdgeCasePlaybookNotes []string                 `json:"edge_case_playbook_notes"`
	EvaluatedAt           time.Time                `json:"evaluated_at"`
}

type PlatformOverview struct {
	TotalTenants         int                     `json:"total_tenants"`
	ActiveTenants        int                     `json:"active_tenants"`
	TrialingTenants      int                     `json:"trialing_tenants"`
	BYOKEnabledTenants   int                     `json:"byok_enabled_tenants"`
	PendingOnboarding    int                     `json:"pending_onboarding"`
	PendingKYBReviews    int                     `json:"pending_kyb_reviews"`
	ActiveClearanceCount int                     `json:"active_clearance_count"`
	Tenants              []PlatformTenantSummary `json:"tenants"`
}

type ProvisionedTableSummary struct {
	ID          uuid.UUID `json:"id"`
	TableNumber string    `json:"table_number"`
	QRToken     string    `json:"qr_token"`
}

type ProvisionedStoreResult struct {
	Application    *TenantOnboardingApplication `json:"application"`
	StoreID        uuid.UUID                    `json:"store_id"`
	StoreSlug      string                       `json:"store_slug"`
	OwnerUserID    uuid.UUID                    `json:"owner_user_id"`
	OwnerEmail     string                       `json:"owner_email"`
	KYBStatus      KYBStatus                    `json:"kyb_status"`
	AlreadyExisted bool                         `json:"already_existed"`
	Tables         []ProvisionedTableSummary    `json:"tables"`
}

type StoreMikrotikProfile struct {
	StoreID             uuid.UUID  `json:"store_id" db:"store_id"`
	RouterModel         string     `json:"router_model" db:"router_model"`
	WANPrimaryInterface string     `json:"wan_primary_interface" db:"wan_primary_interface"`
	WANBackupInterface  string     `json:"wan_backup_interface" db:"wan_backup_interface"`
	POSVLANCIDR         string     `json:"pos_vlan_cidr" db:"pos_vlan_cidr"`
	StaffVLANCIDR       string     `json:"staff_vlan_cidr" db:"staff_vlan_cidr"`
	GuestVLANCIDR       string     `json:"guest_vlan_cidr" db:"guest_vlan_cidr"`
	POSWiFiSSID         string     `json:"pos_wifi_ssid" db:"pos_wifi_ssid"`
	GuestWiFiSSID       string     `json:"guest_wifi_ssid" db:"guest_wifi_ssid"`
	GuestRateLimitMbps  int        `json:"guest_rate_limit_mbps" db:"guest_rate_limit_mbps"`
	POSReservedMbps     int        `json:"pos_reserved_mbps" db:"pos_reserved_mbps"`
	SFTPPort            int        `json:"sftp_port" db:"sftp_port"`
	WinboxPort          int        `json:"winbox_port" db:"winbox_port"`
	WinboxAllowedCIDR   string     `json:"winbox_allowed_cidr" db:"winbox_allowed_cidr"`
	DNSServers          string     `json:"dns_servers" db:"dns_servers"`
	GeneratedRscSHA256  string     `json:"generated_rsc_sha256" db:"generated_rsc_sha256"`
	UpdatedByUserID     *uuid.UUID `json:"updated_by_user_id,omitempty" db:"updated_by_user_id"`
	UpdatedAt           time.Time  `json:"updated_at" db:"updated_at"`
}

type WinboxGuideStep struct {
	StepNumber  int    `json:"step_number"`
	Title       string `json:"title"`
	MenuPath    string `json:"menu_path"`
	Instruction string `json:"instruction"`
	SafetyCheck string `json:"safety_check"`
}

type FileZillaSFTPPolicy struct {
	Protocol               string `json:"protocol"`
	Port                   int    `json:"port"`
	AllowedSubnetCIDR      string `json:"allowed_subnet_cidr"`
	PlainFTPBlocked        bool   `json:"plain_ftp_blocked"`
	PlainFTPPortBlocked    int    `json:"plain_ftp_port_blocked"`
	KeyAuthRecommended     bool   `json:"key_auth_recommended"`
	RecommendedAlternative string `json:"recommended_alternative"`
	SecurityRationale      string `json:"security_rationale"`
}

type MikrotikTopologySummary struct {
	POSSubnetCIDR        string `json:"pos_subnet_cidr"`
	StaffSubnetCIDR      string `json:"staff_subnet_cidr"`
	GuestSubnetCIDR      string `json:"guest_subnet_cidr"`
	StaticDHCPCount      int    `json:"static_dhcp_count"`
	DualWANFailoverReady bool   `json:"dual_wan_failover_ready"`
	OfflineSyncMode      string `json:"offline_sync_mode"`
}

type MikrotikRscBundle struct {
	Profile             *StoreMikrotikProfile   `json:"profile"`
	FileName            string                  `json:"file_name"`
	RscScript           string                  `json:"rsc_script"`
	ScriptSHA256        string                  `json:"script_sha256"`
	WinboxSteps         []WinboxGuideStep       `json:"winbox_steps"`
	FileZillaSFTPPolicy FileZillaSFTPPolicy     `json:"filezilla_sftp_policy"`
	TopologySummary     MikrotikTopologySummary `json:"topology_summary"`
	GeneratedAt         time.Time               `json:"generated_at"`
}

// StoreMobileProfile aligns 1:1 with store_mobile_profiles table (Migration 53).
type StoreMobileProfile struct {
	StoreID                 uuid.UUID  `json:"store_id" db:"store_id"`
	AppBrandDisplayName     string     `json:"app_brand_display_name" db:"app_brand_display_name"`
	AppMonogramText         string     `json:"app_monogram_text" db:"app_monogram_text"`
	AppTechnicalBundleID    string     `json:"app_technical_bundle_id" db:"app_technical_bundle_id"`
	POSTabletOrientation    string     `json:"pos_tablet_orientation" db:"pos_tablet_orientation"`
	POSAutoLockSeconds      int        `json:"pos_auto_lock_seconds" db:"pos_auto_lock_seconds"`
	KDSAutoRefreshSeconds   int        `json:"kds_auto_refresh_seconds" db:"kds_auto_refresh_seconds"`
	KDSSoundAlertEnabled    bool       `json:"kds_sound_alert_enabled" db:"kds_sound_alert_enabled"`
	CashDrawerKickOnCash    bool       `json:"cash_drawer_kick_on_cash" db:"cash_drawer_kick_on_cash"`
	DefaultPrinterInterface string     `json:"default_printer_interface" db:"default_printer_interface"`
	DefaultPrinterPort      int        `json:"default_printer_port" db:"default_printer_port"`
	OfflineSyncIntervalSec  int        `json:"offline_sync_interval_sec" db:"offline_sync_interval_sec"`
	UpdatedByUserID         *uuid.UUID `json:"updated_by_user_id,omitempty" db:"updated_by_user_id"`
	UpdatedAt               time.Time  `json:"updated_at" db:"updated_at"`
}

// MobileBootstrapResponse models the consolidated tablet POS & KDS bootstrap handshake.
type MobileBootstrapResponse struct {
	StoreID        uuid.UUID                `json:"store_id"`
	StoreSlug      string                   `json:"store_slug"`
	StoreName      string                   `json:"store_name"`
	AppBrand       MobileAppBrandInfo       `json:"app_brand"`
	POSLayout      MobilePOSLayoutInfo      `json:"pos_layout"`
	Hardware       MobileHardwareInfo       `json:"hardware"`
	PaymentGateway MobilePaymentGatewayInfo `json:"payment_gateway"`
	StaffSession   MobileStaffSessionInfo   `json:"staff_session"`
}

type MobileAppBrandInfo struct {
	DisplayName       string `json:"display_name"`
	MonogramText      string `json:"monogram_text"`
	BundleID          string `json:"bundle_id"`
	VectorMonogramURL string `json:"vector_monogram_url"`
	VectorLogoURL     string `json:"vector_logo_url"`
	VectorIconURL     string `json:"vector_icon_url"`
}

type MobilePOSLayoutInfo struct {
	TabletOrientation      string `json:"tablet_orientation"`
	LayoutMode             string `json:"layout_mode"`
	AutoLockSeconds        int    `json:"auto_lock_seconds"`
	TabularNumbersEnforced bool   `json:"tabular_numbers_enforced"`
	MinTouchTargetPx       int    `json:"min_touch_target_px"`
}

type MobileHardwareInfo struct {
	DefaultPrinterInterface string `json:"default_printer_interface"`
	DefaultPrinterPort      int    `json:"default_printer_port"`
	CashDrawerKickOnCash    bool   `json:"cash_drawer_kick_on_cash"`
	CashDrawerPulseHex      string `json:"cash_drawer_pulse_hex"`
	KDSAutoRefreshSeconds   int    `json:"kds_auto_refresh_seconds"`
	KDSSoundAlertEnabled    bool   `json:"kds_sound_alert_enabled"`
}

type MobilePaymentGatewayInfo struct {
	Provider                  string `json:"provider"`
	Environment               string `json:"environment"`
	ClientKey                 string `json:"client_key"`
	MerchantID                string `json:"merchant_id"`
	IsEnabled                 bool   `json:"is_enabled"`
	ValidationStatus          string `json:"validation_status"`
	FallbackStaticQRISEnabled bool   `json:"fallback_static_qris_enabled"`
}

type MobileStaffSessionInfo struct {
	UserID      uuid.UUID `json:"user_id"`
	FullName    string    `json:"full_name"`
	Role        string    `json:"role"`
	Email       string    `json:"email"`
	Permissions []string  `json:"permissions"`
}
