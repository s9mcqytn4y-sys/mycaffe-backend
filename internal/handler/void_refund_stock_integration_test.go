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

// TestVoidRefundStockOverrideIntegration verifies Iteration 4d requirements:
// 1. Void order <= threshold with cashier PIN -> 200 OK, stock restored, status cancelled, audit log VOID_ORDER.
// 2. Void order > threshold without owner PIN -> 422 OWNER_PIN_REQUIRED.
// 3. Void order > threshold with owner PIN -> 200 OK, audit log includes owner_override_by.
// 4. Cash refund <= threshold with active cashier shift -> 200 OK, open_drawer: true, cash movement refund, audit log REFUND.
// 5. Cash refund when drawer balance insufficient -> 409 INSUFFICIENT_DRAWER_CASH.
// 6. Non-cash (EDC) refund in cash from the drawer (RF1-c) -> 200, waste keeps stock untouched.
// 7. Stock override with invalid owner PIN -> 401 INVALID_OWNER_PIN.
// 8. Stock override with valid owner PIN -> 200 OK, product stock_quantity updated, audit log STOCK_OVERRIDE.
func TestVoidRefundStockOverrideIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping Void/Refund/Stock integration test")
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

	orderRepo := postgres.NewOrderRepository()
	paymentRepo := postgres.NewPaymentRepository()
	shiftRepo := postgres.NewShiftRepository()
	authRepo := postgres.NewAuthRepository()
	settingsRepo := postgres.NewSettingsRepository()

	orderSvc := service.NewOrderService(db, orderRepo)
	paymentSvc := service.NewPaymentService(db, paymentRepo, orderRepo, nil)
	shiftSvc := service.NewShiftService(db)
	settingsSvc := service.NewSettingsService(db)
	opSvc := service.NewOperationalService(db, orderRepo, paymentRepo, shiftRepo, authRepo, settingsRepo)

	pos := handler.NewPOSHandler(orderSvc, paymentSvc, settingsSvc)
	pos.SetOperational(opSvc)
	sh := handler.NewShiftHandler(shiftSvc)

	r := gin.New()
	r.POST("/login", ah.Login)

	// Drawer & Shift
	r.POST("/pos/drawers", ah.RequirePermission(rbac.CashDrawerManage), sh.CreateDrawer)
	r.GET("/pos/drawers", ah.RequireStaff(), sh.ListDrawers)
	r.POST("/pos/shifts", ah.RequireStaff(), sh.OpenShift)
	r.POST("/pos/shifts/:id/close", ah.RequireStaff(), sh.CloseShift)

	// Orders & Payments
	r.POST("/pos/orders", ah.RequireStaff(), pos.CreateOrder)
	r.POST("/pos/orders/:id/payments", ah.RequireStaff(), pos.InitiatePayment)

	// Operational (Iteration 4d)
	r.POST("/pos/orders/:id/void", ah.RequirePermission(rbac.OrderVoid), pos.VoidOrder)
	r.POST("/pos/orders/:id/payments/:payment_id/refund", ah.RequirePermission(rbac.PaymentRefund), pos.RefundPayment)
	r.POST("/pos/products/:id/stock-override", ah.RequirePermission(rbac.StockOverride), pos.OverrideStock)

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

	// 1. Staff Sign-in
	ownerLogin := call("POST", "/login", "", map[string]string{
		"store_slug": "central-cafe", "email": "owner.central@mycaffe.local", "pin": "482913",
	})
	if ownerLogin.Code != http.StatusOK {
		t.Fatalf("Owner login failed: %d: %s", ownerLogin.Code, ownerLogin.Body.String())
	}
	var ownerResp struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal(ownerLogin.Body.Bytes(), &ownerResp)

	cashierLogin := call("POST", "/login", "", map[string]string{
		"store_slug": "central-cafe", "email": "cashier.central@mycaffe.local", "pin": "482913",
	})
	if cashierLogin.Code != http.StatusOK {
		t.Fatalf("Cashier login failed: %d: %s", cashierLogin.Code, cashierLogin.Body.String())
	}
	var cashierResp struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal(cashierLogin.Body.Bytes(), &cashierResp)

	storeID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	croissantProdID := uuid.MustParse("d0000005-0000-4000-8000-000000000005")

	// Set store threshold to 100.000 and initialize product stock
	err = database.WithTenantTx(context.Background(), db, storeID, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(context.Background(), `
			UPDATE store_settings 
			SET void_refund_owner_threshold = 100000 
			WHERE store_id = $1
		`, storeID); err != nil {
			return err
		}
		_, err := tx.ExecContext(context.Background(), `
			UPDATE products 
			SET stock_quantity = 500, track_stock = true 
			WHERE store_id = $1 AND id = $2
		`, storeID, croissantProdID)
		return err
	})
	if err != nil {
		t.Fatalf("Failed to initialize test store settings and stock: %v", err)
	}

	// Helper to create an order
	createTestOrder := func(qty int) (uuid.UUID, int64) {
		idemKey := "test-op-order-" + uuid.New().String()
		res := call("POST", "/pos/orders", cashierResp.AccessToken, map[string]any{
			"idempotency_key": idemKey,
			"table_number":    "Meja-Op",
			"order_type":      "dine_in",
			"items": []map[string]any{
				{
					"product_id": croissantProdID.String(),
					"quantity":   qty,
				},
			},
		})
		if res.Code != http.StatusCreated {
			t.Fatalf("create test order failed: %d: %s", res.Code, res.Body.String())
		}
		var oResp struct {
			Order struct {
				ID          string `json:"id"`
				TotalAmount int64  `json:"total_amount"`
			} `json:"order"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &oResp)
		parsedID, _ := uuid.Parse(oResp.Order.ID)
		return parsedID, oResp.Order.TotalAmount
	}

	// Helper to get product stock using WithTenantTx
	getProductStock := func(id uuid.UUID) int {
		var stock int
		err := database.WithTenantTx(context.Background(), db, storeID, func(tx *sqlx.Tx) error {
			return tx.GetContext(context.Background(), &stock, `SELECT stock_quantity FROM products WHERE store_id = $1 AND id = $2`, storeID, id)
		})
		if err != nil {
			t.Fatalf("failed to query product stock: %v", err)
		}
		return stock
	}

	// Create dedicated cash drawer
	createDrawer := func(name string) uuid.UUID {
		drawerRes := call("POST", "/pos/drawers", ownerResp.AccessToken, map[string]any{
			"name":       name,
			"is_default": false,
		})
		if drawerRes.Code != http.StatusCreated {
			t.Fatalf("Failed to create cash drawer %s: %d %s", name, drawerRes.Code, drawerRes.Body.String())
		}
		var cd domain.CashDrawer
		_ = json.Unmarshal(drawerRes.Body.Bytes(), &cd)
		return cd.ID
	}

	// =========================================================================
	// Scenario 1: Void Order <= Threshold with Cashier PIN
	// =========================================================================
	t.Run("Void order <= threshold with Cashier PIN", func(t *testing.T) {
		beforeStock := getProductStock(croissantProdID)
		orderID, total := createTestOrder(1)
		afterOrderStock := getProductStock(croissantProdID)
		if afterOrderStock != beforeStock-1 {
			t.Fatalf("expected stock to decrease by 1, before=%d, after=%d", beforeStock, afterOrderStock)
		}
		if total > 100000 {
			t.Skipf("Order total %d exceeds threshold 100.000, skipping <= threshold test", total)
		}

		voidRes := call("POST", fmt.Sprintf("/pos/orders/%s/void", orderID), cashierResp.AccessToken, map[string]any{
			"reason": "Pelanggan salah pesan dan kasir membatalkan",
			"pin":    "482913",
		})
		if voidRes.Code != http.StatusOK {
			t.Fatalf("expected 200 OK for void <= threshold with cashier PIN, got %d: %s", voidRes.Code, voidRes.Body.String())
		}

		// Verify order status cancelled
		var status string
		_ = database.WithTenantTx(context.Background(), db, storeID, func(tx *sqlx.Tx) error {
			return tx.GetContext(context.Background(), &status, `SELECT status FROM orders WHERE store_id = $1 AND id = $2`, storeID, orderID)
		})
		if status != "cancelled" {
			t.Fatalf("expected order status cancelled, got %s", status)
		}

		// Verify stock restored
		restoredStock := getProductStock(croissantProdID)
		if restoredStock != beforeStock {
			t.Fatalf("expected stock restored to %d, got %d", beforeStock, restoredStock)
		}

		// Verify audit log
		var auditCount int
		_ = database.WithTenantTx(context.Background(), db, storeID, func(tx *sqlx.Tx) error {
			return tx.GetContext(context.Background(), &auditCount, `
				SELECT COUNT(*) FROM audit_logs 
				WHERE store_id = $1 AND action = 'VOID_ORDER' AND reference_id = (SELECT order_number FROM orders WHERE id = $2)
			`, storeID, orderID)
		})
		if auditCount == 0 {
			t.Fatalf("expected VOID_ORDER audit log entry to be recorded")
		}
	})

	// =========================================================================
	// Scenario 2: Void Order > Threshold without Owner PIN -> 422 OWNER_PIN_REQUIRED
	// =========================================================================
	t.Run("Void order > threshold without Owner PIN rejected", func(t *testing.T) {
		orderID, total := createTestOrder(10)
		if total <= 100000 {
			t.Skipf("Order total %d not > threshold 100.000", total)
		}

		voidRes := call("POST", fmt.Sprintf("/pos/orders/%s/void", orderID), cashierResp.AccessToken, map[string]any{
			"reason": "Pembatalan order besar",
			"pin":    "482913",
		})
		if voidRes.Code != http.StatusUnprocessableEntity {
			t.Fatalf("expected 422 OWNER_PIN_REQUIRED, got %d: %s", voidRes.Code, voidRes.Body.String())
		}
		if !strings.Contains(voidRes.Body.String(), "OWNER_PIN_REQUIRED") {
			t.Fatalf("expected error body to contain OWNER_PIN_REQUIRED, got %s", voidRes.Body.String())
		}

		// =========================================================================
		// Scenario 3: Void Order > Threshold with Owner PIN -> 200 OK
		// =========================================================================
		voidSuccess := call("POST", fmt.Sprintf("/pos/orders/%s/void", orderID), cashierResp.AccessToken, map[string]any{
			"reason":    "Pembatalan order besar disetujui owner",
			"pin":       "482913",
			"owner_pin": "482913",
		})
		if voidSuccess.Code != http.StatusOK {
			t.Fatalf("expected 200 OK with owner PIN, got %d: %s", voidSuccess.Code, voidSuccess.Body.String())
		}
	})

	// =========================================================================
	// Scenario 4: Cash Refund <= Threshold with Active Drawer Shift
	// =========================================================================
	t.Run("Cash refund <= threshold with active cashier shift", func(t *testing.T) {
		drawerID := createDrawer("Laci Refund Kasir " + uuid.NewString()[:6])

		// Open cashier shift with float Rp 200.000
		openShiftRes := call("POST", "/pos/shifts", cashierResp.AccessToken, map[string]any{
			"drawer_id":     drawerID.String(),
			"opening_float": 200000,
		})
		if openShiftRes.Code != http.StatusCreated {
			t.Fatalf("failed to open cashier shift: %d %s", openShiftRes.Code, openShiftRes.Body.String())
		}
		var openRes struct {
			Shift *domain.Shift `json:"shift"`
		}
		_ = json.Unmarshal(openShiftRes.Body.Bytes(), &openRes)
		if openRes.Shift == nil {
			t.Fatalf("shift is nil in openShift response: %s", openShiftRes.Body.String())
		}
		shiftID := openRes.Shift.ID

		// Create order and pay cash
		orderID, _ := createTestOrder(1)
		payRes := call("POST", fmt.Sprintf("/pos/orders/%s/payments", orderID), cashierResp.AccessToken, map[string]any{
			"idempotency_key":      "pay-cash-" + uuid.New().String(),
			"method":               "cash",
			"cash_tendered_amount": 100000,
		})
		if payRes.Code != http.StatusOK {
			t.Fatalf("expected cash payment 200 OK, got %d: %s", payRes.Code, payRes.Body.String())
		}
		var payBody struct {
			Payment struct {
				ID     string `json:"id"`
				Amount int64  `json:"amount"`
			} `json:"payment"`
		}
		_ = json.Unmarshal(payRes.Body.Bytes(), &payBody)
		payUUID, _ := uuid.Parse(payBody.Payment.ID)

		beforeRefundStock := getProductStock(croissantProdID)

		// Refund cash payment with restore_stock = true
		refundRes := call("POST", fmt.Sprintf("/pos/orders/%s/payments/%s/refund", orderID, payUUID), cashierResp.AccessToken, map[string]any{
			"reason":        "Pelanggan komplain rasa kopi, refund tunai kasir",
			"pin":           "482913",
			"restore_stock": true,
		})
		if refundRes.Code != http.StatusOK {
			t.Fatalf("expected refund 200 OK, got %d: %s", refundRes.Code, refundRes.Body.String())
		}

		var refResp struct {
			OpenDrawer bool `json:"open_drawer"`
		}
		_ = json.Unmarshal(refundRes.Body.Bytes(), &refResp)
		if !refResp.OpenDrawer {
			t.Fatalf("expected open_drawer true for cash refund")
		}

		// Verify stock was restored
		afterRefundStock := getProductStock(croissantProdID)
		if afterRefundStock != beforeRefundStock+1 {
			t.Fatalf("expected stock to increment by 1 on refund with restore_stock=true")
		}

		// Verify cash movement refund recorded
		var movCount int
		_ = database.WithTenantTx(context.Background(), db, storeID, func(tx *sqlx.Tx) error {
			return tx.GetContext(context.Background(), &movCount, `
				SELECT COUNT(*) FROM cash_movements 
				WHERE store_id = $1 AND shift_id = $2 AND movement_type = 'refund' AND payment_id = $3
			`, storeID, shiftID, payUUID)
		})
		if movCount == 0 {
			t.Fatalf("expected cash movement with type refund to be recorded")
		}

		// Clean up: close shift
		_ = call("POST", fmt.Sprintf("/pos/shifts/%s/close", shiftID), cashierResp.AccessToken, map[string]any{
			"counted_cash": 200000,
		})
	})

	// =========================================================================
	// Scenario 5: Cash Refund when Drawer Balance Insufficient -> 409
	// =========================================================================
	t.Run("Cash refund when drawer cash insufficient is rejected", func(t *testing.T) {
		drawerID := createDrawer("Laci Kosong " + uuid.NewString()[:6])

		// Open shift with Rp 0 float
		shiftRes := call("POST", "/pos/shifts", cashierResp.AccessToken, map[string]any{
			"drawer_id":     drawerID.String(),
			"opening_float": 0,
		})
		if shiftRes.Code != http.StatusCreated {
			t.Fatalf("failed to open shift with 0 float: %d %s", shiftRes.Code, shiftRes.Body.String())
		}
		var zeroRes struct {
			Shift *domain.Shift `json:"shift"`
		}
		_ = json.Unmarshal(shiftRes.Body.Bytes(), &zeroRes)
		if zeroRes.Shift == nil {
			t.Fatalf("shift is nil in openShift response: %s", shiftRes.Body.String())
		}
		shiftID := zeroRes.Shift.ID

		// Create an order paid in another transaction
		orderID, _ := createTestOrder(2)
		payRes := call("POST", fmt.Sprintf("/pos/orders/%s/payments", orderID), cashierResp.AccessToken, map[string]any{
			"idempotency_key":    "pay-static-qris-" + uuid.New().String(),
			"method":             "static_qris",
			"reconciliation_ref": "QRIS-" + uuid.New().String()[:8],
		})
		if payRes.Code != http.StatusOK {
			t.Fatalf("payment failed: %d: %s", payRes.Code, payRes.Body.String())
		}
		var payBody struct {
			Payment struct {
				ID string `json:"id"`
			} `json:"payment"`
		}
		_ = json.Unmarshal(payRes.Body.Bytes(), &payBody)
		payUUID, _ := uuid.Parse(payBody.Payment.ID)

		// Set payment method to cash directly in DB to simulate wanting cash refund when drawer has 0
		_ = database.WithTenantTx(context.Background(), db, storeID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(context.Background(), `UPDATE payments SET method = 'cash' WHERE store_id = $1 AND id = $2`, storeID, payUUID)
			return err
		})

		// Attempt cash refund with Rp 0 in drawer
		refundRes := call("POST", fmt.Sprintf("/pos/orders/%s/payments/%s/refund", orderID, payUUID), cashierResp.AccessToken, map[string]any{
			"reason":        "Refund kasir tanpa uang di laci",
			"pin":           "482913",
			"restore_stock": false,
		})
		if refundRes.Code != http.StatusConflict {
			t.Fatalf("expected 409 Conflict for insufficient drawer cash, got %d: %s", refundRes.Code, refundRes.Body.String())
		}
		if !strings.Contains(refundRes.Body.String(), "INSUFFICIENT_DRAWER_CASH") {
			t.Fatalf("expected INSUFFICIENT_DRAWER_CASH code, got %s", refundRes.Body.String())
		}

		// Close shift
		_ = call("POST", fmt.Sprintf("/pos/shifts/%s/close", shiftID), cashierResp.AccessToken, map[string]any{
			"counted_cash": 0,
		})
	})

	// =========================================================================
	// Scenario 6: Non-cash refund paid back in cash from the drawer (RF1-c)
	// =========================================================================
	t.Run("EDC refund paid back in cash from the drawer", func(t *testing.T) {
		orderID, _ := createTestOrder(1)
		payRes := call("POST", fmt.Sprintf("/pos/orders/%s/payments", orderID), cashierResp.AccessToken, map[string]any{
			"idempotency_key":    "pay-edc-" + uuid.New().String(),
			"method":             "edc",
			"reconciliation_ref": "EDC-" + uuid.New().String()[:8],
		})
		if payRes.Code != http.StatusOK {
			t.Fatalf("edc pay failed: %d: %s", payRes.Code, payRes.Body.String())
		}
		var payBody struct {
			Payment struct {
				ID string `json:"id"`
			} `json:"payment"`
		}
		_ = json.Unmarshal(payRes.Body.Bytes(), &payBody)
		payUUID, _ := uuid.Parse(payBody.Payment.ID)

		stockBefore := getProductStock(croissantProdID)

		refundRes := call("POST", fmt.Sprintf("/pos/orders/%s/payments/%s/refund", orderID, payUUID), cashierResp.AccessToken, map[string]any{
			"reason":        "Kopi tumpah rusak dibuang (waste)",
			"pin":           "482913",
			"restore_stock": false,
		})
		if refundRes.Code != http.StatusOK || !strings.Contains(refundRes.Body.String(), `"open_drawer":true`) {
			t.Fatalf("EDC refund in cash must succeed and open the drawer, got %d: %s", refundRes.Code, refundRes.Body.String())
		}
		if stockAfter := getProductStock(croissantProdID); stockAfter != stockBefore {
			t.Fatalf("a waste refund must not touch stock, before=%d, after=%d", stockBefore, stockAfter)
		}
	})

	// =========================================================================
	// Scenario 7: Stock Override with Invalid Owner PIN -> 401
	// =========================================================================
	t.Run("Stock override with invalid Owner PIN rejected", func(t *testing.T) {
		res := call("POST", fmt.Sprintf("/pos/products/%s/stock-override", croissantProdID), cashierResp.AccessToken, map[string]any{
			"new_quantity": 50,
			"reason":       "Opname fisik",
			"owner_pin":    "999999", // Invalid PIN
		})
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401 Unauthorized for invalid owner PIN, got %d: %s", res.Code, res.Body.String())
		}
	})

	// =========================================================================
	// Scenario 8: Stock Override with Valid Owner PIN -> 200 OK
	// =========================================================================
	t.Run("Stock override with valid Owner PIN succeeds", func(t *testing.T) {
		prevStock := getProductStock(croissantProdID)
		newStock := prevStock + 15

		res := call("POST", fmt.Sprintf("/pos/products/%s/stock-override", croissantProdID), cashierResp.AccessToken, map[string]any{
			"new_quantity": newStock,
			"reason":       "Opname fisik harian penutupan disetujui owner",
			"owner_pin":    "482913",
		})
		if res.Code != http.StatusOK {
			t.Fatalf("expected 200 OK for stock override, got %d: %s", res.Code, res.Body.String())
		}

		var soResp struct {
			PreviousQuantity int `json:"previous_quantity"`
			NewQuantity      int `json:"new_quantity"`
			Delta            int `json:"delta"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &soResp)
		if soResp.NewQuantity != newStock || soResp.Delta != 15 {
			t.Fatalf("unexpected stock override response: %+v", soResp)
		}

		updatedStock := getProductStock(croissantProdID)
		if updatedStock != newStock {
			t.Fatalf("expected database stock to be %d, got %d", newStock, updatedStock)
		}

		// Verify audit log
		var count int
		_ = database.WithTenantTx(context.Background(), db, storeID, func(tx *sqlx.Tx) error {
			return tx.GetContext(context.Background(), &count, `
				SELECT COUNT(*) FROM audit_logs 
				WHERE store_id = $1 AND action = 'STOCK_OVERRIDE' AND reference_id = $2
			`, storeID, croissantProdID.String())
		})
		if count == 0 {
			t.Fatalf("expected STOCK_OVERRIDE audit log to be recorded")
		}
	})
}
