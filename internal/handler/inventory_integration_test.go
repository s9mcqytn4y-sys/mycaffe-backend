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
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/handler"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

// TestInventoryIntegration covers v0.11 PR 11.3 (K2, X5, X6, X10): ingredients with an opening cost, recipes
// per variant and per modifier option (substitution), deduction at payment, the low-stock alert and the cost
// of goods per line. It uses products and ingredients of its own so parallel tests are not affected.
func TestInventoryIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping inventory integration test")
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
	inv.PUT("/recipes/:kind/:id", ih.SetRecipe)

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

	ingredient := func(body map[string]any) uuid.UUID {
		t.Helper()
		res := call("POST", "/inventory/ingredients", owner, body)
		if res.Code != http.StatusCreated {
			t.Fatalf("create ingredient failed: %d: %s", res.Code, res.Body.String())
		}
		var out struct {
			ID uuid.UUID `json:"id"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &out)
		t.Cleanup(func() {
			call("PATCH", "/inventory/ingredients/"+out.ID.String(), owner, map[string]any{"is_active": false})
		})
		return out.ID
	}
	balance := func(id uuid.UUID) (stock int64) {
		t.Helper()
		inStore(func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &stock, `SELECT stock_quantity FROM ingredients WHERE store_id = $1 AND id = $2`, storeID, id)
		})
		return stock
	}

	var coffee, milk, oat uuid.UUID
	t.Run("OwnerManagesIngredients", func(t *testing.T) {
		if res := call("GET", "/inventory/ingredients", cashier, nil); res.Code != http.StatusForbidden {
			t.Fatalf("cashier must not manage ingredients, got %d", res.Code)
		}
		if res := call("POST", "/inventory/ingredients", owner, map[string]any{"name": "X", "unit": "kg"}); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("only g, ml and pcs, got %d", res.Code)
		}
		// 1 kg of beans for Rp300.000 = Rp300 per gram (300.000 per 1000 g).
		coffee = ingredient(map[string]any{"name": "Biji Kopi " + suffix, "unit": "g", "min_stock": 980, "opening_quantity": 1000, "opening_cost": 300000})
		milk = ingredient(map[string]any{"name": "Susu " + suffix, "unit": "ml", "opening_quantity": 2000, "opening_cost": 40000})
		oat = ingredient(map[string]any{"name": "Oat " + suffix, "unit": "ml", "opening_quantity": 1000, "opening_cost": 60000})
		if res := call("POST", "/inventory/ingredients", owner, map[string]any{"name": "Susu " + suffix, "unit": "ml"}); res.Code != http.StatusConflict {
			t.Fatalf("a duplicate active name must be 409, got %d", res.Code)
		}
	})

	res := call("POST", "/catalog/products", owner, map[string]any{"name": "Resep Latte " + suffix, "product_type": "prepared",
		"prep_station": "bar", "variants": []map[string]any{{"name": "Reguler", "price": 30000, "is_default": true}}})
	if res.Code != http.StatusCreated {
		t.Fatalf("create product failed: %d: %s", res.Code, res.Body.String())
	}
	var product struct {
		ID       uuid.UUID `json:"id"`
		Variants []struct {
			ID uuid.UUID `json:"id"`
		} `json:"variants"`
	}
	_ = json.Unmarshal(res.Body.Bytes(), &product)
	t.Cleanup(func() { call("DELETE", "/catalog/products/"+product.ID.String(), owner, nil) })
	var group, option struct {
		ID uuid.UUID `json:"id"`
	}
	_ = json.Unmarshal(call("POST", "/catalog/modifier-groups", owner, map[string]any{"name": "Susu Pilihan " + suffix, "min_select": 0, "max_select": 1}).Body.Bytes(), &group)
	_ = json.Unmarshal(call("POST", "/catalog/modifier-groups/"+group.ID.String()+"/options", owner, map[string]any{"name": "Oat milk", "price_delta": 5000}).Body.Bytes(), &option)
	if res := call("POST", "/catalog/products/"+product.ID.String()+"/modifier-groups", owner, map[string]any{"group_id": group.ID}); res.Code >= 300 {
		t.Fatalf("attach modifier group failed: %d: %s", res.Code, res.Body.String())
	}

	t.Run("RecipesPerVariantAndOption", func(t *testing.T) {
		variant := "/inventory/recipes/variant/" + product.Variants[0].ID.String()
		if res := call("PUT", variant, owner, map[string]any{"items": []map[string]any{{"ingredient_id": milk, "quantity": -150}}}); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("a variant recipe cannot take ingredients away, got %d", res.Code)
		}
		if res := call("PUT", variant, owner, map[string]any{"items": []map[string]any{
			{"ingredient_id": coffee, "quantity": 18}, {"ingredient_id": milk, "quantity": 150}}}); res.Code != http.StatusOK {
			t.Fatalf("set variant recipe failed: %d: %s", res.Code, res.Body.String())
		}
		// X6: oat milk replaces the cow milk of the drink.
		if res := call("PUT", "/inventory/recipes/option/"+option.ID.String(), owner, map[string]any{"items": []map[string]any{
			{"ingredient_id": milk, "quantity": -150}, {"ingredient_id": oat, "quantity": 150}}}); res.Code != http.StatusOK {
			t.Fatalf("set option recipe failed: %d: %s", res.Code, res.Body.String())
		}
	})

	var drawer domain.CashDrawer
	_ = json.Unmarshal(call("POST", "/pos/drawers", owner, map[string]any{"name": "Laci Resep " + suffix}).Body.Bytes(), &drawer)
	var opened struct {
		Shift domain.Shift `json:"shift"`
	}
	if res := call("POST", "/pos/shifts", cashier, map[string]any{"drawer_id": drawer.ID, "opening_float": 0}); res.Code != http.StatusCreated {
		t.Fatalf("open shift failed: %d: %s", res.Code, res.Body.String())
	} else {
		_ = json.Unmarshal(res.Body.Bytes(), &opened)
	}
	t.Cleanup(func() {
		call("POST", fmt.Sprintf("/pos/shifts/%s/force-close", opened.Shift.ID), owner, map[string]any{"reason": "Akhir test resep"})
	})

	t.Run("PaymentDeductsIngredientsAndStoresCOGS", func(t *testing.T) {
		res := call("POST", "/pos/orders", cashier, map[string]any{"idempotency_key": "recipe-" + uuid.NewString(), "order_type": "takeaway",
			"customer_name": "Resep", "items": []map[string]any{
				{"product_id": product.ID, "quantity": 1},
				{"product_id": product.ID, "quantity": 1, "modifiers": []map[string]any{{"option_id": option.ID, "quantity": 1}}},
			}})
		if res.Code != http.StatusCreated {
			t.Fatalf("create order failed: %d: %s", res.Code, res.Body.String())
		}
		var created struct {
			Order domain.Order `json:"order"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &created)
		if balance(coffee) != 1000 {
			t.Fatal("nothing is deducted before payment")
		}
		if res := call("POST", fmt.Sprintf("/pos/orders/%s/payments", created.Order.ID), cashier, map[string]any{
			"idempotency_key": "pay-" + uuid.NewString(), "method": "cash", "cash_tendered_amount": 1_000_000}); res.Code != http.StatusOK {
			t.Fatalf("payment failed: %d: %s", res.Code, res.Body.String())
		}
		// Two drinks: 36 g coffee; one with cow milk (150 ml), one with oat instead (150 ml oat, no cow milk).
		if c, m, o := balance(coffee), balance(milk), balance(oat); c != 964 || m != 1850 || o != 850 {
			t.Fatalf("expected coffee 964, milk 1850, oat 850; got %d, %d, %d", c, m, o)
		}
		var costs []int64
		var alerts int
		inStore(func(tx *sqlx.Tx) error {
			if err := tx.SelectContext(ctx, &costs, `SELECT cogs_amount FROM order_items WHERE store_id = $1 AND order_id = $2 ORDER BY cogs_amount`, storeID, created.Order.ID); err != nil {
				return err
			}
			return tx.GetContext(ctx, &alerts, `SELECT COUNT(*) FROM outbox_events WHERE store_id = $1 AND event_type = 'inventory.low_stock' AND aggregate_id = $2`, storeID, coffee.String())
		})
		// Cow milk drink: 18 g x Rp300 + 150 ml x Rp20 = 8.400; oat drink: 5.400 + 150 ml x Rp60 = 14.400.
		if len(costs) != 2 || costs[0] != 8400 || costs[1] != 14400 {
			t.Fatalf("COGS per line must follow the recipes at their costs, got %v", costs)
		}
		if alerts != 1 {
			t.Fatalf("crossing the minimum alerts the owner once, got %d", alerts)
		}
		low := call("GET", "/inventory/ingredients?low=true", owner, nil)
		if !strings.Contains(low.Body.String(), coffee.String()) || strings.Contains(low.Body.String(), milk.String()) {
			t.Fatalf("the low list shows only ingredients under their minimum: %s", low.Body.String())
		}
	})
}
