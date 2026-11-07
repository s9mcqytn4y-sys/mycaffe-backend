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
	OnboardingSubmitted   OnboardingStatus = "submitted"
	OnboardingUnderReview OnboardingStatus = "under_review"
	OnboardingApproved    OnboardingStatus = "approved"
	OnboardingRejected    OnboardingStatus = "rejected"
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
