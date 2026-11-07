package handler_test

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/midtrans/midtrans-go/coreapi"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/handler"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/midtrans"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/secretbox"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

type stubGatewayClient struct {
	label          string
	chargeCalls    int
	lastChargeID   string
	lastChargeAmt  int64
	qrStringToGive string
}

func (g *stubGatewayClient) ChargeCoreAPI(_ context.Context, input midtrans.ChargeRequest) (*coreapi.ChargeResponse, error) {
	g.chargeCalls++
	g.lastChargeID = input.OrderID
	g.lastChargeAmt = input.Amount
	qr := g.qrStringToGive
	if qr == "" {
		qr = "00020101021226680016ID.CO.QRIS.WWW-" + g.label
	}
	return &coreapi.ChargeResponse{
		StatusCode:        "201",
		StatusMessage:     "Success, QRIS transaction is created",
		TransactionID:     "tx-" + g.label + "-" + input.OrderID[:8],
		OrderID:           input.OrderID,
		GrossAmount:       fmt.Sprintf("%d.00", input.Amount),
		PaymentType:       "qris",
		TransactionStatus: "pending",
		QRString:          qr,
	}, nil
}

func (g *stubGatewayClient) CheckTransaction(_ context.Context, providerOrderID string) (*coreapi.TransactionStatusResponse, error) {
	return &coreapi.TransactionStatusResponse{
		StatusCode:        "200",
		TransactionID:     "tx-" + g.label + "-" + providerOrderID[:8],
		OrderID:           providerOrderID,
		GrossAmount:       fmt.Sprintf("%d.00", g.lastChargeAmt),
		Currency:          "IDR",
		PaymentType:       "qris",
		TransactionStatus: "settlement",
		FraudStatus:       "accept",
	}, nil
}

func (g *stubGatewayClient) DirectRefund(_ context.Context, providerOrderID, _ string, amount int64, _ string) (*coreapi.RefundResponse, error) {
	return &coreapi.RefundResponse{
		StatusCode:   "200",
		RefundAmount: fmt.Sprintf("%d.00", amount),
		OrderID:      providerOrderID,
	}, nil
}

func signMidtransWebhook(orderID, statusCode, grossAmount, serverKey string) string {
	sum := sha512.Sum512([]byte(orderID + statusCode + grossAmount + serverKey))
	return hex.EncodeToString(sum[:])
}

