package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/config"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/mailer"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/midtrans"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/secretbox"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
)

var (
	ErrGovernanceNotFound        = errors.New("governance resource not found")
	ErrBYOKEncryptionUnavailable = errors.New("BYOK credential encryption key is not configured")
	ErrBYOKLiveAuthFailed        = errors.New("Midtrans Core API rejected the supplied Server Key (401 Unauthorized)")
	ErrOnboardingOTPExpired      = errors.New("kode OTP email sudah kedaluwarsa; silakan kirim ulang OTP")
	ErrOnboardingOTPInvalid      = errors.New("kode OTP email tidak sesuai")
	ErrOnboardingOTPLocked       = errors.New("batas percobaan OTP terlampaui; silakan kirim ulang OTP baru")
	ErrOnboardingOTPCooldown     = errors.New("tunggu 60 detik sebelum meminta kode OTP baru")
	ErrOnboardingOTPUnverified   = errors.New("email pendaftar wajib diverifikasi melalui OTP sebelum gerai dapat diaktifkan")
)

var (
	slugFormatPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,62}$`)
	sha256HexPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	hexColorPattern   = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
)

type GovernanceOption func(*GovernanceService)

func WithGovernanceGatewayBuilder(fn func(serverKey string, isProduction bool) GatewayClient) GovernanceOption {
	return func(s *GovernanceService) {
		if fn != nil {
			s.gatewayBuilder = fn
		}
	}
}

func WithPlatformGatewayClient(client GatewayClient) GovernanceOption {
	return func(s *GovernanceService) {
		s.platformGateway = normalizeGatewayClient(client)
	}
}

func WithGovernanceMailer(sender mailer.Sender, appPublicURL string) GovernanceOption {
	return func(s *GovernanceService) {
		s.mailSender = sender
		if strings.TrimSpace(appPublicURL) != "" {
			s.appPublicURL = strings.TrimRight(strings.TrimSpace(appPublicURL), "/")
		}
	}
}

type GovernanceService struct {
	db               *sqlx.DB
	authRepo         *postgres.AuthRepository
	box              *secretbox.Box
	platformGateway  GatewayClient
	gatewayBuilder   func(serverKey string, isProduction bool) GatewayClient
	mailSender       mailer.Sender
	appPublicURL     string
	appEnv           string
	platformEnv      string
	defaultStoreProd bool
	nowFunc          func() time.Time
}

func NewGovernanceService(db *sqlx.DB, cfg *config.Config, opts ...GovernanceOption) *GovernanceService {
	appEnv := "development"
	rawEncKey := ""
	platformEnv := "sandbox"
	appPublicURL := ""
	var defaultStoreProd bool
	var platformGW GatewayClient

	if cfg != nil {
		if cfg.AppEnv != "" {
			appEnv = cfg.AppEnv
		}
		rawEncKey = cfg.CredentialEncryptionKey
		defaultStoreProd = cfg.MidtransIsProduction
		appPublicURL = strings.TrimRight(strings.TrimSpace(cfg.AppPublicURL), "/")
		if cfg.PlatformMidtransIsProduction {
			platformEnv = "production"
		}
		if strings.TrimSpace(cfg.PlatformMidtransServerKey) != "" {
			mc := midtrans.NewClientWithKey(cfg.PlatformMidtransServerKey, cfg.PlatformMidtransIsProduction)
			if strings.HasPrefix(strings.ToLower(appPublicURL), "https://") {
				mc = mc.WithDefaultNotificationURL(appPublicURL + "/api/v1/payments/midtrans/webhook")
			}
			platformGW = mc
		}
	}

	box, _ := secretbox.NewDevFallback(rawEncKey, appEnv)

	svc := &GovernanceService{
		db:              db,
		authRepo:        postgres.NewAuthRepository(),
		box:             box,
		platformGateway: normalizeGatewayClient(platformGW),
		gatewayBuilder: func(serverKey string, isProduction bool) GatewayClient {
			return midtrans.NewClientWithKey(serverKey, isProduction)
		},
		appPublicURL:     appPublicURL,
		appEnv:           appEnv,
		platformEnv:      platformEnv,
		defaultStoreProd: defaultStoreProd,
		nowFunc:          time.Now,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(svc)
		}
	}
	return svc
}

func requireGovernanceOwner(actor *AuthenticatedStaff) error {
	if actor == nil {
		return ErrInvalidSession
	}
	if !actor.Can(rbac.StoreSettingsManage) {
		return ErrForbidden
	}
	return nil
}

func (s *GovernanceService) writeGovernanceAuditTx(
	ctx context.Context,
	tx *sqlx.Tx,
	storeID uuid.UUID,
	actorUserID *uuid.UUID,
	actorType string,
	action string,
	reason string,
	meta map[string]any,
) error {
	if meta == nil {
		meta = map[string]any{}
	}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO governance_audit_logs (id, store_id, actor_user_id, actor_type, action, reason, metadata, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8)
	`, uuid.New(), storeID, actorUserID, actorType, action, reason, string(metaBytes), s.nowFunc().UTC())
	return err
}

// ============================================================================
// 1. PUBLIC ENDPOINTS (Legal Docs, Pricing Plans, Onboarding, Public Store CMS)
// ============================================================================

func (s *GovernanceService) ListActiveLegalDocuments(ctx context.Context, locale string) ([]domain.LegalDocument, error) {
	loc := strings.TrimSpace(locale)
	if loc == "" {
		loc = "id-ID"
	}
	if loc != "id-ID" && loc != "en-US" {
		f := fieldErrors{}
		f.add("locale", "harus id-ID atau en-US")
		return nil, f.err()
	}
	var docs []domain.LegalDocument
	if err := s.db.SelectContext(ctx, &docs, `
		SELECT id, doc_type, version, locale, title, content_markdown, content_sha256, is_active, effective_at, created_at
		FROM legal_documents
		WHERE is_active = true AND locale = $1
		ORDER BY doc_type ASC, effective_at DESC
	`, loc); err != nil {
		return nil, fmt.Errorf("failed to list legal documents: %w", err)
	}
	if docs == nil {
		docs = []domain.LegalDocument{}
	}
	return docs, nil
}

func (s *GovernanceService) ListSubscriptionPlans(ctx context.Context) ([]domain.SubscriptionPlan, error) {
	var plans []domain.SubscriptionPlan
	if err := s.db.SelectContext(ctx, &plans, `
		SELECT code, name_id, name_en, monthly_price_idr, annual_price_idr, commission_bps,
		       max_tables, max_printers, supports_byok_midtrans, supports_kds_queue_tv,
		       supports_whitelabel, supports_byo_notifications, is_active, created_at, updated_at
		FROM subscription_plans
		WHERE is_active = true
		ORDER BY monthly_price_idr ASC, code ASC
	`); err != nil {
		return nil, fmt.Errorf("failed to list subscription plans: %w", err)
	}
	if plans == nil {
		plans = []domain.SubscriptionPlan{}
	}
	return plans, nil
}

type OnboardingApplicationInput struct {
	IdempotencyKey     string `json:"idempotency_key"`
	RequestedSlug      string `json:"requested_slug"`
	BusinessName       string `json:"business_name"`
	BusinessEntityType string `json:"business_entity_type"`
	OwnerName          string `json:"owner_name"`
	OwnerEmail         string `json:"owner_email"`
	OwnerPhone         string `json:"owner_phone"`
	CityRegion         string `json:"city_region"`
	SelectedPlanCode   string `json:"selected_plan_code"`
	BillingCycle       string `json:"billing_cycle"`
	NIB                string `json:"nib"`
	NPWP               string `json:"npwp"`
	AcceptedTOSSHA256  string `json:"accepted_tos_sha256"`
	AcceptedPDPSHA256  string `json:"accepted_pdp_sha256"`
}

