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

// TestBundleIntegration covers v0.11 PR 11.2 (X4): owner-defined bundle slots, a bundle sold as component
// lines that add up to the bundle price, component stock, payment and the menu.
// It uses products of its own so parallel tests never see their stock or prices change.
func TestBundleIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping bundle integration test")
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
	r.PUT("/catalog/products/:id/bundle-slots", ah.RequirePermission(rbac.CatalogManage), ch.SetBundleSlots)

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

	newProduct := func(body map[string]any) uuid.UUID {
		t.Helper()
		res := call("POST", "/catalog/products", owner, body)
		if res.Code != http.StatusCreated {
			t.Fatalf("create product failed: %d: %s", res.Code, res.Body.String())
		}
		var p struct {
			ID uuid.UUID `json:"id"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &p)
		t.Cleanup(func() { call("DELETE", "/catalog/products/"+p.ID.String(), owner, nil) })
		return p.ID
	}
	variant := func(price int) []map[string]any {
		return []map[string]any{{"name": "Reguler", "price": price, "is_default": true}}
	}
	coffee := newProduct(map[string]any{"name": "Paket Kopi " + suffix, "product_type": "ready_to_serve", "prep_station": "bar", "variants": variant(25000)})
	bread := newProduct(map[string]any{"name": "Paket Roti " + suffix, "product_type": "ready_to_serve", "prep_station": "kitchen",
		"track_stock": true, "stock_quantity": 5, "variants": variant(20000)})
	if res := call("POST", "/catalog/products", owner, map[string]any{"name": "Paket Stok " + suffix, "product_type": "bundle",
		"track_stock": true, "variants": variant(35000)}); res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a bundle is never stocked itself, got %d", res.Code)
	}
	bundle := newProduct(map[string]any{"name": "Paket Hemat " + suffix, "product_type": "bundle", "variants": variant(35000)})

	var slots []domain.BundleSlot
	t.Run("OwnerDefinesTheBundle", func(t *testing.T) {
		valid := map[string]any{"slots": []map[string]any{
			{"name": "Kopi", "quantity": 1, "product_ids": []uuid.UUID{coffee}},
			{"name": "Roti", "quantity": 1, "product_ids": []uuid.UUID{bread}},
		}}
		if res := call("PUT", "/catalog/products/"+bundle.String()+"/bundle-slots", cashier, valid); res.Code != http.StatusForbidden {
			t.Fatalf("cashier must not edit bundles, got %d", res.Code)
		}
		bad := map[string]any{"slots": []map[string]any{{"name": "Paket", "quantity": 1, "product_ids": []uuid.UUID{bundle}}}}
		if res := call("PUT", "/catalog/products/"+bundle.String()+"/bundle-slots", owner, bad); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("a bundle cannot contain a bundle, got %d", res.Code)
		}
		if res := call("PUT", "/catalog/products/"+coffee.String()+"/bundle-slots", owner, valid); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("only bundle products have slots, got %d", res.Code)
		}
		res := call("PUT", "/catalog/products/"+bundle.String()+"/bundle-slots", owner, valid)
		if res.Code != http.StatusOK {
			t.Fatalf("set slots failed: %d: %s", res.Code, res.Body.String())
		}
		var out struct {
			Slots []domain.BundleSlot `json:"slots"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &out)
		slots = out.Slots
		if menu := call("GET", "/pos/catalog", cashier, nil); !strings.Contains(menu.Body.String(), slots[0].ID.String()) {
			t.Fatal("the menu shows the bundle slots")
		}
	})
	if len(slots) != 2 {
		t.Fatal("slots are needed for the sale")
	}

	var drawer domain.CashDrawer
	_ = json.Unmarshal(call("POST", "/pos/drawers", owner, map[string]any{"name": "Laci Paket " + suffix}).Body.Bytes(), &drawer)
	var opened struct {
		Shift domain.Shift `json:"shift"`
	}
	if res := call("POST", "/pos/shifts", cashier, map[string]any{"drawer_id": drawer.ID, "opening_float": 0}); res.Code != http.StatusCreated {
		t.Fatalf("open shift failed: %d: %s", res.Code, res.Body.String())
	} else {
		_ = json.Unmarshal(res.Body.Bytes(), &opened)
	}
	t.Cleanup(func() {
		call("POST", fmt.Sprintf("/pos/shifts/%s/force-close", opened.Shift.ID), owner, map[string]any{"reason": "Akhir test paket"})
	})

	t.Run("BundleSellsAsComponentLines", func(t *testing.T) {
		res := call("POST", "/pos/orders", cashier, map[string]any{"idempotency_key": "bundle-" + uuid.NewString(), "order_type": "takeaway",
			"customer_name": "Paket", "items": []map[string]any{{"product_id": bundle, "quantity": 2, "bundle_items": []map[string]any{
				{"slot_id": slots[0].ID, "product_id": coffee}, {"slot_id": slots[1].ID, "product_id": bread}}}}})
		if res.Code != http.StatusCreated {
			t.Fatalf("create bundle order failed: %d: %s", res.Code, res.Body.String())
		}
		var created struct {
			Order domain.Order `json:"order"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &created)
		if created.Order.SubtotalAmount != 70000 {
			t.Fatalf("2 bundles at Rp35.000 must be Rp70.000, got %d", created.Order.SubtotalAmount)
		}
		if res := call("POST", fmt.Sprintf("/pos/orders/%s/payments", created.Order.ID), cashier, map[string]any{
			"idempotency_key": "pay-" + uuid.NewString(), "method": "cash", "cash_tendered_amount": 1_000_000}); res.Code != http.StatusOK {
			t.Fatalf("payment failed: %d: %s", res.Code, res.Body.String())
		}
		var lines []struct {
			Product  string `db:"product_id"`
			Subtotal int64  `db:"subtotal_amount"`
			Group    string `db:"bundle_group_id"`
			Station  string `db:"snapshot_prep_station"`
		}
		var stock int
		inStore(func(tx *sqlx.Tx) error {
			if err := tx.SelectContext(ctx, &lines, `SELECT product_id, subtotal_amount, bundle_group_id, snapshot_prep_station FROM order_items
				WHERE store_id = $1 AND order_id = $2 ORDER BY subtotal_amount DESC`, storeID, created.Order.ID); err != nil {
				return err
			}
			return tx.GetContext(ctx, &stock, `SELECT stock_quantity FROM products WHERE store_id = $1 AND id = $2`, storeID, bread)
		})
		if len(lines) != 2 || lines[0].Group != lines[1].Group || lines[0].Subtotal+lines[1].Subtotal != 70000 ||
			lines[0].Product != coffee.String() || lines[1].Station != "kitchen" {
			t.Fatalf("one line per component in one bundle group, allocated by normal price: %+v", lines)
		}
		if stock != 3 {
			t.Fatalf("component stock must drop by 2, got %d", stock)
		}
	})
}
