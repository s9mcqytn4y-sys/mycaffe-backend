package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/handler"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/service"
)

func TestMobileBootstrapRequiresAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	gh := handler.NewGovernanceHandler(nil, "sample-platform-key")
	r.GET("/api/v1/mobile/bootstrap", gh.MobileBootstrap)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/mobile/bootstrap", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401 Unauthorized without staff session, got %d", w.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse json response: %v", err)
	}
	if body["error"] != "UNAUTHENTICATED" {
		t.Fatalf("expected error UNAUTHENTICATED, got %v", body["error"])
	}
}

func TestMobileBootstrapWithMockStaff(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	storeID := uuid.New()
	userID := uuid.New()
	mockStaff := &service.AuthenticatedStaff{
		Staff: service.StaffIdentity{
			UserID:   userID,
			FullName: "Abdul Aziz",
			Role:     domain.RoleOwner,
			Store: service.StaffStore{
				ID:   storeID,
				Slug: "central-cafe",
				Name: "Central Cafe Senopati",
			},
		},
		SessionID: uuid.New(),
		Permissions: []rbac.Permission{
			rbac.OrderPOSCreate,
			rbac.CatalogManage,
			rbac.ReportFinanceRead,
		},
	}

	r.Use(func(c *gin.Context) {
		c.Set("mycaffe.staff", mockStaff)
		c.Next()
	})

	gh := handler.NewGovernanceHandler(nil, "sample-platform-key")
	r.GET("/api/v1/mobile/bootstrap", gh.MobileBootstrap)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/mobile/bootstrap", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Since governance service returns an unhandled error, writeGovernanceError maps it cleanly to 503 Service Unavailable
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 Service Unavailable when governance service is unconfigured, got %d", w.Code)
	}
}

func TestMobileBootstrapDomainContract(t *testing.T) {
	storeID := uuid.New()
	res := domain.MobileBootstrapResponse{
		StoreID:   storeID,
		StoreSlug: "central-cafe",
		StoreName: "Central Cafe Senopati",
		AppBrand: domain.MobileAppBrandInfo{
			DisplayName:       "Cassy",
			MonogramText:      "CS",
			BundleID:          "com.azuredevid.cassy",
			VectorMonogramURL: "https://cdn.abbdullaziz.online/brand/cassy/CassyMonogramVector.svg",
			VectorLogoURL:     "https://cdn.abbdullaziz.online/brand/cassy/CassyLogoLightVector.svg",
			VectorIconURL:     "https://cdn.abbdullaziz.online/brand/cassy/CassyAppIconDarkSquircle.svg",
		},
		POSLayout: domain.MobilePOSLayoutInfo{
			TabletOrientation:      "landscape_primary",
			LayoutMode:             "master_detail_toast",
			AutoLockSeconds:        300,
			TabularNumbersEnforced: true,
			MinTouchTargetPx:       48,
		},
		Hardware: domain.MobileHardwareInfo{
			DefaultPrinterInterface: "lan_tcp",
			DefaultPrinterPort:      9100,
			CashDrawerKickOnCash:    true,
			CashDrawerPulseHex:      "1B700019FA",
			KDSAutoRefreshSeconds:   5,
			KDSSoundAlertEnabled:    true,
		},
		PaymentGateway: domain.MobilePaymentGatewayInfo{
			Provider:                  "midtrans",
			Environment:               "sandbox",
			ClientKey:                 "SB-Mid-client-sample",
			MerchantID:                "G123456789",
			IsEnabled:                 true,
			ValidationStatus:          "valid",
			FallbackStaticQRISEnabled: true,
		},
		StaffSession: domain.MobileStaffSessionInfo{
			UserID:      uuid.New(),
			FullName:    "Abdul Aziz",
			Role:        "owner",
			Email:       "abdulaziz543333@gmail.com",
			Permissions: []string{"pos.access", "catalog.manage"},
		},
	}

	bytes, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("failed to marshal MobileBootstrapResponse: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(bytes, &parsed); err != nil {
		t.Fatalf("failed to unmarshal JSON: %v", err)
	}

	if parsed["store_slug"] != "central-cafe" {
		t.Fatalf("expected store_slug central-cafe, got %v", parsed["store_slug"])
	}
	appBrand := parsed["app_brand"].(map[string]any)
	if appBrand["bundle_id"] != "com.azuredevid.cassy" {
		t.Fatalf("expected bundle_id com.azuredevid.cassy, got %v", appBrand["bundle_id"])
	}
	posLayout := parsed["pos_layout"].(map[string]any)
	if posLayout["tabular_numbers_enforced"] != true {
		t.Fatalf("expected tabular_numbers_enforced true, got %v", posLayout["tabular_numbers_enforced"])
	}
}
