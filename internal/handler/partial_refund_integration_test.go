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

// TestIntegrationPartialRefunds covers PR 8.5a: refunds by order lines with proportional tax, an
// idempotent retry, the last lines taking the residue and releasing stock, the outlet cash fund as a
// refund source, and handing back a tender of a bill that is only partly paid.
func TestIntegrationPartialRefunds(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	migratorDSN := strings.TrimSpace(os.Getenv("TEST_MIGRATOR_DATABASE_URL"))
	if dsn == "" || migratorDSN == "" {
		t.Skip("TEST_APP_DATABASE_URL / TEST_MIGRATOR_DATABASE_URL not set; skipping partial refund integration test")
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
	slug := "prefund-" + suffix
	email := func(name string) string { return name + ".prefund." + suffix + "@mycaffe.local" }
	storeID, ownerID, err := service.BootstrapStore(ctx, migratorDB, service.BootstrapStoreInput{
		Slug: slug, StoreName: "Kafe Refund Sebagian", OwnerName: "Pemilik", OwnerEmail: email("owner"), PIN: "482913",
	})
	if err != nil {
		t.Fatal(err)
	}
	// 10 % tax, no service: proportional amounts are easy to read.
	if _, err := migratorDB.ExecContext(ctx, `UPDATE stores SET tax_bps = 1000, service_bps = 0, enabled_payment_methods = '["cash","edc"]'::jsonb WHERE id = $1`, storeID); err != nil {
		t.Fatal(err)
	}
	if _, err := migratorDB.ExecContext(ctx, `INSERT INTO users (store_id, email, full_name, role, pin_hash, is_active, pin_set_at)
		SELECT store_id, $2, 'Kasir', 'cashier', pin_hash, true, NOW() FROM users WHERE id = $1`, ownerID, email("cashier")); err != nil {
		t.Fatal(err)
	}
	latteID, cakeID := uuid.New(), uuid.New()
	if _, err := migratorDB.ExecContext(ctx, `
		WITH a AS (INSERT INTO products (id, store_id, name, base_price, is_available, track_stock, stock_quantity) VALUES ($1, $3, 'Latte', 0, true, true, 10) RETURNING store_id, id),
		     b AS (INSERT INTO products (id, store_id, name, base_price, is_available) VALUES ($2, $3, 'Kue', 0, true) RETURNING store_id, id)
		INSERT INTO product_variants (store_id, product_id, name, price, is_default)
		SELECT store_id, id, 'Reguler', 20000, true FROM a UNION ALL SELECT store_id, id, 'Reguler', 10000, true FROM b`, latteID, cakeID, storeID); err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	ah := handler.NewAuthHandler(service.NewAuthService(db, postgres.NewAuthRepository(), "integration-test-jwt-secret-at-least-32-chars", service.AuthPolicy{
		AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 12 * time.Hour, SessionIdleTimeout: 2 * time.Hour,
		MaxSessions: 3, PINMaxFailures: 5, PINLockout: 15 * time.Minute, MaxConcurrentHashes: 8,
	}))
	orderRepo, paymentRepo := postgres.NewOrderRepository(), postgres.NewPaymentRepository()
	pos := handler.NewPOSHandler(service.NewOrderService(db, orderRepo), service.NewPaymentService(db, paymentRepo, orderRepo, nil), service.NewSettingsService(db))
	pos.SetOperational(service.NewOperationalService(db, orderRepo, paymentRepo, nil, nil, nil))
	sh := handler.NewShiftHandler(service.NewShiftService(db))
	r := gin.New()
	r.POST("/login", ah.Login)
	r.POST("/pos/drawers", ah.RequirePermission(rbac.CashDrawerManage), sh.CreateDrawer)
	r.POST("/pos/shifts", ah.RequireStaff(), sh.OpenShift)
	r.POST("/pos/orders", ah.RequireStaff(), pos.CreateOrder)
	r.POST("/pos/orders/:id/payments", ah.RequireStaff(), pos.InitiatePayment)
	r.POST("/pos/orders/:id/void", ah.RequirePermission(rbac.OrderVoid), pos.VoidOrder)
	r.POST("/pos/orders/:id/payments/:payment_id/refund", ah.RequirePermission(rbac.PaymentRefund), pos.RefundPayment)

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
	if res := call("POST", "/pos/shifts", cashier, map[string]any{"drawer_id": drawer.ID, "opening_float": 500000}); res.Code != http.StatusCreated {
		t.Fatalf("open shift: %s", res.Body.String())
	}

	newOrder := func(items ...map[string]any) domain.Order {
		t.Helper()
		res := call("POST", "/pos/orders", cashier, map[string]any{"idempotency_key": "pr-" + uuid.NewString(), "order_type": "takeaway", "items": items})
		if res.Code != http.StatusCreated {
			t.Fatalf("create order: %s", res.Body.String())
		}
		var b struct {
			Order domain.Order `json:"order"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &b)
		return b.Order
	}
	pay := func(orderID uuid.UUID, method string, amount *int64) uuid.UUID {
		t.Helper()
		body := map[string]any{"idempotency_key": "pr-pay-" + uuid.NewString(), "method": method, "cash_tendered_amount": 1_000_000}
		if method == "edc" {
			body["reconciliation_ref"] = "TR-" + uuid.NewString()[:6]
		}
		if amount != nil {
			body["amount"] = *amount
		}
		res := call("POST", fmt.Sprintf("/pos/orders/%s/payments", orderID), cashier, body)
		if res.Code != http.StatusOK {
			t.Fatalf("pay: %s", res.Body.String())
		}
		var b struct {
			Payment struct {
				ID uuid.UUID `json:"id"`
			} `json:"payment"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &b)
		return b.Payment.ID
	}
	refund := func(orderID, paymentID uuid.UUID, extra map[string]any) (*httptest.ResponseRecorder, service.RefundPaymentResult) {
		body := map[string]any{"reason": "Komplain", "pin": "482913", "restore_stock": true}
		for k, v := range extra {
			body[k] = v
		}
		res := call("POST", fmt.Sprintf("/pos/orders/%s/payments/%s/refund", orderID, paymentID), cashier, body)
		var out service.RefundPaymentResult
		_ = json.Unmarshal(res.Body.Bytes(), &out)
		return res, out
	}
	stock := func() int {
		var n int
		_ = migratorDB.GetContext(ctx, &n, `SELECT stock_quantity FROM products WHERE id = $1`, latteID)
		return n
	}
	itemID := func(o domain.Order, product uuid.UUID) uuid.UUID {
		for _, it := range o.Items {
			if it.ProductID != nil && *it.ProductID == product {
				return it.ID
			}
		}
		t.Fatalf("no line for %s", product)
		return uuid.Nil
	}

	t.Run("RefundByLinesWithProportionalTax", func(t *testing.T) {
		order := newOrder(map[string]any{"product_id": latteID, "quantity": 2}, map[string]any{"product_id": cakeID, "quantity": 1})
		if order.TotalAmount != 55000 {
			t.Fatalf("subtotal 50.000 + 10 %% tax expected 55.000, got %d", order.TotalAmount)
		}
		paymentID := pay(order.ID, "cash", nil)
		latte, cake := itemID(order, latteID), itemID(order, cakeID)
		before := stock()
		key := "refund-" + uuid.NewString()
		if res, _ := refund(order.ID, paymentID, map[string]any{"lines": []map[string]any{{"order_item_id": latte, "quantity": 1}}}); res.Code != http.StatusUnprocessableEntity ||
			!strings.Contains(res.Body.String(), "idempotency_key") {
			t.Fatalf("line refund without idempotency_key must be 422: %d %s", res.Code, res.Body.String())
		}
		res, out := refund(order.ID, paymentID, map[string]any{"idempotency_key": key, "lines": []map[string]any{{"order_item_id": latte, "quantity": 1}}})
		if res.Code != http.StatusOK || out.RefundAmount != 22000 || out.Payment.Status != domain.PaymentStatusPartiallyRefunded ||
			out.Order.PaymentStatus != domain.OrderPaymentStatusPartiallyRefunded || !out.StockRestored || stock() != before+1 {
			t.Fatalf("one latte (20.000 + 10 %%) = 22.000: %d %+v stock %d->%d", res.Code, out, before, stock())
		}
		res, out = refund(order.ID, paymentID, map[string]any{"idempotency_key": key, "lines": []map[string]any{{"order_item_id": latte, "quantity": 1}}})
		if res.Code != http.StatusOK || !out.Replay || out.Payment.RefundedAmount != 22000 || stock() != before+1 {
			t.Fatalf("a retry with the same key must not refund twice: %+v stock %d", out, stock())
		}
		if res, _ := refund(order.ID, paymentID, map[string]any{"idempotency_key": uuid.NewString(), "lines": []map[string]any{{"order_item_id": latte, "quantity": 2}}}); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("only one latte is left to refund: %d %s", res.Code, res.Body.String())
		}
		res, out = refund(order.ID, paymentID, map[string]any{"idempotency_key": uuid.NewString(),
			"lines": []map[string]any{{"order_item_id": latte, "quantity": 1}, {"order_item_id": cake, "quantity": 1}}})
		if res.Code != http.StatusOK || out.RefundAmount != 33000 || out.Payment.Status != domain.PaymentStatusRefunded ||
			out.Order.PaymentStatus != domain.OrderPaymentStatusRefunded || out.Order.StockStatus != domain.OrderStockStatusReleased || stock() != before+2 {
			t.Fatalf("the last lines take what is left (33.000) and release stock: %d %+v stock %d", res.Code, out, stock())
		}
	})

	t.Run("OutletCashAsRefundSource", func(t *testing.T) {
		order := newOrder(map[string]any{"product_id": cakeID, "quantity": 1})
		paymentID := pay(order.ID, "cash", nil)
		if res, _ := refund(order.ID, paymentID, map[string]any{"refund_method": "outlet_cash"}); codeOf(res) != "OUTLET_INSUFFICIENT_FUNDS" {
			t.Fatalf("an empty fund cannot pay a refund: %d %s", res.Code, res.Body.String())
		}
		if _, err := migratorDB.ExecContext(ctx, `INSERT INTO outlet_cash_movements (store_id, movement_type, amount, note, actor_id) VALUES ($1, 'top_up', 50000, 'Modal', $2)`, storeID, ownerID); err != nil {
			t.Fatal(err)
		}
		res, out := refund(order.ID, paymentID, map[string]any{"refund_method": "outlet_cash"})
		if res.Code != http.StatusOK || out.RefundAmount != order.TotalAmount || out.OpenDrawer {
			t.Fatalf("outlet refund: %d %+v", res.Code, out)
		}
		var balance int64
		_ = migratorDB.GetContext(ctx, &balance, `SELECT SUM(CASE WHEN movement_type IN ('top_up','from_drawer') THEN amount ELSE -amount END) FROM outlet_cash_movements WHERE store_id = $1`, storeID)
		if balance != 50000-order.TotalAmount {
			t.Fatalf("fund balance after refund: %d", balance)
		}
	})

	t.Run("HandBackTenderOfPartlyPaidBill", func(t *testing.T) {
		order := newOrder(map[string]any{"product_id": cakeID, "quantity": 2})
		partial := int64(5000)
		paymentID := pay(order.ID, "edc", &partial)
		if res, _ := refund(order.ID, paymentID, map[string]any{"idempotency_key": uuid.NewString(),
			"lines": []map[string]any{{"order_item_id": itemID(order, cakeID), "quantity": 1}}}); codeOf(res) != "REFUND_LINES_NOT_ALLOWED" {
			t.Fatalf("line refunds need a fully paid bill: %d %s", res.Code, res.Body.String())
		}
		res, out := refund(order.ID, paymentID, nil)
		if res.Code != http.StatusOK || out.Order.PaymentStatus != domain.OrderPaymentStatusUnpaid || out.Order.PaidAmount != 0 || out.Payment.Status != domain.PaymentStatusRefunded {
			t.Fatalf("handed-back tender: %d %+v", res.Code, out)
		}
		if res := call("POST", fmt.Sprintf("/pos/orders/%s/void", order.ID), cashier, map[string]any{"reason": "Batal", "pin": "482913"}); res.Code != http.StatusOK {
			t.Fatalf("an unpaid bill can be voided after the hand-back: %d %s", res.Code, res.Body.String())
		}
	})
}
