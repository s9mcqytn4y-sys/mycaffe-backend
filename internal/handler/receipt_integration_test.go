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

// TestIntegrationReceipts covers B1: receipts read only snapshots (a later catalog price change does
// not alter them), by-item refund and void receipts, ESC/POS output with a QR digital link, the public
// 90-day link (valid, tampered, expired, other store) and RBAC.
func TestIntegrationReceipts(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	migratorDSN := strings.TrimSpace(os.Getenv("TEST_MIGRATOR_DATABASE_URL"))
	if dsn == "" || migratorDSN == "" {
		t.Skip("TEST_APP_DATABASE_URL / TEST_MIGRATOR_DATABASE_URL not set; skipping receipt integration test")
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

	gin.SetMode(gin.TestMode)
	authSvc := service.NewAuthService(db, postgres.NewAuthRepository(), "integration-test-jwt-secret-at-least-32-chars", service.AuthPolicy{
		AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 12 * time.Hour, SessionIdleTimeout: 2 * time.Hour,
		MaxSessions: 3, PINMaxFailures: 5, PINLockout: 15 * time.Minute, MaxConcurrentHashes: 8,
	})
	ah := handler.NewAuthHandler(authSvc)
	orderRepo := postgres.NewOrderRepository()
	paymentRepo := postgres.NewPaymentRepository()
	pos := handler.NewPOSHandler(service.NewOrderService(db, orderRepo), service.NewPaymentService(db, paymentRepo, orderRepo, nil), service.NewSettingsService(db))
	pos.SetOperational(service.NewOperationalService(db, orderRepo, paymentRepo, nil, nil, nil))
	sh := handler.NewShiftHandler(service.NewShiftService(db))
	th := handler.NewTabHandler(service.NewTabService(db, postgres.NewTabRepository(), orderRepo))
	rh := handler.NewReceiptHandler(service.NewReceiptService(db, "https://pos.example.test"))

	r := gin.New()
	r.POST("/login", ah.Login)
	r.POST("/pos/drawers", ah.RequirePermission(rbac.CashDrawerManage), sh.CreateDrawer)
	r.POST("/pos/shifts", ah.RequireStaff(), sh.OpenShift)
	r.POST("/pos/shifts/:id/force-close", ah.RequireStaff(), sh.ForceCloseShift)
	r.POST("/pos/orders", ah.RequireStaff(), pos.CreateOrder)
	r.POST("/pos/orders/:id/payments", ah.RequireStaff(), pos.InitiatePayment)
	r.POST("/pos/orders/:id/void", ah.RequirePermission(rbac.OrderVoid), pos.VoidOrder)
	r.POST("/pos/orders/:id/payments/:payment_id/refund", ah.RequirePermission(rbac.PaymentRefund), pos.RefundPayment)
	r.POST("/pos/tabs", ah.RequirePermission(rbac.TabManage), th.OpenTab)
	r.POST("/pos/tabs/:id/close", ah.RequirePermission(rbac.TabManage), th.CloseTab)
	r.GET("/pos/orders/:id/receipt", ah.RequirePermission(rbac.OrderReadStore), rh.GetReceipt)
	r.POST("/pos/orders/:id/receipt-link", ah.RequirePermission(rbac.OrderReadStore), rh.IssueLink)
	r.GET("/public/receipts/:token", rh.PublicReceipt)

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
	inStore := func(storeID uuid.UUID, fn func(tx *sqlx.Tx) error) {
		t.Helper()
		if err := database.WithTenantTx(ctx, db, storeID, fn); err != nil {
			t.Fatalf("tenant tx failed: %v", err)
		}
	}
	login := func(email string) string {
		t.Helper()
		res := call("POST", "/login", "", map[string]string{"store_slug": "central-cafe", "email": email, "pin": "482913"})
		if res.Code != http.StatusOK {
			t.Fatalf("login %s failed: %d: %s", email, res.Code, res.Body.String())
		}
		var body struct {
			AccessToken string `json:"access_token"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &body)
		return body.AccessToken
	}
	decode := func(res *httptest.ResponseRecorder) service.Receipt {
		t.Helper()
		if res.Code != http.StatusOK {
			t.Fatalf("receipt failed: %d: %s", res.Code, res.Body.String())
		}
		var rc service.Receipt
		_ = json.Unmarshal(res.Body.Bytes(), &rc)
		return rc
	}

	// A dedicated cashier with its own shift keeps this test independent of other tests' shifts.
	storeID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	suffix := uuid.NewString()[:8]
	cashierEmail := "receipt." + suffix + "@mycaffe.local"
	productID, optionID, groupID := uuid.New(), uuid.New(), uuid.New()
	inStore(storeID, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO users (store_id, email, full_name, role, pin_hash, is_active, pin_set_at)
			SELECT store_id, $2, 'Kasir Struk', 'cashier', pin_hash, true, NOW() FROM users WHERE store_id = $1 AND email = 'cashier.central@mycaffe.local'`,
			storeID, cashierEmail); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `WITH p AS (INSERT INTO products (id, store_id, name, base_price, is_available) VALUES ($1, $2, $3, 0, true) RETURNING store_id, id)
			INSERT INTO product_variants (store_id, product_id, name, price, is_default) SELECT store_id, id, 'Besar', 15000, true FROM p`,
			productID, storeID, "Struk Latte "+suffix); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO modifier_groups (id, store_id, name, selection_type, is_required, min_select, max_select)
			VALUES ($1, $2, $3, 'single', false, 0, 1)`, groupID, storeID, "Struk Shot "+suffix); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO modifier_options (id, store_id, group_id, name, price_delta) VALUES ($1, $2, $3, 'Extra shot', 5000)`,
			optionID, storeID, groupID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO product_modifier_groups (store_id, product_id, group_id) VALUES ($1, $2, $3)`, storeID, productID, groupID)
		return err
	})
	cashier := login(cashierEmail)
	owner := login("owner.central@mycaffe.local")
	var drawer domain.CashDrawer
	_ = json.Unmarshal(call("POST", "/pos/drawers", owner, map[string]any{"name": "Laci Struk " + suffix}).Body.Bytes(), &drawer)
	shiftRes := call("POST", "/pos/shifts", cashier, map[string]any{"drawer_id": drawer.ID, "opening_float": 500000})
	if shiftRes.Code != http.StatusCreated {
		t.Fatalf("open shift failed: %d: %s", shiftRes.Code, shiftRes.Body.String())
	}
	var opened struct {
		Shift domain.Shift `json:"shift"`
	}
	_ = json.Unmarshal(shiftRes.Body.Bytes(), &opened)
	t.Cleanup(func() {
		call("POST", fmt.Sprintf("/pos/shifts/%s/force-close", opened.Shift.ID), owner, map[string]any{"reason": "Akhir test struk"})
	})

	line := func(qty int, withOption bool) map[string]any {
		item := map[string]any{"product_id": productID, "quantity": qty}
		if withOption {
			item["modifiers"] = []map[string]any{{"option_id": optionID, "quantity": 1}}
		}
		return item
	}
	createOrder := func(items ...map[string]any) domain.Order {
		t.Helper()
		res := call("POST", "/pos/orders", cashier, map[string]any{"idempotency_key": "struk-" + uuid.NewString(), "order_type": "takeaway", "items": items})
		if res.Code != http.StatusCreated {
			t.Fatalf("create order failed: %d: %s", res.Code, res.Body.String())
		}
		var body struct {
			Order domain.Order `json:"order"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &body)
		return body.Order
	}
	pay := func(orderID uuid.UUID, splitID *uuid.UUID, amount, tendered int64) uuid.UUID {
		t.Helper()
		body := map[string]any{"idempotency_key": "struk-pay-" + uuid.NewString(), "method": "cash", "cash_tendered_amount": tendered}
		if splitID != nil {
			body["split_id"] = splitID
		}
		res := call("POST", fmt.Sprintf("/pos/orders/%s/payments", orderID), cashier, body)
		if res.Code != http.StatusOK {
			t.Fatalf("cash payment of %d failed: %d: %s", amount, res.Code, res.Body.String())
		}
		var out struct {
			Payment struct {
				ID uuid.UUID `json:"id"`
			} `json:"payment"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &out)
		return out.Payment.ID
	}
	receiptPath := func(orderID uuid.UUID, query string) string {
		return fmt.Sprintf("/pos/orders/%s/receipt?%s", orderID, query)
	}

	t.Run("SaleFromSnapshot", func(t *testing.T) {
		order := createOrder(line(2, true))
		pay(order.ID, nil, order.TotalAmount, order.TotalAmount+10000)
		// A later price change must not reach an issued receipt.
		inStore(storeID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE product_variants SET price = 99000 WHERE store_id = $1 AND product_id = $2`, storeID, productID)
			return err
		})
		rc := decode(call("GET", receiptPath(order.ID, "kind=sale"), cashier, nil))
		if len(rc.Lines) != 1 || rc.Lines[0].UnitPrice != 20000 || rc.Lines[0].Amount != 40000 || rc.Lines[0].Variant != "Besar" ||
			len(rc.Lines[0].Options) != 1 || rc.Lines[0].Options[0] != "Extra shot" {
			t.Fatalf("line must come from the snapshot (15000 + 5000 shot), got %+v", rc.Lines)
		}
		if rc.Total != order.TotalAmount || rc.Subtotal != order.SubtotalAmount || rc.Tax != order.TaxAmount || rc.Service != order.ServiceChargeAmount {
			t.Fatalf("totals must equal the order snapshot: receipt %+v order %+v", rc, order)
		}
		if len(rc.Payments) != 1 || rc.Payments[0].CashTendered != order.TotalAmount+10000 || rc.Payments[0].Change != 10000 {
			t.Fatalf("cash tender and change missing: %+v", rc.Payments)
		}
		if rc.StaffName != "Kasir Struk" || rc.LocalTime == "" || rc.Store.Name == "" {
			t.Fatalf("header incomplete: %+v", rc)
		}

		var before, after int
		count := func(n *int) {
			inStore(storeID, func(tx *sqlx.Tx) error {
				return tx.GetContext(ctx, n, `SELECT COUNT(*) FROM receipt_tokens WHERE store_id = $1 AND order_id = $2`, storeID, order.ID)
			})
		}
		count(&before)
		res := call("GET", receiptPath(order.ID, "kind=sale&format=escpos&width=80"), cashier, nil)
		count(&after)
		if res.Code != http.StatusOK || !bytes.HasPrefix(res.Body.Bytes(), []byte{0x1b, '@'}) ||
			!bytes.Contains(res.Body.Bytes(), []byte("https://pos.example.test/receipts/")) || !bytes.Contains(res.Body.Bytes(), []byte("Rp40.000")) {
			t.Fatalf("ESC/POS sale receipt with QR link expected, got %d", res.Code)
		}
		if after != before+1 {
			t.Fatalf("printing a sale receipt must issue one digital link, tokens %d -> %d", before, after)
		}
		if res := call("GET", receiptPath(order.ID, "kind=bogus&width=76"), cashier, nil); res.Code != http.StatusUnprocessableEntity ||
			!strings.Contains(res.Body.String(), `"kind"`) || !strings.Contains(res.Body.String(), `"width"`) {
			t.Fatalf("invalid kind/width must be 422 with fields, got %d: %s", res.Code, res.Body.String())
		}
	})

	t.Run("ByItemRefundAndVoid", func(t *testing.T) {
		res := call("POST", "/pos/tabs", cashier, map[string]any{
			"idempotency_key": "struk-tab-" + uuid.NewString(), "table_number": "Meja-Struk",
			"initial_items": []map[string]any{line(1, true), line(1, false)},
		})
		if res.Code != http.StatusCreated {
			t.Fatalf("open tab failed: %d: %s", res.Code, res.Body.String())
		}
		var tab struct {
			Order domain.Order `json:"order"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &tab)
		var withShot, plain uuid.UUID
		inStore(storeID, func(tx *sqlx.Tx) error {
			if err := tx.GetContext(ctx, &withShot, `SELECT oi.id FROM order_items oi WHERE oi.store_id = $1 AND oi.order_id = $2
				AND EXISTS (SELECT 1 FROM order_item_options o WHERE o.store_id = oi.store_id AND o.order_item_id = oi.id)`, storeID, tab.Order.ID); err != nil {
				return err
			}
			return tx.GetContext(ctx, &plain, `SELECT id FROM order_items WHERE store_id = $1 AND order_id = $2 AND id <> $3`, storeID, tab.Order.ID, withShot)
		})
		closeRes := call("POST", fmt.Sprintf("/pos/tabs/%s/close", tab.Order.ID), cashier, map[string]any{"strategy": "by_item", "splits": []map[string]any{
			{"label": "Tamu 1", "items": []map[string]any{{"order_item_id": withShot, "quantity": 1}}},
			{"label": "Tamu 2", "items": []map[string]any{{"order_item_id": plain, "quantity": 1}}},
		}})
		if closeRes.Code != http.StatusOK {
			t.Fatalf("close tab failed: %d: %s", closeRes.Code, closeRes.Body.String())
		}
		var closed struct {
			Splits []domain.PaymentSplit `json:"splits"`
		}
		_ = json.Unmarshal(closeRes.Body.Bytes(), &closed)
		first := pay(tab.Order.ID, &closed.Splits[0].ID, closed.Splits[0].TotalAmount, closed.Splits[0].TotalAmount)
		pay(tab.Order.ID, &closed.Splits[1].ID, closed.Splits[1].TotalAmount, closed.Splits[1].TotalAmount)

		sale := decode(call("GET", receiptPath(tab.Order.ID, "kind=sale"), cashier, nil))
		if len(sale.Payments) != 2 || sale.Payments[0].SplitLabel != "Tamu 1" || sale.Payments[1].SplitLabel != "Tamu 2" {
			t.Fatalf("sale receipt must list both split payments in order: %+v", sale.Payments)
		}
		if res := call("GET", receiptPath(tab.Order.ID, "kind=refund&payment_id="+first.String()), cashier, nil); res.Code != http.StatusConflict {
			t.Fatalf("refund receipt of an unrefunded payment must be 409, got %d", res.Code)
		}
		refund := call("POST", fmt.Sprintf("/pos/orders/%s/payments/%s/refund", tab.Order.ID, first), cashier,
			map[string]any{"reason": "Kopi tumpah", "pin": "482913", "owner_pin": "482913", "restore_stock": false})
		if refund.Code != http.StatusOK {
			t.Fatalf("refund failed: %d: %s", refund.Code, refund.Body.String())
		}
		rc := decode(call("GET", receiptPath(tab.Order.ID, "kind=refund&payment_id="+first.String()), cashier, nil))
		if rc.Refund == nil || rc.Refund.Amount != closed.Splits[0].TotalAmount || rc.Refund.Reason != "Kopi tumpah" || rc.Refund.StockReturned {
			t.Fatalf("refund block wrong: %+v", rc.Refund)
		}
		if len(rc.Lines) != 1 || rc.Lines[0].Quantity != 1 || len(rc.Lines[0].Options) != 1 {
			t.Fatalf("by-item refund receipt must list only the refunded split's line: %+v", rc.Lines)
		}

		order := createOrder(line(1, false))
		if res := call("GET", receiptPath(order.ID, "kind=void"), cashier, nil); res.Code != http.StatusConflict {
			t.Fatalf("void receipt of a live order must be 409, got %d", res.Code)
		}
		if res := call("POST", fmt.Sprintf("/pos/orders/%s/void", order.ID), cashier, map[string]any{"reason": "Salah input", "pin": "482913", "owner_pin": "482913"}); res.Code != http.StatusOK {
			t.Fatalf("void failed: %d: %s", res.Code, res.Body.String())
		}
		void := decode(call("GET", receiptPath(order.ID, "kind=void"), cashier, nil))
		if void.Kind != service.ReceiptKindVoid || void.VoidReason != "Salah input" || void.Status != "cancelled" {
			t.Fatalf("void receipt wrong: %+v", void)
		}
	})

	t.Run("PublicLink", func(t *testing.T) {
		order := createOrder(line(1, false))
		pay(order.ID, nil, order.TotalAmount, order.TotalAmount)
		res := call("POST", fmt.Sprintf("/pos/orders/%s/receipt-link", order.ID), cashier, nil)
		if res.Code != http.StatusCreated {
			t.Fatalf("issue link failed: %d: %s", res.Code, res.Body.String())
		}
		var link service.ReceiptLink
		_ = json.Unmarshal(res.Body.Bytes(), &link)
		if !strings.HasPrefix(link.URL, "https://pos.example.test/receipts/") || link.ExpiresAt.Sub(time.Now()) < 89*24*time.Hour {
			t.Fatalf("link must be a 90-day URL: %+v", link)
		}
		pub := call("GET", "/public/receipts/"+link.Token, "", nil)
		rc := decode(pub)
		if rc.Total != order.TotalAmount || rc.StaffName != "" || strings.Contains(pub.Body.String(), "customer") {
			t.Fatalf("public receipt must carry totals without staff or customer data: %s", pub.Body.String())
		}
		parts := strings.SplitN(link.Token, ".", 3)
		for name, token := range map[string]string{
			"tampered":    parts[0] + "." + parts[1] + "." + strings.Repeat("A", len(parts[2])),
			"other store": "22222222-2222-4222-8222-222222222222." + parts[1] + "." + parts[2],
			"malformed":   "not-a-token",
		} {
			if res := call("GET", "/public/receipts/"+token, "", nil); res.Code != http.StatusNotFound {
				t.Fatalf("%s token must be 404, got %d", name, res.Code)
			}
		}
		if _, err := migratorDB.ExecContext(ctx, `UPDATE receipt_tokens SET created_at = NOW() - INTERVAL '91 days', expires_at = NOW() - INTERVAL '1 day' WHERE id = $1`, parts[1]); err != nil {
			t.Fatal(err)
		}
		if res := call("GET", "/public/receipts/"+link.Token, "", nil); res.Code != http.StatusNotFound {
			t.Fatalf("expired token must be 404, got %d", res.Code)
		}
		if res := call("POST", fmt.Sprintf("/pos/orders/%s/receipt-link", uuid.New()), cashier, nil); res.Code != http.StatusNotFound {
			t.Fatalf("link for an unknown order must be 404, got %d", res.Code)
		}
	})

	t.Run("RBAC", func(t *testing.T) {
		order := createOrder(line(1, false))
		barista := login("barista.central@mycaffe.local")
		if res := call("GET", receiptPath(order.ID, "kind=sale"), barista, nil); res.Code != http.StatusForbidden {
			t.Fatalf("barista must get 403 on receipts, got %d", res.Code)
		}
		if res := call("POST", fmt.Sprintf("/pos/orders/%s/receipt-link", order.ID), barista, nil); res.Code != http.StatusForbidden {
			t.Fatalf("barista must get 403 on receipt links, got %d", res.Code)
		}
	})
}
