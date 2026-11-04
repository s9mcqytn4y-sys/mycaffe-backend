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

// TestIntegrationOutletCash covers decision V13: owner-only top-ups, cashier expenses within the daily
// limit, an expense over the limit needing an outlet_expense approval for exactly that amount and
// category, drawer transfers booked through the open shift, the default opening float and the
// configurable blind count.
func TestIntegrationOutletCash(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	migratorDSN := strings.TrimSpace(os.Getenv("TEST_MIGRATOR_DATABASE_URL"))
	if dsn == "" || migratorDSN == "" {
		t.Skip("TEST_APP_DATABASE_URL / TEST_MIGRATOR_DATABASE_URL not set; skipping outlet cash integration test")
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
	slug := "outlet-" + suffix
	email := func(name string) string { return name + ".outlet." + suffix + "@mycaffe.local" }
	storeID, ownerID, err := service.BootstrapStore(ctx, migratorDB, service.BootstrapStoreInput{
		Slug: slug, StoreName: "Kafe Outlet", OwnerName: "Pemilik Outlet", OwnerEmail: email("owner"), PIN: "482913",
	})
	if err != nil {
		t.Fatalf("bootstrap store: %v", err)
	}
	for _, role := range []string{"cashier", "barista"} {
		if _, err := migratorDB.ExecContext(ctx, `INSERT INTO users (store_id, email, full_name, role, pin_hash, is_active, pin_set_at)
			SELECT store_id, $2, $3, $3, pin_hash, true, NOW() FROM users WHERE id = $1`, ownerID, email(role), role); err != nil {
			t.Fatal(err)
		}
	}

	gin.SetMode(gin.TestMode)
	authSvc := service.NewAuthService(db, postgres.NewAuthRepository(), "integration-test-jwt-secret-at-least-32-chars", service.AuthPolicy{
		AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 12 * time.Hour, SessionIdleTimeout: 2 * time.Hour,
		MaxSessions: 3, PINMaxFailures: 5, PINLockout: 15 * time.Minute, MaxConcurrentHashes: 8,
	})
	ah := handler.NewAuthHandler(authSvc)
	orderRepo, paymentRepo := postgres.NewOrderRepository(), postgres.NewPaymentRepository()
	ops := service.NewOperationalService(db, orderRepo, paymentRepo, nil, nil, nil)
	pos := handler.NewPOSHandler(service.NewOrderService(db, orderRepo), service.NewPaymentService(db, paymentRepo, orderRepo, nil), service.NewSettingsService(db))
	sh := handler.NewShiftHandler(service.NewShiftService(db))
	aph := handler.NewApprovalHandler(service.NewApprovalService(db, ops))
	oh := handler.NewOutletCashHandler(service.NewOutletCashService(db))

	r := gin.New()
	r.POST("/login", ah.Login)
	r.PATCH("/store/settings", ah.RequirePermission(rbac.StoreSettingsManage), pos.UpdateSettings)
	r.POST("/pos/drawers", ah.RequirePermission(rbac.CashDrawerManage), sh.CreateDrawer)
	r.POST("/pos/shifts", ah.RequireStaff(), sh.OpenShift)
	r.GET("/pos/shifts/:id/x-report", ah.RequireStaff(), sh.GetXReport)
	r.POST("/pos/approvals", ah.RequireStaff(), aph.Create)
	r.POST("/approvals/:id/approve", ah.RequirePermission(rbac.ApprovalDecide), aph.Approve)
	r.GET("/pos/outlet-cash", ah.RequirePermission(rbac.CashMovementCreate), oh.Summary)
	r.POST("/pos/outlet-cash/movements", ah.RequirePermission(rbac.CashMovementCreate), oh.Record)

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
	login := func(who string) string {
		t.Helper()
		res := call("POST", "/login", "", map[string]string{"store_slug": slug, "email": email(who), "pin": "482913"})
		if res.Code != http.StatusOK {
			t.Fatalf("login %s: %d: %s", who, res.Code, res.Body.String())
		}
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
	record := func(token string, body map[string]any) (*httptest.ResponseRecorder, service.OutletCashSummary) {
		res := call("POST", "/pos/outlet-cash/movements", token, body)
		var s service.OutletCashSummary
		_ = json.Unmarshal(res.Body.Bytes(), &s)
		return res, s
	}
	owner, cashier, barista := login("owner"), login("cashier"), login("barista")

	if res := call("PATCH", "/store/settings", owner, map[string]any{"outlet_daily_expense_limit": -1}); res.Code == http.StatusOK {
		t.Fatalf("negative limit must be refused: %d", res.Code)
	}
	if res := call("PATCH", "/store/settings", owner, map[string]any{"outlet_daily_expense_limit": 50000, "default_opening_float": 150000}); res.Code != http.StatusOK {
		t.Fatalf("settings: %d: %s", res.Code, res.Body.String())
	}
	var drawer domain.CashDrawer
	_ = json.Unmarshal(call("POST", "/pos/drawers", owner, map[string]any{"name": "Laci Outlet"}).Body.Bytes(), &drawer)
	shiftRes := call("POST", "/pos/shifts", cashier, map[string]any{"drawer_id": drawer.ID})
	var opened struct {
		Shift domain.Shift `json:"shift"`
	}
	_ = json.Unmarshal(shiftRes.Body.Bytes(), &opened)
	if shiftRes.Code != http.StatusCreated || opened.Shift.OpeningFloat != 150000 {
		t.Fatalf("shift without opening_float must use the default 150000: %d %d %s", shiftRes.Code, opened.Shift.OpeningFloat, shiftRes.Body.String())
	}

	t.Run("TopUpAndExpensesWithinLimit", func(t *testing.T) {
		if res, _ := record(cashier, map[string]any{"movement_type": "top_up", "amount": 100000, "note": "Modal"}); res.Code != http.StatusForbidden {
			t.Fatalf("cashier top-up must be 403: %d", res.Code)
		}
		if res, s := record(owner, map[string]any{"movement_type": "top_up", "amount": 200000, "note": "Modal kas kecil"}); res.Code != http.StatusCreated || s.Balance != 200000 {
			t.Fatalf("owner top-up: %d %+v", res.Code, s)
		}
		res, s := record(cashier, map[string]any{"movement_type": "expense", "amount": 30000, "category": "ingredients", "note": "Susu 2 liter"})
		if res.Code != http.StatusCreated || s.Balance != 170000 || s.ExpensesToday != 30000 || s.RemainingToday != 20000 || s.RecordedMovement == nil {
			t.Fatalf("expense within limit: %d %+v", res.Code, s)
		}
		for body, field := range map[string]map[string]any{
			"category": {"movement_type": "expense", "amount": 1000, "category": "hiburan", "note": "x"},
			"note":     {"movement_type": "expense", "amount": 1000, "category": "other", "note": " "},
			"amount":   {"movement_type": "expense", "amount": 0, "category": "other", "note": "x"},
		} {
			if res, _ := record(cashier, field); res.Code != http.StatusUnprocessableEntity || !strings.Contains(res.Body.String(), `"`+body+`"`) {
				t.Fatalf("%s must be 422: %d: %s", body, res.Code, res.Body.String())
			}
		}
		if res := call("GET", "/pos/outlet-cash", barista, nil); res.Code != http.StatusForbidden {
			t.Fatalf("barista cannot read outlet cash: %d", res.Code)
		}
	})

	t.Run("ExpenseOverLimitNeedsExactApproval", func(t *testing.T) {
		over := map[string]any{"movement_type": "expense", "amount": 30000, "category": "ingredients", "note": "Kopi 1 kg"}
		if res, _ := record(cashier, over); codeOf(res) != "OUTLET_EXPENSE_APPROVAL_REQUIRED" {
			t.Fatalf("over the limit must ask for approval: %d: %s", res.Code, res.Body.String())
		}
		res := call("POST", "/pos/approvals", cashier, map[string]any{"action": "outlet_expense", "reason": "Kopi habis",
			"params": map[string]any{"amount": 30000, "category": "ingredients"}})
		var a service.Approval
		_ = json.Unmarshal(res.Body.Bytes(), &a)
		if res.Code != http.StatusCreated || a.Amount != 30000 || a.ReferenceLabel != "Kas outlet" {
			t.Fatalf("outlet approval request: %d: %s", res.Code, res.Body.String())
		}
		if res := call("POST", fmt.Sprintf("/approvals/%s/approve", a.ID), owner, map[string]any{"pin": "482913"}); res.Code != http.StatusOK {
			t.Fatalf("approve: %d: %s", res.Code, res.Body.String())
		}
		wrong := map[string]any{"movement_type": "expense", "amount": 30000, "category": "other", "note": "Kopi 1 kg", "approval_id": a.ID}
		if res, _ := record(cashier, wrong); codeOf(res) != "APPROVAL_INVALID" {
			t.Fatalf("another category must not use the approval: %d: %s", res.Code, res.Body.String())
		}
		over["approval_id"] = a.ID
		res2, s := record(cashier, over)
		if res2.Code != http.StatusCreated || s.Balance != 140000 || s.ExpensesToday != 60000 {
			t.Fatalf("approved expense: %d %+v", res2.Code, s)
		}
		var approvedBy uuid.UUID
		if err := migratorDB.GetContext(ctx, &approvedBy, `SELECT approved_by FROM outlet_cash_movements WHERE store_id = $1 AND approval_id = $2`, storeID, a.ID); err != nil || approvedBy != ownerID {
			t.Fatalf("ledger must record the approving owner: %s %v", approvedBy, err)
		}
		if res, _ := record(owner, map[string]any{"movement_type": "expense", "amount": 1_000_000, "category": "other", "note": "Besar"}); codeOf(res) != "OUTLET_INSUFFICIENT_FUNDS" {
			t.Fatalf("spending more than the balance must be refused: %d: %s", res.Code, res.Body.String())
		}
	})

	t.Run("DrawerTransfersMoveRealCash", func(t *testing.T) {
		drawerCash := func() (in, out int64) {
			_ = migratorDB.QueryRowxContext(ctx, `SELECT COALESCE(SUM(amount) FILTER (WHERE movement_type = 'cash_in'), 0),
				COALESCE(SUM(amount) FILTER (WHERE movement_type = 'paid_out'), 0) FROM cash_movements WHERE shift_id = $1`, opened.Shift.ID).Scan(&in, &out)
			return
		}
		res, s := record(cashier, map[string]any{"movement_type": "from_drawer", "amount": 50000, "note": "Setor kelebihan kas", "drawer_id": drawer.ID})
		if res.Code != http.StatusCreated || s.Balance != 190000 || !s.OpenDrawer {
			t.Fatalf("cash drop into the fund: %d %+v", res.Code, s)
		}
		if res, s := record(cashier, map[string]any{"movement_type": "to_drawer", "amount": 10000, "note": "Uang kembalian", "drawer_id": drawer.ID}); res.Code != http.StatusCreated || s.Balance != 180000 {
			t.Fatalf("fund into the drawer: %d %+v", res.Code, s)
		}
		if in, out := drawerCash(); in != 10000 || out != 50000 {
			t.Fatalf("drawer ledger must mirror transfers: cash_in %d paid_out %d", in, out)
		}
		if res, _ := record(cashier, map[string]any{"movement_type": "from_drawer", "amount": 10_000_000, "note": "Terlalu besar", "drawer_id": drawer.ID}); codeOf(res) != "INSUFFICIENT_DRAWER_CASH" {
			t.Fatalf("drop more than the drawer holds: %d: %s", res.Code, res.Body.String())
		}
	})

	t.Run("BlindCountIsConfigurable", func(t *testing.T) {
		xr := func() string {
			return call("GET", fmt.Sprintf("/pos/shifts/%s/x-report", opened.Shift.ID), cashier, nil).Body.String()
		}
		if !strings.Contains(xr(), `"expected_redacted":true`) {
			t.Fatalf("blind count is the default: %s", xr())
		}
		if res := call("PATCH", "/store/settings", owner, map[string]any{"cashier_sees_expected_cash": true}); res.Code != http.StatusOK {
			t.Fatal(res.Body.String())
		}
		if body := xr(); !strings.Contains(body, `"expected_redacted":false`) || !strings.Contains(body, `"expected_cash"`) {
			t.Fatalf("visible count when enabled: %s", body)
		}
	})
}
