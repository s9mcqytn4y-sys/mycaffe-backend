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

// TestPOSCashTenderIntegration tests Iteration 4b-2: Cash Tender in POS,
// binding active shift to orders, recording cash_movements ('sale'),
// returning open_drawer flag, and enforcing fail-closed 409 NO_OPEN_SHIFT.
func TestPOSCashTenderIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping POS cash tender integration test")
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

	orderSvc := service.NewOrderService(db, orderRepo)
	paymentSvc := service.NewPaymentService(db, paymentRepo, orderRepo, nil, service.WithShiftRepository(shiftRepo))
	settingsSvc := service.NewSettingsService(db)
	shiftSvc := service.NewShiftService(db)

	pos := handler.NewPOSHandler(orderSvc, paymentSvc, settingsSvc)
	sh := handler.NewShiftHandler(shiftSvc)

	r := gin.New()
	r.POST("/login", ah.Login)

	// Drawer & Shift routes
	r.POST("/pos/drawers", ah.RequirePermission(rbac.CashDrawerManage), sh.CreateDrawer)
	r.POST("/pos/shifts", ah.RequirePermission(rbac.ShiftOpen), sh.OpenShift)
	r.GET("/pos/shifts/:id/x-report", ah.RequireStaff(), sh.GetXReport)

	// POS Order & Payment routes
	r.POST("/pos/orders", ah.RequirePermission(rbac.OrderPOSCreate), pos.CreateOrder)
	r.POST("/pos/orders/:id/payments", ah.RequireStaff(), pos.InitiatePayment)

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

	ownerToken := login("abdulaziz543333@gmail.com")
	cashierToken := login("cashier.central@mycaffe.local")

	// 1. Create a dedicated drawer for this integration test
	drawerName := "Laci POS Tender " + uuid.NewString()[:8]
	wDrawer := call(http.MethodPost, "/pos/drawers", ownerToken, map[string]any{
		"name":       drawerName,
		"is_default": false,
	})
	if wDrawer.Code != http.StatusCreated {
		t.Fatalf("create drawer failed: %d %s", wDrawer.Code, wDrawer.Body.String())
	}
	var drawer domain.CashDrawer
	_ = json.Unmarshal(wDrawer.Body.Bytes(), &drawer)

	// 2. Create POS order for classic iced latte (24,000 + 5% service (1,200) + 10% tax (2,520) = 27,720 IDR)
	latteProdID := "d0000001-0000-4000-8000-000000000001"
	createOrder := func(tableNum string) string {
		wOrder := call(http.MethodPost, "/pos/orders", cashierToken, map[string]any{
			"idempotency_key": "pos-cash-ord-" + uuid.NewString()[:8],
			"table_number":    tableNum,
			"customer_name":   "Budi Cashier POS",
			"order_type":      "dine_in",
			"items": []map[string]any{
				{"product_id": latteProdID, "quantity": 1},
			},
		})
		if wOrder.Code != http.StatusCreated {
			t.Fatalf("create order failed: %d %s", wOrder.Code, wOrder.Body.String())
		}
		var ordRes struct {
			Order domain.Order `json:"order"`
		}
		_ = json.Unmarshal(wOrder.Body.Bytes(), &ordRes)
		return ordRes.Order.ID.String()
	}

	orderID1 := createOrder("Meja 10")

	// 3. Strict Fail-Closed: Attempting cash tender on this drawer before opening shift must return 409 NO_OPEN_SHIFT
	t.Run("FailClosed_NoOpenShift_Returns409", func(t *testing.T) {
		wPay := call(http.MethodPost, "/pos/orders/"+orderID1+"/payments", cashierToken, map[string]any{
			"idempotency_key":      "pay-noshift-" + uuid.NewString()[:8],
			"method":               "cash",
			"cash_tendered_amount": 50000,
			"drawer_id":            drawer.ID.String(),
		})
		if wPay.Code != http.StatusConflict {
			t.Fatalf("expected 409 Conflict for cash tender without open shift, got %d %s", wPay.Code, wPay.Body.String())
		}
		if !strings.Contains(wPay.Body.String(), "NO_OPEN_SHIFT") {
			t.Errorf("expected error code NO_OPEN_SHIFT, got %s", wPay.Body.String())
		}
	})

	// 4. Open shift on drawer with float 150,000
	wOpenShift := call(http.MethodPost, "/pos/shifts", cashierToken, map[string]any{
		"drawer_id":      drawer.ID.String(),
		"starting_float": 150000,
	})
	if wOpenShift.Code != http.StatusCreated {
		t.Fatalf("open shift failed: %d %s", wOpenShift.Code, wOpenShift.Body.String())
	}
	var shift domain.Shift
	_ = json.Unmarshal(wOpenShift.Body.Bytes(), &shift)

	// 5. Insufficient cash tendered returns 400 INVALID_PAYMENT
	t.Run("InsufficientCashTendered_Returns400", func(t *testing.T) {
		wPay := call(http.MethodPost, "/pos/orders/"+orderID1+"/payments", cashierToken, map[string]any{
			"idempotency_key":      "pay-under-" + uuid.NewString()[:8],
			"method":               "cash",
			"cash_tendered_amount": 10000, // order total is 27720
			"drawer_id":            drawer.ID.String(),
		})
		if wPay.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request for under-tender, got %d %s", wPay.Code, wPay.Body.String())
		}
	})

	// 6. Successful cash tender: cash_tendered 50,000 IDR -> change 22,280 IDR, open_drawer true
	payIdemKey := "pay-cash-ok-" + uuid.NewString()[:8]
	t.Run("SuccessfulCashTender_RecordsSale_BindsShift_ReturnsOpenDrawer", func(t *testing.T) {
		wPay := call(http.MethodPost, "/pos/orders/"+orderID1+"/payments", cashierToken, map[string]any{
			"idempotency_key":      payIdemKey,
			"method":               "cash",
			"cash_tendered_amount": 50000,
			"drawer_id":            drawer.ID.String(),
		})
		if wPay.Code != http.StatusOK {
			t.Fatalf("cash tender failed: %d %s", wPay.Code, wPay.Body.String())
		}

		var res struct {
			OpenDrawer bool                `json:"open_drawer"`
			Payment    domain.Payment      `json:"payment"`
			Order      domain.Order        `json:"order"`
			Split      domain.PaymentSplit `json:"split"`
		}
		_ = json.Unmarshal(wPay.Body.Bytes(), &res)

		if !res.OpenDrawer {
			t.Errorf("expected open_drawer=true on cash tender")
		}
		if res.Payment.Method != domain.PaymentMethodCash || res.Payment.Status != domain.PaymentStatusPaid {
			t.Fatalf("unexpected payment status: %+v", res.Payment)
		}
		if res.Payment.CashTenderedAmount != 50000 || res.Payment.ChangeGivenAmount != (50000-res.Order.TotalAmount) {
			t.Fatalf("unexpected tender calculation: tendered=%d change=%d total=%d",
				res.Payment.CashTenderedAmount, res.Payment.ChangeGivenAmount, res.Order.TotalAmount)
		}
		if res.Order.ShiftID == nil || *res.Order.ShiftID != shift.ID {
			t.Fatalf("expected order.ShiftID=%s, got %v", shift.ID, res.Order.ShiftID)
		}

		// Verify cash movement in database
		var movement domain.CashMovement
		query := `
			SELECT id, store_id, shift_id, movement_type, amount, actor_id, order_id, payment_id
			FROM cash_movements
			WHERE store_id = $1 AND shift_id = $2 AND movement_type = 'sale' AND payment_id = $3
		`
		err := db.Get(&movement, query, shift.StoreID, shift.ID, res.Payment.ID)
		if err != nil {
			t.Fatalf("failed to query cash_movements for payment: %v", err)
		}
		if movement.Amount != res.Order.TotalAmount {
			t.Errorf("expected cash movement amount %d, got %d", res.Order.TotalAmount, movement.Amount)
		}
	})

	// 7. Idempotent replay of the exact cash tender request returns open_drawer: true
	t.Run("IdempotentReplay_ReturnsOpenDrawer", func(t *testing.T) {
		wReplay := call(http.MethodPost, "/pos/orders/"+orderID1+"/payments", cashierToken, map[string]any{
			"idempotency_key":      payIdemKey,
			"method":               "cash",
			"cash_tendered_amount": 50000,
			"drawer_id":            drawer.ID.String(),
		})
		if wReplay.Code != http.StatusOK {
			t.Fatalf("idempotent replay failed: %d %s", wReplay.Code, wReplay.Body.String())
		}
		var replayRes struct {
			IsIdempotentReplay bool `json:"is_idempotent_replay"`
			OpenDrawer         bool `json:"open_drawer"`
		}
		_ = json.Unmarshal(wReplay.Body.Bytes(), &replayRes)
		if !replayRes.IsIdempotentReplay {
			t.Errorf("expected is_idempotent_replay=true")
		}
		if !replayRes.OpenDrawer {
			t.Errorf("expected open_drawer=true on replay")
		}
	})

	// 8. Static QRIS settlement binds shift_id but does NOT create a cash movement
	t.Run("StaticQRISTender_BindsShift_NoCashMovement", func(t *testing.T) {
		orderID2 := createOrder("Meja 11")
		wStatic := call(http.MethodPost, "/pos/orders/"+orderID2+"/payments", cashierToken, map[string]any{
			"idempotency_key":    "pay-static-" + uuid.NewString()[:8],
			"method":             "static_qris",
			"reconciliation_ref": "RRN-POS-STATIC-001",
			"drawer_id":          drawer.ID.String(),
		})
		if wStatic.Code != http.StatusOK {
			t.Fatalf("static qris tender failed: %d %s", wStatic.Code, wStatic.Body.String())
		}
		var staticRes struct {
			OpenDrawer bool           `json:"open_drawer"`
			Order      domain.Order   `json:"order"`
			Payment    domain.Payment `json:"payment"`
		}
		_ = json.Unmarshal(wStatic.Body.Bytes(), &staticRes)
		if staticRes.OpenDrawer {
			t.Errorf("expected open_drawer=false for non-cash static QRIS")
		}
		if staticRes.Order.ShiftID == nil || *staticRes.Order.ShiftID != shift.ID {
			t.Errorf("expected order to bind to shift %s, got %v", shift.ID, staticRes.Order.ShiftID)
		}

		// Ensure NO cash_movements row exists for static QRIS
		var staticMvCount int
		err := db.Get(&staticMvCount, `SELECT COUNT(*) FROM cash_movements WHERE payment_id = $1`, staticRes.Payment.ID)
		if err != nil {
			t.Fatalf("query cash_movements failed: %v", err)
		}
		if staticMvCount != 0 {
			t.Errorf("static QRIS must not create physical cash_movements, got %d", staticMvCount)
		}
	})

	// 9. Owner inspects X-Report: expected_cash correctly includes starting float (150,000) + cash sale (27,720) = 177,720 IDR
	t.Run("XReport_ReflectsCashSaleInExpectedCash", func(t *testing.T) {
		wX := call(http.MethodGet, "/pos/shifts/"+shift.ID.String()+"/x-report", ownerToken, nil)
		if wX.Code != http.StatusOK {
			t.Fatalf("get x-report failed: %d %s", wX.Code, wX.Body.String())
		}
		var xReport domain.XReport
		_ = json.Unmarshal(wX.Body.Bytes(), &xReport)

		expectedTotal := int64(150000 + 27720)
		if xReport.ExpectedCash == nil || *xReport.ExpectedCash != expectedTotal {
			t.Errorf("expected expected_cash=%d, got %v", expectedTotal, xReport.ExpectedCash)
		}
		if xReport.Summary.CashSales != 27720 {
			t.Errorf("expected cash_sales=27720 in summary, got %+v", xReport.Summary)
		}
	})
}