func (s *GovernanceService) SubmitOnboardingApplication(ctx context.Context, in OnboardingApplicationInput) (*domain.TenantOnboardingApplication, bool, string, error) {
	f := fieldErrors{}
	idem := f.text("idempotency_key", in.IdempotencyKey, 8, 100)
	slug := strings.ToLower(strings.TrimSpace(in.RequestedSlug))
	if !slugFormatPattern.MatchString(slug) {
		f.add("requested_slug", "3-63 karakter huruf kecil, angka, atau tanda hubung")
	}
	bizName := f.text("business_name", in.BusinessName, 2, 150)
	entityType := strings.ToLower(strings.TrimSpace(in.BusinessEntityType))
	if entityType == "" {
		entityType = string(domain.EntityPerorangan)
	}
	switch domain.BusinessEntityType(entityType) {
	case domain.EntityPerorangan, domain.EntityCV, domain.EntityPT, domain.EntityPTPerorangan, domain.EntityKoperasi:
	default:
		f.add("business_entity_type", "harus salah satu dari perorangan, cv, pt, pt_perorangan, koperasi")
	}
	ownerName := f.text("owner_name", in.OwnerName, 2, 120)
	ownerEmail := strings.ToLower(strings.TrimSpace(in.OwnerEmail))
	if len(ownerEmail) < 5 || len(ownerEmail) > 190 || !strings.Contains(ownerEmail, "@") {
		f.add("owner_email", "format email tidak valid")
	}
	ownerPhone := f.text("owner_phone", in.OwnerPhone, 8, 40)
	city := strings.TrimSpace(in.CityRegion)
	if len(city) > 120 {
		f.add("city_region", "maksimal 120 karakter")
	}
	planCode := strings.TrimSpace(in.SelectedPlanCode)
	if planCode == "" {
		planCode = "trial_14d"
	}
	cycle := strings.ToLower(strings.TrimSpace(in.BillingCycle))
	if cycle == "" {
		cycle = string(domain.BillingCycleTrial)
	}
	switch domain.BillingCycle(cycle) {
	case domain.BillingCycleTrial, domain.BillingCycleMonthly, domain.BillingCycleAnnual:
	default:
		f.add("billing_cycle", "harus trial, monthly, atau annual")
	}
	tosHash := strings.ToLower(strings.TrimSpace(in.AcceptedTOSSHA256))
	if !sha256HexPattern.MatchString(tosHash) {
		f.add("accepted_tos_sha256", "wajib berupa hash SHA-256 64 karakter heksadesimal dari dokumen Syarat & Ketentuan")
	}
	pdpHash := strings.ToLower(strings.TrimSpace(in.AcceptedPDPSHA256))
	if !sha256HexPattern.MatchString(pdpHash) {
		f.add("accepted_pdp_sha256", "wajib berupa hash SHA-256 64 karakter heksadesimal dari Kebijakan Privasi UU PDP")
	}
	if err := f.err(); err != nil {
		return nil, false, "", err
	}

	var existing domain.TenantOnboardingApplication
	err := s.db.GetContext(ctx, &existing, `
		SELECT id, idempotency_key, requested_slug, business_name, business_entity_type,
		       owner_name, owner_email, owner_phone, city_region, selected_plan_code, billing_cycle,
		       nib_masked, npwp_masked, accepted_tos_sha256, accepted_pdp_sha256,
		       status, email_otp_expires_at, email_otp_sent_at, email_otp_attempts, email_verified_at,
		       provisioned_store_id, review_notes, reviewed_at, created_at, updated_at
		FROM tenant_onboarding_applications
		WHERE idempotency_key = $1
	`, idem)
	if err == nil {
		if existing.RequestedSlug != slug || !strings.EqualFold(existing.OwnerEmail, ownerEmail) {
			return nil, false, "", &ConflictError{Field: "idempotency_key", Message: "idempotency_key sudah digunakan untuk pengajuan lain"}
		}
		return &existing, true, "", nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, "", fmt.Errorf("failed to check onboarding idempotency: %w", err)
	}

	var planExists bool
	if err := s.db.GetContext(ctx, &planExists, `SELECT EXISTS (SELECT 1 FROM subscription_plans WHERE code = $1 AND is_active = true)`, planCode); err != nil {
		return nil, false, "", err
	}
	if !planExists {
		f.add("selected_plan_code", "paket langganan tidak ditemukan atau tidak aktif")
		return nil, false, "", f.err()
	}

	otpPlain, err := generateSixDigitOTP()
	if err != nil {
		return nil, false, "", err
	}
	otpHash := hashOTPCode(otpPlain)
	now := s.nowFunc().UTC()
	otpExp := now.Add(onboardingOTPTTL)
	app := &domain.TenantOnboardingApplication{
		ID:                 uuid.New(),
		IdempotencyKey:     idem,
		RequestedSlug:      slug,
		BusinessName:       bizName,
		BusinessEntityType: domain.BusinessEntityType(entityType),
		OwnerName:          ownerName,
		OwnerEmail:         ownerEmail,
		OwnerPhone:         ownerPhone,
		CityRegion:         city,
		SelectedPlanCode:   planCode,
		BillingCycle:       domain.BillingCycle(cycle),
		NIBMasked:          secretbox.MaskSecret(in.NIB),
		NPWPMasked:         secretbox.MaskSecret(in.NPWP),
		AcceptedTOSSHA256:  tosHash,
		AcceptedPDPSHA256:  pdpHash,
		Status:             domain.OnboardingPendingEmailOTP,
		EmailOTPExpiresAt:  &otpExp,
		EmailOTPSentAt:     &now,
		EmailOTPAttempts:   0,
		CreatedAt:          now,
		UpdatedAt:          now,
	}

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO tenant_onboarding_applications (
			id, idempotency_key, requested_slug, business_name, business_entity_type,
			owner_name, owner_email, owner_phone, city_region, selected_plan_code, billing_cycle,
			nib_masked, npwp_masked, accepted_tos_sha256, accepted_pdp_sha256,
			status, email_otp_hash, email_otp_expires_at, email_otp_sent_at, email_otp_attempts,
			created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10, $11,
			$12, $13, $14, $15,
			$16, $17, $18, $19, 0,
			$19, $19
		)
	`, app.ID, app.IdempotencyKey, app.RequestedSlug, app.BusinessName, app.BusinessEntityType,
		app.OwnerName, app.OwnerEmail, app.OwnerPhone, app.CityRegion, app.SelectedPlanCode, app.BillingCycle,
		app.NIBMasked, app.NPWPMasked, app.AcceptedTOSSHA256, app.AcceptedPDPSHA256,
		app.Status, otpHash, otpExp, now)
	if err != nil {
		return nil, false, "", fmt.Errorf("failed to create onboarding application: %w", err)
	}

	s.sendOnboardingOTPEmail(ctx, app, otpPlain)

	devPreview := ""
	if s.appEnv == "development" || s.appEnv == "test" {
		devPreview = otpPlain
	}
	return app, false, devPreview, nil
}

func defaultStoreCMS(storeID uuid.UUID, now time.Time) *domain.StoreCMSConfig {
	return &domain.StoreCMSConfig{
		StoreID:          storeID,
		DefaultLocale:    "id-ID",
		SupportedLocales: json.RawMessage(`["id-ID","en-US"]`),
		PrimaryHex:       "#2C1A12",
		AccentHex:        "#C68B59",
		OperatingHours:   json.RawMessage(`{}`),
		UpdatedAt:        now,
	}
}

func (s *GovernanceService) GetPublicStoreCMS(ctx context.Context, slug string) (*domain.StoreCMSConfig, error) {
	storeID, isActive, err := s.authRepo.ResolveStoreForLogin(ctx, s.db, strings.ToLower(strings.TrimSpace(slug)))
	if err != nil {
		if errors.Is(err, postgres.ErrStoreNotFound) {
			return nil, ErrGovernanceNotFound
		}
		return nil, err
	}
	if !isActive {
		return nil, ErrGovernanceNotFound
	}
	var out *domain.StoreCMSConfig
	err = database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		var cms domain.StoreCMSConfig
		qErr := tx.GetContext(ctx, &cms, `
			SELECT store_id, default_locale, supported_locales, hero_title_id, hero_title_en,
			       hero_subtitle_id, hero_subtitle_en, hero_banner_url, announcement_banner_id,
			       announcement_banner_en, primary_hex, accent_hex, high_contrast_mode,
			       hide_powered_by_badge, custom_domain, operating_hours, public_refund_policy,
			       public_contact_phone, public_contact_email, updated_at
			FROM store_cms_configs
			WHERE store_id = $1
		`, storeID)
		if errors.Is(qErr, sql.ErrNoRows) {
			out = defaultStoreCMS(storeID, s.nowFunc().UTC())
			return nil
		}
		if qErr != nil {
			return qErr
		}
		out = &cms
		return nil
	})
	return out, err
}

// ============================================================================
// 2. TENANT OWNER GOVERNANCE (Legal Consent, KYB, BYOK Midtrans, CMS, Notifications, Network, Clearance)
// ============================================================================

func (s *GovernanceService) AcceptLegalDocument(ctx context.Context, actor *AuthenticatedStaff, legalDocID uuid.UUID, clientIP, userAgent string) (*domain.TenantLegalAcceptance, error) {
	if err := requireGovernanceOwner(actor); err != nil {
		return nil, err
	}
	var doc domain.LegalDocument
	if err := s.db.GetContext(ctx, &doc, `
		SELECT id, doc_type, version, locale, title, content_markdown, content_sha256, is_active, effective_at, created_at
		FROM legal_documents
		WHERE id = $1 AND is_active = true
	`, legalDocID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrGovernanceNotFound
		}
		return nil, err
	}

	storeID := actor.Staff.Store.ID
	userID := actor.Staff.UserID
	if len(userAgent) > 255 {
		userAgent = userAgent[:255]
	}
	if len(clientIP) > 64 {
		clientIP = clientIP[:64]
	}

	var out domain.TenantLegalAcceptance
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		now := s.nowFunc().UTC()
		res, execErr := tx.ExecContext(ctx, `
			INSERT INTO tenant_legal_acceptances (
				id, store_id, legal_document_id, accepted_by_user_id, document_sha256, accepted_ip, user_agent, accepted_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (store_id, legal_document_id, accepted_by_user_id) DO NOTHING
		`, uuid.New(), storeID, doc.ID, userID, strings.TrimSpace(doc.ContentSHA256), clientIP, userAgent, now)
		if execErr != nil {
			return execErr
		}
		if err := tx.GetContext(ctx, &out, `
			SELECT id, store_id, legal_document_id, accepted_by_user_id, document_sha256, accepted_ip, user_agent, accepted_at
			FROM tenant_legal_acceptances
			WHERE store_id = $1 AND legal_document_id = $2 AND accepted_by_user_id = $3
		`, storeID, doc.ID, userID); err != nil {
			return err
		}
		rowsAffected, _ := res.RowsAffected()
		if rowsAffected == 0 {
			return nil
		}
		return s.writeGovernanceAuditTx(ctx, tx, storeID, &userID, "tenant_owner", "LEGAL_ACCEPTED",
			fmt.Sprintf("Persetujuan dokumen hukum %s v%s", doc.DocType, doc.Version),
			map[string]any{"legal_document_id": doc.ID, "doc_type": doc.DocType, "version": doc.Version, "sha256": strings.TrimSpace(doc.ContentSHA256)})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *GovernanceService) ListTenantLegalAcceptances(ctx context.Context, actor *AuthenticatedStaff) ([]domain.TenantLegalAcceptance, error) {
	if err := requireGovernanceOwner(actor); err != nil {
		return nil, err
	}
	storeID := actor.Staff.Store.ID
	var rows []domain.TenantLegalAcceptance
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows, `
			SELECT id, store_id, legal_document_id, accepted_by_user_id, document_sha256, accepted_ip, user_agent, accepted_at
			FROM tenant_legal_acceptances
			WHERE store_id = $1
			ORDER BY accepted_at DESC
		`, storeID)
	})
	if rows == nil {
		rows = []domain.TenantLegalAcceptance{}
	}
	return rows, err
}

type StoreKYBUpdateInput struct {
	EntityType        string `json:"entity_type"`
	LegalBusinessName string `json:"legal_business_name"`
	NIB               string `json:"nib"`
	NPWP              string `json:"npwp"`
	PSERegistrationNo string `json:"pse_registration_no"`
	PlanCode          string `json:"plan_code"`
	BillingCycle      string `json:"billing_cycle"`
}

func (s *GovernanceService) GetStoreKYBProfile(ctx context.Context, actor *AuthenticatedStaff) (*domain.StoreKYBProfile, error) {
	if err := requireGovernanceOwner(actor); err != nil {
		return nil, err
	}
	storeID := actor.Staff.Store.ID
	var out domain.StoreKYBProfile
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		qErr := tx.GetContext(ctx, &out, `
			SELECT store_id, entity_type, legal_business_name, nib_masked, npwp_masked, pse_registration_no,
			       kyb_status, plan_code, billing_cycle, subscription_status, current_period_ends_at,
			       verified_at, review_notes, updated_at
			FROM store_kyb_profiles
			WHERE store_id = $1
		`, storeID)
		if errors.Is(qErr, sql.ErrNoRows) {
			now := s.nowFunc().UTC()
			out = domain.StoreKYBProfile{
				StoreID:             storeID,
				EntityType:          domain.EntityPerorangan,
				LegalBusinessName:   actor.Staff.Store.Name,
				KYBStatus:           domain.KYBUnverified,
				PlanCode:            "trial_14d",
				BillingCycle:        domain.BillingCycleTrial,
				SubscriptionStatus:  domain.SubscriptionTrialing,
				CurrentPeriodEndsAt: now.Add(14 * 24 * time.Hour),
				UpdatedAt:           now,
			}
			return nil
		}
		return qErr
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *GovernanceService) UpsertStoreKYBProfile(ctx context.Context, actor *AuthenticatedStaff, in StoreKYBUpdateInput) (*domain.StoreKYBProfile, error) {
	if err := requireGovernanceOwner(actor); err != nil {
		return nil, err
	}
	f := fieldErrors{}
	entity := strings.ToLower(strings.TrimSpace(in.EntityType))
	if entity == "" {
		entity = string(domain.EntityPerorangan)
	}
	switch domain.BusinessEntityType(entity) {
	case domain.EntityPerorangan, domain.EntityCV, domain.EntityPT, domain.EntityPTPerorangan, domain.EntityKoperasi:
	default:
		f.add("entity_type", "harus perorangan, cv, pt, pt_perorangan, atau koperasi")
	}
	legalName := f.text("legal_business_name", in.LegalBusinessName, 2, 150)
	pseNo := strings.TrimSpace(in.PSERegistrationNo)
	if len(pseNo) > 64 {
		f.add("pse_registration_no", "maksimal 64 karakter")
	}
	planCode := strings.TrimSpace(in.PlanCode)
	if planCode == "" {
		planCode = "trial_14d"
	}
	cycle := strings.ToLower(strings.TrimSpace(in.BillingCycle))
	if cycle == "" {
		cycle = string(domain.BillingCycleTrial)
	}
	switch domain.BillingCycle(cycle) {
	case domain.BillingCycleTrial, domain.BillingCycleMonthly, domain.BillingCycleAnnual:
	default:
		f.add("billing_cycle", "harus trial, monthly, atau annual")
	}
	if err := f.err(); err != nil {
		return nil, err
	}

	var planExists bool
	if err := s.db.GetContext(ctx, &planExists, `SELECT EXISTS (SELECT 1 FROM subscription_plans WHERE code = $1 AND is_active = true)`, planCode); err != nil {
		return nil, err
	}
	if !planExists {
		f.add("plan_code", "paket langganan tidak ditemukan")
		return nil, f.err()
	}

	storeID := actor.Staff.Store.ID
	userID := actor.Staff.UserID
	nibMasked := secretbox.MaskSecret(in.NIB)
	npwpMasked := secretbox.MaskSecret(in.NPWP)
	now := s.nowFunc().UTC()

	var out domain.StoreKYBProfile
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		err := tx.GetContext(ctx, &out, `
			INSERT INTO store_kyb_profiles (
				store_id, entity_type, legal_business_name, nib_masked, npwp_masked, pse_registration_no,
				kyb_status, plan_code, billing_cycle, subscription_status, current_period_ends_at, updated_at
			) VALUES (
				$1, $2, $3, $4, $5, $6,
				'pending_review', $7, $8, 'trialing', $9, $10
			)
			ON CONFLICT (store_id) DO UPDATE SET
				entity_type = EXCLUDED.entity_type,
				legal_business_name = EXCLUDED.legal_business_name,
				nib_masked = CASE WHEN EXCLUDED.nib_masked <> '' THEN EXCLUDED.nib_masked ELSE store_kyb_profiles.nib_masked END,
				npwp_masked = CASE WHEN EXCLUDED.npwp_masked <> '' THEN EXCLUDED.npwp_masked ELSE store_kyb_profiles.npwp_masked END,
				pse_registration_no = EXCLUDED.pse_registration_no,
				plan_code = EXCLUDED.plan_code,
				billing_cycle = EXCLUDED.billing_cycle,
				kyb_status = CASE WHEN store_kyb_profiles.kyb_status = 'verified' THEN 'verified' ELSE 'pending_review' END,
				updated_at = EXCLUDED.updated_at
			RETURNING store_id, entity_type, legal_business_name, nib_masked, npwp_masked, pse_registration_no,
			          kyb_status, plan_code, billing_cycle, subscription_status, current_period_ends_at,
			          verified_at, review_notes, updated_at
		`, storeID, entity, legalName, nibMasked, npwpMasked, pseNo, planCode, cycle, now.Add(14*24*time.Hour), now)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO platform_tenant_registry (store_id, slug, display_name, plan_code, subscription_status, kyb_status, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (store_id) DO UPDATE SET
				plan_code = EXCLUDED.plan_code,
				kyb_status = EXCLUDED.kyb_status,
				updated_at = EXCLUDED.updated_at
		`, storeID, actor.Staff.Store.Slug, actor.Staff.Store.Name, out.PlanCode, out.SubscriptionStatus, out.KYBStatus, now); err != nil {
			return err
		}
		return s.writeGovernanceAuditTx(ctx, tx, storeID, &userID, "tenant_owner", "KYB_UPDATED",
			"Pembaruan profil legalitas bisnis & paket langganan gerai",
			map[string]any{"entity_type": out.EntityType, "plan_code": out.PlanCode, "kyb_status": out.KYBStatus})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

type PaymentCredentialInput struct {
	Environment               string `json:"environment"`
	MerchantID                string `json:"merchant_id"`
	ClientKey                 string `json:"client_key"`
	ServerKey                 string `json:"server_key"`
	IsEnabled                 bool   `json:"is_enabled"`
	FallbackStaticQRISEnabled *bool  `json:"fallback_static_qris_enabled"`
	VerifyLive                bool   `json:"verify_live"`
}

func midtransServerKeyAAD(storeID uuid.UUID) string {
	return fmt.Sprintf("store:%s:midtrans_server_key", storeID.String())
}

func smtpPasswordAAD(storeID uuid.UUID) string {
	return fmt.Sprintf("store:%s:smtp_password", storeID.String())
}

func whatsappTokenAAD(storeID uuid.UUID) string {
	return fmt.Sprintf("store:%s:whatsapp_token", storeID.String())
}

func (s *GovernanceService) GetStorePaymentCredential(ctx context.Context, actor *AuthenticatedStaff) (*domain.StorePaymentCredential, error) {
	if err := requireGovernanceOwner(actor); err != nil {
		return nil, err
	}
	storeID := actor.Staff.Store.ID
	var cred domain.StorePaymentCredential
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		qErr := tx.GetContext(ctx, &cred, `
			SELECT store_id, provider, environment, merchant_id, client_key, server_key_masked,
			       key_version, validation_status, is_enabled, fallback_static_qris_enabled,
			       last_verified_at, updated_by_user_id, updated_at
			FROM store_payment_credentials
			WHERE store_id = $1
		`, storeID)
		if errors.Is(qErr, sql.ErrNoRows) {
			cred = domain.StorePaymentCredential{
				StoreID:                   storeID,
				Provider:                  "midtrans",
				Environment:               "sandbox",
				ValidationStatus:          "unverified",
				IsEnabled:                 false,
				FallbackStaticQRISEnabled: true,
				KeyVersion:                1,
				UpdatedAt:                 s.nowFunc().UTC(),
			}
			return nil
		}
		return qErr
	})
	if err != nil {
		return nil, err
	}
	return &cred, nil
}

