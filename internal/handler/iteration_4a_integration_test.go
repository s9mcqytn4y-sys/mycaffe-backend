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
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/handler"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

// TestIteration4aIntegration tests Iteration 4a: Modifier CRUD, negative deltas, sub-addon depth 1,
// POS & Public Quote APIs, and the 409 QUOTE_CHANGED expected_total contract.
func TestIteration4aIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping Iteration 4a integration test")
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
	catSvc := service.NewCatalogService(db)
	ch := handler.NewCatalogHandler(catSvc)
	orderRepo := postgres.NewOrderRepository()
	orderSvc := service.NewOrderService(db, orderRepo)
	pos := handler.NewPOSHandler(orderSvc, service.NewPaymentService(db, postgres.NewPaymentRepository(), orderRepo, nil), service.NewSettingsService(db))

	r := gin.New()
	r.POST("/login", ah.Login)
	r.GET("/pos/catalog", ah.RequireStaff(), ch.StaffCatalog)
	r.GET("/public/stores/:slug/catalog", ch.PublicCatalog)
	r.POST("/public/stores/:slug/orders/quote", pos.PublicQuoteOrder)

	manage := ah.RequirePermission(rbac.CatalogManage)
	r.POST("/catalog/products", manage, ch.CreateProduct)
	r.POST("/catalog/modifier-groups", manage, ch.CreateModifierGroup)
	r.GET("/catalog/modifier-groups", manage, ch.ListModifierGroups)
	r.GET("/catalog/modifier-groups/:id", manage, ch.GetModifierGroup)
	r.PATCH("/catalog/modifier-groups/:id", manage, ch.UpdateModifierGroup)
	r.DELETE("/catalog/modifier-groups/:id", manage, ch.ArchiveModifierGroup)

	r.POST("/catalog/products/:id/modifier-groups", manage, ch.AttachModifierGroupToProduct)
	r.DELETE("/catalog/products/:id/modifier-groups/:group_id", manage, ch.DetachModifierGroupFromProduct)

	r.POST("/catalog/modifier-groups/:id/options", manage, ch.CreateModifierOption)
	r.PATCH("/catalog/modifier-groups/:id/options/:option_id", manage, ch.UpdateModifierOption)
	r.DELETE("/catalog/modifier-groups/:id/options/:option_id", manage, ch.ArchiveModifierOption)

	r.POST("/catalog/modifier-options/:id/child-groups", manage, ch.AttachChildGroupToOption)
	r.DELETE("/catalog/modifier-options/:id/child-groups/:group_id", manage, ch.DetachChildGroupFromOption)

	r.POST("/pos/orders/quote", ah.RequirePermission(rbac.OrderPOSCreate), pos.QuoteOrder)
	r.POST("/pos/orders", ah.RequirePermission(rbac.OrderPOSCreate), pos.CreateOrder)

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
		w := call(http.MethodPost, "/login", "", map[string]string{"email": email, "pin": "482913"})
		if w.Code != http.StatusOK {
			t.Fatalf("login %s failed: %d %s", email, w.Code, w.Body.String())
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

	// 1. RBAC: Cashier cannot create modifier groups (403 FORBIDDEN); Barista/Kitchen cannot access POS Quote
	t.Run("RBAC_CashierDeniedModifierManage", func(t *testing.T) {
		w := call(http.MethodPost, "/catalog/modifier-groups", cashier, map[string]any{"name": "Level Pedas"})
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403 FORBIDDEN for cashier, got %d", w.Code)
		}
	})

	t.Run("RBAC_BaristaAndKitchenDeniedPOSQuote", func(t *testing.T) {
		quotePayload := map[string]any{
			"order_type": "dine_in",
			"items": []map[string]any{
				{"product_id": uuid.NewString(), "quantity": 1},
			},
		}
		wBarista := call(http.MethodPost, "/pos/orders/quote", barista, quotePayload)
		if wBarista.Code != http.StatusForbidden {
			t.Fatalf("expected 403 FORBIDDEN for barista on POS quote, got %d", wBarista.Code)
		}
		wKitchen := call(http.MethodPost, "/pos/orders/quote", kitchen, quotePayload)
		if wKitchen.Code != http.StatusForbidden {
			t.Fatalf("expected 403 FORBIDDEN for kitchen on POS quote, got %d", wKitchen.Code)
		}
	})

	// 2. Modifier Group CRUD
	var groupID string
	t.Run("CreateModifierGroup", func(t *testing.T) {
		w := call(http.MethodPost, "/catalog/modifier-groups", owner, map[string]any{
			"name":           "Pilihan Susu 4A",
			"selection_type": "single",
			"is_required":    true,
			"min_select":     1,
			"max_select":     1,
			"display_order":  1,
		})
		if w.Code != http.StatusCreated {
			t.Fatalf("create modifier group failed: %d %s", w.Code, w.Body.String())
		}
		var created struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		decode(w, &created)
		if created.ID == "" || created.Name != "Pilihan Susu 4A" {
			t.Fatalf("unexpected group: %+v", created)
		}
		groupID = created.ID

		// Unique constraint clash (case-insensitive) -> 409 CONFLICT
		dup := call(http.MethodPost, "/catalog/modifier-groups", owner, map[string]any{
			"name": "pilihan susu 4a",
		})
		if dup.Code != http.StatusConflict {
			t.Fatalf("expected 409 on duplicate group name, got %d", dup.Code)
		}
	})

	// 3. Modifier Option CRUD & Negative Price Delta (Decision P1)
	var optOatID, optNoneID string
	t.Run("CreateModifierOptionsWithNegativeDelta", func(t *testing.T) {
		// Positive delta: Oatmilk +Rp5.000
		w1 := call(http.MethodPost, "/catalog/modifier-groups/"+groupID+"/options", owner, map[string]any{
			"name":         "Oatmilk",
			"price_delta":  5000,
			"is_available": true,
			"max_quantity": 1,
		})
		if w1.Code != http.StatusCreated {
			t.Fatalf("create option 1 failed: %d %s", w1.Code, w1.Body.String())
		}
		var o1 struct {
			ID string `json:"id"`
		}
		decode(w1, &o1)
		optOatID = o1.ID

		// Negative delta: Tanpa Susu -Rp2.000 (Owner Decision P1)
		w2 := call(http.MethodPost, "/catalog/modifier-groups/"+groupID+"/options", owner, map[string]any{
			"name":         "Tanpa Susu",
			"price_delta":  -2000,
			"is_available": true,
			"max_quantity": 1,
		})
		if w2.Code != http.StatusCreated {
			t.Fatalf("create option 2 (negative delta) failed: %d %s", w2.Code, w2.Body.String())
		}
		var o2 struct {
			ID         string `json:"id"`
			PriceDelta int64  `json:"price_delta"`
		}
		decode(w2, &o2)
		optNoneID = o2.ID
		if o2.PriceDelta != -2000 {
			t.Fatalf("expected negative delta -2000, got %d", o2.PriceDelta)
		}

		// Update price delta audits PRICE_CHANGE
		w3 := call(http.MethodPatch, "/catalog/modifier-groups/"+groupID+"/options/"+optNoneID, owner, map[string]any{
			"price_delta": -3000,
		})
		if w3.Code != http.StatusOK {
			t.Fatalf("update delta failed: %d %s", w3.Code, w3.Body.String())
		}
	})

	// 4. Sub-addon depth 1 constraint and cycle prevention
	t.Run("SubAddonDepth1AndCyclePrevention", func(t *testing.T) {
		// Create Child Group
		wChild := call(http.MethodPost, "/catalog/modifier-groups", owner, map[string]any{
			"name":           "Brand Oatmilk 4A",
			"selection_type": "single",
			"is_required":    false,
			"min_select":     0,
			"max_select":     1,
		})
		if wChild.Code != http.StatusCreated {
			t.Fatalf("create child group failed: %d %s", wChild.Code, wChild.Body.String())
		}
		var child struct {
			ID string `json:"id"`
		}
		decode(wChild, &child)

		// Attach child group to optOat
		wAttach := call(http.MethodPost, "/catalog/modifier-options/"+optOatID+"/child-groups", owner, map[string]any{
			"child_group_id": child.ID,
			"display_order":  1,
		})
		if wAttach.Code != http.StatusNoContent {
			t.Fatalf("attach child group failed: %d %s", wAttach.Code, wAttach.Body.String())
		}

		// Cycle prevention: Attaching parent group as child of its own option -> 422
		wCycle := call(http.MethodPost, "/catalog/modifier-options/"+optOatID+"/child-groups", owner, map[string]any{
			"child_group_id": groupID,
		})
		if wCycle.Code != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422 on cycle attachment, got %d", wCycle.Code)
		}
	})

	// 5. Product creation and attaching modifier group
	var productID, variantID string
	t.Run("CreateProductAndAttachGroup", func(t *testing.T) {
		wProd := call(http.MethodPost, "/catalog/products", owner, map[string]any{
			"name":         "Americano 4A",
			"description":  "Kopi espresso segar",
			"product_type": "prepared",
			"prep_station": "bar",
			"variants": []map[string]any{
				{"name": "Regular", "sku": "AME-4A-R", "price": 18000, "is_default": true},
			},
		})
		if wProd.Code != http.StatusCreated {
			t.Fatalf("create product failed: %d %s", wProd.Code, wProd.Body.String())
		}
		var prod struct {
			ID       string `json:"id"`
			Variants []struct {
				ID string `json:"id"`
			} `json:"variants"`
		}
		decode(wProd, &prod)
		productID = prod.ID
		variantID = prod.Variants[0].ID

		wAttach := call(http.MethodPost, "/catalog/products/"+productID+"/modifier-groups", owner, map[string]any{
			"group_id":      groupID,
			"display_order": 1,
		})
		if wAttach.Code != http.StatusNoContent {
			t.Fatalf("attach group to product failed: %d %s", wAttach.Code, wAttach.Body.String())
		}
	})

	// 6. Quote POS and Public Quote APIs
	t.Run("QuoteAPI_POSAndPublic", func(t *testing.T) {
		quotePayload := map[string]any{
			"order_type": "dine_in",
			"items": []map[string]any{
				{
					"product_id": productID,
					"variant_id": variantID,
					"quantity":   2,
					"modifiers": []map[string]any{
						{"option_id": optNoneID, "quantity": 1},
					},
				},
			},
		}

		// Staff POS quote: 2 * (18.000 - 3.000) = 30.000 subtotal
		wPOS := call(http.MethodPost, "/pos/orders/quote", cashier, quotePayload)
		if wPOS.Code != http.StatusOK {
			t.Fatalf("POS quote failed: %d %s", wPOS.Code, wPOS.Body.String())
		}
		var posQuote service.OrderQuoteResult
		decode(wPOS, &posQuote)
		if posQuote.SubtotalAmount != 30000 {
			t.Fatalf("expected subtotal 30000, got %d", posQuote.SubtotalAmount)
		}
		if len(posQuote.Lines) != 1 || posQuote.Lines[0].UnitSnapshotPrice != 15000 {
			t.Fatalf("unexpected line price: %+v", posQuote.Lines)
		}

		// Public Quote API for guest ordering: POST /public/stores/central-cafe/orders/quote
		wPub := call(http.MethodPost, "/public/stores/central-cafe/orders/quote", "", quotePayload)
		if wPub.Code != http.StatusOK {
			t.Fatalf("Public quote failed: %d %s", wPub.Code, wPub.Body.String())
		}
		var pubQuote service.OrderQuoteResult
		decode(wPub, &pubQuote)
		if pubQuote.TotalAmount != posQuote.TotalAmount {
			t.Fatalf("public and pos quote totals mismatch: %d vs %d", pubQuote.TotalAmount, posQuote.TotalAmount)
		}
	})

	// 7. Expected Total Contract (Decision P8)
	t.Run("ExpectedTotal_409QuoteChanged", func(t *testing.T) {
		// Mismatched expected total -> 409 QUOTE_CHANGED with fresh quote
		wConflict := call(http.MethodPost, "/pos/orders", cashier, map[string]any{
			"idempotency_key": "test-exp-total-" + uuid.NewString(),
			"order_type":      "dine_in",
			"table_number":    "T4A",
			"customer_name":   "Budi 4A",
			"expected_total":  999999, // intentionally wrong expected total
			"items": []map[string]any{
				{
					"product_id": productID,
					"variant_id": variantID,
					"quantity":   1,
					"modifiers": []map[string]any{
						{"option_id": optNoneID, "quantity": 1},
					},
				},
			},
		})
		if wConflict.Code != http.StatusConflict {
			t.Fatalf("expected 409 QUOTE_CHANGED, got %d %s", wConflict.Code, wConflict.Body.String())
		}
		var errResp struct {
			Code        string                   `json:"code"`
			ActualTotal int64                    `json:"actual_total"`
			Quote       service.OrderQuoteResult `json:"quote"`
		}
		decode(wConflict, &errResp)
		if errResp.Code != "QUOTE_CHANGED" {
			t.Fatalf("expected code QUOTE_CHANGED, got %s", errResp.Code)
		}
		if errResp.ActualTotal <= 0 || errResp.Quote.TotalAmount != errResp.ActualTotal {
			t.Fatalf("invalid quote payload in 409 response: %+v", errResp)
		}

		// Correct expected_total succeeds (201 CREATED)
		wSuccess := call(http.MethodPost, "/pos/orders", cashier, map[string]any{
			"idempotency_key": "test-exp-total-" + uuid.NewString(),
			"order_type":      "dine_in",
			"table_number":    "T4A",
			"customer_name":   "Budi 4A",
			"expected_total":  errResp.ActualTotal,
			"items": []map[string]any{
				{
					"product_id": productID,
					"variant_id": variantID,
					"quantity":   1,
					"modifiers": []map[string]any{
						{"option_id": optNoneID, "quantity": 1},
					},
				},
			},
		})
		if wSuccess.Code != http.StatusCreated {
			t.Fatalf("expected 201 with correct expected_total, got %d %s", wSuccess.Code, wSuccess.Body.String())
		}
	})

	// 8. Quote Fingerprint & Compensating Price Change Protection (Decision P8)
	t.Run("QuoteFingerprint_CompensatingPriceChangeConflict", func(t *testing.T) {
		quotePayload := map[string]any{
			"order_type": "dine_in",
			"items": []map[string]any{
				{
					"product_id": productID,
					"variant_id": variantID,
					"quantity":   1,
					"modifiers": []map[string]any{
						{"option_id": optNoneID, "quantity": 1},
					},
				},
			},
		}

		wQuote := call(http.MethodPost, "/pos/orders/quote", cashier, quotePayload)
		if wQuote.Code != http.StatusOK {
			t.Fatalf("POS quote failed: %d %s", wQuote.Code, wQuote.Body.String())
		}
		var initialQuote service.OrderQuoteResult
		decode(wQuote, &initialQuote)
		if initialQuote.QuoteFingerprint == "" || !strings.HasPrefix(initialQuote.QuoteFingerprint, "qf_") {
			t.Fatalf("expected valid quote_fingerprint starting with qf_, got %q", initialQuote.QuoteFingerprint)
		}

		// Valid fingerprint succeeds
		wOrder := call(http.MethodPost, "/pos/orders", cashier, map[string]any{
			"idempotency_key":            "test-fingerprint-" + uuid.NewString(),
			"order_type":                 "dine_in",
			"table_number":               "T4A-FP",
			"customer_name":              "Budi Fingerprint",
			"expected_quote_fingerprint": initialQuote.QuoteFingerprint,
			"items":                      quotePayload["items"],
		})
		if wOrder.Code != http.StatusCreated {
			t.Fatalf("expected 201 created with matching quote fingerprint, got %d %s", wOrder.Code, wOrder.Body.String())
		}

		// Altered/stale fingerprint (e.g. compensating price change) returns 409 QUOTE_CHANGED
		wStale := call(http.MethodPost, "/pos/orders", cashier, map[string]any{
			"idempotency_key":            "test-fingerprint-mismatch-" + uuid.NewString(),
			"order_type":                 "dine_in",
			"table_number":               "T4A-FP",
			"customer_name":              "Budi Mismatch",
			"expected_total":             initialQuote.TotalAmount, // total may match...
			"expected_quote_fingerprint": "qf_stale_fingerprint_0000000000000000",
			"items":                      quotePayload["items"],
		})
		if wStale.Code != http.StatusConflict {
			t.Fatalf("expected 409 QUOTE_CHANGED for mismatched fingerprint, got %d %s", wStale.Code, wStale.Body.String())
		}
		var conflictResp struct {
			Code                string `json:"code"`
			ExpectedFingerprint string `json:"expected_fingerprint"`
			ActualFingerprint   string `json:"actual_fingerprint"`
		}
		decode(wStale, &conflictResp)
		if conflictResp.Code != "QUOTE_CHANGED" || conflictResp.ActualFingerprint != initialQuote.QuoteFingerprint {
			t.Fatalf("unexpected conflict response payload: %+v", conflictResp)
		}
	})

	// 9. Graph Integrity & Archived Entity Protection
	t.Run("ModifierGraph_ArchivedProtection", func(t *testing.T) {
		// Create temporary group and archive it
		wGrp := call(http.MethodPost, "/catalog/modifier-groups", owner, map[string]any{
			"name":           "Archived Group Test",
			"selection_type": "single",
			"is_required":    false,
			"min_select":     0,
			"max_select":     1,
		})
		if wGrp.Code != http.StatusCreated {
			t.Fatalf("create group failed: %d %s", wGrp.Code, wGrp.Body.String())
		}
		var grp struct {
			Group domain.ModifierGroup `json:"group"`
		}
		decode(wGrp, &grp)
		archivedGroupID := grp.Group.ID.String()

		wArch := call(http.MethodDelete, "/catalog/modifier-groups/"+archivedGroupID, owner, nil)
		if wArch.Code != http.StatusNoContent {
			t.Fatalf("archive group failed: %d %s", wArch.Code, wArch.Body.String())
		}

		// Attaching archived group to product must fail (400 validation error)
		wAttach := call(http.MethodPost, "/catalog/products/"+productID+"/modifier-groups", owner, map[string]any{
			"group_id": archivedGroupID,
		})
		if wAttach.Code != http.StatusBadRequest {
			t.Fatalf("attaching archived group must return 400, got %d %s", wAttach.Code, wAttach.Body.String())
		}

		// Adding option to archived group must fail (400 validation error)
		wOpt := call(http.MethodPost, "/catalog/modifier-groups/"+archivedGroupID+"/options", owner, map[string]any{
			"name":        "Invalid Option",
			"price_delta": 0,
		})
		if wOpt.Code != http.StatusBadRequest {
			t.Fatalf("adding option to archived group must return 400, got %d %s", wOpt.Code, wOpt.Body.String())
		}
	})
}
