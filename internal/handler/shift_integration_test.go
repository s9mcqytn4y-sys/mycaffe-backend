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

// TestShiftLifecycleIntegration tests Iteration 4b: Drawer CRUD, Shift Open/Close,
// Cash Movements (paid_out, cash_in, tip, staff_meal, no_sale), Blind Count with Owner PIN Override,
// and X/Z Reports with expected_cash redaction.
func TestShiftLifecycleIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping Shift integration test")
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

	shiftSvc := service.NewShiftService(db)
	sh := handler.NewShiftHandler(shiftSvc)

	r := gin.New()
	r.POST("/login", ah.Login)

	// Drawer routes
	r.POST("/pos/drawers", ah.RequirePermission(rbac.CashDrawerManage), sh.CreateDrawer)
	r.GET("/pos/drawers", ah.RequireStaff(), sh.ListDrawers)
	r.PATCH("/pos/drawers/:id", ah.RequirePermission(rbac.CashDrawerManage), sh.UpdateDrawer)

	// Shift routes
	r.POST("/pos/shifts", ah.RequirePermission(rbac.ShiftOpen), sh.OpenShift)
	r.GET("/pos/shifts/current", ah.RequireStaff(), sh.GetCurrentShift)
	r.POST("/pos/shifts/movements", ah.RequirePermission(rbac.CashMovementCreate), sh.CreateCashMovement)
	r.POST("/pos/shifts/:id/close", ah.RequirePermission(rbac.ShiftClose), sh.CloseShift)
	r.POST("/pos/shifts/:id/force-close", ah.RequirePermission(rbac.ShiftForceClose), sh.ForceCloseShift)
	r.GET("/pos/shifts/:id/x-report", ah.RequireStaff(), sh.GetXReport)
	r.GET("/pos/shifts/:id/z-report", ah.RequireStaff(), sh.GetZReport)

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

	ownerToken := login("owner.central@mycaffe.local")
	cashierToken := login("cashier.central@mycaffe.local")

	var drawerID string

	t.Run("1. Cashier cannot create drawer (RBAC guard)", func(t *testing.T) {
		w := call(http.MethodPost, "/pos/drawers", cashierToken, map[string]any{"name": "Drawer Unauthorized"})
		if w.Code != http.StatusForbidden {
			t.Fatalf("cashier create drawer should be 403, got %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("2. Owner creates cash drawer", func(t *testing.T) {
		drawerName := "Kasir POS " + uuid.NewString()[:6]
		w := call(http.MethodPost, "/pos/drawers", ownerToken, map[string]any{"name": drawerName})
		if w.Code != http.StatusCreated {
			t.Fatalf("create drawer failed: %d %s", w.Code, w.Body.String())
		}
		var d domain.CashDrawer
		_ = json.Unmarshal(w.Body.Bytes(), &d)
		if d.ID == uuid.Nil {
			t.Fatal("empty drawer id returned")
		}
		drawerID = d.ID.String()
	})

	var shiftID string

	t.Run("3. Cashier opens shift with float 200,000", func(t *testing.T) {
		w := call(http.MethodPost, "/pos/shifts", cashierToken, map[string]any{
			"drawer_id":     drawerID,
			"opening_float": 200000,
		})
		if w.Code != http.StatusCreated {
			t.Fatalf("open shift failed: %d %s", w.Code, w.Body.String())
		}
		var res service.OpenShiftResult
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		if res.Shift == nil || res.Shift.ID == uuid.Nil {
			t.Fatal("empty shift returned")
		}
		if !res.OpenDrawer {
			t.Error("expected open_drawer true on shift opening")
		}
		shiftID = res.Shift.ID.String()
	})

	t.Run("4. Cannot open second shift on the same drawer (Partial unique conflict)", func(t *testing.T) {
		w := call(http.MethodPost, "/pos/shifts", cashierToken, map[string]any{
			"drawer_id":     drawerID,
			"opening_float": 100000,
		})
		if w.Code != http.StatusConflict {
			t.Fatalf("second open shift on same drawer should return 409, got %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("5. Cashier gets current shift", func(t *testing.T) {
		w := call(http.MethodGet, "/pos/shifts/current?drawer_id="+drawerID, cashierToken, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("get current shift failed: %d %s", w.Code, w.Body.String())
		}
		var s domain.Shift
		_ = json.Unmarshal(w.Body.Bytes(), &s)
		if s.ID.String() != shiftID {
			t.Fatalf("expected shiftID %s, got %s", shiftID, s.ID.String())
		}
	})

	t.Run("6. Cashier records paid_out and rejects excessive amount", func(t *testing.T) {
		// Attempt paid_out 1,000,000 with only 200,000 float in drawer -> must reject 422
		wExcess := call(http.MethodPost, "/pos/shifts/movements", cashierToken, map[string]any{
			"shift_id":      shiftID,
			"movement_type": "paid_out",
			"amount":        1000000,
			"reason":        "Beli blender darurat",
		})
		if wExcess.Code != http.StatusUnprocessableEntity {
			t.Fatalf("excessive paid_out should return 422 INSUFFICIENT_DRAWER_CASH, got %d %s", wExcess.Code, wExcess.Body.String())
		}

		// Valid paid_out 50,000
		wValid := call(http.MethodPost, "/pos/shifts/movements", cashierToken, map[string]any{
			"shift_id":      shiftID,
			"movement_type": "paid_out",
			"amount":        50000,
			"reason":        "Beli es batu kristal",
		})
		if wValid.Code != http.StatusCreated {
			t.Fatalf("valid paid_out failed: %d %s", wValid.Code, wValid.Body.String())
		}
		var res service.CreateCashMovementResult
		_ = json.Unmarshal(wValid.Body.Bytes(), &res)
		if !res.OpenDrawer {
			t.Error("expected open_drawer true on paid_out")
		}
	})

	t.Run("7. Cashier records cash_in, tip, and no_sale", func(t *testing.T) {
		// cash_in 100,000
		wCashIn := call(http.MethodPost, "/pos/shifts/movements", cashierToken, map[string]any{
			"shift_id":      shiftID,
			"movement_type": "cash_in",
			"amount":        100000,
			"reason":        "Tambahan modal receh",
		})
		if wCashIn.Code != http.StatusCreated {
			t.Fatalf("cash_in failed: %d %s", wCashIn.Code, wCashIn.Body.String())
		}

		// tip 20,000
		wTip := call(http.MethodPost, "/pos/shifts/movements", cashierToken, map[string]any{
			"shift_id":      shiftID,
			"movement_type": "tip",
			"amount":        20000,
			"reason":        "Tip meja 4",
		})
		if wTip.Code != http.StatusCreated {
			t.Fatalf("tip failed: %d %s", wTip.Code, wTip.Body.String())
		}

		// no_sale without reason must reject 422
		wNoSaleFail := call(http.MethodPost, "/pos/shifts/movements", cashierToken, map[string]any{
			"shift_id":      shiftID,
			"movement_type": "no_sale",
			"amount":        0,
			"reason":        "",
		})
		if wNoSaleFail.Code != http.StatusUnprocessableEntity {
			t.Fatalf("no_sale without reason must be 422, got %d %s", wNoSaleFail.Code, wNoSaleFail.Body.String())
		}

		// no_sale with reason succeeds
		wNoSaleOk := call(http.MethodPost, "/pos/shifts/movements", cashierToken, map[string]any{
			"shift_id":      shiftID,
			"movement_type": "no_sale",
			"amount":        0,
			"reason":        "Tukar receh ke toko sebelah",
		})
		if wNoSaleOk.Code != http.StatusCreated {
			t.Fatalf("no_sale with reason failed: %d %s", wNoSaleOk.Code, wNoSaleOk.Body.String())
		}
	})

	t.Run("8. X Report redaction for cashier vs visible for owner", func(t *testing.T) {
		// Cashier X Report: expected_cash must be nil and expected_redacted must be true
		wCashier := call(http.MethodGet, "/pos/shifts/"+shiftID+"/x-report", cashierToken, nil)
		if wCashier.Code != http.StatusOK {
			t.Fatalf("cashier x-report failed: %d %s", wCashier.Code, wCashier.Body.String())
		}
		var xCashier domain.XReport
		_ = json.Unmarshal(wCashier.Body.Bytes(), &xCashier)
		if !xCashier.ExpectedRedacted || xCashier.ExpectedCash != nil {
			t.Fatalf("cashier x-report expected_cash must be redacted; got redacted=%v expected=%v", xCashier.ExpectedRedacted, xCashier.ExpectedCash)
		}

		// Owner X Report: expected_cash must be visible (200,000 float - 50,000 paid_out + 100,000 cash_in + 20,000 tip = 270,000)
		wOwner := call(http.MethodGet, "/pos/shifts/"+shiftID+"/x-report", ownerToken, nil)
		if wOwner.Code != http.StatusOK {
			t.Fatalf("owner x-report failed: %d %s", wOwner.Code, wOwner.Body.String())
		}
		var xOwner domain.XReport
		_ = json.Unmarshal(wOwner.Body.Bytes(), &xOwner)
		if xOwner.ExpectedRedacted || xOwner.ExpectedCash == nil || *xOwner.ExpectedCash != 270000 {
			t.Fatalf("owner x-report expected 270000; got redacted=%v val=%v", xOwner.ExpectedRedacted, xOwner.ExpectedCash)
		}
	})

	t.Run("9. Close shift with variance exceeds tolerance requires valid owner PIN", func(t *testing.T) {
		// Expected is 270,000. Cashier counts 350,000 (variance +80,000 > tolerance 5,000) without PIN -> must fail 422
		wNoPIN := call(http.MethodPost, "/pos/shifts/"+shiftID+"/close", cashierToken, map[string]any{
			"counted_cash": 350000,
		})
		if wNoPIN.Code != http.StatusUnprocessableEntity {
			t.Fatalf("variance without owner PIN must return 422, got %d %s", wNoPIN.Code, wNoPIN.Body.String())
		}

		// Invalid Owner PIN -> must fail 401
		wWrongPIN := call(http.MethodPost, "/pos/shifts/"+shiftID+"/close", cashierToken, map[string]any{
			"counted_cash":    350000,
			"variance_reason": "Lebih uang kembalian pelanggan",
			"owner_pin":       "999999",
		})
		if wWrongPIN.Code != http.StatusUnauthorized {
			t.Fatalf("variance with wrong owner PIN must return 401, got %d %s", wWrongPIN.Code, wWrongPIN.Body.String())
		}

		// Valid Owner PIN ("482913") -> succeeds 200 OK and generates Z Report
		reason := "Lebih uang kembalian pelanggan di meja"
		wSuccess := call(http.MethodPost, "/pos/shifts/"+shiftID+"/close", cashierToken, map[string]any{
			"counted_cash":    350000,
			"variance_reason": reason,
			"owner_pin":       "482913",
		})
		if wSuccess.Code != http.StatusOK {
			t.Fatalf("close shift with valid owner PIN failed: %d %s", wSuccess.Code, wSuccess.Body.String())
		}
		var res service.CloseShiftResult
		_ = json.Unmarshal(wSuccess.Body.Bytes(), &res)
		if res.Report == nil {
			t.Fatal("expected z-report in close shift result")
		}
		if res.Report.Status != domain.ShiftStatusClosed {
			t.Fatalf("expected shift closed, got %s", res.Report.Status)
		}
		if res.Report.ExpectedCash != 270000 {
			t.Fatalf("expected expected_cash 270000, got %d", res.Report.ExpectedCash)
		}
		if res.Report.CountedCash != 350000 {
			t.Fatalf("expected counted_cash 350000, got %d", res.Report.CountedCash)
		}
		if res.Report.Variance != 80000 {
			t.Fatalf("expected variance 80000, got %d", res.Report.Variance)
		}
		if !res.OpenDrawer {
			t.Error("expected open_drawer true on close shift")
		}
	})

	t.Run("10. Closed shift cannot be closed again (Conflict)", func(t *testing.T) {
		w := call(http.MethodPost, "/pos/shifts/"+shiftID+"/close", cashierToken, map[string]any{
			"counted_cash": 270000,
		})
		if w.Code != http.StatusConflict {
			t.Fatalf("closing already closed shift should be 409, got %d %s", w.Code, w.Body.String())
		}
	})

	t.Run("11. Z Report readable after closure", func(t *testing.T) {
		w := call(http.MethodGet, "/pos/shifts/"+shiftID+"/z-report", cashierToken, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("get z-report failed: %d %s", w.Code, w.Body.String())
		}
		var z domain.ZReport
		_ = json.Unmarshal(w.Body.Bytes(), &z)
		if z.Variance != 80000 || z.ExpectedCash != 270000 {
			t.Fatalf("mismatched z-report data: exp=%d var=%d", z.ExpectedCash, z.Variance)
		}
	})
}