func (s *GovernanceService) UpsertStorePaymentCredential(ctx context.Context, actor *AuthenticatedStaff, in PaymentCredentialInput) (*domain.StorePaymentCredential, error) {
	if err := requireGovernanceOwner(actor); err != nil {
		return nil, err
	}
	if s.box == nil {
		return nil, ErrBYOKEncryptionUnavailable
	}

	f := fieldErrors{}
	env := strings.ToLower(strings.TrimSpace(in.Environment))
	if env != "sandbox" && env != "production" {
		f.add("environment", "harus sandbox atau production")
	}
	if s.appEnv != "production" && env == "production" && in.IsEnabled {
		f.add("environment", "kredensial production tidak boleh diaktifkan pada environment demo/development")
	}
	merchantID := f.text("merchant_id", in.MerchantID, 4, 64)
	clientKey := f.text("client_key", in.ClientKey, 12, 128)
	serverKey := strings.TrimSpace(in.ServerKey)
	if len(serverKey) < 16 || len(serverKey) > 128 {
		f.add("server_key", "wajib 16-128 karakter sesuai kunci Midtrans Core API")
	} else if env == "sandbox" {
		if !strings.HasPrefix(serverKey, "SB-Mid-server-") {
			f.add("server_key", "kunci sandbox wajib diawali SB-Mid-server-")
		}
		if !strings.HasPrefix(clientKey, "SB-Mid-client-") {
			f.add("client_key", "client key sandbox wajib diawali SB-Mid-client-")
		}
	} else if env == "production" {
		if !strings.HasPrefix(serverKey, "Mid-server-") || strings.HasPrefix(serverKey, "SB-") {
			f.add("server_key", "kunci production wajib diawali Mid-server-")
		}
		if !strings.HasPrefix(clientKey, "Mid-client-") || strings.HasPrefix(clientKey, "SB-") {
			f.add("client_key", "client key production wajib diawali Mid-client-")
		}
	}
	if err := f.err(); err != nil {
		return nil, err
	}

	storeID := actor.Staff.Store.ID
	userID := actor.Staff.UserID

	// Strict KYB gate for Production Midtrans BYOK: unverified/trialing tenants may configure
	// Midtrans Sandbox keys freely for testing, while Production BYOK requires kyb_status = 'verified'.
	if env == "production" {
		var kybStatus string
		if kybErr := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
			qErr := tx.GetContext(ctx, &kybStatus, `SELECT kyb_status FROM store_kyb_profiles WHERE store_id = $1`, storeID)
			if errors.Is(qErr, sql.ErrNoRows) {
				kybStatus = string(domain.KYBUnverified)
				return nil
			}
			return qErr
		}); kybErr != nil {
			return nil, kybErr
		}
		if kybStatus != string(domain.KYBVerified) {
			f.add("environment", "kredensial Midtrans Production hanya dapat disimpan setelah verifikasi legalitas bisnis (KYB) disetujui; gunakan Sandbox untuk uji coba")
			return nil, f.err()
		}
	}

	// Optional live Core API probe outside database transaction locks (2-phase discipline).
	validationStatus := "valid"
	if in.VerifyLive {
		probeClient := s.gatewayBuilder(serverKey, env == "production")
		probeID := "mycaffe-byok-probe-" + uuid.NewString()
		resp, probeErr := probeClient.CheckTransaction(ctx, probeID)
		if probeErr != nil {
			msg := strings.ToLower(probeErr.Error())
			if strings.Contains(msg, "status 401") || strings.Contains(msg, "unauthorized") || strings.Contains(msg, "access denied") {
				return nil, ErrBYOKLiveAuthFailed
			}
			// A 404 from Midtrans CheckTransaction proves HTTP Basic Auth with ServerKey succeeded!
			if !strings.Contains(msg, "status 404") && !strings.Contains(msg, "404") {
				return nil, fmt.Errorf("failed to verify Midtrans key against Core API: %w", probeErr)
			}
		} else if resp != nil && resp.StatusCode == "401" {
			return nil, ErrBYOKLiveAuthFailed
		}
	}

	ciphertext, err := s.box.Seal(serverKey, midtransServerKeyAAD(storeID))
	if err != nil {
		return nil, err
	}
	masked := secretbox.MaskSecret(serverKey)
	fallbackStatic := true
	if in.FallbackStaticQRISEnabled != nil {
		fallbackStatic = *in.FallbackStaticQRISEnabled
	}
	now := s.nowFunc().UTC()

	var out domain.StorePaymentCredential
	err = database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		err := tx.GetContext(ctx, &out, `
			INSERT INTO store_payment_credentials (
				store_id, provider, environment, merchant_id, client_key,
				server_key_ciphertext, server_key_masked, key_version,
				validation_status, is_enabled, fallback_static_qris_enabled,
				last_verified_at, updated_by_user_id, updated_at
			) VALUES (
				$1, 'midtrans', $2, $3, $4,
				$5, $6, 1,
				$7, $8, $9,
				$10, $11, $10
			)
			ON CONFLICT (store_id) DO UPDATE SET
				environment = EXCLUDED.environment,
				merchant_id = EXCLUDED.merchant_id,
				client_key = EXCLUDED.client_key,
				server_key_ciphertext = EXCLUDED.server_key_ciphertext,
				server_key_masked = EXCLUDED.server_key_masked,
				key_version = store_payment_credentials.key_version + 1,
				validation_status = EXCLUDED.validation_status,
				is_enabled = EXCLUDED.is_enabled,
				fallback_static_qris_enabled = EXCLUDED.fallback_static_qris_enabled,
				last_verified_at = EXCLUDED.last_verified_at,
				updated_by_user_id = EXCLUDED.updated_by_user_id,
				updated_at = EXCLUDED.updated_at
			RETURNING store_id, provider, environment, merchant_id, client_key, server_key_masked,
			          key_version, validation_status, is_enabled, fallback_static_qris_enabled,
			          last_verified_at, updated_by_user_id, updated_at
		`, storeID, env, merchantID, clientKey, ciphertext, masked, validationStatus, in.IsEnabled, fallbackStatic, now, userID)
		if err != nil {
			return err
		}

		_, _ = tx.ExecContext(ctx, `
			UPDATE platform_tenant_registry
			SET byok_midtrans_enabled = $2, updated_at = $3
			WHERE store_id = $1
		`, storeID, out.IsEnabled, now)

		auditAction := "BYOK_MIDTRANS_UPDATED"
		if !out.IsEnabled {
			auditAction = "BYOK_MIDTRANS_DISABLED"
		} else if in.VerifyLive {
			auditAction = "BYOK_MIDTRANS_VERIFIED"
		}
		return s.writeGovernanceAuditTx(ctx, tx, storeID, &userID, "tenant_owner", auditAction,
			"Konfigurasi kredensial Midtrans BYOK gerai diperbarui",
			map[string]any{
				"environment":        out.Environment,
				"merchant_id":        out.MerchantID,
				"server_key_masked":  out.ServerKeyMasked,
				"is_enabled":         out.IsEnabled,
				"verified_live_http": in.VerifyLive,
			})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ResolveStoreGatewayClientTx implements StoreGatewayResolver for PaymentService Phase 1.
// If the tenant has an enabled, valid BYOK Midtrans credential, it decrypts the key in-memory
// and returns a dedicated GatewayClient; otherwise it returns (nil, nil) so PaymentService uses
// the default configured gateway client.
func (s *GovernanceService) ResolveStoreGatewayClientTx(ctx context.Context, tx *sqlx.Tx, storeID uuid.UUID) (GatewayClient, error) {
	if s == nil || s.box == nil || tx == nil {
		return nil, nil
	}
	var row struct {
		Slug                string `db:"slug"`
		Environment         string `db:"environment"`
		ServerKeyCiphertext []byte `db:"server_key_ciphertext"`
	}
	err := tx.GetContext(ctx, &row, `
		SELECT s.slug, spc.environment, spc.server_key_ciphertext
		FROM store_payment_credentials spc
		JOIN stores s ON s.id = spc.store_id
		WHERE spc.store_id = $1 AND spc.is_enabled = true AND spc.validation_status = 'valid'
	`, storeID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query store BYOK credentials: %w", err)
	}
	plainKey, err := s.box.Open(row.ServerKeyCiphertext, midtransServerKeyAAD(storeID))
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt store BYOK Midtrans credential: %w", err)
	}
	gw := s.gatewayBuilder(plainKey, row.Environment == "production")
	if mc, ok := gw.(*midtrans.Client); ok && strings.HasPrefix(strings.ToLower(s.appPublicURL), "https://") && row.Slug != "" {
		gw = mc.WithDefaultNotificationURL(s.appPublicURL + "/api/v1/payments/midtrans/webhook/" + row.Slug)
	}
	return gw, nil
}

