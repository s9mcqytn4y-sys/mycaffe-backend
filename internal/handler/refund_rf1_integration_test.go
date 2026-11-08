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

// TestIntegrationRefundRF1 covers B4 in a store of its own: RF1-c (cash from the drawer for non-cash
// payments, owner-only manual bank transfer), RF1-e (refund windows per role from store settings),
// the append-only refund ledger and settings validation (400 INVALID_SETTINGS).
func TestIntegrationRefundRF1(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	migratorDSN := strings.TrimSpace(os.Getenv("TEST_MIGRATOR_DATABASE_URL"))
	if dsn == "" || migratorDSN == "" {
		t.Skip("TEST_APP_DATABASE_URL / TEST_MIGRATOR_DATABASE_URL not set; skipping RF1 refund integration test")
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
	slug := "refund-" + suffix
	email := func(name string) string { return name + ".refund." + suffix + "@mycaffe.local" }
	storeID, ownerID, err := service.BootstrapStore(ctx, migratorDB, service.BootstrapStoreInput{
		Slug: slug, StoreName: "Kafe Refund", OwnerName: "Pemilik Refund", OwnerEmail: email("owner"), PIN: "482913",
	})
	if err != nil {
		t.Fatalf("bootstrap store: %v", err)
	}
	if _, err := migratorDB.ExecContext(ctx, `UPDATE stores SET enabled_payment_methods = '["cash","edc","static_qris"]'::jsonb WHERE id = $1`, storeID); err != nil {
		t.Fatal(err)
	}

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

	r := gin.New()
	r.POST("/login", ah.Login)
	r.PATCH("/store/settings", ah.RequirePermission(rbac.StoreSettingsManage), pos.UpdateSettings)
	r.POST("/pos/drawers", ah.RequirePermission(rbac.CashDrawerManage), sh.CreateDrawer)
	r.POST("/pos/shifts", ah.RequireStaff(), sh.OpenShift)
	r.GET("/pos/shifts/:id/x-report", ah.RequireStaff(), sh.GetXReport)
	r.POST("/pos/orders", ah.RequireStaff(), pos.CreateOrder)
	r.POST("/pos/orders/:id/payments", ah.RequireStaff(), pos.InitiatePayment)
	r.POST("/pos/orders/:id/payments/:payment_id/refund", ah.RequirePermission(rbac.PaymentRefund), pos.RefundPayment)

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
	inStore := func(fn func(tx *sqlx.Tx) error) {
		t.Helper()
		if err := database.WithTenantTx(ctx, db, storeID, fn); err != nil {
			t.Fatalf("tenant tx failed: %v", err)
		}
	}
	login := func(who string) string {
		t.Helper()
		res := call("POST", "/login", "", map[string]string{"store_slug": slug, "email": email(who), "pin": "482913"})
		if res.Code != http.StatusOK {
			t.Fatalf("login %s failed: %d: %s", who, res.Code, res.Body.String())
		}
		var body struct {
			AccessToken string `json:"access_token"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &body)
		return body.AccessToken
	}

	productID := uuid.New()
	inStore(func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO users (store_id, email, full_name, role, pin_hash, is_active, pin_set_at)
			SELECT store_id, $2, 'Kasir Refund', 'cashier', pin_hash, true, NOW() FROM users WHERE store_id = $1 AND id = $3`,
			storeID, email("cashier"), ownerID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `WITH p AS (INSERT INTO products (id, store_id, name, base_price, is_available) VALUES ($1, $2, 'Teh', 0, true) RETURNING store_id, id)
			INSERT INTO product_variants (store_id, product_id, name, price, is_default) SELECT store_id, id, 'Reguler', 20000, true FROM p`, productID, storeID)
		return err
	})
	owner, cashier := login("owner"), login("cashier")
	var drawer domain.CashDrawer
	_ = json.Unmarshal(call("POST", "/pos/drawers", owner, map[string]any{"name": "Laci Refund"}).Body.Bytes(), &drawer)
	var opened struct {
		Shift domain.Shift `json:"shift"`
	}
	_ = json.Unmarshal(call("POST", "/pos/shifts", cashier, map[string]any{"drawer_id": drawer.ID, "opening_float": 200000}).Body.Bytes(), &opened)

	// paid creates an order and settles it with method; it returns the order and payment ids.
	paid := func(method string) (uuid.UUID, uuid.UUID, int64) {
		t.Helper()
		res := call("POST", "/pos/orders", cashier, map[string]any{"idempotency_key": "rf1-" + uuid.NewString(), "order_type": "takeaway",
			"items": []map[string]any{{"product_id": productID, "quantity": 1}}})
		var created struct {
			Order domain.Order `json:"order"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &created)
		body := map[string]any{"idempotency_key": "rf1-pay-" + uuid.NewString(), "method": method}
		if method == "cash" {
			body["cash_tendered_amount"] = created.Order.TotalAmount
		} else {
			body["reconciliation_ref"] = "REF-" + uuid.NewString()[:8]
		}
		payRes := call("POST", fmt.Sprintf("/pos/orders/%s/payments", created.Order.ID), cashier, body)
		if payRes.Code != http.StatusOK {
			t.Fatalf("%s payment failed: %d: %s", method, payRes.Code, payRes.Body.String())
		}
		var out struct {
			Payment struct {
				ID uuid.UUID `json:"id"`
			} `json:"payment"`
		}
		_ = json.Unmarshal(payRes.Body.Bytes(), &out)
		return created.Order.ID, out.Payment.ID, created.Order.TotalAmount
	}
	refund := func(token string, orderID, paymentID uuid.UUID, extra map[string]any) *httptest.ResponseRecorder {
		body := map[string]any{"reason": "Komplain", "pin": "482913", "restore_stock": true}
		for k, v := range extra {
			body[k] = v
		}
		return call("POST", fmt.Sprintf("/pos/orders/%s/payments/%s/refund", orderID, paymentID), token, body)
	}
	ledger := func(paymentID uuid.UUID) (method string, movement bool, bank, last4, ref string, approved bool, n int) {
		inStore(func(tx *sqlx.Tx) error {
			return tx.QueryRowxContext(ctx, `SELECT COUNT(*) OVER (), method, cash_movement_id IS NOT NULL, COALESCE(bank_name, ''),
				COALESCE(account_last4, ''), COALESCE(transfer_reference, ''), approved_by IS NOT NULL
				FROM payment_refunds WHERE store_id = $1 AND payment_id = $2`, storeID, paymentID).
				Scan(&n, &method, &movement, &bank, &last4, &ref, &approved)
		})
		return
	}
	backdate := func(paymentID uuid.UUID, days int) {
		t.Helper()
		if _, err := migratorDB.ExecContext(ctx, `UPDATE payments SET paid_at = NOW() - make_interval(days => $2) WHERE id = $1`, paymentID, days); err != nil {
			t.Fatal(err)
		}
	}
	xRefunds := func() int64 {
		var x domain.XReport
		_ = json.Unmarshal(call("GET", fmt.Sprintf("/pos/shifts/%s/x-report", opened.Shift.ID), owner, nil).Body.Bytes(), &x)
		return x.Summary.CashRefunds
	}

	t.Run("CashForNonCashPayment", func(t *testing.T) {
		for _, method := range []string{"edc", "static_qris"} {
			before := xRefunds()
			orderID, paymentID, total := paid(method)
			res := refund(cashier, orderID, paymentID, nil)
			if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"open_drawer":true`) {
				t.Fatalf("%s refund in cash failed: %d: %s", method, res.Code, res.Body.String())
			}
			if after := xRefunds(); after != before+total {
				t.Fatalf("%s refund must leave the drawer: cash refunds %d -> %d (amount %d)", method, before, after, total)
			}
			if m, movement, _, _, _, _, n := ledger(paymentID); n != 1 || m != "cash" || !movement {
				t.Fatalf("%s ledger row wrong: method=%s movement=%v rows=%d", method, m, movement, n)
			}
			var payMethod, status string
			inStore(func(tx *sqlx.Tx) error {
				return tx.QueryRowxContext(ctx, `SELECT method, status FROM payments WHERE store_id = $1 AND id = $2`, storeID, paymentID).Scan(&payMethod, &status)
			})
			if payMethod != method || status != "refunded" {
				t.Fatalf("payment must keep method %s and become refunded, got %s/%s", method, payMethod, status)
			}
		}
	})

	t.Run("BankTransferOwnerOnly", func(t *testing.T) {
		orderID, paymentID, _ := paid("edc")
		transfer := map[string]any{"refund_method": "bank_transfer", "bank_name": "BCA", "account_last4": "4321", "transfer_reference": "TRF-778899"}
		if res := refund(cashier, orderID, paymentID, transfer); res.Code != http.StatusForbidden {
			t.Fatalf("cashier bank transfer must be 403, got %d", res.Code)
		}
		res := refund(owner, orderID, paymentID, map[string]any{"refund_method": "bank_transfer", "account_last4": "1234567890"})
		if res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("missing bank fields must be 422, got %d", res.Code)
		}
		for _, field := range []string{`"bank_name"`, `"account_last4"`, `"transfer_reference"`} {
			if !strings.Contains(res.Body.String(), field) {
				t.Fatalf("422 must name %s: %s", field, res.Body.String())
			}
		}
		if res := refund(owner, orderID, paymentID, map[string]any{"refund_method": "voucher"}); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("unknown refund method must be 422, got %d", res.Code)
		}
		before := xRefunds()
		res = refund(owner, orderID, paymentID, transfer)
		if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"open_drawer":false`) {
			t.Fatalf("owner bank transfer failed: %d: %s", res.Code, res.Body.String())
		}
		if after := xRefunds(); after != before {
			t.Fatalf("a bank transfer must not touch the drawer: %d -> %d", before, after)
		}
		if m, movement, bank, last4, ref, _, n := ledger(paymentID); n != 1 || m != "bank_transfer" || movement || bank != "BCA" || last4 != "4321" || ref != "TRF-778899" {
			t.Fatalf("bank transfer ledger wrong: %s %v %s %s %s rows=%d", m, movement, bank, last4, ref, n)
		}
		if res := refund(owner, orderID, paymentID, transfer); res.Code != http.StatusConflict {
			t.Fatalf("second refund of the same payment must be 409, got %d", res.Code)
		}
	})

	t.Run("RefundWindow", func(t *testing.T) {
		// Yesterday: past the cashier window (0 days) but within the owner's (7).
		orderID, paymentID, _ := paid("cash")
		backdate(paymentID, 1)
		if res := refund(cashier, orderID, paymentID, nil); res.Code != http.StatusUnprocessableEntity || !strings.Contains(res.Body.String(), "OWNER_PIN_REQUIRED") {
			t.Fatalf("cashier past the window must need the owner PIN, got %d: %s", res.Code, res.Body.String())
		}
		if res := refund(cashier, orderID, paymentID, map[string]any{"owner_pin": "482913"}); res.Code != http.StatusOK {
			t.Fatalf("cashier with owner PIN must refund within the owner window, got %d: %s", res.Code, res.Body.String())
		}
		if _, _, _, _, _, approved, _ := ledger(paymentID); !approved {
			t.Fatal("the ledger must record the approving owner")
		}

		// Eight days ago: outside every window, even for the owner.
		orderID, paymentID, _ = paid("edc")
		backdate(paymentID, 8)
		if res := refund(owner, orderID, paymentID, map[string]any{"refund_method": "bank_transfer", "bank_name": "BRI", "account_last4": "0001", "transfer_reference": "X"}); res.Code != http.StatusConflict ||
			!strings.Contains(res.Body.String(), "REFUND_WINDOW_EXCEEDED") {
			t.Fatalf("payment older than the owner window must be 409, got %d: %s", res.Code, res.Body.String())
		}

		// A custom cashier window of 2 days lets the cashier refund yesterday's payment alone.
		if res := call("PATCH", "/store/settings", owner, map[string]any{"refund_window_cashier_days": 2, "refund_window_owner_days": 7}); res.Code != http.StatusOK {
			t.Fatalf("settings update failed: %d: %s", res.Code, res.Body.String())
		}
		orderID, paymentID, _ = paid("cash")
		backdate(paymentID, 1)
		if res := refund(cashier, orderID, paymentID, nil); res.Code != http.StatusOK {
			t.Fatalf("custom cashier window must allow yesterday, got %d: %s", res.Code, res.Body.String())
		}
	})

	t.Run("SettingsRefundWindowValidation", func(t *testing.T) {
		for _, body := range []map[string]any{
			{"refund_window_cashier_days": 5, "refund_window_owner_days": 3},
			{"refund_window_owner_days": 31},
			{"refund_window_cashier_days": -1},
		} {
			// The settings endpoint answers 400 INVALID_SETTINGS for out-of-range policies.
			if res := call("PATCH", "/store/settings", owner, body); res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "INVALID_SETTINGS") {
				t.Fatalf("%v must be 400 INVALID_SETTINGS, got %d: %s", body, res.Code, res.Body.String())
			}
		}
		if res := call("PATCH", "/store/settings", cashier, map[string]any{"refund_window_cashier_days": 3}); res.Code != http.StatusForbidden {
			t.Fatalf("cashier must not change refund windows, got %d", res.Code)
		}
	})
}
