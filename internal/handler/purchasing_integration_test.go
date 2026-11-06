package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/handler"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

// TestPurchasingIntegration covers v0.11 PR 11.4 (X7, X9): suppliers, a purchase order received in two // deliveries (one with a corrected price), a direct purchase, and the moving-average cost of an ingredient // and of a ready-to-serve product. It uses items of its own so parallel tests are not affected.
func TestPurchasingIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping purchasing integration test")
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
	ih := handler.NewInventoryHandler(service.NewInventoryService(db))
	sh := handler.NewShiftHandler(service.NewShiftService(db))

	r := gin.New()
	r.POST("/login", ah.Login)
	r.POST("/pos/drawers", ah.RequirePermission(rbac.CashDrawerManage), sh.CreateDrawer)
	r.POST("/pos/shifts", ah.RequireStaff(), sh.OpenShift)
	r.POST("/pos/shifts/:id/force-close", ah.RequireStaff(), sh.ForceCloseShift)
	r.POST("/pos/orders", ah.RequireStaff(), pos.CreateOrder)
	r.POST("/pos/orders/:id/payments", ah.RequireStaff(), pos.InitiatePayment)
	r.POST("/catalog/products", ah.RequirePermission(rbac.CatalogManage), ch.CreateProduct)
	r.DELETE("/catalog/products/:id", ah.RequirePermission(rbac.CatalogManage), ch.ArchiveProduct)
	r.POST("/catalog/modifier-groups", ah.RequirePermission(rbac.CatalogManage), ch.CreateModifierGroup)
	r.POST("/catalog/modifier-groups/:id/options", ah.RequirePermission(rbac.CatalogManage), ch.CreateModifierOption)
	r.POST("/catalog/products/:id/modifier-groups", ah.RequirePermission(rbac.CatalogManage), ch.AttachModifierGroupToProduct)
	inv := r.Group("/inventory", ah.RequirePermission(rbac.InventoryManage))
	inv.GET("/ingredients", ih.ListIngredients)
	inv.POST("/ingredients", ih.CreateIngredient)
	inv.PATCH("/ingredients/:id", ih.UpdateIngredient)
	inv.GET("/suppliers", ih.ListSuppliers)
	inv.POST("/suppliers", ih.CreateSupplier)
	inv.POST("/purchase-orders", ih.CreatePurchaseOrder)
	inv.PATCH("/purchase-orders/:id", ih.UpdatePurchaseOrder)
	inv.POST("/purchase-orders/:id/send", ih.TransitionPurchaseOrder("send"))
	inv.POST("/purchase-orders/:id/close", ih.TransitionPurchaseOrder("close"))
	inv.POST("/purchase-orders/:id/receipts", ih.ReceivePurchaseOrder)
	inv.POST("/purchases", ih.DirectPurchase)
	inv.GET("/receipts", ih.ListReceipts)

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

	created := func(res *httptest.ResponseRecorder, what string) uuid.UUID {
		t.Helper()
		if res.Code != http.StatusCreated {
			t.Fatalf("create %s failed: %d: %s", what, res.Code, res.Body.String())
		}
		var out struct {
			ID uuid.UUID `json:"id"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &out)
		return out.ID
	}
	milk := created(call("POST", "/inventory/ingredients", owner, map[string]any{"name": "Susu PO " + suffix, "unit": "ml",
		"opening_quantity": 1000, "opening_cost": 20000}), "ingredient")
	t.Cleanup(func() {
		call("PATCH", "/inventory/ingredients/"+milk.String(), owner, map[string]any{"is_active": false})
	})
	water := created(call("POST", "/catalog/products", owner, map[string]any{"name": "Air PO " + suffix, "product_type": "ready_to_serve",
		"prep_station": "none", "track_stock": true, "stock_quantity": 0, "variants": []map[string]any{{"name": "Botol", "price": 5000, "is_default": true}}}), "product")
	t.Cleanup(func() { call("DELETE", "/catalog/products/"+water.String(), owner, nil) })
	state := func() (stock, cost, waterStock, waterCost int64) {
		t.Helper()
		inStore(func(tx *sqlx.Tx) error {
			if err := tx.QueryRowxContext(ctx, `SELECT stock_quantity, cost_per_1000 FROM ingredients WHERE store_id = $1 AND id = $2`, storeID, milk).Scan(&stock, &cost); err != nil {
				return err
			}
			return tx.QueryRowxContext(ctx, `SELECT stock_quantity, unit_cost FROM products WHERE store_id = $1 AND id = $2`, storeID, water).Scan(&waterStock, &waterCost)
		})
		return
	}

	var supplier uuid.UUID
	t.Run("OwnerManagesSuppliers", func(t *testing.T) {
		if res := call("POST", "/inventory/suppliers", cashier, map[string]any{"name": "X"}); res.Code != http.StatusForbidden {
			t.Fatalf("cashier must not manage suppliers, got %d", res.Code)
		}
		supplier = created(call("POST", "/inventory/suppliers", owner, map[string]any{"name": "Dairy " + suffix, "contact": "0812", "payment_days": 14}), "supplier")
		if res := call("POST", "/inventory/suppliers", owner, map[string]any{"name": "Dairy " + suffix}); res.Code != http.StatusConflict {
			t.Fatalf("a duplicate supplier name must be 409, got %d", res.Code)
		}
	})

	var po struct {
		ID    uuid.UUID `json:"id"`
		Lines []struct {
			ID           uuid.UUID  `json:"id"`
			IngredientID *uuid.UUID `json:"ingredient_id"`
		} `json:"lines"`
	}
	t.Run("PurchaseOrderInTwoDeliveries", func(t *testing.T) {
		res := call("POST", "/inventory/purchase-orders", owner, map[string]any{"supplier_id": supplier, "expected_date": "2026-11-10", "lines": []map[string]any{
			{"ingredient_id": milk, "purchase_unit": "karton", "conversion": 12000, "quantity": 2, "unit_price": 216000},
			{"product_id": water, "purchase_unit": "dus", "conversion": 24, "quantity": 1, "unit_price": 72000},
		}})
		_ = json.Unmarshal(res.Body.Bytes(), &po)
		created(res, "purchase order")
		milkLine, waterLine := po.Lines[0].ID, po.Lines[1].ID
		if po.Lines[0].IngredientID == nil {
			milkLine, waterLine = waterLine, milkLine
		}
		receive := func(body map[string]any) *httptest.ResponseRecorder {
			return call("POST", "/inventory/purchase-orders/"+po.ID.String()+"/receipts", owner, body)
		}
		one := map[string]any{"lines": []map[string]any{{"purchase_order_line_id": milkLine, "quantity": 1}}}
		if res := receive(one); res.Code != http.StatusConflict {
			t.Fatalf("a draft PO cannot be received, got %d", res.Code)
		}
		if res := call("POST", "/inventory/purchase-orders/"+po.ID.String()+"/send", owner, nil); res.Code != http.StatusOK {
			t.Fatalf("send failed: %d: %s", res.Code, res.Body.String())
		}
		if res := call("PATCH", "/inventory/purchase-orders/"+po.ID.String(), owner, map[string]any{"notes": "x"}); res.Code != http.StatusConflict {
			t.Fatalf("a sent PO cannot be edited, got %d", res.Code)
		}
		if res := receive(one); res.Code != http.StatusCreated {
			t.Fatalf("first delivery failed: %d: %s", res.Code, res.Body.String())
		}
		// 1000 ml at Rp20/ml + 12000 ml at Rp18/ml: (1000 x 20000 + 12000 x 18000) / 13000 = 18154 per 1000 ml.
		if stock, cost, _, _ := state(); stock != 13000 || cost != 18154 {
			t.Fatalf("after the first delivery expected 13000 ml at 18154, got %d at %d", stock, cost)
		}
		if res := receive(map[string]any{"lines": []map[string]any{{"purchase_order_line_id": milkLine, "quantity": 2}}}); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("receiving more than ordered must be 422, got %d", res.Code)
		}
		// The second carton came at a corrected Rp19/ml, with the water.
		if res := receive(map[string]any{"lines": []map[string]any{
			{"purchase_order_line_id": milkLine, "quantity": 1, "unit_price": 228000},
			{"purchase_order_line_id": waterLine, "quantity": 1}}}); res.Code != http.StatusCreated {
			t.Fatalf("second delivery failed: %d: %s", res.Code, res.Body.String())
		}
		stock, cost, waterStock, waterCost := state()
		if stock != 25000 || cost != 18560 || waterStock != 24 || waterCost != 3000 {
			t.Fatalf("expected milk 25000 at 18560 and water 24 at 3000, got %d at %d and %d at %d", stock, cost, waterStock, waterCost)
		}
		if res := call("POST", "/inventory/purchase-orders/"+po.ID.String()+"/close", owner, nil); res.Code != http.StatusConflict {
			t.Fatalf("a fully received PO is not closed by hand, got %d", res.Code)
		}
		var receipts struct {
			Receipts []struct {
				TotalAmount int64 `json:"total_amount"`
			} `json:"receipts"`
		}
		_ = json.Unmarshal(call("GET", "/inventory/receipts?purchase_order_id="+po.ID.String(), owner, nil).Body.Bytes(), &receipts)
		if len(receipts.Receipts) != 2 || receipts.Receipts[0].TotalAmount+receipts.Receipts[1].TotalAmount != 216000+228000+72000 {
			t.Fatalf("two receipts with their totals, got %+v", receipts.Receipts)
		}
	})

	t.Run("DirectPurchaseWithoutPO", func(t *testing.T) {
		if res := call("POST", "/inventory/purchases", owner, map[string]any{"lines": []map[string]any{
			{"ingredient_id": milk, "purchase_unit": "liter", "conversion": 1000, "quantity": 1, "unit_price": 25000}}}); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("a direct purchase names where it was bought, got %d", res.Code)
		}
		created(call("POST", "/inventory/purchases", owner, map[string]any{"supplier_name": "Pasar", "lines": []map[string]any{
			{"ingredient_id": milk, "purchase_unit": "liter", "conversion": 1000, "quantity": 1, "unit_price": 25000}}}), "direct purchase")
		// (25000 x 18560 + 1000 x 25000) / 26000 = 18808 per 1000 ml.
		if stock, cost, _, _ := state(); stock != 26000 || cost != 18808 {
			t.Fatalf("expected 26000 ml at 18808, got %d at %d", stock, cost)
		}
		var movements int
		inStore(func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &movements, `SELECT COUNT(*) FROM ingredient_movements WHERE store_id = $1 AND ingredient_id = $2 AND kind = 'purchase'`, storeID, milk)
		})
		if movements != 3 {
			t.Fatalf("each receipt line writes a purchase movement, got %d", movements)
		}
	})
}