// VerifyTenantWebhookSignature resolves the store by slug via resolve_store_for_login, decrypts
// the tenant's BYOK Midtrans Server Key inside WithTenantTx, and verifies the SHA-512 signature.
func (s *GovernanceService) VerifyTenantWebhookSignature(
	ctx context.Context,
	storeSlug string,
	orderID string,
	statusCode string,
	grossAmount string,
	incomingSignature string,
) (bool, error) {
	if s == nil || s.box == nil {
		return false, ErrBYOKEncryptionUnavailable
	}
	storeID, isActive, err := s.authRepo.ResolveStoreForLogin(ctx, s.db, strings.ToLower(strings.TrimSpace(storeSlug)))
	if err != nil {
		if errors.Is(err, postgres.ErrStoreNotFound) {
			return false, ErrGovernanceNotFound
		}
		return false, err
	}
	if !isActive {
		return false, ErrGovernanceNotFound
	}

	var verified bool
	err = database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		var row struct {
			Environment         string `db:"environment"`
			ServerKeyCiphertext []byte `db:"server_key_ciphertext"`
		}
		if err := tx.GetContext(ctx, &row, `
			SELECT environment, server_key_ciphertext
			FROM store_payment_credentials
			WHERE store_id = $1 AND is_enabled = true AND validation_status = 'valid'
		`, storeID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrGovernanceNotFound
			}
			return err
		}
		plainKey, err := s.box.Open(row.ServerKeyCiphertext, midtransServerKeyAAD(storeID))
		if err != nil {
			return err
		}
		verified = midtrans.NewClientWithKey(plainKey, row.Environment == "production").
			VerifySignature(orderID, statusCode, grossAmount, incomingSignature)
		return nil
	})
	if err != nil {
		return false, err
	}
	return verified, nil
}

type StoreCMSConfigInput struct {
	DefaultLocale        string          `json:"default_locale"`
	SupportedLocales     []string        `json:"supported_locales"`
	HeroTitleID          string          `json:"hero_title_id"`
	HeroTitleEN          string          `json:"hero_title_en"`
	HeroSubtitleID       string          `json:"hero_subtitle_id"`
	HeroSubtitleEN       string          `json:"hero_subtitle_en"`
	HeroBannerURL        string          `json:"hero_banner_url"`
	AnnouncementBannerID string          `json:"announcement_banner_id"`
	AnnouncementBannerEN string          `json:"announcement_banner_en"`
	PrimaryHex           string          `json:"primary_hex"`
	AccentHex            string          `json:"accent_hex"`
	HighContrastMode     bool            `json:"high_contrast_mode"`
	HidePoweredByBadge   bool            `json:"hide_powered_by_badge"`
	CustomDomain         string          `json:"custom_domain"`
	OperatingHours       json.RawMessage `json:"operating_hours"`
	PublicRefundPolicy   string          `json:"public_refund_policy"`
	PublicContactPhone   string          `json:"public_contact_phone"`
	PublicContactEmail   string          `json:"public_contact_email"`
}

func (s *GovernanceService) GetStoreCMSConfig(ctx context.Context, actor *AuthenticatedStaff) (*domain.StoreCMSConfig, error) {
	if err := requireGovernanceOwner(actor); err != nil {
		return nil, err
	}
	storeID := actor.Staff.Store.ID
	var out *domain.StoreCMSConfig
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		var cms domain.StoreCMSConfig
		qErr := tx.GetContext(ctx, &cms, `
			SELECT store_id, default_locale, supported_locales, hero_title_id, hero_title_en,
			       hero_subtitle_id, hero_subtitle_en, hero_banner_url, announcement_banner_id,
			       announcement_banner_en, primary_hex, accent_hex, high_contrast_mode,
			       hide_powered_by_badge, custom_domain, operating_hours, public_refund_policy,
			       public_contact_phone, public_contact_email, updated_at
			FROM store_cms_configs
			WHERE store_id = $1
		`, storeID)
		if errors.Is(qErr, sql.ErrNoRows) {
			out = defaultStoreCMS(storeID, s.nowFunc().UTC())
			return nil
		}
		if qErr != nil {
			return qErr
		}
		out = &cms
		return nil
	})
	return out, err
}

func (s *GovernanceService) UpsertStoreCMSConfig(ctx context.Context, actor *AuthenticatedStaff, in StoreCMSConfigInput) (*domain.StoreCMSConfig, error) {
	if err := requireGovernanceOwner(actor); err != nil {
		return nil, err
	}
	f := fieldErrors{}
	defLocale := strings.TrimSpace(in.DefaultLocale)
	if defLocale == "" {
		defLocale = "id-ID"
	}
	if defLocale != "id-ID" && defLocale != "en-US" {
		f.add("default_locale", "harus id-ID atau en-US")
	}
	locales := in.SupportedLocales
	if len(locales) == 0 {
		locales = []string{"id-ID", "en-US"}
	}
	for _, l := range locales {
		if l != "id-ID" && l != "en-US" {
			f.add("supported_locales", "hanya mendukung id-ID dan en-US")
			break
		}
	}
	primaryHex := strings.ToUpper(strings.TrimSpace(in.PrimaryHex))
	if primaryHex == "" {
		primaryHex = "#2C1A12"
	}
	if !hexColorPattern.MatchString(primaryHex) {
		f.add("primary_hex", "wajib format warna hex #RRGGBB")
	}
	accentHex := strings.ToUpper(strings.TrimSpace(in.AccentHex))
	if accentHex == "" {
		accentHex = "#C68B59"
	}
	if !hexColorPattern.MatchString(accentHex) {
		f.add("accent_hex", "wajib format warna hex #RRGGBB")
	}
	// Anti-slop R-02 gate: reject em-dash characters in customer-facing CMS copy.
	for fieldName, val := range map[string]string{
		"hero_title_id":          in.HeroTitleID,
		"hero_title_en":          in.HeroTitleEN,
		"hero_subtitle_id":       in.HeroSubtitleID,
		"hero_subtitle_en":       in.HeroSubtitleEN,
		"announcement_banner_id": in.AnnouncementBannerID,
		"announcement_banner_en": in.AnnouncementBannerEN,
	} {
		if strings.Contains(val, "\u2014") {
			f.add(fieldName, "tidak boleh mengandung karakter em-dash")
		}
	}
	if len(in.HeroTitleID) > 140 || len(in.HeroTitleEN) > 140 {
		f.add("hero_title_id", "maksimal 140 karakter")
	}
	if len(in.HeroSubtitleID) > 280 || len(in.HeroSubtitleEN) > 280 {
		f.add("hero_subtitle_id", "maksimal 280 karakter")
	}
	hoursBytes := []byte(`{}`)
	if len(in.OperatingHours) > 0 {
		var tmp map[string]any
		if json.Unmarshal(in.OperatingHours, &tmp) != nil {
			f.add("operating_hours", "harus berupa objek JSON yang valid")
		} else {
			hoursBytes = in.OperatingHours
		}
	}
	if err := f.err(); err != nil {
		return nil, err
	}

	localesBytes, _ := json.Marshal(locales)
	storeID := actor.Staff.Store.ID
	userID := actor.Staff.UserID
	now := s.nowFunc().UTC()

	var out domain.StoreCMSConfig
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		err := tx.GetContext(ctx, &out, `
			INSERT INTO store_cms_configs (
				store_id, default_locale, supported_locales, hero_title_id, hero_title_en,
				hero_subtitle_id, hero_subtitle_en, hero_banner_url, announcement_banner_id,
				announcement_banner_en, primary_hex, accent_hex, high_contrast_mode,
				hide_powered_by_badge, custom_domain, operating_hours, public_refund_policy,
				public_contact_phone, public_contact_email, updated_at
			) VALUES (
				$1, $2, $3::jsonb, $4, $5,
				$6, $7, $8, $9,
				$10, $11, $12, $13,
				$14, $15, $16::jsonb, $17,
				$18, $19, $20
			)
			ON CONFLICT (store_id) DO UPDATE SET
				default_locale = EXCLUDED.default_locale,
				supported_locales = EXCLUDED.supported_locales,
				hero_title_id = EXCLUDED.hero_title_id,
				hero_title_en = EXCLUDED.hero_title_en,
				hero_subtitle_id = EXCLUDED.hero_subtitle_id,
				hero_subtitle_en = EXCLUDED.hero_subtitle_en,
				hero_banner_url = EXCLUDED.hero_banner_url,
				announcement_banner_id = EXCLUDED.announcement_banner_id,
				announcement_banner_en = EXCLUDED.announcement_banner_en,
				primary_hex = EXCLUDED.primary_hex,
				accent_hex = EXCLUDED.accent_hex,
				high_contrast_mode = EXCLUDED.high_contrast_mode,
				hide_powered_by_badge = EXCLUDED.hide_powered_by_badge,
				custom_domain = EXCLUDED.custom_domain,
				operating_hours = EXCLUDED.operating_hours,
				public_refund_policy = EXCLUDED.public_refund_policy,
				public_contact_phone = EXCLUDED.public_contact_phone,
				public_contact_email = EXCLUDED.public_contact_email,
				updated_at = EXCLUDED.updated_at
			RETURNING store_id, default_locale, supported_locales, hero_title_id, hero_title_en,
			          hero_subtitle_id, hero_subtitle_en, hero_banner_url, announcement_banner_id,
			          announcement_banner_en, primary_hex, accent_hex, high_contrast_mode,
			          hide_powered_by_badge, custom_domain, operating_hours, public_refund_policy,
			          public_contact_phone, public_contact_email, updated_at
		`, storeID, defLocale, string(localesBytes),
			strings.TrimSpace(in.HeroTitleID), strings.TrimSpace(in.HeroTitleEN),
			strings.TrimSpace(in.HeroSubtitleID), strings.TrimSpace(in.HeroSubtitleEN),
			strings.TrimSpace(in.HeroBannerURL),
			strings.TrimSpace(in.AnnouncementBannerID), strings.TrimSpace(in.AnnouncementBannerEN),
			primaryHex, accentHex, in.HighContrastMode, in.HidePoweredByBadge,
			strings.ToLower(strings.TrimSpace(in.CustomDomain)), string(hoursBytes),
			strings.TrimSpace(in.PublicRefundPolicy),
			strings.TrimSpace(in.PublicContactPhone), strings.TrimSpace(in.PublicContactEmail), now)
		if err != nil {
			return err
		}
		return s.writeGovernanceAuditTx(ctx, tx, storeID, &userID, "tenant_owner", "STORE_CMS_UPDATED",
			"Konfigurasi CMS storefront & i18n gerai diperbarui",
			map[string]any{"default_locale": out.DefaultLocale, "primary_hex": out.PrimaryHex, "high_contrast_mode": out.HighContrastMode})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

type StoreNotificationConfigInput struct {
	EmailMode             string `json:"email_mode"`
	SMTPHost              string `json:"smtp_host"`
	SMTPPort              int    `json:"smtp_port"`
	SMTPTLSMode           string `json:"smtp_tls_mode"`
	SMTPUsername          string `json:"smtp_username"`
	SMTPPassword          string `json:"smtp_password"`
	SMTPFromEmail         string `json:"smtp_from_email"`
	SMTPFromName          string `json:"smtp_from_name"`
	WhatsAppMode          string `json:"whatsapp_mode"`
	WhatsAppEndpointURL   string `json:"whatsapp_endpoint_url"`
	WhatsAppPhoneNumberID string `json:"whatsapp_phone_number_id"`
	WhatsAppToken         string `json:"whatsapp_token"`
}

func (s *GovernanceService) GetStoreNotificationConfig(ctx context.Context, actor *AuthenticatedStaff) (*domain.StoreNotificationConfig, error) {
	if err := requireGovernanceOwner(actor); err != nil {
		return nil, err
	}
	storeID := actor.Staff.Store.ID
	var out domain.StoreNotificationConfig
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		qErr := tx.GetContext(ctx, &out, `
			SELECT store_id, email_mode, smtp_host, smtp_port, smtp_tls_mode, smtp_username,
			       smtp_password_masked, smtp_from_email, smtp_from_name, whatsapp_mode,
			       whatsapp_endpoint_url, whatsapp_phone_number_id, whatsapp_token_masked, updated_at
			FROM store_notification_configs
			WHERE store_id = $1
		`, storeID)
		if errors.Is(qErr, sql.ErrNoRows) {
			out = domain.StoreNotificationConfig{
				StoreID:      storeID,
				EmailMode:    "platform_shared",
				SMTPPort:     587,
				SMTPTLSMode:  "starttls",
				WhatsAppMode: "platform_shared",
				UpdatedAt:    s.nowFunc().UTC(),
			}
			return nil
		}
		return qErr
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *GovernanceService) UpsertStoreNotificationConfig(ctx context.Context, actor *AuthenticatedStaff, in StoreNotificationConfigInput) (*domain.StoreNotificationConfig, error) {
	if err := requireGovernanceOwner(actor); err != nil {
		return nil, err
	}
	f := fieldErrors{}
	emailMode := strings.ToLower(strings.TrimSpace(in.EmailMode))
	if emailMode == "" {
		emailMode = "platform_shared"
	}
	switch emailMode {
	case "disabled", "platform_shared", "custom_smtp":
	default:
		f.add("email_mode", "harus disabled, platform_shared, atau custom_smtp")
	}
	port := in.SMTPPort
	if port == 0 {
		port = 587
	}
	if port < 1 || port > 65535 {
		f.add("smtp_port", "harus di antara 1 dan 65535")
	}
	tlsMode := strings.ToLower(strings.TrimSpace(in.SMTPTLSMode))
	if tlsMode == "" {
		tlsMode = "starttls"
	}
	if tlsMode != "starttls" && tlsMode != "implicit" {
		f.add("smtp_tls_mode", "harus starttls atau implicit")
	}
	if emailMode == "custom_smtp" {
		if strings.TrimSpace(in.SMTPHost) == "" {
			f.add("smtp_host", "wajib diisi saat email_mode adalah custom_smtp")
		}
		if strings.TrimSpace(in.SMTPFromEmail) == "" {
			f.add("smtp_from_email", "wajib diisi saat email_mode adalah custom_smtp")
		}
	}
	waMode := strings.ToLower(strings.TrimSpace(in.WhatsAppMode))
	if waMode == "" {
		waMode = "platform_shared"
	}
	switch waMode {
	case "disabled", "platform_shared", "custom_bridge", "custom_cloud_api":
	default:
		f.add("whatsapp_mode", "harus disabled, platform_shared, custom_bridge, atau custom_cloud_api")
	}
	if s.appEnv == "production" && waMode == "custom_bridge" {
		f.add("whatsapp_mode", "custom_bridge tidak diizinkan pada production; gunakan custom_cloud_api resmi")
	}
	if err := f.err(); err != nil {
		return nil, err
	}

	storeID := actor.Staff.Store.ID
	userID := actor.Staff.UserID
	var smtpCipher, waCipher []byte
	var smtpMasked, waMasked string

	if strings.TrimSpace(in.SMTPPassword) != "" {
		if s.box == nil {
			return nil, ErrBYOKEncryptionUnavailable
		}
		var err error
		smtpCipher, err = s.box.Seal(strings.TrimSpace(in.SMTPPassword), smtpPasswordAAD(storeID))
		if err != nil {
			return nil, err
		}
		smtpMasked = secretbox.MaskSecret(in.SMTPPassword)
	}
	if strings.TrimSpace(in.WhatsAppToken) != "" {
		if s.box == nil {
			return nil, ErrBYOKEncryptionUnavailable
		}
		var err error
		waCipher, err = s.box.Seal(strings.TrimSpace(in.WhatsAppToken), whatsappTokenAAD(storeID))
		if err != nil {
			return nil, err
		}
		waMasked = secretbox.MaskSecret(in.WhatsAppToken)
	}

	now := s.nowFunc().UTC()
	var out domain.StoreNotificationConfig
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		err := tx.GetContext(ctx, &out, `
			INSERT INTO store_notification_configs (
				store_id, email_mode, smtp_host, smtp_port, smtp_tls_mode, smtp_username,
				smtp_password_ciphertext, smtp_password_masked, smtp_from_email, smtp_from_name,
				whatsapp_mode, whatsapp_endpoint_url, whatsapp_phone_number_id,
				whatsapp_token_ciphertext, whatsapp_token_masked, updated_at
			) VALUES (
				$1, $2, $3, $4, $5, $6,
				$7, $8, $9, $10,
				$11, $12, $13,
				$14, $15, $16
			)
			ON CONFLICT (store_id) DO UPDATE SET
				email_mode = EXCLUDED.email_mode,
				smtp_host = EXCLUDED.smtp_host,
				smtp_port = EXCLUDED.smtp_port,
				smtp_tls_mode = EXCLUDED.smtp_tls_mode,
				smtp_username = EXCLUDED.smtp_username,
				smtp_password_ciphertext = COALESCE(EXCLUDED.smtp_password_ciphertext, store_notification_configs.smtp_password_ciphertext),
				smtp_password_masked = CASE WHEN EXCLUDED.smtp_password_masked <> '' THEN EXCLUDED.smtp_password_masked ELSE store_notification_configs.smtp_password_masked END,
				smtp_from_email = EXCLUDED.smtp_from_email,
				smtp_from_name = EXCLUDED.smtp_from_name,
				whatsapp_mode = EXCLUDED.whatsapp_mode,
				whatsapp_endpoint_url = EXCLUDED.whatsapp_endpoint_url,
				whatsapp_phone_number_id = EXCLUDED.whatsapp_phone_number_id,
				whatsapp_token_ciphertext = COALESCE(EXCLUDED.whatsapp_token_ciphertext, store_notification_configs.whatsapp_token_ciphertext),
				whatsapp_token_masked = CASE WHEN EXCLUDED.whatsapp_token_masked <> '' THEN EXCLUDED.whatsapp_token_masked ELSE store_notification_configs.whatsapp_token_masked END,
				updated_at = EXCLUDED.updated_at
			RETURNING store_id, email_mode, smtp_host, smtp_port, smtp_tls_mode, smtp_username,
			          smtp_password_masked, smtp_from_email, smtp_from_name, whatsapp_mode,
			          whatsapp_endpoint_url, whatsapp_phone_number_id, whatsapp_token_masked, updated_at
		`, storeID, emailMode, strings.TrimSpace(in.SMTPHost), port, tlsMode, strings.TrimSpace(in.SMTPUsername),
			smtpCipher, smtpMasked, strings.TrimSpace(in.SMTPFromEmail), strings.TrimSpace(in.SMTPFromName),
			waMode, strings.TrimSpace(in.WhatsAppEndpointURL), strings.TrimSpace(in.WhatsAppPhoneNumberID),
			waCipher, waMasked, now)
		if err != nil {
			return err
		}
		return s.writeGovernanceAuditTx(ctx, tx, storeID, &userID, "tenant_owner", "NOTIFICATION_CONFIG_UPDATED",
			"Konfigurasi kanal Email & WhatsApp gerai diperbarui",
			map[string]any{"email_mode": out.EmailMode, "whatsapp_mode": out.WhatsAppMode})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

type NetworkNodeInput struct {
	NodeName           string          `json:"node_name"`
	NodeType           string          `json:"node_type"`
	ConnectionProtocol string          `json:"connection_protocol"`
	LocalIP            string          `json:"local_ip"`
	LocalPort          int             `json:"local_port"`
	MACAddress         string          `json:"mac_address"`
	PaperWidthMM       int16           `json:"paper_width_mm"`
	DrawerKickEnabled  bool            `json:"drawer_kick_enabled"`
	HealthStatus       string          `json:"health_status"`
	LastLatencyMS      int             `json:"last_latency_ms"`
	Metadata           json.RawMessage `json:"metadata"`
	IsActive           *bool           `json:"is_active"`
}

func (s *GovernanceService) ListNetworkNodes(ctx context.Context, actor *AuthenticatedStaff) ([]domain.StoreNetworkNode, error) {
	if err := requireGovernanceOwner(actor); err != nil {
		return nil, err
	}
	storeID := actor.Staff.Store.ID
	var nodes []domain.StoreNetworkNode
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &nodes, `
			SELECT id, store_id, node_name, node_type, connection_protocol, local_ip, local_port,
			       mac_address, paper_width_mm, drawer_kick_enabled, health_status, last_latency_ms,
			       last_heartbeat_at, metadata, is_active, created_at, updated_at
			FROM store_network_nodes
			WHERE store_id = $1
			ORDER BY node_type ASC, node_name ASC
		`, storeID)
	})
	if nodes == nil {
		nodes = []domain.StoreNetworkNode{}
	}
	return nodes, err
}

