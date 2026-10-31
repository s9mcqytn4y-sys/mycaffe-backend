package handler_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/handler"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

// TestRBACRoutesIntegration exercises 02.01-C end to end over HTTP against PostgreSQL 18:
// route permissions, the token as the only source of store and actor, and the C3 toggle.
func TestRBACRoutesIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping RBAC HTTP integration test")
	}
	db, err := database.NewPostgresDB(&config.Config{DatabaseURL: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	gin.SetMode(gin.TestMode)
	authSvc := service.NewAuthService(db, postgres.NewAuthRepository(), "integration-test-jwt-secret-at-least-32-chars", service.AuthPolicy{
		AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 12 * time.Hour, SessionIdleTimeout: 2 * time.Hour,
		MaxSessions: 3, PINMaxFailures: 5, PINLockout: 15 * time.Minute, MaxConcurrentHashes: 8,
	})
	orderRepo := postgres.NewOrderRepository()
	pos := handler.NewPOSHandler(
		service.NewOrderService(db, orderRepo),
		service.NewPaymentService(db, postgres.NewPaymentRepository(), orderRepo, nil),
		service.NewSettingsService(db),
	)
	ah := handler.NewAuthHandler(authSvc)
	r := gin.New()
	r.POST("/api/v1/auth/staff/login", ah.Login)
	r.GET("/api/v1/auth/staff/me", ah.Me)
	r.GET("/api/v1/staff", ah.RequirePermission(rbac.StaffManage), ah.ListStaff)
	r.GET("/api/v1/store/settings", ah.RequireStaff(), pos.GetSettings)
	r.PATCH("/api/v1/store/settings", ah.RequirePermission(rbac.StoreSettingsManage), pos.UpdateSettings)
	r.POST("/api/v1/pos/orders", ah.RequirePermission(rbac.OrderPOSCreate), pos.CreateOrder)
	r.POST("/api/v1/pos/orders/:id/payments", ah.RequireStaff(), pos.InitiatePayment)

	call := func(method, path, token string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	login := func(email string) string {
		w := call(http.MethodPost, "/api/v1/auth/staff/login", "", map[string]string{"store_slug": "central-cafe", "email": email, "pin": "482913"})
		if w.Code != http.StatusOK {
			t.Fatalf("login %s: %d %s", email, w.Code, w.Body.String())
		}
		var out struct {
			AccessToken string `json:"access_token"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out.AccessToken
	}
	permissions := func(token string) []string {
		w := call(http.MethodGet, "/api/v1/auth/staff/me", token, nil)
		var out struct {
			Permissions []string `json:"permissions"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out.Permissions
	}

	owner := login("abdulaziz543333@gmail.com")
	cashier := login("cashier.central@mycaffe.local")
	barista := login("barista.central@mycaffe.local")
	storeA := "11111111-1111-4111-8111-111111111111"
	storeB := "22222222-2222-4222-8222-222222222222"
	t.Cleanup(func() {
		call(http.MethodPatch, "/api/v1/store/settings", owner, map[string]any{"cashier_handles_bar": false})
	})

	t.Run("StaffManagementIsOwnerOnly", func(t *testing.T) {
		if w := call(http.MethodGet, "/api/v1/staff", cashier, nil); w.Code != http.StatusForbidden {
			t.Fatalf("cashier /staff: %d", w.Code)
		}
		if w := call(http.MethodGet, "/api/v1/staff", owner, nil); w.Code != http.StatusOK {
			t.Fatalf("owner /staff: %d %s", w.Code, w.Body.String())
		}
		if w := call(http.MethodGet, "/api/v1/staff", "", nil); w.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous /staff: %d", w.Code)
		}
	})

	var orderID string
	t.Run("POSOrderUsesTokenStore_IgnoresBodyStoreID", func(t *testing.T) {
		body := map[string]any{
			"store_id":        storeB, // must be ignored
			"actor_user_id":   uuid.NewString(),
			"idempotency_key": "rbac-pos-" + uuid.NewString()[:8],
			"order_type":      "takeaway",
			"items":           []map[string]any{{"product_id": "d0000003-0000-4000-8000-000000000003", "quantity": 1}},
		}
		if w := call(http.MethodPost, "/api/v1/pos/orders", barista, body); w.Code != http.StatusForbidden {
			t.Fatalf("barista must not create POS orders: %d", w.Code)
		}
		w := call(http.MethodPost, "/api/v1/pos/orders", cashier, body)
		if w.Code != http.StatusCreated {
			t.Fatalf("cashier POS order: %d %s", w.Code, w.Body.String())
		}
		var res struct {
			Order struct {
				ID      string `json:"id"`
				StoreID string `json:"store_id"`
				Total   int64  `json:"total_amount"`
			} `json:"order"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		if res.Order.StoreID != storeA {
			t.Fatalf("order must belong to the token's store %s, got %s", storeA, res.Order.StoreID)
		}
		orderID = res.Order.ID
	})

	t.Run("CashTenderRecordedAgainstTokenActor", func(t *testing.T) {
		pay := map[string]any{"idempotency_key": "rbac-cash-" + uuid.NewString()[:8], "method": "cash", "cash_tendered_amount": 50000, "actor_user_id": uuid.NewString()}
		if w := call(http.MethodPost, "/api/v1/pos/orders/"+orderID+"/payments", barista, pay); w.Code != http.StatusForbidden {
			t.Fatalf("barista cash tender: %d", w.Code)
		}
		w := call(http.MethodPost, "/api/v1/pos/orders/"+orderID+"/payments", cashier, pay)
		if w.Code != http.StatusOK {
			t.Fatalf("cashier cash tender: %d %s", w.Code, w.Body.String())
		}
		var res struct {
			Payment struct {
				VerifiedBy string `json:"verified_by_user_id"`
			} `json:"payment"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		if res.Payment.VerifiedBy != "a0000003-0000-4000-8000-000000000003" {
			t.Fatalf("payment must be verified by the signed-in cashier, got %q", res.Payment.VerifiedBy)
		}
	})

	t.Run("SettingsOwnerOnly_AndCashierHandlesBarToggle", func(t *testing.T) {
		if w := call(http.MethodPatch, "/api/v1/store/settings", cashier, map[string]any{"cashier_handles_bar": true}); w.Code != http.StatusForbidden {
			t.Fatalf("cashier settings update: %d", w.Code)
		}
		if w := call(http.MethodGet, "/api/v1/store/settings", cashier, nil); w.Code != http.StatusOK {
			t.Fatalf("cashier settings read: %d", w.Code)
		}
		if w := call(http.MethodPatch, "/api/v1/store/settings", owner, map[string]any{"cashier_max_discount_bps": 20000}); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid settings must be rejected: %d", w.Code)
		}
		if slices.Contains(permissions(cashier), string(rbac.KDSBaristaUpdate)) {
			t.Fatal("cashier must not update barista tickets by default")
		}
		if w := call(http.MethodPatch, "/api/v1/store/settings", owner, map[string]any{"cashier_handles_bar": true}); w.Code != http.StatusOK {
			t.Fatalf("owner settings update: %d %s", w.Code, w.Body.String())
		}
		// Permissions are recomputed per request; no re-login needed.
		if !slices.Contains(permissions(cashier), string(rbac.KDSBaristaUpdate)) {
			t.Fatal("cashier_handles_bar must grant barista ticket updates")
		}
		if slices.Contains(permissions(barista), string(rbac.OrderPOSCreate)) {
			t.Fatal("the toggle must not widen the barista role")
		}
	})
}