// TestGovernanceIntegration verifies ADR-009 end-to-end against PostgreSQL 18 with mycaffe_app (NOBYPASSRLS):
//  1. Public legal docs, IDR pricing plans (0% commission), and idempotent onboarding with NIB/NPWP masking.
//  2. RBAC owner-only guard vs cashier denial.
//  3. AES-256-GCM BYOK Midtrans encryption bound to store_id AAD + cross-tenant RLS isolation.
//  4. Per-tenant BYOK dynamic QRIS charge routing + per-store SHA-512 webhook verification.
//  5. Storefront CMS bilingual copy + anti-slop R-02 em-dash rejection + public storefront endpoint.
//  6. Custom SMTP/WhatsApp secret masking + Self-Maintain LAN/ESC-POS nodes + fault-injection simulator.
//  7. Lane 1 Platform Subscription Invoice isolated from Lane 2 tenant F&B gateway.
//  8. Superadmin fail-closed auth (503/401/200), Midtrans Business Review readiness audit, and Tax-Safe Account Clearance.
func TestGovernanceIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping governance integration test")
	}
	db, err := database.NewPostgresDB(&config.Config{DatabaseURL: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	storeAID := uuid.MustParse("11111111-1111-4111-8111-111111111111") // central-cafe
	storeBID := uuid.MustParse("22222222-2222-4222-8222-222222222222") // express-counter

	// Ensure state on shared seeded stores is cleanly disarmed after this test finishes.
	t.Cleanup(func() {
		for _, sid := range []uuid.UUID{storeAID, storeBID} {
			_ = database.WithTenantTx(ctx, db, sid, func(tx *sqlx.Tx) error {
				_, _ = tx.ExecContext(ctx, `UPDATE store_payment_credentials SET is_enabled = false WHERE store_id = $1`, sid)
				_, _ = tx.ExecContext(ctx, `UPDATE store_kyb_profiles SET subscription_status = 'active', kyb_status = 'verified' WHERE store_id = $1`, sid)
				_, _ = tx.ExecContext(ctx, `UPDATE platform_tenant_registry SET subscription_status = 'active', byok_midtrans_enabled = false WHERE store_id = $1`, sid)
				return nil
			})
		}
	})

	encKey := strings.Repeat("a1", 32)
	platformAdminKey := strings.Repeat("admin-test-", 4)
	tenantAServerKey := "SB-Mid-server-" + strings.Repeat("A", 18)
	defaultFallbackKey := "SB-Mid-server-" + strings.Repeat("F", 18)

	defaultGateway := &stubGatewayClient{label: "default-fallback"}
	platformBillingGateway := &stubGatewayClient{label: "azuredevid-lane1", qrStringToGive: "000201010212-LANE1-SAAS-INVOICE"}
	byokGatewaysByKey := map[string]*stubGatewayClient{}

	cfg := &config.Config{
		AppEnv:                       "staging",
		CredentialEncryptionKey:      encKey,
		PlatformAdminAPIKey:          platformAdminKey,
		MidtransIsProduction:         false,
		MidtransServerKey:            defaultFallbackKey,
		PlatformMidtransIsProduction: false,
	}

	govSvc := service.NewGovernanceService(
		db,
		cfg,
		service.WithPlatformGatewayClient(platformBillingGateway),
		service.WithGovernanceGatewayBuilder(func(serverKey string, _ bool) service.GatewayClient {
			gw, ok := byokGatewaysByKey[serverKey]
			if !ok {
				gw = &stubGatewayClient{label: "byok-" + serverKey[len(serverKey)-4:]}
				byokGatewaysByKey[serverKey] = gw
			}
			return gw
		}),
	)

	orderRepo := postgres.NewOrderRepository()
	paymentRepo := postgres.NewPaymentRepository()
	orderSvc := service.NewOrderService(db, orderRepo)
	paymentSvc := service.NewPaymentService(
		db,
		paymentRepo,
		orderRepo,
		defaultGateway,
		service.WithStoreGatewayResolver(govSvc.ResolveStoreGatewayClientTx),
	)

	authSvc := service.NewAuthService(db, postgres.NewAuthRepository(), "integration-test-jwt-secret-at-least-32-chars", service.AuthPolicy{
		AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 12 * time.Hour, SessionIdleTimeout: 2 * time.Hour,
		MaxSessions: 5, PINMaxFailures: 5, PINLockout: 15 * time.Minute, MaxConcurrentHashes: 8,
	})
	ah := handler.NewAuthHandler(authSvc)
	gh := handler.NewGovernanceHandler(govSvc, platformAdminKey)
	mh := handler.NewMidtransHandler(midtrans.NewClientWithKey(defaultFallbackKey, false), nil)
	mh.SetPaymentService(paymentSvc)
	mh.SetGovernanceService(govSvc)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.POST("/auth/staff/login", ah.Login)
	v1.POST("/payments/midtrans/webhook/:store_slug", mh.HandleTenantWebhook)

	v1.GET("/public/legal", gh.ListPublicLegalDocuments)
	v1.GET("/public/plans", gh.ListPublicSubscriptionPlans)
	v1.POST("/public/onboarding/apply", gh.SubmitOnboardingApplication)
	v1.GET("/public/stores/:slug/cms", gh.GetPublicStoreCMS)

	ownerManage := ah.RequirePermission(rbac.StoreSettingsManage)
	storeGov := v1.Group("/store", ownerManage)
	storeGov.GET("/legal/acceptances", gh.ListTenantLegalAcceptances)
	storeGov.POST("/legal/:id/accept", gh.AcceptLegalDocument)
	storeGov.GET("/kyb", gh.GetStoreKYB)
	storeGov.PUT("/kyb", gh.UpsertStoreKYB)
	storeGov.GET("/payment-credentials", gh.GetStorePaymentCredentials)
	storeGov.PUT("/payment-credentials", gh.UpsertStorePaymentCredentials)
	storeGov.GET("/cms", gh.GetStoreCMS)
	storeGov.PUT("/cms", gh.UpsertStoreCMS)
	storeGov.GET("/notifications", gh.GetStoreNotifications)
	storeGov.PUT("/notifications", gh.UpsertStoreNotifications)
	storeGov.GET("/network/nodes", gh.ListNetworkNodes)
	storeGov.PUT("/network/nodes", gh.UpsertNetworkNode)
	storeGov.POST("/network/simulate", gh.RunNetworkSimulation)
	storeGov.GET("/subscription/invoices", gh.ListStoreSubscriptionInvoices)
	storeGov.POST("/subscription/invoices", gh.CreateStoreSubscriptionInvoice)
	storeGov.POST("/clearance", gh.RequestAccountClearance)
	storeGov.POST("/clearance/:id/cancel", gh.CancelAccountClearance)

	platform := v1.Group("/platform", gh.RequirePlatformAdmin())
	platform.GET("/overview", gh.PlatformOverview)
	platform.GET("/onboarding", gh.ListOnboardingApplications)
	platform.POST("/onboarding/:id/review", gh.ReviewOnboardingApplication)
	platform.POST("/tenants/:store_id/kyb-review", gh.ReviewStoreKYB)
	platform.POST("/tenants/:store_id/clearance/:id/execute", gh.ExecuteTenantClearance)
	platform.GET("/midtrans-readiness", gh.MidtransBusinessReviewReadiness)

	call := func(method, path string, headers map[string]string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	loginBearer := func(storeSlug, email string) string {
		t.Helper()
		res := call("POST", "/api/v1/auth/staff/login", nil, map[string]string{
			"store_slug": storeSlug,
			"email":      email,
			"pin":        "482913",
		})
		if res.Code != http.StatusOK {
			t.Fatalf("login %s@%s failed: %d %s", email, storeSlug, res.Code, res.Body.String())
		}
		var out struct {
			AccessToken string `json:"access_token"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &out)
		return "Bearer " + out.AccessToken
	}

	ownerA := map[string]string{"Authorization": loginBearer("central-cafe", "abdulaziz543333@gmail.com")}
	cashierA := map[string]string{"Authorization": loginBearer("central-cafe", "cashier.central@mycaffe.local")}
	ownerB := map[string]string{"Authorization": loginBearer("express-counter", "abdulaziz543333@gmail.com")}
	superadmin := map[string]string{handler.PlatformAdminHeader: platformAdminKey}

	var tosDocID uuid.UUID
	var tosSHA, pdpSHA string
	var onboardingAppID uuid.UUID

	t.Run("PublicPlansLegalAndIdempotentOnboarding", func(t *testing.T) {
		plansRes := call("GET", "/api/v1/public/plans", nil, nil)
		if plansRes.Code != http.StatusOK {
			t.Fatalf("expected 200 plans, got %d: %s", plansRes.Code, plansRes.Body.String())
		}
		var plansBody struct {
			Plans []domain.SubscriptionPlan `json:"plans"`
		}
		_ = json.Unmarshal(plansRes.Body.Bytes(), &plansBody)
		if len(plansBody.Plans) < 4 {
			t.Fatalf("expected at least 4 plans, got %d", len(plansBody.Plans))
		}
		for _, p := range plansBody.Plans {
			if p.CommissionBPS != 0 {
				t.Fatalf("plan %s must have 0%% transaction commission, got %d bps", p.Code, p.CommissionBPS)
			}
		}

		legalRes := call("GET", "/api/v1/public/legal?locale=id-ID", nil, nil)
		if legalRes.Code != http.StatusOK {
			t.Fatalf("expected 200 legal docs, got %d: %s", legalRes.Code, legalRes.Body.String())
		}
		var legalBody struct {
			Documents []domain.LegalDocument `json:"documents"`
		}
		_ = json.Unmarshal(legalRes.Body.Bytes(), &legalBody)
		if len(legalBody.Documents) < 4 {
			t.Fatalf("expected 4 seeded legal documents, got %d", len(legalBody.Documents))
		}
		for _, d := range legalBody.Documents {
			if d.DocType == domain.LegalDocTermsOfService {
				tosDocID = d.ID
				tosSHA = strings.TrimSpace(d.ContentSHA256)
			}
			if d.DocType == domain.LegalDocPrivacyPDP {
				pdpSHA = strings.TrimSpace(d.ContentSHA256)
			}
		}

		idemKey := "onboard-" + uuid.NewString()
		rawNIB := "9120300988771"
		rawNPWP := "092233445566000"
		slug := "kopi-nusantara-" + strings.ToLower(uuid.NewString()[:6])
		applyPayload := map[string]any{
			"idempotency_key":      idemKey,
			"requested_slug":       slug,
			"business_name":        "Kopi Nusantara Roasters",
			"business_entity_type": "perorangan",
			"owner_name":           "Budi Santoso",
			"owner_email":          "budi@kopinusantara.id",
			"owner_phone":          "+6281299887766",
			"city_region":          "Jakarta Selatan",
			"selected_plan_code":   "pro_cafe",
			"billing_cycle":        "monthly",
			"nib":                  rawNIB,
			"npwp":                 rawNPWP,
			"accepted_tos_sha256":  tosSHA,
			"accepted_pdp_sha256":  pdpSHA,
		}
		apply1 := call("POST", "/api/v1/public/onboarding/apply", nil, applyPayload)
		if apply1.Code != http.StatusCreated {
			t.Fatalf("expected 201 onboarding apply, got %d: %s", apply1.Code, apply1.Body.String())
		}
		if strings.Contains(apply1.Body.String(), rawNIB) || strings.Contains(apply1.Body.String(), rawNPWP) {
			t.Fatalf("SECURITY VIOLATION: raw NIB/NPWP leaked in onboarding response: %s", apply1.Body.String())
		}
		var applyResp struct {
			Application domain.TenantOnboardingApplication `json:"application"`
			Replay      bool                               `json:"is_idempotent_replay"`
		}
		_ = json.Unmarshal(apply1.Body.Bytes(), &applyResp)
		onboardingAppID = applyResp.Application.ID

		// Idempotent replay with same key & slug returns 200
		apply2 := call("POST", "/api/v1/public/onboarding/apply", nil, applyPayload)
		if apply2.Code != http.StatusOK {
			t.Fatalf("expected 200 on idempotent onboarding replay, got %d", apply2.Code)
		}

		// Reuse of idempotency_key with different slug returns 409
		applyPayload["requested_slug"] = slug + "-other"
		applyConflict := call("POST", "/api/v1/public/onboarding/apply", nil, applyPayload)
		if applyConflict.Code != http.StatusConflict {
			t.Fatalf("expected 409 on conflicting onboarding idempotency_key, got %d", applyConflict.Code)
		}
	})

	t.Run("RBACEnforcementCashierDeniedOwnerAllowed", func(t *testing.T) {
		for _, ep := range []struct {
			method string
			path   string
			body   any
		}{
			{"PUT", "/api/v1/store/payment-credentials", map[string]any{"environment": "sandbox"}},
			{"PUT", "/api/v1/store/cms", map[string]any{"default_locale": "id-ID"}},
			{"PUT", "/api/v1/store/kyb", map[string]any{"legal_business_name": "X"}},
			{"POST", "/api/v1/store/clearance", map[string]any{"reason": "Testing cashier block"}},
		} {
			res := call(ep.method, ep.path, cashierA, ep.body)
			if res.Code != http.StatusForbidden {
				t.Fatalf("cashier must receive 403 on %s %s, got %d", ep.method, ep.path, res.Code)
			}
		}

		accRes := call("POST", "/api/v1/store/legal/"+tosDocID.String()+"/accept", ownerA, nil)
		if accRes.Code != http.StatusCreated {
			t.Fatalf("owner legal accept failed: %d %s", accRes.Code, accRes.Body.String())
		}

		kybRes := call("PUT", "/api/v1/store/kyb", ownerA, map[string]any{
			"entity_type":         "perorangan",
			"legal_business_name": "AzureDevID Flagship Roastery",
			"nib":                 "1234567890123",
			"npwp":                "987654321098000",
			"pse_registration_no": "PSE-PB-UMKU-2026",
			"plan_code":           "pro_cafe",
			"billing_cycle":       "monthly",
		})
		if kybRes.Code != http.StatusOK {
			t.Fatalf("owner KYB update failed: %d %s", kybRes.Code, kybRes.Body.String())
		}
		if strings.Contains(kybRes.Body.String(), "1234567890123") {
			t.Fatalf("raw NIB leaked in KYB response")
		}
	})

	t.Run("CrossTenantRLSIsolationAndBYOKMidtrans", func(t *testing.T) {
		// Reject live production key activation in staging environment
		prodReject := call("PUT", "/api/v1/store/payment-credentials", ownerA, map[string]any{
			"environment": "production",
			"merchant_id": "G889900112",
			"client_key":  "Mid-client-ProdKeyExample1234",
			"server_key":  "Mid-server-ProdKeyExample1234",
			"is_enabled":  true,
		})
		if prodReject.Code != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422 when enabling production BYOK in staging, got %d: %s", prodReject.Code, prodReject.Body.String())
		}

		saveA := call("PUT", "/api/v1/store/payment-credentials", ownerA, map[string]any{
			"environment":                  "sandbox",
			"merchant_id":                  "G111111111",
			"client_key":                   "SB-Mid-client-TenantAClientKey11",
			"server_key":                   tenantAServerKey,
			"is_enabled":                   true,
			"fallback_static_qris_enabled": true,
		})
		if saveA.Code != http.StatusOK {
			t.Fatalf("expected 200 saving Store A BYOK credential, got %d: %s", saveA.Code, saveA.Body.String())
		}
		if strings.Contains(saveA.Body.String(), tenantAServerKey) {
			t.Fatalf("SECURITY VIOLATION: plaintext Midtrans server key leaked in response: %s", saveA.Body.String())
		}

		// Store B must NOT see Store A's BYOK Midtrans credentials
		getB := call("GET", "/api/v1/store/payment-credentials", ownerB, nil)
		if getB.Code != http.StatusOK {
			t.Fatalf("expected 200 reading Store B credential state, got %d", getB.Code)
		}
		if strings.Contains(getB.Body.String(), "G111111111") || strings.Contains(getB.Body.String(), "AAAA") {
			t.Fatalf("RLS ISOLATION VIOLATION: Store B saw Store A credentials: %s", getB.Body.String())
		}

		// Verify ciphertext in DB cannot be decrypted using Store B's AAD
		box, _ := secretbox.New(encKey)
		var cipherA []byte
		if err := database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &cipherA, `SELECT server_key_ciphertext FROM store_payment_credentials WHERE store_id = $1`, storeAID)
		}); err != nil {
			t.Fatalf("failed reading Store A ciphertext: %v", err)
		}
		if _, err := box.Open(cipherA, fmt.Sprintf("store:%s:midtrans_server_key", storeBID.String())); err == nil {
			t.Fatalf("expected AES-256-GCM AAD check to block cross-tenant ciphertext transplant")
		}
	})

	t.Run("PerTenantBYOKChargeRoutingAndWebhookSignature", func(t *testing.T) {
		var itemRow struct {
			ProductID uuid.UUID  `db:"product_id"`
			VariantID *uuid.UUID `db:"variant_id"`
		}
		if err := database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &itemRow, `
				SELECT p.id AS product_id, pv.id AS variant_id
				FROM products p
				LEFT JOIN product_variants pv ON pv.store_id = p.store_id AND pv.product_id = p.id AND pv.is_available = true
				WHERE p.store_id = $1
				  AND p.is_available = true
				  AND p.track_stock = false
				  AND p.product_type <> 'bundle'
				  AND NOT EXISTS (
				      SELECT 1 FROM product_modifier_groups pmg
				      JOIN modifier_groups mg ON mg.store_id = pmg.store_id AND mg.id = pmg.group_id
				      WHERE pmg.store_id = p.store_id AND pmg.product_id = p.id AND (mg.is_required = true OR mg.min_select > 0)
				  )
				LIMIT 1
			`, storeAID)
		}); err != nil {
			t.Fatalf("failed to pick simple product in Store A: %v", err)
		}

		createdOrder, err := orderSvc.CreateOrder(ctx, storeAID, service.OrderCreationInput{
			IdempotencyKey: "gov-byok-order-" + uuid.NewString(),
			OrderType:      domain.OrderTypeTakeaway,
			CustomerName:   "Tamu QR BYOK",
			Items: []service.OrderItemInput{
				{ProductID: itemRow.ProductID, VariantID: itemRow.VariantID, Quantity: 1},
			},
		})
		if err != nil {
			t.Fatalf("CreateOrder failed: %v", err)
		}

		payRes, err := paymentSvc.InitiatePayment(ctx, storeAID, service.InitiatePaymentInput{
			OrderID:        createdOrder.Order.ID,
			IdempotencyKey: "gov-byok-pay-" + uuid.NewString(),
			Method:         domain.PaymentMethodQRIS,
		})
		if err != nil {
			t.Fatalf("InitiatePayment with BYOK failed: %v", err)
		}

		tenantGW := byokGatewaysByKey[tenantAServerKey]
		if tenantGW == nil || tenantGW.chargeCalls != 1 {
			t.Fatalf("expected Store A BYOK gateway to receive exactly 1 ChargeCoreAPI call, defaultGateway calls=%d", defaultGateway.chargeCalls)
		}
		if defaultGateway.chargeCalls != 0 {
			t.Fatalf("expected default gateway NOT to be called when Store A BYOK is enabled, got %d", defaultGateway.chargeCalls)
		}

		grossStr := fmt.Sprintf("%d.00", payRes.Payment.Amount)
		providerOrderID := payRes.Payment.ProviderOrderID

		// 1. Webhook signed with default fallback key MUST be rejected (401) on Store A's BYOK webhook endpoint
		badWebhook := map[string]any{
			"transaction_status": "settlement",
			"transaction_id":     "tx-byok-settlement-1",
			"status_code":        "200",
			"order_id":           providerOrderID,
			"gross_amount":       grossStr,
			"fraud_status":       "accept",
			"payment_type":       "qris",
			"currency":           "IDR",
			"signature_key":      signMidtransWebhook(providerOrderID, "200", grossStr, defaultFallbackKey),
		}
		badRes := call("POST", "/api/v1/payments/midtrans/webhook/central-cafe", nil, badWebhook)
		if badRes.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 when signing Store A webhook with wrong key, got %d: %s", badRes.Code, badRes.Body.String())
		}

		// 2. Webhook signed with Store A's BYOK Server Key MUST succeed (200) and settle payment to paid
		goodWebhook := map[string]any{
			"transaction_status": "settlement",
			"transaction_id":     "tx-byok-settlement-1",
			"status_code":        "200",
			"order_id":           providerOrderID,
			"gross_amount":       grossStr,
			"fraud_status":       "accept",
			"payment_type":       "qris",
			"currency":           "IDR",
			"signature_key":      signMidtransWebhook(providerOrderID, "200", grossStr, tenantAServerKey),
		}
		goodRes := call("POST", "/api/v1/payments/midtrans/webhook/central-cafe", nil, goodWebhook)
		if goodRes.Code != http.StatusOK {
			t.Fatalf("expected 200 on valid Store A BYOK webhook, got %d: %s", goodRes.Code, goodRes.Body.String())
		}

		var settledStatus string
		if err := database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &settledStatus, `SELECT status FROM payments WHERE store_id = $1 AND id = $2`, storeAID, payRes.Payment.ID)
		}); err != nil || settledStatus != string(domain.PaymentStatusPaid) {
			t.Fatalf("expected payment status 'paid' after BYOK webhook, got %q (err=%v)", settledStatus, err)
		}
	})

	t.Run("StoreCMSAntiSlopAndPublicStorefront", func(t *testing.T) {
		emDashReject := call("PUT", "/api/v1/store/cms", ownerA, map[string]any{
			"default_locale": "id-ID",
			"hero_title_id":  "Kopi Spesialti \u2014 Disangrai Segar",
			"primary_hex":    "#2C1A12",
			"accent_hex":     "#C68B59",
		})
		if emDashReject.Code != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422 rejecting em-dash in CMS copy (antislop R-02), got %d", emDashReject.Code)
		}

		cmsOK := call("PUT", "/api/v1/store/cms", ownerA, map[string]any{
			"default_locale":         "id-ID",
			"supported_locales":      []string{"id-ID", "en-US"},
			"hero_title_id":          "Kopi Spesialti, Disangrai Segar Tiap Pagi",
			"hero_title_en":          "Specialty Coffee, Freshly Roasted Daily",
			"announcement_banner_id": "Pesan langsung dari meja Anda menggunakan QRIS tanpa antre.",
			"announcement_banner_en": "Order straight from your table via QRIS without waiting in line.",
			"primary_hex":            "#2C1A12",
			"accent_hex":             "#C68B59",
			"high_contrast_mode":     true,
		})
		if cmsOK.Code != http.StatusOK {
			t.Fatalf("expected 200 saving valid Store CMS, got %d: %s", cmsOK.Code, cmsOK.Body.String())
		}

		pubCMS := call("GET", "/api/v1/public/stores/central-cafe/cms", nil, nil)
		if pubCMS.Code != http.StatusOK || !strings.Contains(pubCMS.Body.String(), "Specialty Coffee") {
			t.Fatalf("expected public CMS endpoint to serve bilingual copy, got %d: %s", pubCMS.Code, pubCMS.Body.String())
		}
	})

	t.Run("BYONotificationsNetworkNodesAndFaultSimulation", func(t *testing.T) {
		notifRes := call("PUT", "/api/v1/store/notifications", ownerA, map[string]any{
			"email_mode":               "custom_smtp",
			"smtp_host":                "smtp.hostinger.com",
			"smtp_port":                465,
			"smtp_tls_mode":            "implicit",
			"smtp_username":            "kasir@abbdullaziz.online",
			"smtp_password":            "SuperSecretSmtpPass8899",
			"smtp_from_email":          "kasir@abbdullaziz.online",
			"smtp_from_name":           "MyCaffe Senopati",
			"whatsapp_mode":            "custom_cloud_api",
			"whatsapp_phone_number_id": "1092837465",
			"whatsapp_token":           "EAAGSuperSecretWhatsAppCloudToken7766",
		})
		if notifRes.Code != http.StatusOK {
			t.Fatalf("expected 200 saving notifications config, got %d: %s", notifRes.Code, notifRes.Body.String())
		}
		if strings.Contains(notifRes.Body.String(), "SuperSecretSmtpPass") || strings.Contains(notifRes.Body.String(), "EAAGSuperSecret") {
			t.Fatalf("SECURITY VIOLATION: raw SMTP/WhatsApp secret leaked: %s", notifRes.Body.String())
		}

		nodeRes := call("PUT", "/api/v1/store/network/nodes", ownerA, map[string]any{
			"node_name":           "Printer Kasir Utama",
			"node_type":           "escpos_printer",
			"connection_protocol": "lan_tcp_9100",
			"local_ip":            "192.168.1.210",
			"local_port":          9100,
			"mac_address":         "AA:BB:CC:11:22:33",
			"paper_width_mm":      80,
			"drawer_kick_enabled": true,
			"health_status":       "online",
			"last_latency_ms":     4,
		})
		if nodeRes.Code != http.StatusOK {
			t.Fatalf("expected 200 upserting network node, got %d: %s", nodeRes.Code, nodeRes.Body.String())
		}

		for _, sc := range []string{"healthy", "wan_offline", "printer_paper_out", "lan_ip_conflict", "midtrans_review"} {
			simRes := call("POST", "/api/v1/store/network/simulate", ownerA, map[string]string{"scenario": sc})
			if simRes.Code != http.StatusOK {
				t.Fatalf("simulation %s failed: %d %s", sc, simRes.Code, simRes.Body.String())
			}
		}
	})

	t.Run("Lane1PlatformSubscriptionInvoice", func(t *testing.T) {
		invRes := call("POST", "/api/v1/store/subscription/invoices", ownerA, map[string]string{
			"plan_code":     "pro_cafe",
			"billing_cycle": "monthly",
		})
		if invRes.Code != http.StatusCreated {
			t.Fatalf("expected 201 creating platform subscription invoice, got %d: %s", invRes.Code, invRes.Body.String())
		}
		var inv domain.PlatformInvoice
		_ = json.Unmarshal(invRes.Body.Bytes(), &inv)
		if inv.AmountIDR != 299000 || inv.ProviderQRString != "000201010212-LANE1-SAAS-INVOICE" {
			t.Fatalf("unexpected Lane 1 invoice: amount=%d qr=%q", inv.AmountIDR, inv.ProviderQRString)
		}
		if platformBillingGateway.chargeCalls != 1 {
			t.Fatalf("expected Lane 1 platformBillingGateway to receive 1 call, got %d", platformBillingGateway.chargeCalls)
		}
	})

	t.Run("SuperadminFailClosedAndTaxSafeAccountClearance", func(t *testing.T) {
		// 1. Unconfigured PlatformAdminKey MUST fail closed with 503
		unconfRouter := gin.New()
		unconfGH := handler.NewGovernanceHandler(govSvc, "")
		unconfRouter.GET("/platform/overview", unconfGH.RequirePlatformAdmin(), unconfGH.PlatformOverview)
		w503 := httptest.NewRecorder()
		unconfRouter.ServeHTTP(w503, httptest.NewRequest("GET", "/platform/overview", nil))
		if w503.Code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 when PLATFORM_ADMIN_API_KEY is unset, got %d", w503.Code)
		}

		// 2. Wrong key MUST fail with 401
		badAdmin := call("GET", "/api/v1/platform/overview", map[string]string{handler.PlatformAdminHeader: "wrong-key"}, nil)
		if badAdmin.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 with wrong X-Platform-Admin-Key, got %d", badAdmin.Code)
		}

		// 3. Valid Superadmin overview, onboarding review, KYB review, and Midtrans readiness audit
		ovRes := call("GET", "/api/v1/platform/overview", superadmin, nil)
		if ovRes.Code != http.StatusOK {
			t.Fatalf("expected 200 platform overview, got %d: %s", ovRes.Code, ovRes.Body.String())
		}

		readyRes := call("GET", "/api/v1/platform/midtrans-readiness", superadmin, nil)
		if readyRes.Code != http.StatusOK {
			t.Fatalf("expected 200 midtrans readiness, got %d", readyRes.Code)
		}
		var ready domain.MidtransBusinessReviewReadiness
		_ = json.Unmarshal(readyRes.Body.Bytes(), &ready)
		if ready.ScorePercent != 100 || !ready.ReadyForLiveCutover {
			t.Fatalf("expected 100%% readiness score, got %d%% (%+v)", ready.ScorePercent, ready.Checks)
		}

		revApp := call("POST", "/api/v1/platform/onboarding/"+onboardingAppID.String()+"/review", superadmin, map[string]string{
			"status":       "approved",
			"review_notes": "NIB OSS RBA terverifikasi oleh Direktur AzureDevID",
		})
		if revApp.Code != http.StatusOK {
			t.Fatalf("expected 200 reviewing onboarding application, got %d: %s", revApp.Code, revApp.Body.String())
		}

		revKYB := call("POST", "/api/v1/platform/tenants/"+storeAID.String()+"/kyb-review", superadmin, map[string]string{
			"kyb_status":          "verified",
			"subscription_status": "active",
			"review_notes":        "Dokumen legal lengkap",
		})
		if revKYB.Code != http.StatusOK {
			t.Fatalf("expected 200 reviewing Store A KYB, got %d: %s", revKYB.Code, revKYB.Body.String())
		}

		// 4. Active order guard on Store A MUST block premature account clearance (409 Conflict)
		blockedClear := call("POST", "/api/v1/store/clearance", ownerA, map[string]string{
			"reason": "Coba tutup akun saat masih ada pesanan aktif di antrean KDS",
		})
		if blockedClear.Code != http.StatusConflict {
			t.Fatalf("expected 409 Conflict when Store A has active order, got %d: %s", blockedClear.Code, blockedClear.Body.String())
		}

		// 5. Tax-Safe Account Clearance on Store B (express-counter)
		// Seed KYB & BYOK on Store B first, then request clearance -> execute clearance -> verify BYOK wiped while orders preserved!
		_ = call("PUT", "/api/v1/store/kyb", ownerB, map[string]any{
			"entity_type":         "perorangan",
			"legal_business_name": "Express Counter Test Clearance",
			"plan_code":           "starter_umkm",
			"billing_cycle":       "monthly",
		})
		_ = call("PUT", "/api/v1/store/payment-credentials", ownerB, map[string]any{
			"environment": "sandbox",
			"merchant_id": "G222222222",
			"client_key":  "SB-Mid-client-StoreBTempKey22",
			"server_key":  "SB-Mid-server-StoreBTempKey22",
			"is_enabled":  true,
		})

		clearReq := call("POST", "/api/v1/store/clearance", ownerB, map[string]string{
			"reason": "Penutupan gerai cabang sementara setelah rekonsiliasi pajak",
		})
		if clearReq.Code != http.StatusCreated {
			t.Fatalf("expected 201 requesting clearance on Store B, got %d: %s", clearReq.Code, clearReq.Body.String())
		}
		var cr domain.TenantClearanceRequest
		_ = json.Unmarshal(clearReq.Body.Bytes(), &cr)

		execRes := call("POST", fmt.Sprintf("/api/v1/platform/tenants/%s/clearance/%s/execute", storeBID, cr.ID), superadmin, nil)
		if execRes.Code != http.StatusOK {
			t.Fatalf("expected 200 executing Store B clearance, got %d: %s", execRes.Code, execRes.Body.String())
		}

		credAfter := call("GET", "/api/v1/store/payment-credentials", ownerB, nil)
		if !strings.Contains(credAfter.Body.String(), `"server_key_masked":"REVOKED"`) || !strings.Contains(credAfter.Body.String(), `"is_enabled":false`) {
			t.Fatalf("expected Store B BYOK credential to be cryptographically wiped and disabled, got: %s", credAfter.Body.String())
		}
	})
}
