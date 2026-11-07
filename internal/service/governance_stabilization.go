package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/auth"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/mailer"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/midtrans"
)

const (
	onboardingOTPTTL         = 10 * time.Minute
	onboardingOTPCooldown    = 60 * time.Second
	onboardingOTPMaxAttempts = 5
)

var (
	otpSixDigitsPattern  = regexp.MustCompile(`^[0-9]{6}$`)
	routerSafeTokenRegex = regexp.MustCompile(`^[a-zA-Z0-9._()/ -]{2,80}$`)
	macAddrRegex         = regexp.MustCompile(`^([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}$`)
)

func generateSixDigitOTP() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "", fmt.Errorf("failed to generate OTP: %w", err)
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

func hashOTPCode(code string) string {
	sum := sha256.Sum256([]byte("mycaffe-onboarding-otp:" + strings.TrimSpace(code)))
	return hex.EncodeToString(sum[:])
}

func (s *GovernanceService) sendOnboardingOTPEmail(ctx context.Context, app *domain.TenantOnboardingApplication, otpCode string) {
	if s == nil || s.mailSender == nil || app == nil {
		return
	}
	rendered, err := mailer.Render(mailer.TemplateOnboardingOTP, mailer.Data{
		StoreName:     app.BusinessName,
		StoreSlug:     app.RequestedSlug,
		RecipientName: app.OwnerName,
		OTPCode:       otpCode,
		ExpiresIn:     "10 menit",
		Year:          s.nowFunc().UTC().Year(),
	})
	if err != nil {
		log.Printf("[ONBOARDING OTP] template render error slug=%s: %v", app.RequestedSlug, err)
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.mailSender.Send(sendCtx, mailer.Message{
		To:      app.OwnerEmail,
		ToName:  app.OwnerName,
		Subject: rendered.Subject,
		HTML:    rendered.HTML,
		Text:    rendered.Text,
	}); err != nil {
		log.Printf("[ONBOARDING OTP] smtp send warning slug=%s to=%s: %v", app.RequestedSlug, app.OwnerEmail, err)
	}
}

func (s *GovernanceService) sendOnboardingProvisionedEmail(ctx context.Context, app *domain.TenantOnboardingApplication, storeSlug string) {
	if s == nil || s.mailSender == nil || app == nil {
		return
	}
	actionURL := s.appPublicURL
	if actionURL == "" {
		actionURL = "https://demo.abbdullaziz.online"
	}
	rendered, err := mailer.Render(mailer.TemplateOnboardingProvisioned, mailer.Data{
		StoreName:     app.BusinessName,
		StoreSlug:     storeSlug,
		RecipientName: app.OwnerName,
		PlanLabel:     fmt.Sprintf("%s (%s)", app.SelectedPlanCode, app.BillingCycle),
		ActionURL:     strings.TrimRight(actionURL, "/") + "/login?store=" + storeSlug,
		Year:          s.nowFunc().UTC().Year(),
	})
	if err != nil {
		log.Printf("[ONBOARDING PROVISIONED] template render error slug=%s: %v", storeSlug, err)
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.mailSender.Send(sendCtx, mailer.Message{
		To:      app.OwnerEmail,
		ToName:  app.OwnerName,
		Subject: rendered.Subject,
		HTML:    rendered.HTML,
		Text:    rendered.Text,
	}); err != nil {
		log.Printf("[ONBOARDING PROVISIONED] smtp send warning slug=%s to=%s: %v", storeSlug, app.OwnerEmail, err)
	}
}

// ============================================================================
// 1. STRICT ONBOARDING EMAIL OTP VERIFICATION & UNVERIFIED STORE PROVISIONING
// ============================================================================

func (s *GovernanceService) VerifyOnboardingEmailOTP(ctx context.Context, appID uuid.UUID, otpCode string) (*domain.TenantOnboardingApplication, error) {
	cleanOTP := strings.TrimSpace(otpCode)
	if !otpSixDigitsPattern.MatchString(cleanOTP) {
		f := fieldErrors{}
		f.add("otp_code", "wajib 6 digit angka")
		return nil, f.err()
	}

	var row struct {
		domain.TenantOnboardingApplication
		EmailOTPHash sql.NullString `db:"email_otp_hash"`
	}
	err := s.db.GetContext(ctx, &row, `
		SELECT id, idempotency_key, requested_slug, business_name, business_entity_type,
		       owner_name, owner_email, owner_phone, city_region, selected_plan_code, billing_cycle,
		       nib_masked, npwp_masked, accepted_tos_sha256, accepted_pdp_sha256,
		       status, email_otp_hash, email_otp_expires_at, email_otp_sent_at, email_otp_attempts,
		       email_verified_at, provisioned_store_id, review_notes, reviewed_at, created_at, updated_at
		FROM tenant_onboarding_applications
		WHERE id = $1
	`, appID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrGovernanceNotFound
	}
	if err != nil {
		return nil, err
	}

	// Idempotent replay when email is already verified.
	if row.EmailVerifiedAt != nil {
		return &row.TenantOnboardingApplication, nil
	}
	if row.EmailOTPAttempts >= onboardingOTPMaxAttempts {
		return nil, ErrOnboardingOTPLocked
	}
	now := s.nowFunc().UTC()
	if row.EmailOTPExpiresAt == nil || now.After(*row.EmailOTPExpiresAt) || !row.EmailOTPHash.Valid {
		return nil, ErrOnboardingOTPExpired
	}

	expectedHash := strings.TrimSpace(row.EmailOTPHash.String)
	providedHash := hashOTPCode(cleanOTP)
	if subtle.ConstantTimeCompare([]byte(expectedHash), []byte(providedHash)) != 1 {
		newAttempts := row.EmailOTPAttempts + 1
		_, _ = s.db.ExecContext(ctx, `
			UPDATE tenant_onboarding_applications
			SET email_otp_attempts = $2, updated_at = $3
			WHERE id = $1
		`, appID, newAttempts, now)
		if newAttempts >= onboardingOTPMaxAttempts {
			return nil, ErrOnboardingOTPLocked
		}
		return nil, ErrOnboardingOTPInvalid
	}

	var out domain.TenantOnboardingApplication
	err = s.db.GetContext(ctx, &out, `
		UPDATE tenant_onboarding_applications
		SET email_verified_at = $2,
		    email_otp_hash = NULL,
		    status = CASE WHEN status = 'pending_email_otp' THEN 'submitted' ELSE status END,
		    updated_at = $2
		WHERE id = $1
		RETURNING id, idempotency_key, requested_slug, business_name, business_entity_type,
		          owner_name, owner_email, owner_phone, city_region, selected_plan_code, billing_cycle,
		          nib_masked, npwp_masked, accepted_tos_sha256, accepted_pdp_sha256,
		          status, email_otp_expires_at, email_otp_sent_at, email_otp_attempts, email_verified_at,
		          provisioned_store_id, review_notes, reviewed_at, created_at, updated_at
	`, appID, now)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *GovernanceService) ResendOnboardingEmailOTP(ctx context.Context, appID uuid.UUID) (*time.Time, string, error) {
	var app domain.TenantOnboardingApplication
	err := s.db.GetContext(ctx, &app, `
		SELECT id, idempotency_key, requested_slug, business_name, business_entity_type,
		       owner_name, owner_email, owner_phone, city_region, selected_plan_code, billing_cycle,
		       nib_masked, npwp_masked, accepted_tos_sha256, accepted_pdp_sha256,
		       status, email_otp_expires_at, email_otp_sent_at, email_otp_attempts, email_verified_at,
		       provisioned_store_id, review_notes, reviewed_at, created_at, updated_at
		FROM tenant_onboarding_applications
		WHERE id = $1
	`, appID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrGovernanceNotFound
	}
	if err != nil {
		return nil, "", err
	}
	if app.EmailVerifiedAt != nil {
		return nil, "", &ConflictError{Field: "email", Message: "email pendaftar sudah terverifikasi"}
	}

	now := s.nowFunc().UTC()
	if app.EmailOTPSentAt != nil && now.Sub(*app.EmailOTPSentAt) < onboardingOTPCooldown {
		return nil, "", ErrOnboardingOTPCooldown
	}

	otpPlain, err := generateSixDigitOTP()
	if err != nil {
		return nil, "", err
	}
	otpHash := hashOTPCode(otpPlain)
	otpExp := now.Add(onboardingOTPTTL)

	if _, err := s.db.ExecContext(ctx, `
		UPDATE tenant_onboarding_applications
		SET email_otp_hash = $2,
		    email_otp_expires_at = $3,
		    email_otp_sent_at = $4,
		    email_otp_attempts = 0,
		    updated_at = $4
		WHERE id = $1
	`, appID, otpHash, otpExp, now); err != nil {
		return nil, "", err
	}

	s.sendOnboardingOTPEmail(ctx, &app, otpPlain)

	devPreview := ""
	if s.appEnv == "development" || s.appEnv == "test" {
		devPreview = otpPlain
	}
	return &otpExp, devPreview, nil
}

type ProvisionOnboardingInput struct {
	InitialOwnerPIN string `json:"initial_owner_pin"`
	TableCount      int    `json:"table_count"`
	ReviewNotes     string `json:"review_notes"`
}

func (s *GovernanceService) ProvisionStoreFromOnboarding(ctx context.Context, appID uuid.UUID, in ProvisionOnboardingInput) (*domain.ProvisionedStoreResult, error) {
	pin := strings.TrimSpace(in.InitialOwnerPIN)
	if err := auth.ValidatePINPolicy(pin); err != nil {
		f := fieldErrors{}
		f.add("initial_owner_pin", err.Error())
		return nil, f.err()
	}
	pinHash, err := auth.HashPIN(pin)
	if err != nil {
		return nil, err
	}

	tableCount := in.TableCount
	if tableCount <= 0 {
		tableCount = 3
	}
	if tableCount > 20 {
		f := fieldErrors{}
		f.add("table_count", "jumlah meja awal maksimal 20")
		return nil, f.err()
	}

	newStoreID := uuid.New()
	newOwnerID := uuid.New()
	var provRow struct {
		StoreID        uuid.UUID `db:"out_store_id"`
		StoreSlug      string    `db:"out_store_slug"`
		OwnerID        uuid.UUID `db:"out_owner_id"`
		OwnerEmail     string    `db:"out_owner_email"`
		AlreadyExisted bool      `db:"already_existed"`
	}

	err = s.db.GetContext(ctx, &provRow, `
		SELECT out_store_id, out_store_slug, out_owner_id, out_owner_email, already_existed
		FROM provision_store_from_onboarding($1, $2, $3, $4, $5)
	`, appID, newStoreID, newOwnerID, pinHash, strings.TrimSpace(in.ReviewNotes))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case "P0002":
				return nil, ErrGovernanceNotFound
			case "P0001":
				return nil, ErrOnboardingOTPUnverified
			case "23505":
				return nil, &ConflictError{Field: "requested_slug", Message: "slug gerai sudah digunakan oleh tenant lain"}
			}
		}
		return nil, fmt.Errorf("failed to provision store from onboarding: %w", err)
	}

	var app domain.TenantOnboardingApplication
	if err := s.db.GetContext(ctx, &app, `
		SELECT id, idempotency_key, requested_slug, business_name, business_entity_type,
		       owner_name, owner_email, owner_phone, city_region, selected_plan_code, billing_cycle,
		       nib_masked, npwp_masked, accepted_tos_sha256, accepted_pdp_sha256,
		       status, email_otp_expires_at, email_otp_sent_at, email_otp_attempts, email_verified_at,
		       provisioned_store_id, review_notes, reviewed_at, created_at, updated_at
		FROM tenant_onboarding_applications
		WHERE id = $1
	`, appID); err != nil {
		return nil, err
	}

	var activeDocs []domain.LegalDocument
	_ = s.db.SelectContext(ctx, &activeDocs, `
		SELECT id, doc_type, content_sha256
		FROM legal_documents
		WHERE is_active = true AND locale = 'id-ID' AND doc_type IN ('terms_of_service', 'privacy_policy_pdp')
	`)

	var tables []domain.ProvisionedTableSummary
	kybStatus := domain.KYBUnverified
	now := s.nowFunc().UTC()

	err = database.WithTenantTx(ctx, s.db, provRow.StoreID, func(tx *sqlx.Tx) error {
		if !provRow.AlreadyExisted {
			if _, dErr := tx.ExecContext(ctx, `
				INSERT INTO cash_drawers (id, store_id, name, is_active)
				VALUES ($1, $2, 'Kasir Utama', true)
			`, uuid.New(), provRow.StoreID); dErr != nil {
				return dErr
			}

			for i := 1; i <= tableCount; i++ {
				qrToken, qErr := newQRToken()
				if qErr != nil {
					return qErr
				}
				tNum := fmt.Sprintf("T-%02d", i)
				tLabel := fmt.Sprintf("Meja QR %02d", i)
				if _, tErr := tx.ExecContext(ctx, `
					INSERT INTO tables (id, store_id, table_number, label, section, capacity, qr_token, is_active)
					VALUES ($1, $2, $3, $4, 'Area Utama', 4, $5, true)
				`, uuid.New(), provRow.StoreID, tNum, tLabel, qrToken); tErr != nil {
					return tErr
				}
			}

			for _, doc := range activeDocs {
				_, _ = tx.ExecContext(ctx, `
					INSERT INTO tenant_legal_acceptances (
						id, store_id, legal_document_id, accepted_by_user_id, document_sha256, accepted_ip, user_agent, accepted_at
					) VALUES ($1, $2, $3, $4, $5, 'onboarding-otp-verified', 'AzureDevID-Provisioner/1.0', $6)
					ON CONFLICT (store_id, legal_document_id, accepted_by_user_id) DO NOTHING
				`, uuid.New(), provRow.StoreID, doc.ID, provRow.OwnerID, strings.TrimSpace(doc.ContentSHA256), now)
			}

			ownerID := provRow.OwnerID
			if aErr := s.writeGovernanceAuditTx(ctx, tx, provRow.StoreID, &ownerID, "platform_admin", "ONBOARDING_STORE_PROVISIONED",
				"Gerai tenant baru diaktifkan dari pendaftaran terverifikasi OTP (status KYB awal: unverified)",
				map[string]any{
					"application_id": app.ID,
					"store_slug":     provRow.StoreSlug,
					"table_count":    tableCount,
					"kyb_status":     domain.KYBUnverified,
				}); aErr != nil {
				return aErr
			}
		}

		var rawTables []struct {
			ID          uuid.UUID `db:"id"`
			TableNumber string    `db:"table_number"`
			QRToken     string    `db:"qr_token"`
		}
		if qErr := tx.SelectContext(ctx, &rawTables, `
			SELECT id, table_number, qr_token
			FROM tables
			WHERE store_id = $1 AND archived_at IS NULL
			ORDER BY table_number ASC
		`, provRow.StoreID); qErr != nil {
			return qErr
		}
		for _, rt := range rawTables {
			tables = append(tables, domain.ProvisionedTableSummary{
				ID:          rt.ID,
				TableNumber: rt.TableNumber,
				QRToken:     rt.QRToken,
			})
		}

		var ks string
		if qErr := tx.GetContext(ctx, &ks, `SELECT kyb_status FROM store_kyb_profiles WHERE store_id = $1`, provRow.StoreID); qErr == nil && ks != "" {
			kybStatus = domain.KYBStatus(ks)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if tables == nil {
		tables = []domain.ProvisionedTableSummary{}
	}

	if !provRow.AlreadyExisted {
		s.sendOnboardingProvisionedEmail(ctx, &app, provRow.StoreSlug)
	}

	return &domain.ProvisionedStoreResult{
		Application:    &app,
		StoreID:        provRow.StoreID,
		StoreSlug:      provRow.StoreSlug,
		OwnerUserID:    provRow.OwnerID,
		OwnerEmail:     provRow.OwnerEmail,
		KYBStatus:      kybStatus,
		AlreadyExisted: provRow.AlreadyExisted,
		Tables:         tables,
	}, nil
}

// ============================================================================
// 2. LANE 1 PLATFORM SUBSCRIPTION INVOICE WEBHOOK SETTLEMENT
// ============================================================================

type PlatformWebhookResult struct {
	InvoiceID uuid.UUID
	StoreID   uuid.UUID
	Status    domain.PlatformInvoiceStatus
	Duplicate bool
	Ignored   bool
}

func (s *GovernanceService) ProcessPlatformInvoiceWebhook(ctx context.Context, input MidtransWebhookInput) (*PlatformWebhookResult, error) {
	if s == nil || s.db == nil {
		return nil, ErrGovernanceNotFound
	}
	providerOrderID := strings.TrimSpace(input.OrderID)
	if providerOrderID == "" {
		return nil, ErrInvalidMidtransWebhook
	}

	var resolved struct {
		InvoiceID uuid.UUID `db:"invoice_id"`
		StoreID   uuid.UUID `db:"store_id"`
		AmountIDR int64     `db:"amount_idr"`
		Status    string    `db:"status"`
	}
	err := s.db.GetContext(ctx, &resolved, `
		SELECT invoice_id, store_id, amount_idr, status
		FROM resolve_platform_invoice_webhook_context($1)
	`, providerOrderID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrGovernanceNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to resolve platform invoice webhook context: %w", err)
	}

	if !strings.EqualFold(strings.TrimSpace(input.Currency), "IDR") {
		return nil, ErrInvalidMidtransWebhook
	}
	grossAmountIDR, err := midtrans.ParseGrossAmountIDR(input.GrossAmount)
	if err != nil || grossAmountIDR <= 0 || grossAmountIDR != resolved.AmountIDR {
		return nil, ErrInvalidMidtransWebhook
	}
	mappedStatus, err := midtrans.MapMidtransStatus(input.TransactionStatus, input.FraudStatus)
	if err != nil {
		return nil, ErrInvalidMidtransWebhook
	}
	if mappedStatus == "paid" && strings.TrimSpace(input.StatusCode) != "200" {
		return nil, ErrInvalidMidtransWebhook
	}

	now := s.nowFunc().UTC()
	res := &PlatformWebhookResult{
		InvoiceID: resolved.InvoiceID,
		StoreID:   resolved.StoreID,
	}

	err = database.WithTenantTx(ctx, s.db, resolved.StoreID, func(tx *sqlx.Tx) error {
		var inv domain.PlatformInvoice
		if qErr := tx.GetContext(ctx, &inv, `
			SELECT id, store_id, invoice_number, plan_code, billing_cycle, amount_idr,
			       status, provider_environment, provider_order_id, provider_qr_string,
			       provider_va_number, paid_at, due_at, created_at, updated_at
			FROM platform_invoices
			WHERE store_id = $1 AND id = $2
			FOR UPDATE
		`, resolved.StoreID, resolved.InvoiceID); qErr != nil {
			if errors.Is(qErr, sql.ErrNoRows) {
				return ErrGovernanceNotFound
			}
			return qErr
		}

		res.Status = inv.Status

		// Monotonic idempotency: never downgrade a paid/refunded invoice.
		if inv.Status == domain.PlatformInvoicePaid || inv.Status == domain.PlatformInvoiceRefunded {
			if mappedStatus == "paid" {
				res.Duplicate = true
			} else {
				res.Ignored = true
			}
			return nil
		}

		switch mappedStatus {
		case "paid":
			if _, uErr := tx.ExecContext(ctx, `
				UPDATE platform_invoices
				SET status = 'paid', paid_at = $3, updated_at = $3
				WHERE store_id = $1 AND id = $2
			`, resolved.StoreID, inv.ID, now); uErr != nil {
				return uErr
			}
			periodEnd := now.Add(30 * 24 * time.Hour)
			if inv.BillingCycle == domain.BillingCycleAnnual {
				periodEnd = now.Add(365 * 24 * time.Hour)
			}
			_, _ = tx.ExecContext(ctx, `
				UPDATE store_kyb_profiles
				SET subscription_status = 'active',
				    plan_code = $2,
				    billing_cycle = $3,
				    current_period_ends_at = $4,
				    updated_at = $5
				WHERE store_id = $1
			`, resolved.StoreID, inv.PlanCode, inv.BillingCycle, periodEnd, now)

			_, _ = tx.ExecContext(ctx, `
				UPDATE platform_tenant_registry
				SET subscription_status = 'active',
				    plan_code = $2,
				    updated_at = $3
				WHERE store_id = $1
			`, resolved.StoreID, inv.PlanCode, now)

			res.Status = domain.PlatformInvoicePaid
			return s.writeGovernanceAuditTx(ctx, tx, resolved.StoreID, nil, "payment_webhook", "PLATFORM_INVOICE_WEBHOOK_SETTLED",
				fmt.Sprintf("Tagihan langganan SaaS %s lunas via Midtrans Core API", inv.InvoiceNumber),
				map[string]any{
					"invoice_id":     inv.ID,
					"invoice_number": inv.InvoiceNumber,
					"amount_idr":     inv.AmountIDR,
					"plan_code":      inv.PlanCode,
					"billing_cycle":  inv.BillingCycle,
					"transaction_id": input.TransactionID,
				})

		case "expired", "failed", "cancelled":
			nextStatus := domain.PlatformInvoiceCancelled
			if mappedStatus == "expired" {
				nextStatus = domain.PlatformInvoiceExpired
			}
			if inv.Status == nextStatus {
				res.Duplicate = true
				return nil
			}
			if _, uErr := tx.ExecContext(ctx, `
				UPDATE platform_invoices
				SET status = $3, updated_at = $4
				WHERE store_id = $1 AND id = $2
			`, resolved.StoreID, inv.ID, nextStatus, now); uErr != nil {
				return uErr
			}
			res.Status = nextStatus
			return s.writeGovernanceAuditTx(ctx, tx, resolved.StoreID, nil, "payment_webhook", "PLATFORM_INVOICE_WEBHOOK_CLOSED",
				fmt.Sprintf("Tagihan langganan SaaS %s ditutup (%s)", inv.InvoiceNumber, nextStatus),
				map[string]any{"invoice_id": inv.ID, "status": nextStatus, "transaction_id": input.TransactionID})

		default:
			res.Ignored = true
			return nil
		}
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// ============================================================================
// 3. MIKROTIK ROUTEROS V7 (.RSC), WINBOX GUIDE & SFTP-ONLY FILEZILLA AUTOMATION
// ============================================================================

type MikrotikProfileInput struct {
	RouterModel         string `json:"router_model"`
	WANPrimaryInterface string `json:"wan_primary_interface"`
	WANBackupInterface  string `json:"wan_backup_interface"`
	POSVLANCIDR         string `json:"pos_vlan_cidr"`
	StaffVLANCIDR       string `json:"staff_vlan_cidr"`
	GuestVLANCIDR       string `json:"guest_vlan_cidr"`
	POSWiFiSSID         string `json:"pos_wifi_ssid"`
	GuestWiFiSSID       string `json:"guest_wifi_ssid"`
	GuestRateLimitMbps  int    `json:"guest_rate_limit_mbps"`
	POSReservedMbps     int    `json:"pos_reserved_mbps"`
	SFTPPort            int    `json:"sftp_port"`
	WinboxPort          int    `json:"winbox_port"`
	WinboxAllowedCIDR   string `json:"winbox_allowed_cidr"`
	DNSServers          string `json:"dns_servers"`
}

func defaultStoreMikrotikProfile(storeID uuid.UUID, now time.Time) *domain.StoreMikrotikProfile {
	return &domain.StoreMikrotikProfile{
		StoreID:             storeID,
		RouterModel:         "hAP ax2 / RB750Gr3 (RouterOS v7)",
		WANPrimaryInterface: "ether1-isp-utama",
		WANBackupInterface:  "ether2-modem-4g",
		POSVLANCIDR:         "192.168.10.0/24",
		StaffVLANCIDR:       "192.168.20.0/24",
		GuestVLANCIDR:       "192.168.30.0/23",
		POSWiFiSSID:         "MyCaffe-POS-Internal",
		GuestWiFiSSID:       "MyCaffe-Guest-QR",
		GuestRateLimitMbps:  20,
		POSReservedMbps:     10,
		SFTPPort:            2222,
		WinboxPort:          8291,
		WinboxAllowedCIDR:   "192.168.10.0/24",
		DNSServers:          "1.1.1.1,8.8.8.8",
		UpdatedAt:           now,
	}
}

type parsedSubnet struct {
	cidr      string
	networkIP net.IP
	gatewayIP string
	poolStart string
	poolEnd   string
	netObj    *net.IPNet
}

func parseIPv4VLANSubnet(raw string) (*parsedSubnet, error) {
	ip, ipNet, err := net.ParseCIDR(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return nil, errors.New("hanya subnet IPv4 yang didukung")
	}
	ones, bits := ipNet.Mask.Size()
	if bits != 32 || ones < 16 || ones > 28 {
		return nil, errors.New("prefix subnet harus antara /16 dan /28")
	}
	netIP := ipNet.IP.To4()
	base := binary.BigEndian.Uint32(netIP)
	mask := binary.BigEndian.Uint32(ipNet.Mask)
	bcast := base | ^mask

	gw := make(net.IP, 4)
	binary.BigEndian.PutUint32(gw, base+1)

	pStartOffset := uint32(50)
	if (bcast - base) < 60 {
		pStartOffset = 10
	}
	pStart := make(net.IP, 4)
	binary.BigEndian.PutUint32(pStart, base+pStartOffset)

	pEnd := make(net.IP, 4)
	binary.BigEndian.PutUint32(pEnd, bcast-2)

	return &parsedSubnet{
		cidr:      fmt.Sprintf("%s/%d", netIP.String(), ones),
		networkIP: netIP,
		gatewayIP: fmt.Sprintf("%s/%d", gw.String(), ones),
		poolStart: fmt.Sprintf("%s-%s", pStart.String(), pEnd.String()),
		poolEnd:   gw.String(),
		netObj:    ipNet,
	}, nil
}

func subnetsOverlap(a, b *net.IPNet) bool {
	return a.Contains(b.IP) || b.Contains(a.IP)
}

func (s *GovernanceService) GetStoreMikrotikProfile(ctx context.Context, actor *AuthenticatedStaff) (*domain.StoreMikrotikProfile, error) {
	if err := requireGovernanceOwner(actor); err != nil {
		return nil, err
	}
	storeID := actor.Staff.Store.ID
	var out *domain.StoreMikrotikProfile
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		var row domain.StoreMikrotikProfile
		qErr := tx.GetContext(ctx, &row, `
			SELECT store_id, router_model, wan_primary_interface, wan_backup_interface,
			       pos_vlan_cidr, staff_vlan_cidr, guest_vlan_cidr, pos_wifi_ssid, guest_wifi_ssid,
			       guest_rate_limit_mbps, pos_reserved_mbps, sftp_port, winbox_port,
			       winbox_allowed_cidr, dns_servers, generated_rsc_sha256, updated_by_user_id, updated_at
			FROM store_mikrotik_profiles
			WHERE store_id = $1
		`, storeID)
		if errors.Is(qErr, sql.ErrNoRows) {
			out = defaultStoreMikrotikProfile(storeID, s.nowFunc().UTC())
			return nil
		}
		if qErr != nil {
			return qErr
		}
		out = &row
		return nil
	})
	return out, err
}

func (s *GovernanceService) UpsertStoreMikrotikProfile(ctx context.Context, actor *AuthenticatedStaff, in MikrotikProfileInput) (*domain.StoreMikrotikProfile, error) {
	if err := requireGovernanceOwner(actor); err != nil {
		return nil, err
	}
	now := s.nowFunc().UTC()
	storeID := actor.Staff.Store.ID
	userID := actor.Staff.UserID
	def := defaultStoreMikrotikProfile(storeID, now)

	pickStr := func(val, fallback string) string {
		if strings.TrimSpace(val) == "" {
			return fallback
		}
		return strings.TrimSpace(val)
	}

	routerModel := pickStr(in.RouterModel, def.RouterModel)
	wanPrimary := pickStr(in.WANPrimaryInterface, def.WANPrimaryInterface)
	wanBackup := pickStr(in.WANBackupInterface, def.WANBackupInterface)
	posCIDR := pickStr(in.POSVLANCIDR, def.POSVLANCIDR)
	staffCIDR := pickStr(in.StaffVLANCIDR, def.StaffVLANCIDR)
	guestCIDR := pickStr(in.GuestVLANCIDR, def.GuestVLANCIDR)
	posSSID := pickStr(in.POSWiFiSSID, def.POSWiFiSSID)
	guestSSID := pickStr(in.GuestWiFiSSID, def.GuestWiFiSSID)
	winboxCIDR := pickStr(in.WinboxAllowedCIDR, posCIDR)
	dnsServers := pickStr(in.DNSServers, def.DNSServers)

	guestRate := in.GuestRateLimitMbps
	if guestRate == 0 {
		guestRate = def.GuestRateLimitMbps
	}
	posReserved := in.POSReservedMbps
	if posReserved == 0 {
		posReserved = def.POSReservedMbps
	}
	sftpPort := in.SFTPPort
	if sftpPort == 0 {
		sftpPort = def.SFTPPort
	}
	winboxPort := in.WinboxPort
	if winboxPort == 0 {
		winboxPort = def.WinboxPort
	}

	f := fieldErrors{}
	for k, v := range map[string]string{
		"router_model":          routerModel,
		"wan_primary_interface": wanPrimary,
		"wan_backup_interface":  wanBackup,
		"pos_wifi_ssid":         posSSID,
		"guest_wifi_ssid":       guestSSID,
	} {
		if !routerSafeTokenRegex.MatchString(v) {
			f.add(k, "mengandung karakter tidak valid untuk konfigurasi RouterOS")
		}
	}
	if strings.EqualFold(wanPrimary, wanBackup) {
		f.add("wan_backup_interface", "antarmuka WAN cadangan harus berbeda dari WAN utama")
	}

	posSub, errPOS := parseIPv4VLANSubnet(posCIDR)
	if errPOS != nil {
		f.add("pos_vlan_cidr", "wajib format CIDR IPv4 valid (/16 s.d. /28), contoh: 192.168.10.0/24")
	}
	staffSub, errStaff := parseIPv4VLANSubnet(staffCIDR)
	if errStaff != nil {
		f.add("staff_vlan_cidr", "wajib format CIDR IPv4 valid (/16 s.d. /28), contoh: 192.168.20.0/24")
	}
	guestSub, errGuest := parseIPv4VLANSubnet(guestCIDR)
	if errGuest != nil {
		f.add("guest_vlan_cidr", "wajib format CIDR IPv4 valid (/16 s.d. /28), contoh: 192.168.30.0/23")
	}
	if posSub != nil && staffSub != nil && subnetsOverlap(posSub.netObj, staffSub.netObj) {
		f.add("staff_vlan_cidr", "subnet Staff tidak boleh tumpang tindih dengan subnet POS/KDS")
	}
	if posSub != nil && guestSub != nil && subnetsOverlap(posSub.netObj, guestSub.netObj) {
		f.add("guest_vlan_cidr", "subnet Guest WiFi tidak boleh tumpang tindih dengan subnet POS/KDS")
	}
	if staffSub != nil && guestSub != nil && subnetsOverlap(staffSub.netObj, guestSub.netObj) {
		f.add("guest_vlan_cidr", "subnet Guest WiFi tidak boleh tumpang tindih dengan subnet Staff")
	}

	if _, _, err := net.ParseCIDR(winboxCIDR); err != nil {
		f.add("winbox_allowed_cidr", "wajib format CIDR IPv4 valid, contoh: 192.168.10.0/24")
	}
	if guestRate < 1 || guestRate > 10000 {
		f.add("guest_rate_limit_mbps", "harus antara 1 dan 10000 Mbps")
	}
	if posReserved < 1 || posReserved > 10000 {
		f.add("pos_reserved_mbps", "harus antara 1 dan 10000 Mbps")
	}
	if sftpPort == 21 || (sftpPort != 22 && (sftpPort < 1024 || sftpPort > 65535)) {
		f.add("sftp_port", "plain FTP port 21 dilarang keras; gunakan SFTP (SSH) pada port 22 atau 1024-65535 (disarankan 2222)")
	}
	if winboxPort < 1024 || winboxPort > 65535 {
		f.add("winbox_port", "port Winbox harus antara 1024 dan 65535")
	}

	dnsParts := strings.Split(dnsServers, ",")
	cleanDNS := make([]string, 0, len(dnsParts))
	for _, dp := range dnsParts {
		ip := net.ParseIP(strings.TrimSpace(dp))
		if ip == nil || ip.To4() == nil {
			f.add("dns_servers", "daftar DNS harus berupa alamat IPv4 dipisahkan koma, contoh: 1.1.1.1,8.8.8.8")
			break
		}
		cleanDNS = append(cleanDNS, ip.String())
	}
	if len(cleanDNS) == 0 {
		f.add("dns_servers", "minimal satu server DNS IPv4 wajib diisi")
	}
	if err := f.err(); err != nil {
		return nil, err
	}

	var out domain.StoreMikrotikProfile
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		err := tx.GetContext(ctx, &out, `
			INSERT INTO store_mikrotik_profiles (
				store_id, router_model, wan_primary_interface, wan_backup_interface,
				pos_vlan_cidr, staff_vlan_cidr, guest_vlan_cidr, pos_wifi_ssid, guest_wifi_ssid,
				guest_rate_limit_mbps, pos_reserved_mbps, sftp_port, winbox_port,
				winbox_allowed_cidr, dns_servers, updated_by_user_id, updated_at
			) VALUES (
				$1, $2, $3, $4,
				$5, $6, $7, $8, $9,
				$10, $11, $12, $13,
				$14, $15, $16, $17
			)
			ON CONFLICT (store_id) DO UPDATE SET
				router_model = EXCLUDED.router_model,
				wan_primary_interface = EXCLUDED.wan_primary_interface,
				wan_backup_interface = EXCLUDED.wan_backup_interface,
				pos_vlan_cidr = EXCLUDED.pos_vlan_cidr,
				staff_vlan_cidr = EXCLUDED.staff_vlan_cidr,
				guest_vlan_cidr = EXCLUDED.guest_vlan_cidr,
				pos_wifi_ssid = EXCLUDED.pos_wifi_ssid,
				guest_wifi_ssid = EXCLUDED.guest_wifi_ssid,
				guest_rate_limit_mbps = EXCLUDED.guest_rate_limit_mbps,
				pos_reserved_mbps = EXCLUDED.pos_reserved_mbps,
				sftp_port = EXCLUDED.sftp_port,
				winbox_port = EXCLUDED.winbox_port,
				winbox_allowed_cidr = EXCLUDED.winbox_allowed_cidr,
				dns_servers = EXCLUDED.dns_servers,
				updated_by_user_id = EXCLUDED.updated_by_user_id,
				updated_at = EXCLUDED.updated_at
			RETURNING store_id, router_model, wan_primary_interface, wan_backup_interface,
			          pos_vlan_cidr, staff_vlan_cidr, guest_vlan_cidr, pos_wifi_ssid, guest_wifi_ssid,
			          guest_rate_limit_mbps, pos_reserved_mbps, sftp_port, winbox_port,
			          winbox_allowed_cidr, dns_servers, generated_rsc_sha256, updated_by_user_id, updated_at
		`, storeID, routerModel, wanPrimary, wanBackup,
			posSub.cidr, staffSub.cidr, guestSub.cidr, posSSID, guestSSID,
			guestRate, posReserved, sftpPort, winboxPort,
			winboxCIDR, strings.Join(cleanDNS, ","), userID, now)
		if err != nil {
			return err
		}
		return s.writeGovernanceAuditTx(ctx, tx, storeID, &userID, "tenant_owner", "MIKROTIK_PROFILE_UPDATED",
			"Profil jaringan MikroTik RouterOS v7 & keamanan SFTP diperbarui",
			map[string]any{
				"pos_vlan":    out.POSVLANCIDR,
				"guest_vlan":  out.GuestVLANCIDR,
				"sftp_port":   out.SFTPPort,
				"winbox_port": out.WinboxPort,
			})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *GovernanceService) GenerateStoreMikrotikRsc(ctx context.Context, actor *AuthenticatedStaff) (*domain.MikrotikRscBundle, error) {
	if err := requireGovernanceOwner(actor); err != nil {
		return nil, err
	}
	profile, err := s.GetStoreMikrotikProfile(ctx, actor)
	if err != nil {
		return nil, err
	}

	posSub, err := parseIPv4VLANSubnet(profile.POSVLANCIDR)
	if err != nil {
		return nil, err
	}
	staffSub, err := parseIPv4VLANSubnet(profile.StaffVLANCIDR)
	if err != nil {
		return nil, err
	}
	guestSub, err := parseIPv4VLANSubnet(profile.GuestVLANCIDR)
	if err != nil {
		return nil, err
	}

	storeID := actor.Staff.Store.ID
	storeSlug := actor.Staff.Store.Slug
	userID := actor.Staff.UserID
	now := s.nowFunc().UTC()

	var nodes []domain.StoreNetworkNode
	if err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		return tx.SelectContext(ctx, &nodes, `
			SELECT id, store_id, node_name, node_type, connection_protocol, local_ip, local_port,
			       mac_address, paper_width_mm, drawer_kick_enabled, health_status, last_latency_ms,
			       last_heartbeat_at, metadata, is_active, created_at, updated_at
			FROM store_network_nodes
			WHERE store_id = $1 AND is_active = true
			ORDER BY node_type ASC, node_name ASC
		`, storeID)
	}); err != nil {
		return nil, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# ============================================================================\n")
	fmt.Fprintf(&b, "# MyCaffe POS by AzureDevID - MikroTik RouterOS v7 Baseline Automation (.rsc)\n")
	fmt.Fprintf(&b, "# Store Slug   : %s\n", storeSlug)
	fmt.Fprintf(&b, "# Router Model : %s\n", profile.RouterModel)
	fmt.Fprintf(&b, "# Generated At : %s\n", now.Format(time.RFC3339))
	fmt.Fprintf(&b, "# Security     : Plain FTP (21), Telnet (23), HTTP (80), API (8728) DISABLED\n")
	fmt.Fprintf(&b, "# ============================================================================\n\n")

	fmt.Fprintf(&b, "/system identity set name=\"mycaffe-%s\"\n", storeSlug)
	fmt.Fprintf(&b, "/ip dns set servers=%s allow-remote-requests=yes cache-size=4096KiB\n\n", profile.DNSServers)

	fmt.Fprintf(&b, "# 1. Bridge & 3-VLAN Segmentation (POS/KDS Critical, Staff Ops, Isolated Guest QR)\n")
	fmt.Fprintf(&b, "/interface bridge add name=br-mycaffe vlan-filtering=no comment=\"MyCaffe Core LAN Bridge\"\n")
	fmt.Fprintf(&b, "/interface vlan\n")
	fmt.Fprintf(&b, "add interface=br-mycaffe name=vlan10-pos-critical vlan-id=10 comment=\"POS Kasir, KDS, Thermal Printer TCP 9100, Local Print Agent\"\n")
	fmt.Fprintf(&b, "add interface=br-mycaffe name=vlan20-staff-ops vlan-id=20 comment=\"Perangkat Operasional Staf & Manajer\"\n")
	fmt.Fprintf(&b, "add interface=br-mycaffe name=vlan30-guest-qr vlan-id=30 comment=\"WiFi Pelanggan Scan QR Meja (Isolated)\"\n\n")

	fmt.Fprintf(&b, "# 2. IPv4 Addressing, DHCP Pools & Servers\n")
	fmt.Fprintf(&b, "/ip address\n")
	fmt.Fprintf(&b, "add address=%s interface=vlan10-pos-critical comment=\"Gateway VLAN 10 POS\"\n", posSub.gatewayIP)
	fmt.Fprintf(&b, "add address=%s interface=vlan20-staff-ops comment=\"Gateway VLAN 20 Staff\"\n", staffSub.gatewayIP)
	fmt.Fprintf(&b, "add address=%s interface=vlan30-guest-qr comment=\"Gateway VLAN 30 Guest QR\"\n\n", guestSub.gatewayIP)

	fmt.Fprintf(&b, "/ip pool\n")
	fmt.Fprintf(&b, "add name=pool-pos ranges=%s\n", posSub.poolStart)
	fmt.Fprintf(&b, "add name=pool-staff ranges=%s\n", staffSub.poolStart)
	fmt.Fprintf(&b, "add name=pool-guest ranges=%s\n\n", guestSub.poolStart)

	fmt.Fprintf(&b, "/ip dhcp-server\n")
	fmt.Fprintf(&b, "add name=dhcp-pos interface=vlan10-pos-critical address-pool=pool-pos lease-time=12h disabled=no\n")
	fmt.Fprintf(&b, "add name=dhcp-staff interface=vlan20-staff-ops address-pool=pool-staff lease-time=8h disabled=no\n")
	fmt.Fprintf(&b, "add name=dhcp-guest interface=vlan30-guest-qr address-pool=pool-guest lease-time=2h disabled=no\n\n")

	fmt.Fprintf(&b, "/ip dhcp-server network\n")
	fmt.Fprintf(&b, "add address=%s gateway=%s dns-server=%s comment=\"POS Critical Network\"\n", posSub.cidr, posSub.poolEnd, posSub.poolEnd)
	fmt.Fprintf(&b, "add address=%s gateway=%s dns-server=%s comment=\"Staff Network\"\n", staffSub.cidr, staffSub.poolEnd, staffSub.poolEnd)
	fmt.Fprintf(&b, "add address=%s gateway=%s dns-server=%s comment=\"Guest QR Network\"\n\n", guestSub.cidr, guestSub.poolEnd, guestSub.poolEnd)

	staticLeases := 0
	fmt.Fprintf(&b, "# 3. Static DHCP Reservations for Registered POS / Printer / KDS / Local Server Nodes\n")
	fmt.Fprintf(&b, "/ip dhcp-server lease\n")
	for _, n := range nodes {
		ip := net.ParseIP(strings.TrimSpace(n.LocalIP))
		mac := strings.ToUpper(strings.TrimSpace(n.MACAddress))
		if ip == nil || ip.To4() == nil || !macAddrRegex.MatchString(mac) {
			continue
		}
		staticLeases++
		safeComment := strings.ReplaceAll(n.NodeName, "\"", "")
		fmt.Fprintf(&b, "add address=%s mac-address=%s server=dhcp-pos comment=\"%s (%s:%d)\"\n",
			ip.String(), mac, safeComment, n.NodeType, n.LocalPort)
	}
	if staticLeases == 0 {
		fmt.Fprintf(&b, "# (Tambahkan MAC Address & Local IP pada panel Self-Maintain Perangkat untuk mengunci lease otomatis)\n")
	}
	fmt.Fprintf(&b, "\n")

	fmt.Fprintf(&b, "# 4. Dual-WAN Recursive Failover (Primary Fiber ISP -> 4G LTE Backup Modem)\n")
	fmt.Fprintf(&b, "/interface list\n")
	fmt.Fprintf(&b, "add name=WAN-MYCAFFE\n")
	fmt.Fprintf(&b, "add name=LAN-MYCAFFE\n")
	fmt.Fprintf(&b, "/interface list member\n")
	fmt.Fprintf(&b, "add list=WAN-MYCAFFE interface=\"%s\"\n", profile.WANPrimaryInterface)
	fmt.Fprintf(&b, "add list=WAN-MYCAFFE interface=\"%s\"\n", profile.WANBackupInterface)
	fmt.Fprintf(&b, "add list=LAN-MYCAFFE interface=vlan10-pos-critical\n")
	fmt.Fprintf(&b, "add list=LAN-MYCAFFE interface=vlan20-staff-ops\n")
	fmt.Fprintf(&b, "add list=LAN-MYCAFFE interface=vlan30-guest-qr\n\n")

	fmt.Fprintf(&b, "/ip firewall nat\n")
	fmt.Fprintf(&b, "add chain=srcnat out-interface-list=WAN-MYCAFFE action=masquerade comment=\"MyCaffe Dual-WAN NAT\"\n\n")

	fmt.Fprintf(&b, "# Catatan: sesuaikan gateway IP ISP Utama & Modem 4G pada menu IP -> Routes di Winbox bila tidak memakai DHCP-Client otomatis\n")
	fmt.Fprintf(&b, "/ip dhcp-client\n")
	fmt.Fprintf(&b, "add interface=\"%s\" default-route-distance=1 use-peer-dns=no disabled=no comment=\"WAN1 Primary Fiber\"\n", profile.WANPrimaryInterface)
	fmt.Fprintf(&b, "add interface=\"%s\" default-route-distance=2 use-peer-dns=no disabled=no comment=\"WAN2 Backup 4G LTE\"\n\n", profile.WANBackupInterface)

	fmt.Fprintf(&b, "# 5. QoS Priority for MyCaffe Cloud (HTTPS/WSS) & Midtrans Core API + Guest Bandwidth Cap\n")
	fmt.Fprintf(&b, "/ip firewall address-list\n")
	fmt.Fprintf(&b, "add list=mycaffe-critical-cloud address=abbdullaziz.online comment=\"MyCaffe Production Root\"\n")
	fmt.Fprintf(&b, "add list=mycaffe-critical-cloud address=demo.abbdullaziz.online comment=\"MyCaffe Cloud API & WSS\"\n")
	fmt.Fprintf(&b, "add list=mycaffe-critical-cloud address=api.midtrans.com comment=\"Midtrans Production Core API\"\n")
	fmt.Fprintf(&b, "add list=mycaffe-critical-cloud address=api.sandbox.midtrans.com comment=\"Midtrans Sandbox Core API\"\n\n")

	fmt.Fprintf(&b, "/ip firewall mangle\n")
	fmt.Fprintf(&b, "add chain=prerouting src-address=%s dst-address-list=mycaffe-critical-cloud action=mark-connection new-connection-mark=pos-cloud-conn passthrough=yes\n", posSub.cidr)
	fmt.Fprintf(&b, "add chain=prerouting connection-mark=pos-cloud-conn action=mark-packet new-packet-mark=pos-cloud-pkt passthrough=no\n")
	fmt.Fprintf(&b, "add chain=prerouting src-address=%s action=mark-connection new-connection-mark=guest-wifi-conn passthrough=yes\n", guestSub.cidr)
	fmt.Fprintf(&b, "add chain=prerouting connection-mark=guest-wifi-conn action=mark-packet new-packet-mark=guest-wifi-pkt passthrough=no\n\n")

	fmt.Fprintf(&b, "/queue simple\n")
	fmt.Fprintf(&b, "add name=\"01-POS-Midtrans-Guaranteed\" target=%s packet-marks=pos-cloud-pkt priority=1/1 limit-at=%dM/%dM max-limit=100M/100M comment=\"Jaminan latensi rendah POS, KDS WSS & Midtrans QRIS\"\n",
		posSub.cidr, profile.POSReservedMbps, profile.POSReservedMbps)
	fmt.Fprintf(&b, "add name=\"02-Guest-QR-WiFi-Cap\" target=%s packet-marks=guest-wifi-pkt priority=8/8 max-limit=%dM/%dM queue=pcq-upload-default/pcq-download-default comment=\"Batas kuota adil WiFi pelanggan\"\n\n",
		guestSub.cidr, profile.GuestRateLimitMbps, profile.GuestRateLimitMbps)

	fmt.Fprintf(&b, "# 6. Service Hardening (Winbox LAN-Only, SFTP Custom Port, Plain FTP Port 21 BLOCKED)\n")
	fmt.Fprintf(&b, "/ip service\n")
	fmt.Fprintf(&b, "set telnet disabled=yes\n")
	fmt.Fprintf(&b, "set ftp disabled=yes\n")
	fmt.Fprintf(&b, "set www disabled=yes\n")
	fmt.Fprintf(&b, "set api disabled=yes\n")
	fmt.Fprintf(&b, "set api-ssl disabled=yes\n")
	fmt.Fprintf(&b, "set ssh port=%d address=%s disabled=no\n", profile.SFTPPort, profile.WinboxAllowedCIDR)
	fmt.Fprintf(&b, "set winbox port=%d address=%s disabled=no\n", profile.WinboxPort, profile.WinboxAllowedCIDR)
	fmt.Fprintf(&b, "/tool mac-server set allowed-interface-list=LAN-MYCAFFE\n")
	fmt.Fprintf(&b, "/tool mac-server mac-winbox set allowed-interface-list=LAN-MYCAFFE\n\n")

	fmt.Fprintf(&b, "# 7. Stateful Firewall & Strict Guest-to-POS Isolation\n")
	fmt.Fprintf(&b, "/ip firewall filter\n")
	fmt.Fprintf(&b, "add chain=input connection-state=established,related,untracked action=accept comment=\"Accept established input\"\n")
	fmt.Fprintf(&b, "add chain=input connection-state=invalid action=drop comment=\"Drop invalid input\"\n")
	fmt.Fprintf(&b, "add chain=input in-interface=vlan30-guest-qr dst-port=%d,%d,21,22,23,80,8728 protocol=tcp action=drop comment=\"Block Guest WiFi from Winbox/SFTP/Router Management\"\n",
		profile.WinboxPort, profile.SFTPPort)
	fmt.Fprintf(&b, "add chain=input in-interface-list=WAN-MYCAFFE action=drop comment=\"Drop unsolicited WAN input\"\n")
	fmt.Fprintf(&b, "add chain=forward connection-state=established,related,untracked action=accept comment=\"Accept established forward\"\n")
	fmt.Fprintf(&b, "add chain=forward connection-state=invalid action=drop comment=\"Drop invalid forward\"\n")
	fmt.Fprintf(&b, "add chain=forward in-interface=vlan30-guest-qr out-interface=vlan10-pos-critical action=drop comment=\"CRITICAL: Block Guest WiFi -> POS/Printer TCP 9100/Local Server\"\n")
	fmt.Fprintf(&b, "add chain=forward in-interface=vlan30-guest-qr out-interface=vlan20-staff-ops action=drop comment=\"Block Guest WiFi -> Staff VLAN\"\n")
	fmt.Fprintf(&b, "# Aktifkan baris di bawah pada akhir setup melalui Safe Mode di Winbox:\n")
	fmt.Fprintf(&b, "# /interface bridge set br-mycaffe vlan-filtering=yes\n")

	rscScript := b.String()
	sum := sha256.Sum256([]byte(rscScript))
	scriptHash := hex.EncodeToString(sum[:])

	_ = database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		_, _ = tx.ExecContext(ctx, `
			INSERT INTO store_mikrotik_profiles (
				store_id, router_model, wan_primary_interface, wan_backup_interface,
				pos_vlan_cidr, staff_vlan_cidr, guest_vlan_cidr, pos_wifi_ssid, guest_wifi_ssid,
				guest_rate_limit_mbps, pos_reserved_mbps, sftp_port, winbox_port,
				winbox_allowed_cidr, dns_servers, generated_rsc_sha256, updated_by_user_id, updated_at
			) VALUES (
				$1, $2, $3, $4,
				$5, $6, $7, $8, $9,
				$10, $11, $12, $13,
				$14, $15, $16, $17, $18
			)
			ON CONFLICT (store_id) DO UPDATE SET
				generated_rsc_sha256 = EXCLUDED.generated_rsc_sha256,
				updated_by_user_id = EXCLUDED.updated_by_user_id,
				updated_at = EXCLUDED.updated_at
		`, storeID, profile.RouterModel, profile.WANPrimaryInterface, profile.WANBackupInterface,
			posSub.cidr, staffSub.cidr, guestSub.cidr, profile.POSWiFiSSID, profile.GuestWiFiSSID,
			profile.GuestRateLimitMbps, profile.POSReservedMbps, profile.SFTPPort, profile.WinboxPort,
			profile.WinboxAllowedCIDR, profile.DNSServers, scriptHash, userID, now)
		profile.GeneratedRscSHA256 = scriptHash
		return s.writeGovernanceAuditTx(ctx, tx, storeID, &userID, "tenant_owner", "MIKROTIK_RSC_GENERATED",
			"Skrip otomasi MikroTik RouterOS v7 (.rsc) dihasilkan",
			map[string]any{"script_sha256": scriptHash, "static_dhcp_leases": staticLeases})
	})

	winboxSteps := []domain.WinboxGuideStep{
		{
			StepNumber:  1,
			Title:       "Koneksi Awal via MAC-Winbox & Aktifkan Safe Mode",
			MenuPath:    "Winbox -> Neighbors -> Klik MAC Address -> Tombol [Safe Mode] (Ctrl+X)",
			Instruction: "Hubungkan laptop teknisi melalui kabel LAN ke port ether3/ether4, masuk via MAC Address di tab Neighbors, lalu wajib tekan tombol Safe Mode di kiri atas agar router otomatis rollback bila koneksi terputus.",
			SafetyCheck: "Indikator Safe Mode menyala hijau sebelum menjalankan perintah apa pun.",
		},
		{
			StepNumber:  2,
			Title:       "Backup Konfigurasi Lama Sebelum Perubahan",
			MenuPath:    "Files -> Backup -> Save (enkripsi AES-SHA256)",
			Instruction: "Simpan cadangan konfigurasi router saat ini dan unduh salinannya ke laptop lokal.",
			SafetyCheck: "Berkas .backup muncul di daftar Files.",
		},
		{
			StepNumber:  3,
			Title:       "Unggah & Impor Skrip Otomasi MyCaffe (.rsc)",
			MenuPath:    "Files -> Upload -> New Terminal -> /import file-name=mycaffe-" + storeSlug + ".rsc",
			Instruction: fmt.Sprintf("Unggah berkas mycaffe-%s.rsc ke panel Files, buka New Terminal, lalu jalankan perintah import hingga muncul pesan 'Script file loaded and executed successfully'.", storeSlug),
			SafetyCheck: "Tidak ada baris merah error pada jendela Terminal.",
		},
		{
			StepNumber:  4,
			Title:       "Verifikasi Static DHCP Printer ESC/POS & KDS",
			MenuPath:    "IP -> DHCP Server -> Leases",
			Instruction: fmt.Sprintf("Pastikan %d perangkat kasir/printer terdaftar memiliki status Bound di subnet %s sehingga IP printer TCP:9100 tidak pernah berubah saat mati listrik.", staticLeases, posSub.cidr),
			SafetyCheck: "Seluruh printer thermal & terminal KDS berada di VLAN 10.",
		},
		{
			StepNumber:  5,
			Title:       "Uji Isolasi WiFi Pelanggan & Failover Modem 4G",
			MenuPath:    "IP -> Firewall -> Filter Rules & IP -> Routes",
			Instruction: fmt.Sprintf("Sambungkan ponsel ke SSID %s (%s): pastikan halaman QR Order https://demo.abbdullaziz.online terbuka cepat namun ping ke printer kasir (%s) dan port Winbox (%d) ditolak total.", profile.GuestWiFiSSID, guestSub.cidr, posSub.poolEnd, profile.WinboxPort),
			SafetyCheck: "Counter paket pada rule 'CRITICAL: Block Guest WiFi -> POS' bertambah saat diuji.",
		},
		{
			StepNumber:  6,
			Title:       "Kunci Bridge VLAN Filtering & Lepas Safe Mode",
			MenuPath:    "Bridge -> br-mycaffe -> VLANs -> Centang VLAN Filtering -> Tekan [Safe Mode] lagi",
			Instruction: "Setelah port trunk/access sesuai dengan topologi switch gerai, aktifkan VLAN Filtering. Jika koneksi Winbox tetap stabil selama 30 detik, tekan kembali tombol Safe Mode untuk menyimpan permanen.",
			SafetyCheck: "Koneksi kasir ke Cloud WSS & Midtrans Core API hijau pada Simulasi Jaringan MyCaffe.",
		},
	}

	sftpPolicy := domain.FileZillaSFTPPolicy{
		Protocol:               "SFTP (SSH File Transfer Protocol - Ed25519)",
		Port:                   profile.SFTPPort,
		AllowedSubnetCIDR:      profile.WinboxAllowedCIDR,
		PlainFTPBlocked:        true,
		PlainFTPPortBlocked:    21,
		KeyAuthRecommended:     true,
		RecommendedAlternative: "Cloudflare R2 Presigned Upload (Media Produk) & MyCaffe Offline Sync API (Data Transaksi)",
		SecurityRationale:      "Protokol FTP murni (port 21) mengirim kredensial dalam teks terbuka (cleartext) yang rentan disadap di jaringan WiFi kafe. Bila transfer berkas manual ke Local Edge Server diperlukan via FileZilla, wajib memilih protokol SFTP di port khusus dengan kunci SSH Ed25519 hanya dari VLAN POS.",
	}

	return &domain.MikrotikRscBundle{
		Profile:             profile,
		FileName:            fmt.Sprintf("mycaffe-%s-routeros7.rsc", storeSlug),
		RscScript:           rscScript,
		ScriptSHA256:        scriptHash,
		WinboxSteps:         winboxSteps,
		FileZillaSFTPPolicy: sftpPolicy,
		TopologySummary: domain.MikrotikTopologySummary{
			POSSubnetCIDR:        posSub.cidr,
			StaffSubnetCIDR:      staffSub.cidr,
			GuestSubnetCIDR:      guestSub.cidr,
			StaticDHCPCount:      staticLeases,
			DualWANFailoverReady: profile.WANPrimaryInterface != "" && profile.WANBackupInterface != "",
			OfflineSyncMode:      "IndexedDB Device Queue + Local Print Agent LAN Relay -> /api/v1/device/offline-orders/sync",
		},
		GeneratedAt: now,
	}, nil
}
