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
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/config"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/handler"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/service"
)

// TestPayablesIntegration covers v0.11 PR 11.5 (X7): a receipt owed to its supplier after the payment terms,
// paid in instalments by transfer and from the outlet cash fund, and a direct purchase paid at once.
func TestPayablesIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping payables integration test")
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
	inv.GET("/payables", ih.ListPayables)
	inv.POST("/payables/:id/payments", ih.PayPayable)
	oh := handler.NewOutletCashHandler(service.NewOutletCashService(db))
	r.POST("/pos/outlet-cash/movements", ah.RequirePermission(rbac.CashMovementCreate), oh.Record)

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
	type payable struct {
		ID         uuid.UUID `json:"id"`
		Receipt    uuid.UUID `json:"goods_receipt_id"`
		Amount     int64     `json:"amount"`
		PaidAmount int64     `json:"paid_amount"`
		Remaining  int64     `json:"remaining"`
		DueDate    string    `json:"due_date"`
		Overdue    bool      `json:"overdue"`
	}
	payableOf := func(receipt uuid.UUID) payable {
		t.Helper()
		var out struct {
			Payables []payable `json:"payables"`
		}
		_ = json.Unmarshal(call("GET", "/inventory/payables", owner, nil).Body.Bytes(), &out)
		for _, p := range out.Payables {
			if p.Receipt == receipt {
				return p
			}
		}
		t.Fatalf("no payable for receipt %s", receipt)
		return payable{}
	}

	milk := created(call("POST", "/inventory/ingredients", owner, map[string]any{"name": "Susu AP " + suffix, "unit": "ml"}), "ingredient")
	t.Cleanup(func() {
		call("PATCH", "/inventory/ingredients/"+milk.String(), owner, map[string]any{"is_active": false})
	})
	supplier := created(call("POST", "/inventory/suppliers", owner, map[string]any{"name": "Dairy AP " + suffix, "payment_days": 14}), "supplier")
	res := call("POST", "/inventory/purchase-orders", owner, map[string]any{"supplier_id": supplier, "lines": []map[string]any{
		{"ingredient_id": milk, "purchase_unit": "karton", "conversion": 12000, "quantity": 2, "unit_price": 50000}}})
	var po struct {
		ID    uuid.UUID `json:"id"`
		Lines []struct {
			ID uuid.UUID `json:"id"`
		} `json:"lines"`
	}
	_ = json.Unmarshal(res.Body.Bytes(), &po)
	created(res, "purchase order")
	call("POST", "/inventory/purchase-orders/"+po.ID.String()+"/send", owner, nil)
	receipt := created(call("POST", "/inventory/purchase-orders/"+po.ID.String()+"/receipts", owner, map[string]any{
		"lines": []map[string]any{{"purchase_order_line_id": po.Lines[0].ID, "quantity": 2}}}), "receipt")

	t.Run("ReceiptIsOwedAfterTheTerms", func(t *testing.T) {
		p := payableOf(receipt)
		wib, _ := time.LoadLocation("Asia/Jakarta")
		if p.Amount != 100000 || p.Remaining != 100000 || p.Overdue || p.DueDate != time.Now().In(wib).AddDate(0, 0, 14).Format("2006-01-02") {
			t.Fatalf("a receipt is owed in full, due after 14 days: %+v", p)
		}
		if res := call("GET", "/inventory/payables", cashier, nil); res.Code != http.StatusForbidden {
			t.Fatalf("cashier must not see payables, got %d", res.Code)
		}
	})

	t.Run("InstalmentsByTransferAndOutletCash", func(t *testing.T) {
		p := payableOf(receipt)
		pay := func(body map[string]any) *httptest.ResponseRecorder {
			return call("POST", "/inventory/payables/"+p.ID.String()+"/payments", owner, body)
		}
		if res := pay(map[string]any{"amount": 60000, "method": "transfer", "reference": "TRF-" + suffix}); res.Code != http.StatusCreated {
			t.Fatalf("transfer instalment failed: %d: %s", res.Code, res.Body.String())
		}
		if res := pay(map[string]any{"amount": 50000, "method": "transfer"}); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("paying more than the rest must be 422, got %d", res.Code)
		}
		if res := pay(map[string]any{"amount": 10000, "method": "gopay"}); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("unknown methods must be 422, got %d", res.Code)
		}
		// The fund gets exactly what this test spends, so parallel tests keep their own balance.
		if res := call("POST", "/pos/outlet-cash/movements", owner, map[string]any{"movement_type": "top_up", "amount": 40000, "note": "Test hutang " + suffix}); res.Code >= 300 {
			t.Fatalf("top up failed: %d: %s", res.Code, res.Body.String())
		}
		if res := pay(map[string]any{"amount": 40000, "method": "outlet_cash"}); res.Code != http.StatusCreated {
			t.Fatalf("outlet cash instalment failed: %d: %s", res.Code, res.Body.String())
		}
		if p = payableOf(receipt); p.Remaining != 0 || p.PaidAmount != 100000 {
			t.Fatalf("two instalments settle the payable: %+v", p)
		}
		var expenses, audits int
		inStore(func(tx *sqlx.Tx) error {
			if err := tx.GetContext(ctx, &expenses, `SELECT COUNT(*) FROM outlet_cash_movements m JOIN payable_payments pp ON pp.store_id = m.store_id
				AND pp.outlet_cash_movement_id = m.id WHERE m.store_id = $1 AND pp.payable_id = $2 AND m.movement_type = 'expense'
				AND m.category = 'ingredients' AND m.amount = 40000`, storeID, p.ID); err != nil {
				return err
			}
			return tx.GetContext(ctx, &audits, `SELECT COUNT(*) FROM audit_logs WHERE store_id = $1 AND action = 'PAYABLE_PAID' AND reference_id = $2`, storeID, p.ID.String())
		})
		if expenses != 1 || audits != 2 {
			t.Fatalf("the outlet payment is an ingredients expense and both payments are audited, got %d and %d", expenses, audits)
		}
	})

	t.Run("DirectPurchasePaidAtOnce", func(t *testing.T) {
		if res := call("POST", "/inventory/purchases", owner, map[string]any{"supplier_name": "Pasar", "pay_with": "gopay", "lines": []map[string]any{
			{"ingredient_id": milk, "purchase_unit": "liter", "conversion": 1000, "quantity": 1, "unit_price": 25000}}}); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("an unknown pay_with must be 422, got %d", res.Code)
		}
		direct := created(call("POST", "/inventory/purchases", owner, map[string]any{"supplier_name": "Pasar", "pay_with": "cash", "lines": []map[string]any{
			{"ingredient_id": milk, "purchase_unit": "liter", "conversion": 1000, "quantity": 1, "unit_price": 25000}}}), "direct purchase")
		if p := payableOf(direct); p.Amount != 25000 || p.Remaining != 0 {
			t.Fatalf("a direct purchase paid at once leaves nothing owed: %+v", p)
		}
		var open struct {
			Payables []payable `json:"payables"`
		}
		_ = json.Unmarshal(call("GET", "/inventory/payables?status=open", owner, nil).Body.Bytes(), &open)
		for _, p := range open.Payables {
			if p.Receipt == direct || p.Receipt == receipt {
				t.Fatalf("settled payables are not open: %+v", p)
			}
		}
	})
}
