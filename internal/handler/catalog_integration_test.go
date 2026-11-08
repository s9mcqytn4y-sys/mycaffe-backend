package handler_test

import (
	"bytes"
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
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/config"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/handler"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/service"
)

// TestCatalogIntegration covers catalog C1–C3 over HTTP against PostgreSQL 18: read models with
// ETag, the CRUD error contract (422 fields, 409 field, 403, 404), station availability toggles,
// price-change audit, archiving, and variant pricing on POS orders.
func TestCatalogIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping catalog HTTP integration test")
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
	ah := handler.NewAuthHandler(authSvc)
	ah.DefaultStoreSlug = "central-cafe"
	ch := handler.NewCatalogHandler(service.NewCatalogService(db))
	orderRepo := postgres.NewOrderRepository()
	pos := handler.NewPOSHandler(service.NewOrderService(db, orderRepo), service.NewPaymentService(db, postgres.NewPaymentRepository(), orderRepo, nil), service.NewSettingsService(db))

	r := gin.New()
	r.POST("/login", ah.Login)
	r.GET("/pos/catalog", ah.RequireStaff(), ch.StaffCatalog)
	r.GET("/public/stores/:slug/catalog", ch.PublicCatalog)
	manage := ah.RequirePermission(rbac.CatalogManage)
	r.POST("/catalog/categories", manage, ch.CreateCategory)
	r.POST("/catalog/products", manage, ch.CreateProduct)
	r.DELETE("/catalog/products/:id", manage, ch.ArchiveProduct)
	r.PATCH("/catalog/products/:id/availability", ah.RequireStaff(), ch.SetAvailability)
	r.PATCH("/catalog/variants/:id", manage, ch.UpdateVariant)
	r.DELETE("/catalog/variants/:id", manage, ch.ArchiveVariant)
	r.POST("/pos/orders", ah.RequirePermission(rbac.OrderPOSCreate), pos.CreateOrder)

	call := func(method, path, token string, body any, headers ...string) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		for i := 0; i+1 < len(headers); i += 2 {
			req.Header.Set(headers[i], headers[i+1])
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	login := func(email string) string {
		// store_slug omitted: DEFAULT_STORE_SLUG fills it (single-cafe MVP).
		w := call(http.MethodPost, "/login", "", map[string]string{"email": email, "pin": "482913"})
		if w.Code != http.StatusOK {
			t.Fatalf("login %s: %d %s", email, w.Code, w.Body.String())
		}
		var out struct {
			AccessToken string `json:"access_token"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out.AccessToken
	}
	decode := func(w *httptest.ResponseRecorder, v any) {
		if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
			t.Fatalf("decode %q: %v", w.Body.String(), err)
		}
	}

	owner := login("abdulaziz543333@gmail.com")
	cashier := login("cashier.central@mycaffe.local")
	barista := login("barista.central@mycaffe.local")
	kitchen := login("kitchen.central@mycaffe.local")
	suffix := uuid.NewString()[:6]

	t.Run("ReadModels_ETag_PublicSlug", func(t *testing.T) {
		w := call(http.MethodGet, "/pos/catalog", cashier, nil)
		if w.Code != http.StatusOK || w.Header().Get("ETag") == "" {
			t.Fatalf("staff catalog: %d etag=%q", w.Code, w.Header().Get("ETag"))
		}
		var view service.CatalogView
		decode(w, &view)
		var latte *service.CatalogProduct
		for i := range view.Products {
			if view.Products[i].Name == "Caffe Latte" {
				latte = &view.Products[i]
			}
		}
		if latte == nil || len(latte.Variants) != 2 {
			t.Fatalf("Caffe Latte must expose Reguler + Large variants, got %+v", latte)
		}
		if w2 := call(http.MethodGet, "/pos/catalog", cashier, nil, "If-None-Match", w.Header().Get("ETag")); w2.Code != http.StatusNotModified {
			t.Fatalf("unchanged catalog must answer 304, got %d", w2.Code)
		}
		if w := call(http.MethodGet, "/public/stores/central-cafe/catalog", "", nil); w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Cache-Control"), "public") {
			t.Fatalf("public catalog: %d %q", w.Code, w.Header().Get("Cache-Control"))
		}
		if w := call(http.MethodGet, "/public/stores/no-such-cafe/catalog", "", nil); w.Code != http.StatusNotFound {
			t.Fatalf("unknown slug: %d", w.Code)
		}
	})

	var productID, largeID, regularID string
	t.Run("CRUD_ErrorContract", func(t *testing.T) {
		w := call(http.MethodPost, "/catalog/categories", owner, map[string]any{"display_order": 1})
		var verr struct {
			Code   string            `json:"code"`
			Fields map[string]string `json:"fields"`
		}
		decode(w, &verr)
		if w.Code != http.StatusUnprocessableEntity || verr.Fields["name"] == "" {
			t.Fatalf("missing name must be 422 with fields.name: %d %s", w.Code, w.Body.String())
		}
		if w := call(http.MethodPost, "/catalog/categories", owner, `{"name":`); w.Code != http.StatusBadRequest {
			t.Fatalf("malformed JSON must be 400, got %d", w.Code)
		}
		w = call(http.MethodPost, "/catalog/categories", owner, map[string]any{"name": "Non-Kopi " + suffix})
		var cat struct {
			ID string `json:"id"`
		}
		decode(w, &cat)
		if w.Code != http.StatusCreated {
			t.Fatalf("create category: %d %s", w.Code, w.Body.String())
		}

		product := map[string]any{
			"category_id": cat.ID, "name": "Matcha Latte " + suffix, "product_type": "prepared", "prep_station": "bar",
			"variants": []map[string]any{
				{"name": "Reguler", "sku": "mtc-r-" + suffix, "price": 28000, "is_default": true},
				{"name": "Large", "sku": "MTC-L-" + suffix, "price": 34000},
			},
		}
		if w := call(http.MethodPost, "/catalog/products", cashier, product); w.Code != http.StatusForbidden {
			t.Fatalf("cashier must not manage the catalog: %d", w.Code)
		}
		bad := map[string]any{"name": "Salah " + suffix, "variants": []map[string]any{{"name": "Reguler", "price": -1}}}
		w = call(http.MethodPost, "/catalog/products", owner, bad)
		decode(w, &verr)
		if w.Code != http.StatusUnprocessableEntity || verr.Fields["variants[0].price"] == "" {
			t.Fatalf("negative price must be 422 on variants[0].price: %d %s", w.Code, w.Body.String())
		}

		w = call(http.MethodPost, "/catalog/products", owner, product)
		if w.Code != http.StatusCreated {
			t.Fatalf("create product: %d %s", w.Code, w.Body.String())
		}
		var created service.ProductWithVariants
		decode(w, &created)
		productID = created.ID.String()
		for _, v := range created.Variants {
			if v.IsDefault {
				regularID = v.ID.String()
				if v.SKU != strings.ToUpper("mtc-r-"+suffix) {
					t.Fatalf("SKU must be normalized to upper case, got %q", v.SKU)
				}
			} else {
				largeID = v.ID.String()
			}
		}
		if created.BasePrice != 28000 {
			t.Fatalf("base_price must follow the default variant, got %d", created.BasePrice)
		}

		var conflict struct {
			Field string `json:"field"`
		}
		w = call(http.MethodPost, "/catalog/products", owner, map[string]any{"name": "matcha latte " + suffix, "variants": []map[string]any{{"name": "Reguler", "price": 1000}}})
		decode(w, &conflict)
		if w.Code != http.StatusConflict || conflict.Field != "name" {
			t.Fatalf("case-insensitive duplicate name must be 409 on name: %d %s", w.Code, w.Body.String())
		}
		w = call(http.MethodPost, "/catalog/products", owner, map[string]any{"name": "Lain " + suffix, "variants": []map[string]any{{"name": "Reguler", "sku": "MTC-L-" + suffix, "price": 1000}}})
		decode(w, &conflict)
		if w.Code != http.StatusConflict || conflict.Field != "sku" {
			t.Fatalf("duplicate SKU must be 409 on sku: %d %s", w.Code, w.Body.String())
		}
		if w := call(http.MethodDelete, "/catalog/variants/"+uuid.NewString(), owner, nil); w.Code != http.StatusNotFound {
			t.Fatalf("unknown variant must be 404: %d", w.Code)
		}
	})

	t.Run("StationAvailability_PriceAudit_Archive", func(t *testing.T) {
		if w := call(http.MethodPatch, "/catalog/products/"+productID+"/availability", kitchen, map[string]any{"is_available": false}); w.Code != http.StatusForbidden {
			t.Fatalf("kitchen must not toggle a bar item: %d", w.Code)
		}
		if w := call(http.MethodPatch, "/catalog/products/"+productID+"/availability", barista, map[string]any{"is_available": false}); w.Code != http.StatusNoContent {
			t.Fatalf("barista toggles bar items: %d %s", w.Code, w.Body.String())
		}
		if w := call(http.MethodPatch, "/catalog/products/"+productID+"/availability", barista, map[string]any{"is_available": true}); w.Code != http.StatusNoContent {
			t.Fatalf("barista re-enables: %d", w.Code)
		}

		if w := call(http.MethodPatch, "/catalog/variants/"+largeID, owner, map[string]any{"price": 36000}); w.Code != http.StatusOK {
			t.Fatalf("update price: %d %s", w.Code, w.Body.String())
		}
		var audits int
		_ = database.WithTenantTx(t.Context(), db, uuid.MustParse("11111111-1111-4111-8111-111111111111"), func(tx *sqlx.Tx) error {
			return tx.GetContext(t.Context(), &audits, `SELECT COUNT(*) FROM audit_logs WHERE action = 'PRICE_CHANGE' AND reference_id = $1`, largeID)
		})
		if audits != 1 {
			t.Fatalf("price change must be audited once, got %d", audits)
		}
		if w := call(http.MethodDelete, "/catalog/variants/"+regularID, owner, nil); w.Code != http.StatusConflict {
			t.Fatalf("archiving the default variant must be 409: %d", w.Code)
		}
	})

	t.Run("POSOrderPricesTheChosenVariant", func(t *testing.T) {
		w := call(http.MethodPost, "/pos/orders", cashier, map[string]any{
			"idempotency_key": "cat-var-" + suffix, "order_type": "takeaway",
			"items": []map[string]any{{"product_id": productID, "variant_id": largeID, "quantity": 2}},
		})
		if w.Code != http.StatusCreated {
			t.Fatalf("order with variant: %d %s", w.Code, w.Body.String())
		}
		var res struct {
			Order struct {
				Subtotal int64 `json:"subtotal_amount"`
			} `json:"order"`
		}
		decode(w, &res)
		if res.Order.Subtotal != 72000 {
			t.Fatalf("2 × Large (Rp36.000) must subtotal 72000, got %d", res.Order.Subtotal)
		}
		w = call(http.MethodPost, "/pos/orders", cashier, map[string]any{
			"idempotency_key": "cat-var-bad-" + suffix, "order_type": "takeaway",
			"items": []map[string]any{{"product_id": "d0000003-0000-4000-8000-000000000003", "variant_id": largeID, "quantity": 1}},
		})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("a variant of another product must be rejected (400), got %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("ArchivedProductLeavesTheMenu", func(t *testing.T) {
		if w := call(http.MethodDelete, "/catalog/products/"+productID, owner, nil); w.Code != http.StatusNoContent {
			t.Fatalf("archive product: %d", w.Code)
		}
		w := call(http.MethodGet, "/public/stores/central-cafe/catalog", "", nil)
		if strings.Contains(w.Body.String(), productID) {
			t.Fatal("archived product must not appear in the public menu")
		}
	})
}
