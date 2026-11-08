package handler_test

import (
	"bytes"
	"context"
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
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/config"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/handler"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/service"
)

// TestPromotionIntegration covers v0.11 PR 11.1 (K7, X2): owner promotions, one best promotion per line
// taken off the line subtotal, the cashier discount on top, the quota counted on payment, and the menu.
// It promotes a product of its own so parallel tests never see their prices change.
func TestPromotionIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping promotion integration test")
	}
	db, err := database.NewPostgresDB(&config.Config{DatabaseURL: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	storeID := uuid.MustParse("11111111-1111-4111-8111-111111111111")

	gin.SetMode(gin.TestMode)
	authSvc := service.NewAuthService(db, postgres.NewAuthRepository(), "integration-test-jwt-secret-at-least-32-chars", service.AuthPolicy{
		AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 12 * time.Hour, SessionIdleTimeout: 2 * time.Hour,
		MaxSessions: 3, PINMaxFailures: 5, PINLockout: 15 * time.Minute, MaxConcurrentHashes: 8,
	})
	ah := handler.NewAuthHandler(authSvc)
	orderRepo, paymentRepo := postgres.NewOrderRepository(), postgres.NewPaymentRepository()
	pos := handler.NewPOSHandler(service.NewOrderService(db, orderRepo), service.NewPaymentService(db, paymentRepo, orderRepo, nil), service.NewSettingsService(db))
	ch := handler.NewCatalogHandler(service.NewCatalogService(db))
	sh := handler.NewShiftHandler(service.NewShiftService(db))

	r := gin.New()
	r.POST("/login", ah.Login)
	r.POST("/pos/drawers", ah.RequirePermission(rbac.CashDrawerManage), sh.CreateDrawer)
	r.POST("/pos/shifts", ah.RequireStaff(), sh.OpenShift)
	r.POST("/pos/shifts/:id/force-close", ah.RequireStaff(), sh.ForceCloseShift)
	r.POST("/pos/orders", ah.RequireStaff(), pos.CreateOrder)
	r.POST("/pos/orders/:id/payments", ah.RequireStaff(), pos.InitiatePayment)
	r.GET("/pos/catalog", ah.RequireStaff(), ch.StaffCatalog)
	r.POST("/catalog/products", ah.RequirePermission(rbac.CatalogManage), ch.CreateProduct)
	r.DELETE("/catalog/products/:id", ah.RequirePermission(rbac.CatalogManage), ch.ArchiveProduct)
	r.GET("/catalog/promotions", ah.RequirePermission(rbac.CatalogManage), ch.ListPromotions)
	r.POST("/catalog/promotions", ah.RequirePermission(rbac.CatalogManage), ch.CreatePromotion)
	r.PATCH("/catalog/promotions/:id", ah.RequirePermission(rbac.CatalogManage), ch.UpdatePromotion)

	call := func(method, path, authz string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		req.Header.Set("Content-Type", "application/json")
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	login := func(email string) string {
		t.Helper()
		res := call("POST", "/login", "", map[string]string{"store_slug": "central-cafe", "email": email, "pin": "482913"})
		if res.Code != http.StatusOK {
			t.Fatalf("login %s failed: %d", email, res.Code)
		}
		var s struct {
			AccessToken string `json:"access_token"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &s)
		return "Bearer " + s.AccessToken
	}
	inStore := func(fn func(tx *sqlx.Tx) error) {
		t.Helper()
		if err := database.WithTenantTx(ctx, db, storeID, fn); err != nil {
			t.Fatalf("tenant tx failed: %v", err)
		}
	}
	owner, cashier := login("abdulaziz543333@gmail.com"), login("cashier.central@mycaffe.local")
	suffix := uuid.NewString()[:6]

	res := call("POST", "/catalog/products", owner, map[string]any{"name": "Promo Latte " + suffix, "product_type": "ready_to_serve",
		"prep_station": "none", "variants": []map[string]any{{"name": "Reguler", "price": 20000, "is_default": true}}})
	if res.Code != http.StatusCreated {
		t.Fatalf("create product failed: %d: %s", res.Code, res.Body.String())
	}
	var product struct {
		ID uuid.UUID `json:"id"`
	}
	_ = json.Unmarshal(res.Body.Bytes(), &product)
	t.Cleanup(func() { call("DELETE", "/catalog/products/"+product.ID.String(), owner, nil) })

	var promo domain.Promotion
	t.Run("OwnerManagesPromotions", func(t *testing.T) {
		valid := map[string]any{"name": "Latte 50% " + suffix, "kind": "percent", "value": 5000, "product_ids": []uuid.UUID{product.ID}, "quota": 1}
		if res := call("POST", "/catalog/promotions", cashier, valid); res.Code != http.StatusForbidden {
			t.Fatalf("cashier must not create promotions, got %d", res.Code)
		}
		for _, bad := range []map[string]any{
			{"name": "X", "kind": "percent", "value": 5000},                                                            // no target
			{"name": "X", "kind": "percent", "value": 20000, "product_ids": []uuid.UUID{product.ID}},                   // above 100%
			{"name": "X", "kind": "amount", "value": 1000, "product_ids": []uuid.UUID{uuid.New()}},                     // unknown product
			{"name": "X", "kind": "percent", "value": 10, "product_ids": []uuid.UUID{product.ID}, "start_minute": 600}, // half a happy hour
		} {
			if res := call("POST", "/catalog/promotions", owner, bad); res.Code != http.StatusUnprocessableEntity {
				t.Fatalf("%v must be 422, got %d: %s", bad, res.Code, res.Body.String())
			}
		}
		res := call("POST", "/catalog/promotions", owner, valid)
		if res.Code != http.StatusCreated {
			t.Fatalf("create promotion failed: %d: %s", res.Code, res.Body.String())
		}
		_ = json.Unmarshal(res.Body.Bytes(), &promo) // it only targets this test's product, archived at the end
		if list := call("GET", "/catalog/promotions", owner, nil); !strings.Contains(list.Body.String(), promo.ID.String()) {
			t.Fatal("the owner list must show the promotion")
		}
		if menu := call("GET", "/pos/catalog", cashier, nil); !strings.Contains(menu.Body.String(), promo.ID.String()) || strings.Contains(menu.Body.String(), "used_count") {
			t.Fatal("the menu shows active promotions without usage counters")
		}
	})

	var drawer domain.CashDrawer
	_ = json.Unmarshal(call("POST", "/pos/drawers", owner, map[string]any{"name": "Laci Promo " + suffix}).Body.Bytes(), &drawer)
	var opened struct {
		Shift domain.Shift `json:"shift"`
	}
	if res := call("POST", "/pos/shifts", cashier, map[string]any{"drawer_id": drawer.ID, "opening_float": 0}); res.Code != http.StatusCreated {
		t.Fatalf("open shift failed: %d: %s", res.Code, res.Body.String())
	} else {
		_ = json.Unmarshal(res.Body.Bytes(), &opened)
	}
	t.Cleanup(func() {
		call("POST", fmt.Sprintf("/pos/shifts/%s/force-close", opened.Shift.ID), owner, map[string]any{"reason": "Akhir test promo"})
	})

	order := func() domain.Order {
		t.Helper()
		res := call("POST", "/pos/orders", cashier, map[string]any{"idempotency_key": "promo-" + uuid.NewString(), "order_type": "takeaway",
			"customer_name": "Promo", "items": []map[string]any{{"product_id": product.ID, "quantity": 2}}})
		if res.Code != http.StatusCreated {
			t.Fatalf("create order failed: %d: %s", res.Code, res.Body.String())
		}
		var created struct {
			Order domain.Order `json:"order"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &created)
		return created.Order
	}
	pay := func(o domain.Order) {
		t.Helper()
		if res := call("POST", fmt.Sprintf("/pos/orders/%s/payments", o.ID), cashier, map[string]any{
			"idempotency_key": "pay-" + uuid.NewString(), "method": "cash", "cash_tendered_amount": 1_000_000}); res.Code != http.StatusOK {
			t.Fatalf("payment failed: %d: %s", res.Code, res.Body.String())
		}
	}

	t.Run("PromotionPricesTheLineAndCountsOnPayment", func(t *testing.T) {
		first := order()
		if first.SubtotalAmount != 20000 {
			t.Fatalf("2 x Rp20.000 at 50%% must be Rp20.000, got %d", first.SubtotalAmount)
		}
		var line struct {
			Promo    *string `db:"promotion_id"`
			Discount int64   `db:"promo_discount_amount"`
			Subtotal int64   `db:"subtotal_amount"`
		}
		inStore(func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &line, `SELECT promotion_id, promo_discount_amount, subtotal_amount FROM order_items WHERE store_id = $1 AND order_id = $2`, storeID, first.ID)
		})
		if line.Promo == nil || *line.Promo != promo.ID.String() || line.Discount != 20000 || line.Subtotal != 20000 {
			t.Fatalf("the line must record its promotion: %+v", line)
		}
		pay(first)
		var used int
		inStore(func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &used, `SELECT used_count FROM promotions WHERE store_id = $1 AND id = $2`, storeID, promo.ID)
		})
		if used != 1 {
			t.Fatalf("a paid order counts against the quota once, got %d", used)
		}
		if second := order(); second.SubtotalAmount != 40000 {
			t.Fatalf("a used-up quota prices at the normal price, got %d", second.SubtotalAmount)
		}
	})

	t.Run("DeactivatedPromotionStops", func(t *testing.T) {
		res := call("PATCH", "/catalog/promotions/"+promo.ID.String(), owner, map[string]any{"is_active": false, "clear_quota": true})
		if res.Code != http.StatusOK {
			t.Fatalf("deactivate failed: %d: %s", res.Code, res.Body.String())
		}
		if o := order(); o.SubtotalAmount != 40000 {
			t.Fatalf("an inactive promotion never applies, got %d", o.SubtotalAmount)
		}
		var audits int
		inStore(func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &audits, `SELECT COUNT(*) FROM audit_logs WHERE store_id = $1 AND reference_id = $2
				AND action IN ('PROMOTION_CREATED', 'PROMOTION_UPDATED')`, storeID, promo.ID.String())
		})
		if audits != 2 {
			t.Fatalf("create and update are audited, got %d", audits)
		}
	})
}