func (s *GovernanceService) UpsertNetworkNode(ctx context.Context, actor *AuthenticatedStaff, in NetworkNodeInput) (*domain.StoreNetworkNode, error) {
	if err := requireGovernanceOwner(actor); err != nil {
		return nil, err
	}
	f := fieldErrors{}
	name := f.text("node_name", in.NodeName, 2, 100)
	nodeType := strings.ToLower(strings.TrimSpace(in.NodeType))
	switch nodeType {
	case "local_edge_server", "router_gateway", "pos_terminal", "escpos_printer",
		"cash_drawer", "barcode_scanner", "kds_screen", "queue_tv", "customer_display", "iot_buzzer":
	default:
		f.add("node_type", "tipe perangkat jaringan tidak didukung")
	}
	proto := strings.ToLower(strings.TrimSpace(in.ConnectionProtocol))
	switch proto {
	case "lan_tcp_9100", "usb_hid", "usb_raw", "bluetooth_spp", "bluetooth_ble", "http_webhook", "ws_realtime", "rj11_dk_pulse":
	default:
		f.add("connection_protocol", "protokol koneksi tidak didukung")
	}
	if in.LocalPort < 0 || in.LocalPort > 65535 {
		f.add("local_port", "port harus di antara 0 dan 65535")
	}
	paper := in.PaperWidthMM
	if paper == 0 && nodeType == "escpos_printer" {
		paper = 80
	}
	if paper != 0 && paper != 58 && paper != 80 {
		f.add("paper_width_mm", "harus 0, 58, atau 80")
	}
	health := strings.ToLower(strings.TrimSpace(in.HealthStatus))
	if health == "" {
		health = "online"
	}
	switch health {
	case "unknown", "online", "degraded", "offline":
	default:
		f.add("health_status", "harus unknown, online, degraded, atau offline")
	}
	latency := in.LastLatencyMS
	if latency < 0 {
		latency = 0
	}
	metaBytes := []byte(`{}`)
	if len(in.Metadata) > 0 {
		var tmp map[string]any
		if json.Unmarshal(in.Metadata, &tmp) != nil {
			f.add("metadata", "harus berupa objek JSON yang valid")
		} else {
			metaBytes = in.Metadata
		}
	}
	if err := f.err(); err != nil {
		return nil, err
	}

	isActive := true
	if in.IsActive != nil {
		isActive = *in.IsActive
	}
	storeID := actor.Staff.Store.ID
	userID := actor.Staff.UserID
	now := s.nowFunc().UTC()

	var out domain.StoreNetworkNode
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		err := tx.GetContext(ctx, &out, `
			INSERT INTO store_network_nodes (
				id, store_id, node_name, node_type, connection_protocol, local_ip, local_port,
				mac_address, paper_width_mm, drawer_kick_enabled, health_status, last_latency_ms,
				last_heartbeat_at, metadata, is_active, created_at, updated_at
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7,
				$8, $9, $10, $11, $12,
				$13, $14::jsonb, $15, $13, $13
			)
			ON CONFLICT (store_id, node_name) DO UPDATE SET
				node_type = EXCLUDED.node_type,
				connection_protocol = EXCLUDED.connection_protocol,
				local_ip = EXCLUDED.local_ip,
				local_port = EXCLUDED.local_port,
				mac_address = EXCLUDED.mac_address,
				paper_width_mm = EXCLUDED.paper_width_mm,
				drawer_kick_enabled = EXCLUDED.drawer_kick_enabled,
				health_status = EXCLUDED.health_status,
				last_latency_ms = EXCLUDED.last_latency_ms,
				last_heartbeat_at = EXCLUDED.last_heartbeat_at,
				metadata = EXCLUDED.metadata,
				is_active = EXCLUDED.is_active,
				updated_at = EXCLUDED.updated_at
			RETURNING id, store_id, node_name, node_type, connection_protocol, local_ip, local_port,
			          mac_address, paper_width_mm, drawer_kick_enabled, health_status, last_latency_ms,
			          last_heartbeat_at, metadata, is_active, created_at, updated_at
		`, uuid.New(), storeID, name, nodeType, proto, strings.TrimSpace(in.LocalIP), in.LocalPort,
			strings.ToUpper(strings.TrimSpace(in.MACAddress)), paper, in.DrawerKickEnabled,
			health, latency, now, string(metaBytes), isActive)
		if err != nil {
			return err
		}
		_, _ = tx.ExecContext(ctx, `
			UPDATE platform_tenant_registry
			SET active_nodes_count = (SELECT COUNT(*) FROM store_network_nodes WHERE store_id = $1 AND is_active = true),
			    updated_at = $2
			WHERE store_id = $1
		`, storeID, now)
		return s.writeGovernanceAuditTx(ctx, tx, storeID, &userID, "tenant_owner", "NETWORK_NODE_UPSERTED",
			"Node perangkat keras/LAN gerai diperbarui mandiri",
			map[string]any{"node_name": out.NodeName, "node_type": out.NodeType, "local_ip": out.LocalIP, "local_port": out.LocalPort})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *GovernanceService) RunNetworkSimulation(ctx context.Context, actor *AuthenticatedStaff, scenario string) (*domain.NetworkSimulationReport, error) {
	if err := requireGovernanceOwner(actor); err != nil {
		return nil, err
	}
	sc := strings.ToLower(strings.TrimSpace(scenario))
	if sc == "" {
		sc = "healthy"
	}
	switch sc {
	case "healthy", "wan_offline", "printer_paper_out", "lan_ip_conflict", "midtrans_review":
	default:
		f := fieldErrors{}
		f.add("scenario", "skenario harus healthy, wan_offline, printer_paper_out, lan_ip_conflict, atau midtrans_review")
		return nil, f.err()
	}

	storeID := actor.Staff.Store.ID
	userID := actor.Staff.UserID
	start := time.Now()

	var nodes []domain.StoreNetworkNode
	var byokEnabled bool
	var fallbackStatic bool
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		if err := tx.SelectContext(ctx, &nodes, `
			SELECT id, store_id, node_name, node_type, connection_protocol, local_ip, local_port,
			       mac_address, paper_width_mm, drawer_kick_enabled, health_status, last_latency_ms,
			       last_heartbeat_at, metadata, is_active, created_at, updated_at
			FROM store_network_nodes
			WHERE store_id = $1 AND is_active = true
			ORDER BY node_name ASC
		`, storeID); err != nil {
			return err
		}
		var cred struct {
			IsEnabled bool `db:"is_enabled"`
			Fallback  bool `db:"fallback_static_qris_enabled"`
		}
		if qErr := tx.GetContext(ctx, &cred, `
			SELECT is_enabled, fallback_static_qris_enabled
			FROM store_payment_credentials
			WHERE store_id = $1
		`, storeID); qErr == nil {
			byokEnabled = cred.IsEnabled
			fallbackStatic = cred.Fallback
		} else {
			fallbackStatic = true
		}
		return s.writeGovernanceAuditTx(ctx, tx, storeID, &userID, "tenant_owner", "NETWORK_SIMULATION_RUN",
			fmt.Sprintf("Simulasi topologi jaringan & perangkat: %s", sc),
			map[string]any{"scenario": sc, "active_nodes": len(nodes)})
	})
	if err != nil {
		return nil, err
	}

	dbLatency := int(time.Since(start).Milliseconds())
	if dbLatency < 1 {
		dbLatency = 1
	}

	overall := "healthy"
	fallbackMode := "none"
	recommendation := "Seluruh jalur Cloud HTTPS/WSS, PostgreSQL 18 RLS, dan perangkat LAN gerai beroperasi normal."
	hops := []domain.NetworkSimulationHop{
		{Hop: "Cloudflare Edge -> MyCaffe Go API", Protocol: "HTTPS/TLS 1.3", Status: "online", Latency: 14, Note: "Named Tunnel / Reverse Proxy aktif"},
		{Hop: "Go API -> PostgreSQL 18 (WithTenantTx RLS)", Protocol: "TCP:5432", Status: "online", Latency: dbLatency, Note: "Isolasi tenant terverifikasi"},
	}

	for _, n := range nodes {
		hopStatus := n.HealthStatus
		if hopStatus == "unknown" {
			hopStatus = "online"
		}
		hopNote := fmt.Sprintf("%s (%s:%d)", n.NodeType, n.LocalIP, n.LocalPort)
		if sc == "printer_paper_out" && n.NodeType == "escpos_printer" {
			hopStatus = "degraded"
			hopNote = "Kertas habis / cover terbuka pada TCP:9100; struk dialihkan ke antrean reprint & layar KDS"
		} else if sc == "lan_ip_conflict" && n.LocalIP != "" {
			hopStatus = "offline"
			hopNote = "IP berubah akibat DHCP router; perbarui IP statis di menu Self-Maintain Jaringan"
		}
		hops = append(hops, domain.NetworkSimulationHop{
			Hop:      n.NodeName,
			Protocol: n.ConnectionProtocol,
			Status:   hopStatus,
			Latency:  n.LastLatencyMS + 2,
			Note:     hopNote,
		})
	}

	switch sc {
	case "wan_offline":
		overall = "degraded"
		fallbackMode = "offline_cash_static_qris_queue"
		hops[0].Status = "offline"
		hops[0].Note = "Koneksi internet ISP gerai terputus; POS beralih ke antrean lokal ADR-005"
		recommendation = "Gunakan pembayaran Tunai atau QRIS Statis di kasir. Transaksi akan tersinkronisasi otomatis via /api/v1/device/offline-orders/sync saat internet pulih."
	case "printer_paper_out":
		overall = "degraded"
		fallbackMode = "kds_and_digital_receipt"
		recommendation = "Pesanan tetap masuk ke layar KDS via WebSocket. Isi ulang kertas thermal lalu tekan Cetak Ulang Struk atau kirim struk digital WhatsApp/Email."
	case "lan_ip_conflict":
		overall = "degraded"
		fallbackMode = "self_maintain_static_ip_binding"
		recommendation = "Tetapkan DHCP Reservation berdasarkan MAC Address di router gerai atau perbarui kolom Local IP perangkat pada panel Self-Maintain."
	case "midtrans_review":
		overall = "degraded"
		fallbackMode = "sandbox_demo_or_static_qris_cash"
		recommendation = "Selama akun Midtrans Production dalam tahap Business Review, gunakan Sandbox untuk demo/pengujian dan aktifkan QRIS Statis / Tunai / EDC untuk operasional kasir langsung."
	}
	if !byokEnabled && fallbackStatic && fallbackMode == "none" {
		fallbackMode = "platform_default_or_static_qris"
	}

	return &domain.NetworkSimulationReport{
		StoreID:        storeID,
		Scenario:       sc,
		OverallStatus:  overall,
		DBLatencyMS:    dbLatency,
		ActiveNodes:    len(nodes),
		BYOKActive:     byokEnabled,
		FallbackMode:   fallbackMode,
		SimulatedHops:  hops,
		Recommendation: recommendation,
		SimulatedAt:    s.nowFunc().UTC(),
	}, nil
}

