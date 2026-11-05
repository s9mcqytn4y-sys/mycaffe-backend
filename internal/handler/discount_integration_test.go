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
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/handler"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

// TestIntegrationOrderDiscounts covers PR 8.6: a cashier discount within cashier_max_discount_bps
// re-totals service and tax; above it needs the owner PIN or an approved "discount" request bound to the
// amount; a 100 % compliment settles the order without payment; paid or already discounted orders refuse.
func TestIntegrationOrderDiscounts(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	migratorDSN := strings.TrimSpace(os.Getenv("TEST_MIGRATOR_DATABASE_URL"))
	if dsn == "" || migratorDSN == "" {
		t.Skip("TEST_APP_DATABASE_URL / TEST_MIGRATOR_DATABASE_URL not set; skipping discount integration test")
	}
	db, err := database.NewPostgresDB(&config.Config{DatabaseURL: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	migratorDB, err := database.NewPostgresDB(&config.Config{DatabaseURL: migratorDSN})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migratorDB.Close() })
	ctx := context.Background()

	suffix := uuid.NewString()[:8]
	slug := "disc-" + suffix
	email := func(name string) string { return name + ".disc." + suffix + "@mycaffe.local" }
	storeID, ownerID, err := service.BootstrapStore(ctx, migratorDB, service.BootstrapStoreInput{
		Slug: slug, StoreName: "Kafe Diskon", OwnerName: "Pemilik Diskon", OwnerEmail: email("owner"), PIN: "482913",
	})
	if err != nil {
		t.Fatal(err)
	}
	// 10 % tax, no service; cashier limit stays at the default 10 %.
	if _, err := migratorDB.ExecContext(ctx, `UPDATE stores SET tax_bps = 1000, service_bps = 0, enabled_payment_methods = '["cash"]'::jsonb WHERE id = $1`, storeID); err != nil {
		t.Fatal(err)
	}
	if _, err := migratorDB.ExecContext(ctx, `INSERT INTO users (store_id, email, full_name, role, pin_hash, is_active, pin_set_at)
		SELECT store_id, $2, 'Kasir', 'cashier', pin_hash, true, NOW() FROM users WHERE id = $1`, ownerID, email("cashier")); err != nil {
		t.Fatal(err)
	}
	productID := uuid.New()
	if _, err := migratorDB.ExecContext(ctx, `WITH p AS (INSERT INTO products (id, store_id, name, base_price, is_available) VALUES ($1, $2, 'Kopi', 0, true) RETURNING store_id, id)
		INSERT INTO product_variants (store_id, product_id, name, price, is_default) SELECT store_id, id, 'Reguler', 50000, true FROM p`, productID, storeID); err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	ah := handler.NewAuthHandler(service.NewAuthService(db, postgres.NewAuthRepository(), "integration-test-jwt-secret-at-least-32-chars", service.AuthPolicy{
		AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 12 * time.Hour, SessionIdleTimeout: 2 * time.Hour,
		MaxSessions: 3, PINMaxFailures: 5, PINLockout: 15 * time.Minute, MaxConcurrentHashes: 8,
	}))
	orderRepo, paymentRepo := postgres.NewOrderRepository(), postgres.NewPaymentRepository()
	ops := service.NewOperationalService(db, orderRepo, paymentRepo, nil, nil, nil)
	pos := handler.NewPOSHandler(service.NewOrderService(db, orderRepo), service.NewPaymentService(db, paymentRepo, orderRepo, nil), service.NewSettingsService(db))
	pos.SetOperational(ops)
	sh := handler.NewShiftHandler(service.NewShiftService(db))
	aph := handler.NewApprovalHandler(service.NewApprovalService(db, ops))
	r := gin.New()
	r.POST("/login", ah.Login)
	r.POST("/pos/drawers", ah.RequirePermission(rbac.CashDrawerManage), sh.CreateDrawer)
	r.POST("/pos/shifts", ah.RequireStaff(), sh.OpenShift)
	r.POST("/pos/orders", ah.RequireStaff(), pos.CreateOrder)
	r.POST("/pos/orders/:id/payments", ah.RequireStaff(), pos.InitiatePayment)
	r.POST("/pos/orders/:id/discount", ah.RequirePermission(rbac.OrderDiscountApply), pos.ApplyDiscount)
	r.POST("/pos/approvals", ah.RequireStaff(), aph.Create)
	r.POST("/approvals/:id/approve", ah.RequirePermission(rbac.ApprovalDecide), aph.Approve)

	call := func(method, path, token string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		_ = json.NewEncoder(&buf).Encode(body)
		req := httptest.NewRequest(method, path, &buf)
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	login := func(who string) string {
		res := call("POST", "/login", "", map[string]string{"store_slug": slug, "email": email(who), "pin": "482913"})
		var b struct {
			AccessToken string `json:"access_token"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &b)
		return b.AccessToken
	}
	codeOf := func(res *httptest.ResponseRecorder) string {
		var b struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &b)
		return b.Code
	}
	owner, cashier := login("owner"), login("cashier")
	var drawer domain.CashDrawer
	_ = json.Unmarshal(call("POST", "/pos/drawers", owner, map[string]any{"name": "Laci"}).Body.Bytes(), &drawer)
	call("POST", "/pos/shifts", cashier, map[string]any{"drawer_id": drawer.ID, "opening_float": 100000})

	newOrder := func() domain.Order {
		t.Helper()
		res := call("POST", "/pos/orders", cashier, map[string]any{"idempotency_key": "disc-" + uuid.NewString(), "order_type": "takeaway",
			"items": []map[string]any{{"product_id": productID, "quantity": 2}}})
		var b struct {
			Order domain.Order `json:"order"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &b)
		if res.Code != http.StatusCreated || b.Order.TotalAmount != 110000 {
			t.Fatalf("order 2 x 50.000 + 10 %% tax: %d %s", res.Code, res.Body.String())
		}
		return b.Order
	}
	discount := func(token string, id uuid.UUID, body map[string]any) (*httptest.ResponseRecorder, service.ApplyDiscountResult) {
		res := call("POST", fmt.Sprintf("/pos/orders/%s/discount", id), token, body)
		var out service.ApplyDiscountResult
		_ = json.Unmarshal(res.Body.Bytes(), &out)
		return res, out
	}

	t.Run("WithinCashierLimitRetotals", func(t *testing.T) {
		o := newOrder()
		if res, _ := discount(cashier, o.ID, map[string]any{"reason": "Promo"}); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("amount or bps is required: %d", res.Code)
		}
		res, out := discount(cashier, o.ID, map[string]any{"discount_bps": 1000, "reason": "Member"})
		if res.Code != http.StatusOK || out.Order.DiscountAmount != 10000 || out.Order.TaxAmount != 9000 || out.Order.TotalAmount != 99000 {
			t.Fatalf("10 %% off 100.000 then 10 %% tax = 99.000: %d %s", res.Code, res.Body.String())
		}
		if res, _ := discount(cashier, o.ID, map[string]any{"discount_amount": 1000, "reason": "Lagi"}); codeOf(res) != "ORDER_NOT_DISCOUNTABLE" {
			t.Fatalf("a second discount must be refused: %d %s", res.Code, res.Body.String())
		}
		pay := call("POST", fmt.Sprintf("/pos/orders/%s/payments", o.ID), cashier, map[string]any{"idempotency_key": "d-pay-" + uuid.NewString(),
			"method": "cash", "cash_tendered_amount": 99000})
		if pay.Code != http.StatusOK || !strings.Contains(pay.Body.String(), `"amount":99000`) {
			t.Fatalf("payment charges the discounted total: %d %s", pay.Code, pay.Body.String())
		}
	})

	t.Run("AboveLimitNeedsOwner", func(t *testing.T) {
		o := newOrder()
		body := map[string]any{"discount_amount": 30000, "reason": "Komplain rasa"}
		if res, _ := discount(cashier, o.ID, body); codeOf(res) != "OWNER_PIN_REQUIRED" {
			t.Fatalf("30 %% is above the 10 %% cashier limit: %d %s", res.Code, res.Body.String())
		}
		ar := call("POST", "/pos/approvals", cashier, map[string]any{"action": "discount", "reference_id": o.ID, "reason": "Komplain rasa",
			"params": map[string]any{"amount": 30000}})
		var a service.Approval
		_ = json.Unmarshal(ar.Body.Bytes(), &a)
		if ar.Code != http.StatusCreated || a.Amount != 30000 || a.ReferenceLabel != o.OrderNumber {
			t.Fatalf("discount approval request: %d %s", ar.Code, ar.Body.String())
		}
		call("POST", fmt.Sprintf("/approvals/%s/approve", a.ID), owner, map[string]any{"pin": "482913"})
		if res, _ := discount(cashier, o.ID, map[string]any{"discount_amount": 35000, "reason": "x", "approval_id": a.ID}); codeOf(res) != "APPROVAL_INVALID" {
			t.Fatalf("approval is bound to the amount: %d %s", res.Code, res.Body.String())
		}
		body["approval_id"] = a.ID
		if res, out := discount(cashier, o.ID, body); res.Code != http.StatusOK || out.Order.TotalAmount != 77000 {
			t.Fatalf("approved discount: %d %s", res.Code, res.Body.String())
		}
		small := newOrder()
		if res := call("POST", "/pos/approvals", cashier, map[string]any{"action": "discount", "reference_id": small.ID, "reason": "x",
			"params": map[string]any{"amount": 5000}}); codeOf(res) != "APPROVAL_NOT_NEEDED" {
			t.Fatalf("within the limit no approval is needed: %d %s", res.Code, res.Body.String())
		}
	})

	t.Run("ComplimentSettlesWithoutPayment", func(t *testing.T) {
		o := newOrder()
		res, out := discount(owner, o.ID, map[string]any{"discount_bps": 10000, "reason": "Tamu owner"})
		if res.Code != http.StatusOK || !out.Compliment || out.Order.TotalAmount != 0 || out.Order.PaymentStatus != domain.OrderPaymentStatusPaid ||
			out.Order.Status == domain.OrderStatusPendingPayment {
			t.Fatalf("compliment must settle the order: %d %s", res.Code, res.Body.String())
		}
		var audits int
		_ = migratorDB.GetContext(ctx, &audits, `SELECT COUNT(*) FROM audit_logs WHERE store_id = $1 AND action = 'ORDER_DISCOUNT' AND reference_id = $2`, storeID, o.OrderNumber)
		if audits != 1 {
			t.Fatalf("one ORDER_DISCOUNT audit expected, got %d", audits)
		}
	})
}