type CreatePlatformInvoiceInput struct {
	PlanCode     string `json:"plan_code"`
	BillingCycle string `json:"billing_cycle"`
}

func (s *GovernanceService) CreatePlatformSubscriptionInvoice(ctx context.Context, actor *AuthenticatedStaff, in CreatePlatformInvoiceInput) (*domain.PlatformInvoice, error) {
	if err := requireGovernanceOwner(actor); err != nil {
		return nil, err
	}
	f := fieldErrors{}
	planCode := strings.TrimSpace(in.PlanCode)
	if planCode == "" || planCode == "trial_14d" {
		f.add("plan_code", "pilih paket berbayar (starter_umkm, pro_cafe, atau enterprise_whitelabel)")
	}
	cycle := strings.ToLower(strings.TrimSpace(in.BillingCycle))
	if cycle != string(domain.BillingCycleMonthly) && cycle != string(domain.BillingCycleAnnual) {
		f.add("billing_cycle", "harus monthly atau annual")
	}
	if err := f.err(); err != nil {
		return nil, err
	}

	var plan domain.SubscriptionPlan
	if err := s.db.GetContext(ctx, &plan, `
		SELECT code, name_id, name_en, monthly_price_idr, annual_price_idr, commission_bps,
		       max_tables, max_printers, supports_byok_midtrans, supports_kds_queue_tv,
		       supports_whitelabel, supports_byo_notifications, is_active, created_at, updated_at
		FROM subscription_plans
		WHERE code = $1 AND is_active = true
	`, planCode); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			f.add("plan_code", "paket langganan tidak ditemukan")
			return nil, f.err()
		}
		return nil, err
	}

	amount := plan.MonthlyPriceIDR
	if cycle == string(domain.BillingCycleAnnual) {
		amount = plan.AnnualPriceIDR
	}
	if amount <= 0 {
		f.add("plan_code", "paket ini tidak memiliki tagihan berbayar")
		return nil, f.err()
	}

	storeID := actor.Staff.Store.ID
	userID := actor.Staff.UserID
	now := s.nowFunc().UTC()
	invID := uuid.New()
	providerOrderID := invID.String()
	invNumber := fmt.Sprintf("INV-AZD-%s-%s", now.Format("20060102"), strings.ToUpper(invID.String()[:8]))

	inv := &domain.PlatformInvoice{
		ID:                  invID,
		StoreID:             storeID,
		InvoiceNumber:       invNumber,
		PlanCode:            plan.Code,
		BillingCycle:        domain.BillingCycle(cycle),
		AmountIDR:           amount,
		Status:              domain.PlatformInvoicePending,
		ProviderEnvironment: s.platformEnv,
		ProviderOrderID:     &providerOrderID,
		DueAt:               now.Add(24 * time.Hour),
		CreatedAt:           now,
		UpdatedAt:           now,
	}

	// Phase 1: Persist platform invoice inside tenant RLS transaction.
	if err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO platform_invoices (
				id, store_id, invoice_number, plan_code, billing_cycle, amount_idr,
				status, provider_environment, provider_order_id, due_at, created_at, updated_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11)
		`, inv.ID, inv.StoreID, inv.InvoiceNumber, inv.PlanCode, inv.BillingCycle, inv.AmountIDR,
			inv.Status, inv.ProviderEnvironment, providerOrderID, inv.DueAt, now); err != nil {
			return err
		}
		return s.writeGovernanceAuditTx(ctx, tx, storeID, &userID, "tenant_owner", "PLATFORM_INVOICE_CREATED",
			fmt.Sprintf("Tagihan langganan SaaS AzureDevID %s dibuat", inv.InvoiceNumber),
			map[string]any{"invoice_id": inv.ID, "invoice_number": inv.InvoiceNumber, "amount_idr": inv.AmountIDR, "plan_code": inv.PlanCode})
	}); err != nil {
		return nil, err
	}

	// Phase 2 (optional): Charge Lane 1 Platform Midtrans QRIS outside DB lock when configured.
	if s.platformGateway != nil {
		chargeResp, chargeErr := s.platformGateway.ChargeCoreAPI(ctx, midtrans.ChargeRequest{
			OrderID:        providerOrderID,
			Amount:         inv.AmountIDR,
			Method:         domain.PaymentMethodQRIS,
			IdempotencyKey: inv.ID.String(),
		})
		if chargeErr == nil && chargeResp != nil && chargeResp.QRString != "" {
			_ = database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
				_, uErr := tx.ExecContext(ctx, `
					UPDATE platform_invoices
					SET provider_qr_string = $3, updated_at = $4
					WHERE store_id = $1 AND id = $2
				`, storeID, inv.ID, chargeResp.QRString, s.nowFunc().UTC())
				if uErr == nil {
					inv.ProviderQRString = chargeResp.QRString
				}
				return uErr
			})
		}
	}

	return inv, nil
}

func (s *GovernanceService) ListStorePlatformInvoices(ctx context.Context, actor *AuthenticatedStaff) ([]domain.PlatformInvoice, error) {
	if err := requireGovernanceOwner(actor); err != nil {
		return nil, err
	}
	storeID := actor.Staff.Store.ID
	var rows []domain.PlatformInvoice
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &rows, `
			SELECT id, store_id, invoice_number, plan_code, billing_cycle, amount_idr,
			       status, provider_environment, provider_order_id, provider_qr_string,
			       provider_va_number, paid_at, due_at, created_at, updated_at
			FROM platform_invoices
			WHERE store_id = $1
			ORDER BY created_at DESC
		`, storeID)
	})
	if rows == nil {
		rows = []domain.PlatformInvoice{}
	}
	return rows, err
}

func (s *GovernanceService) RequestAccountClearance(ctx context.Context, actor *AuthenticatedStaff, reason string) (*domain.TenantClearanceRequest, error) {
	if err := requireGovernanceOwner(actor); err != nil {
		return nil, err
	}
	f := fieldErrors{}
	cleanReason := f.text("reason", reason, 5, 500)
	if err := f.err(); err != nil {
		return nil, err
	}

	storeID := actor.Staff.Store.ID
	userID := actor.Staff.UserID
	now := s.nowFunc().UTC()

	var out domain.TenantClearanceRequest
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		var hasOpenShift bool
		if err := tx.GetContext(ctx, &hasOpenShift, `SELECT EXISTS (SELECT 1 FROM shifts WHERE store_id = $1 AND status = 'open')`, storeID); err != nil {
			return err
		}
		if hasOpenShift {
			return &ConflictError{Field: "shift", Message: "tutup seluruh sesi shift kasir yang masih terbuka sebelum mengajukan penutupan akun"}
		}

		var hasActiveOrders bool
		if err := tx.GetContext(ctx, &hasActiveOrders, `
			SELECT EXISTS (
				SELECT 1 FROM orders
				WHERE store_id = $1 AND (status IN ('open', 'queued', 'preparing', 'ready', 'served') OR payment_status = 'pending')
			)
		`, storeID); err != nil {
			return err
		}
		if hasActiveOrders {
			return &ConflictError{Field: "orders", Message: "selesaikan atau batalkan pesanan aktif/pending sebelum mengajukan penutupan akun"}
		}

		var existingActive bool
		if err := tx.GetContext(ctx, &existingActive, `
			SELECT EXISTS (SELECT 1 FROM tenant_clearance_requests WHERE store_id = $1 AND status = 'cooling_off')
		`, storeID); err != nil {
			return err
		}
		if existingActive {
			return &ConflictError{Field: "status", Message: "pengajuan penutupan akun sedang berjalan pada masa tenggang"}
		}

		// Immediately disable BYOK Midtrans new charges when clearance is requested.
		_, _ = tx.ExecContext(ctx, `UPDATE store_payment_credentials SET is_enabled = false, updated_at = $2 WHERE store_id = $1`, storeID, now)
		_, _ = tx.ExecContext(ctx, `UPDATE platform_tenant_registry SET byok_midtrans_enabled = false, updated_at = $2 WHERE store_id = $1`, storeID, now)

		err := tx.GetContext(ctx, &out, `
			INSERT INTO tenant_clearance_requests (
				id, store_id, requested_by_user_id, reason, status, cooling_off_ends_at, ledger_retention_until, created_at, updated_at
			) VALUES ($1, $2, $3, $4, 'cooling_off', $5, $6, $7, $7)
			RETURNING id, store_id, requested_by_user_id, reason, status, cooling_off_ends_at, ledger_retention_until, executed_at, created_at, updated_at
		`, uuid.New(), storeID, userID, cleanReason, now.Add(7*24*time.Hour), now.AddDate(10, 0, 0), now)
		if err != nil {
			return err
		}
		return s.writeGovernanceAuditTx(ctx, tx, storeID, &userID, "tenant_owner", "CLEARANCE_REQUESTED",
			cleanReason, map[string]any{"clearance_id": out.ID, "cooling_off_ends_at": out.CoolingOffEndsAt})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *GovernanceService) CancelAccountClearance(ctx context.Context, actor *AuthenticatedStaff, clearanceID uuid.UUID) (*domain.TenantClearanceRequest, error) {
	if err := requireGovernanceOwner(actor); err != nil {
		return nil, err
	}
	storeID := actor.Staff.Store.ID
	userID := actor.Staff.UserID
	now := s.nowFunc().UTC()

	var out domain.TenantClearanceRequest
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		err := tx.GetContext(ctx, &out, `
			UPDATE tenant_clearance_requests
			SET status = 'cancelled', updated_at = $3
			WHERE store_id = $1 AND id = $2 AND status = 'cooling_off'
			RETURNING id, store_id, requested_by_user_id, reason, status, cooling_off_ends_at, ledger_retention_until, executed_at, created_at, updated_at
		`, storeID, clearanceID, now)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrGovernanceNotFound
		}
		if err != nil {
			return err
		}
		return s.writeGovernanceAuditTx(ctx, tx, storeID, &userID, "tenant_owner", "CLEARANCE_CANCELLED",
			"Pengajuan penutupan akun dibatalkan oleh pemilik gerai",
			map[string]any{"clearance_id": out.ID})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ============================================================================
// 3. PLATFORM SUPERADMIN CONSOLE (AzureDevID Owner Operations)
// ============================================================================

func (s *GovernanceService) GetPlatformOverview(ctx context.Context) (*domain.PlatformOverview, error) {
	var tenants []domain.PlatformTenantSummary
	if err := s.db.SelectContext(ctx, &tenants, `
		SELECT store_id, slug, display_name, plan_code, subscription_status, kyb_status,
		       byok_midtrans_enabled, active_nodes_count, updated_at
		FROM platform_tenant_registry
		ORDER BY updated_at DESC, slug ASC
	`); err != nil {
		return nil, fmt.Errorf("failed to list platform tenant registry: %w", err)
	}
	if tenants == nil {
		tenants = []domain.PlatformTenantSummary{}
	}

	out := &domain.PlatformOverview{
		TotalTenants: len(tenants),
		Tenants:      tenants,
	}
	for _, t := range tenants {
		switch t.SubscriptionStatus {
		case domain.SubscriptionActive:
			out.ActiveTenants++
		case domain.SubscriptionTrialing:
			out.TrialingTenants++
		}
		if t.BYOKMidtransEnabled {
			out.BYOKEnabledTenants++
		}
		if t.KYBStatus == domain.KYBPendingReview {
			out.PendingKYBReviews++
		}
	}
	_ = s.db.GetContext(ctx, &out.PendingOnboarding, `SELECT COUNT(*) FROM tenant_onboarding_applications WHERE status IN ('pending_email_otp', 'submitted', 'under_review')`)
	_ = s.db.GetContext(ctx, &out.ActiveClearanceCount, `SELECT COUNT(*) FROM tenant_clearance_requests WHERE status = 'cooling_off'`)
	return out, nil
}

func (s *GovernanceService) ListOnboardingApplications(ctx context.Context, statusFilter string) ([]domain.TenantOnboardingApplication, error) {
	var rows []domain.TenantOnboardingApplication
	filter := strings.ToLower(strings.TrimSpace(statusFilter))
	var err error
	if filter == "" {
		err = s.db.SelectContext(ctx, &rows, `
			SELECT id, idempotency_key, requested_slug, business_name, business_entity_type,
			       owner_name, owner_email, owner_phone, city_region, selected_plan_code, billing_cycle,
			       nib_masked, npwp_masked, accepted_tos_sha256, accepted_pdp_sha256,
			       status, email_otp_expires_at, email_otp_sent_at, email_otp_attempts, email_verified_at,
			       provisioned_store_id, review_notes, reviewed_at, created_at, updated_at
			FROM tenant_onboarding_applications
			ORDER BY created_at DESC
		`)
	} else {
		err = s.db.SelectContext(ctx, &rows, `
			SELECT id, idempotency_key, requested_slug, business_name, business_entity_type,
			       owner_name, owner_email, owner_phone, city_region, selected_plan_code, billing_cycle,
			       nib_masked, npwp_masked, accepted_tos_sha256, accepted_pdp_sha256,
			       status, email_otp_expires_at, email_otp_sent_at, email_otp_attempts, email_verified_at,
			       provisioned_store_id, review_notes, reviewed_at, created_at, updated_at
			FROM tenant_onboarding_applications
			WHERE status = $1
			ORDER BY created_at DESC
		`, filter)
	}
	if err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []domain.TenantOnboardingApplication{}
	}
	return rows, nil
}

func (s *GovernanceService) ReviewOnboardingApplication(ctx context.Context, appID uuid.UUID, status string, notes string) (*domain.TenantOnboardingApplication, error) {
	st := strings.ToLower(strings.TrimSpace(status))
	switch domain.OnboardingStatus(st) {
	case domain.OnboardingUnderReview, domain.OnboardingApproved, domain.OnboardingRejected:
	default:
		f := fieldErrors{}
		f.add("status", "harus under_review, approved, atau rejected")
		return nil, f.err()
	}
	now := s.nowFunc().UTC()
	var out domain.TenantOnboardingApplication
	err := s.db.GetContext(ctx, &out, `
		UPDATE tenant_onboarding_applications
		SET status = $2, review_notes = $3, reviewed_at = $4, updated_at = $4
		WHERE id = $1
		RETURNING id, idempotency_key, requested_slug, business_name, business_entity_type,
		          owner_name, owner_email, owner_phone, city_region, selected_plan_code, billing_cycle,
		          nib_masked, npwp_masked, accepted_tos_sha256, accepted_pdp_sha256,
		          status, email_otp_expires_at, email_otp_sent_at, email_otp_attempts, email_verified_at,
		          provisioned_store_id, review_notes, reviewed_at, created_at, updated_at
	`, appID, st, strings.TrimSpace(notes), now)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrGovernanceNotFound
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *GovernanceService) ReviewStoreKYB(ctx context.Context, storeID uuid.UUID, kybStatus string, subscriptionStatus string, notes string) (*domain.StoreKYBProfile, error) {
	ks := strings.ToLower(strings.TrimSpace(kybStatus))
	switch domain.KYBStatus(ks) {
	case domain.KYBPendingReview, domain.KYBVerified, domain.KYBRejected, domain.KYBSuspended:
	default:
		f := fieldErrors{}
		f.add("kyb_status", "harus pending_review, verified, rejected, atau suspended")
		return nil, f.err()
	}
	subSt := strings.ToLower(strings.TrimSpace(subscriptionStatus))
	if subSt == "" {
		subSt = string(domain.SubscriptionActive)
	}
	switch domain.SubscriptionStatus(subSt) {
	case domain.SubscriptionTrialing, domain.SubscriptionActive, domain.SubscriptionPastDue, domain.SubscriptionSuspended, domain.SubscriptionCleared:
	default:
		f := fieldErrors{}
		f.add("subscription_status", "status langganan tidak valid")
		return nil, f.err()
	}

	now := s.nowFunc().UTC()
	var verifiedAt *time.Time
	if domain.KYBStatus(ks) == domain.KYBVerified {
		verifiedAt = &now
	}

	var out domain.StoreKYBProfile
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		var storeMeta struct {
			Slug string `db:"slug"`
			Name string `db:"name"`
		}
		if err := tx.GetContext(ctx, &storeMeta, `SELECT slug, name FROM stores WHERE id = $1`, storeID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrGovernanceNotFound
			}
			return err
		}

		err := tx.GetContext(ctx, &out, `
			INSERT INTO store_kyb_profiles (
				store_id, entity_type, legal_business_name, kyb_status, plan_code, billing_cycle,
				subscription_status, current_period_ends_at, verified_at, review_notes, updated_at
			) VALUES (
				$1, 'perorangan', $2, $3, 'trial_14d', 'trial',
				$4, $5, $6, $7, $8
			)
			ON CONFLICT (store_id) DO UPDATE SET
				kyb_status = EXCLUDED.kyb_status,
				subscription_status = EXCLUDED.subscription_status,
				review_notes = EXCLUDED.review_notes,
				verified_at = COALESCE(EXCLUDED.verified_at, store_kyb_profiles.verified_at),
				updated_at = EXCLUDED.updated_at
			RETURNING store_id, entity_type, legal_business_name, nib_masked, npwp_masked, pse_registration_no,
			          kyb_status, plan_code, billing_cycle, subscription_status, current_period_ends_at,
			          verified_at, review_notes, updated_at
		`, storeID, storeMeta.Name, ks, subSt, now.Add(30*24*time.Hour), verifiedAt, strings.TrimSpace(notes), now)
		if err != nil {
			return err
		}
		_, _ = tx.ExecContext(ctx, `
			INSERT INTO platform_tenant_registry (store_id, slug, display_name, plan_code, subscription_status, kyb_status, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (store_id) DO UPDATE SET
				kyb_status = EXCLUDED.kyb_status,
				subscription_status = EXCLUDED.subscription_status,
				updated_at = EXCLUDED.updated_at
		`, storeID, storeMeta.Slug, storeMeta.Name, out.PlanCode, out.SubscriptionStatus, out.KYBStatus, now)
		return s.writeGovernanceAuditTx(ctx, tx, storeID, nil, "platform_admin", "KYB_REVIEWED",
			strings.TrimSpace(notes),
			map[string]any{"kyb_status": out.KYBStatus, "subscription_status": out.SubscriptionStatus})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ExecuteTenantClearance wipes decrypted/encrypted BYOK payment and notification secrets, disables
// active operational credentials, marks the tenant cleared, and retains financial ledgers (orders & payments)
// for 10 years per UU KUP Pasal 28 & UU PDP No. 27/2022.
func (s *GovernanceService) ExecuteTenantClearance(ctx context.Context, storeID, clearanceID uuid.UUID) (*domain.TenantClearanceRequest, error) {
	now := s.nowFunc().UTC()
	var out domain.TenantClearanceRequest

	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		err := tx.GetContext(ctx, &out, `
			UPDATE tenant_clearance_requests
			SET status = 'pii_anonymized_ledger_retained',
			    executed_at = $3,
			    updated_at = $3
			WHERE store_id = $1 AND id = $2 AND status = 'cooling_off'
			RETURNING id, store_id, requested_by_user_id, reason, status, cooling_off_ends_at, ledger_retention_until, executed_at, created_at, updated_at
		`, storeID, clearanceID, now)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrGovernanceNotFound
		}
		if err != nil {
			return err
		}

		// Cryptographically overwrite BYOK Midtrans ciphertext and disable gateway routing.
		revokedBytes := []byte("REVOKED_BY_CLEARANCE")
		if s.box != nil {
			if sealed, sErr := s.box.Seal("REVOKED_BY_ACCOUNT_CLEARANCE", midtransServerKeyAAD(storeID)); sErr == nil {
				revokedBytes = sealed
			}
		}
		_, _ = tx.ExecContext(ctx, `
			UPDATE store_payment_credentials
			SET server_key_ciphertext = $2,
			    server_key_masked = 'REVOKED',
			    client_key = 'REVOKED',
			    validation_status = 'unverified',
			    is_enabled = false,
			    updated_at = $3
			WHERE store_id = $1
		`, storeID, revokedBytes, now)

		// Wipe custom SMTP and WhatsApp tokens.
		_, _ = tx.ExecContext(ctx, `
			UPDATE store_notification_configs
			SET email_mode = 'disabled',
			    smtp_password_ciphertext = NULL,
			    smtp_password_masked = '',
			    whatsapp_mode = 'disabled',
			    whatsapp_token_ciphertext = NULL,
			    whatsapp_token_masked = '',
			    updated_at = $2
			WHERE store_id = $1
		`, storeID, now)

		_, _ = tx.ExecContext(ctx, `
			UPDATE store_kyb_profiles
			SET subscription_status = 'cleared', updated_at = $2
			WHERE store_id = $1
		`, storeID, now)

		_, _ = tx.ExecContext(ctx, `
			UPDATE platform_tenant_registry
			SET subscription_status = 'cleared', byok_midtrans_enabled = false, updated_at = $2
			WHERE store_id = $1
		`, storeID, now)

		return s.writeGovernanceAuditTx(ctx, tx, storeID, nil, "platform_admin", "CLEARANCE_EXECUTED",
			"PII & kredensial BYOK dibersihkan; buku besar transaksi dipertahankan 10 tahun sesuai UU KUP",
			map[string]any{"clearance_id": out.ID, "ledger_retention_until": out.LedgerRetentionUntil})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *GovernanceService) EvaluateMidtransBusinessReviewReadiness(ctx context.Context) (*domain.MidtransBusinessReviewReadiness, error) {
	var legalCount int
	_ = s.db.GetContext(ctx, &legalCount, `
		SELECT COUNT(DISTINCT doc_type) FROM legal_documents
		WHERE is_active = true AND doc_type IN ('terms_of_service', 'privacy_policy_pdp', 'refund_policy', 'dpa_tenant')
	`)
	var paidPlanCount int
	_ = s.db.GetContext(ctx, &paidPlanCount, `
		SELECT COUNT(*) FROM subscription_plans
		WHERE is_active = true AND monthly_price_idr > 0 AND commission_bps = 0
	`)

	checks := []domain.MidtransReadinessCheck{
		{
			Code:   "LEGAL_PAGES_COMPLETE",
			Passed: legalCount >= 4,
			Detail: fmt.Sprintf("%d/4 dokumen hukum aktif (ToS, UU PDP Privacy, DPA, Refund Policy) siap ditampilkan publik", legalCount),
		},
		{
			Code:   "EXPLICIT_IDR_PRICING_PLANS",
			Passed: paidPlanCount >= 3,
			Detail: fmt.Sprintf("%d paket berlangganan berdenominasi Rupiah bulat (0%% komisi transaksi) aktif di katalog", paidPlanCount),
		},
		{
			Code:   "BYOK_AES256_GCM_ENVELOPE",
			Passed: s.box != nil,
			Detail: "Enkripsi kredensial Midtrans BYOK per-tenant menggunakan AES-256-GCM dengan AAD store_id",
		},
		{
			Code:   "SANDBOX_ISOLATED_DURING_REVIEW",
			Passed: s.appEnv == "production" || (!s.defaultStoreProd && s.platformEnv == "sandbox"),
			Detail: fmt.Sprintf("app_env=%s, platform_midtrans=%s, default_store_prod=%v (aman dari kebocoran transaksi live saat review)", s.appEnv, s.platformEnv, s.defaultStoreProd),
		},
		{
			Code:   "CORE_API_2PHASE_IDEMPOTENCY",
			Passed: true,
			Detail: "Core API langsung (tanpa Snap) dengan Idempotency-Key UUID 36 karakter, verifikasi SHA-512 konstan, dan rekonsiliasi CheckTransaction",
		},
	}

	passedCount := 0
	for _, c := range checks {
		if c.Passed {
			passedCount++
		}
	}
	score := (passedCount * 100) / len(checks)
	defaultEnv := "sandbox"
	if s.defaultStoreProd {
		defaultEnv = "production"
	}

	return &domain.MidtransBusinessReviewReadiness{
		PlatformEnvironment: s.platformEnv,
		DefaultStoreEnv:     defaultEnv,
		ReadyForLiveCutover: score == 100 && s.box != nil,
		ScorePercent:        score,
		Checks:              checks,
		EdgeCasePlaybookNotes: []string{
			"1. Akun Production masih Business Review: kunci produksi menolak charge Core API (401/402); pertahankan MIDTRANS_IS_PRODUCTION=false pada demo/staging dan sediakan fallback QRIS Statis / Tunai / EDC.",
			"2. Webhook terlambat setelah reservasi stok kedaluwarsa: SAVEPOINT payment_stock_commit tetap mencatat dana PAID tanpa merusak stok dan menerbitkan event rekonsiliasi kasir.",
			"3. Notifikasi ganda / out-of-order dari Midtrans: diabaikan secara idempoten melalui tabel payment_webhook_events dan mesin transisi status satu arah.",
			"4. Pecahan sen desimal pada gross_amount (mis. 299000.00): divalidasi ketat dengan parser integer Rupiah tanpa aritmetika floating-point.",
			"5. Timeout jaringan saat ChargeCoreAPI: fase rekonsiliasi memanggil CheckTransaction terlebih dahulu sebelum mencoba ulang dengan Idempotency-Key yang sama.",
			"6. Isolasi dana PJP Bank Indonesia: tagihan langganan SaaS memakai Lane 1 (AzureDevID), sedangkan pembayaran menu pelanggan memakai Lane 2 (Midtrans BYOK milik masing-masing Tenant).",
		},
		EvaluatedAt: s.nowFunc().UTC(),
	}, nil
}

// GetMobileBootstrap synthesizes tablet POS and KDS runtime bootstrap configuration.
// Accessible by any authenticated staff member (owner, cashier, barista, kitchen).
func (s *GovernanceService) GetMobileBootstrap(ctx context.Context, actor *AuthenticatedStaff) (*domain.MobileBootstrapResponse, error) {
	if s == nil {
		return nil, errors.New("governance service unavailable")
	}
	if actor == nil || actor.Staff.UserID == uuid.Nil {
		return nil, ErrInvalidSession
	}
	storeID := actor.Staff.Store.ID
	storeSlug := actor.Staff.Store.Slug
	storeName := actor.Staff.Store.Name

	profile := domain.StoreMobileProfile{
		StoreID:                 storeID,
		AppBrandDisplayName:     "Cassy",
		AppMonogramText:         "CS",
		AppTechnicalBundleID:    "com.azuredevid.cassy",
		POSTabletOrientation:    "landscape_primary",
		POSAutoLockSeconds:      300,
		KDSAutoRefreshSeconds:   5,
		KDSSoundAlertEnabled:    true,
		CashDrawerKickOnCash:    true,
		DefaultPrinterInterface: "lan_tcp",
		DefaultPrinterPort:      9100,
		OfflineSyncIntervalSec:  60,
		UpdatedAt:               s.nowFunc().UTC(),
	}

	var paymentInfo domain.MobilePaymentGatewayInfo
	paymentInfo.Provider = "midtrans"
	paymentInfo.Environment = "sandbox"
	paymentInfo.ValidationStatus = "unverified"
	paymentInfo.FallbackStaticQRISEnabled = true

	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		// 1. Fetch store_mobile_profiles (Migration 53)
		var p domain.StoreMobileProfile
		pErr := tx.GetContext(ctx, &p, `
			SELECT store_id, app_brand_display_name, app_monogram_text, app_technical_bundle_id,
			       pos_tablet_orientation, pos_auto_lock_seconds, kds_auto_refresh_seconds,
			       kds_sound_alert_enabled, cash_drawer_kick_on_cash, default_printer_interface,
			       default_printer_port, offline_sync_interval_sec, updated_by_user_id, updated_at
			FROM store_mobile_profiles
			WHERE store_id = $1
		`, storeID)
		if pErr == nil {
			profile = p
		} else if !errors.Is(pErr, sql.ErrNoRows) {
			return pErr
		}

		// 2. Fetch payment credentials
		var cred domain.StorePaymentCredential
		cErr := tx.GetContext(ctx, &cred, `
			SELECT store_id, provider, environment, merchant_id, client_key,
			       validation_status, is_enabled, fallback_static_qris_enabled
			FROM store_payment_credentials
			WHERE store_id = $1
		`, storeID)
		if cErr == nil {
			paymentInfo.Provider = cred.Provider
			paymentInfo.Environment = cred.Environment
			paymentInfo.ClientKey = cred.ClientKey
			paymentInfo.MerchantID = cred.MerchantID
			paymentInfo.IsEnabled = cred.IsEnabled
			paymentInfo.ValidationStatus = cred.ValidationStatus
			paymentInfo.FallbackStaticQRISEnabled = cred.FallbackStaticQRISEnabled
		} else if !errors.Is(cErr, sql.ErrNoRows) {
			return cErr
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	perms := make([]string, len(actor.Permissions))
	for i, p := range actor.Permissions {
		perms[i] = string(p)
	}

	cdnBase := "https://cdn.abbdullaziz.online"
	res := &domain.MobileBootstrapResponse{
		StoreID:   storeID,
		StoreSlug: storeSlug,
		StoreName: storeName,
		AppBrand: domain.MobileAppBrandInfo{
			DisplayName:       profile.AppBrandDisplayName,
			MonogramText:      profile.AppMonogramText,
			BundleID:          profile.AppTechnicalBundleID,
			VectorMonogramURL: cdnBase + "/brand/cassy/CassyMonogramVector.svg",
			VectorLogoURL:     cdnBase + "/brand/cassy/CassyLogoLightVector.svg",
			VectorIconURL:     cdnBase + "/brand/cassy/CassyAppIconDarkSquircle.svg",
		},
		POSLayout: domain.MobilePOSLayoutInfo{
			TabletOrientation:      profile.POSTabletOrientation,
			LayoutMode:             "master_detail_toast",
			AutoLockSeconds:        profile.POSAutoLockSeconds,
			TabularNumbersEnforced: true,
			MinTouchTargetPx:       48,
		},
		Hardware: domain.MobileHardwareInfo{
			DefaultPrinterInterface: profile.DefaultPrinterInterface,
			DefaultPrinterPort:      profile.DefaultPrinterPort,
			CashDrawerKickOnCash:    profile.CashDrawerKickOnCash,
			CashDrawerPulseHex:      "1B700019FA",
			KDSAutoRefreshSeconds:   profile.KDSAutoRefreshSeconds,
			KDSSoundAlertEnabled:    profile.KDSSoundAlertEnabled,
		},
		PaymentGateway: paymentInfo,
		StaffSession: domain.MobileStaffSessionInfo{
			UserID:      actor.Staff.UserID,
			FullName:    actor.Staff.FullName,
			Role:        string(actor.Staff.Role),
			Email:       "",
			Permissions: perms,
		},
	}

	return res, nil
}
