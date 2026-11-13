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

// TestTablesIteration5aIntegration covers tables CRUD, QR rotation, derived occupancy (T1), lazy
// auto-close (QR7), the public QR resolver (QR1/QR2/QR4, emergency pause) and tenant isolation.
func TestTablesIteration5aIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping tables integration test")
	}
	db, err := database.NewPostgresDB(&config.Config{DatabaseURL: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	centralID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	expressID := uuid.MustParse("22222222-2222-4222-8222-222222222222")

	gin.SetMode(gin.TestMode)
	authSvc := service.NewAuthService(db, postgres.NewAuthRepository(), "integration-test-jwt-secret-at-least-32-chars", service.AuthPolicy{
		AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 12 * time.Hour, SessionIdleTimeout: 2 * time.Hour,
		MaxSessions: 3, PINMaxFailures: 5, PINLockout: 15 * time.Minute, MaxConcurrentHashes: 8,
	})
	ah := handler.NewAuthHandler(authSvc)
	th := handler.NewTableHandler(service.NewTableService(db))
	sh := handler.NewShiftHandler(service.NewShiftService(db))

	r := gin.New()
	r.POST("/login", ah.Login)
	r.GET("/pos/tables", ah.RequireStaff(), th.ListTables)
	r.POST("/pos/tables", ah.RequirePermission(rbac.TableManage), th.CreateTable)
	r.PATCH("/pos/tables/:id", ah.RequirePermission(rbac.TableManage), th.UpdateTable)
	r.POST("/pos/tables/:id/archive", ah.RequirePermission(rbac.TableManage), th.ArchiveTable)
	r.POST("/pos/tables/:id/rotate-qr", ah.RequirePermission(rbac.TableQRManage), th.RotateQR)
	r.POST("/pos/tables/:id/sessions", ah.RequirePermission(rbac.TabManage), th.OpenSession)
	r.POST("/pos/table-sessions/:id/close", ah.RequirePermission(rbac.TabManage), th.CloseSession)
	r.GET("/public/qr/:token", th.ResolveQR)
	r.POST("/pos/drawers", ah.RequirePermission(rbac.CashDrawerManage), sh.CreateDrawer)
	r.POST("/pos/shifts", ah.RequireStaff(), sh.OpenShift)
	r.POST("/pos/shifts/:id/force-close", ah.RequireStaff(), sh.ForceCloseShift)

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
	login := func(slug, email string) string {
		t.Helper()
		res := call("POST", "/login", "", map[string]string{"store_slug": slug, "email": email, "pin": "482913"})
		if res.Code != http.StatusOK {
			t.Fatalf("login %s failed: %d: %s", email, res.Code, res.Body.String())
		}
		var body struct {
			AccessToken string `json:"access_token"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &body)
		return body.AccessToken
	}
	inStore := func(storeID uuid.UUID, fn func(tx *sqlx.Tx) error) {
		t.Helper()
		if err := database.WithTenantTx(ctx, db, storeID, fn); err != nil {
			t.Fatalf("tenant tx failed: %v", err)
		}
	}
	board := func(token string) map[uuid.UUID]domain.TableBoardEntry {
		t.Helper()
		res := call("GET", "/pos/tables", token, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("list tables failed: %d: %s", res.Code, res.Body.String())
		}
		var body struct {
			Tables []domain.TableBoardEntry `json:"tables"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &body)
		out := map[uuid.UUID]domain.TableBoardEntry{}
		for _, e := range body.Tables {
			out[e.ID] = e
		}
		return out
	}
	resolve := func(token string) (int, service.PublicTable) {
		res := call("GET", "/public/qr/"+token, "", nil)
		var out service.PublicTable
		_ = json.Unmarshal(res.Body.Bytes(), &out)
		return res.Code, out
	}

	owner := login("central-cafe", "abdulaziz543333@gmail.com")
	cashier := login("central-cafe", "cashier.central@mycaffe.local")
	number := "T-" + uuid.NewString()[:6]

	var table domain.Table
	t.Run("CreateValidateAndAuthorize", func(t *testing.T) {
		if res := call("POST", "/pos/tables", owner, map[string]any{"table_number": number, "capacity": 0}); res.Code != http.StatusUnprocessableEntity || !strings.Contains(res.Body.String(), "capacity") {
			t.Fatalf("expected 422 on capacity, got %d: %s", res.Code, res.Body.String())
		}
		if res := call("POST", "/pos/tables", cashier, map[string]any{"table_number": number}); res.Code != http.StatusForbidden {
			t.Fatalf("cashier must not create tables (QR3), got %d", res.Code)
		}
		res := call("POST", "/pos/tables", owner, map[string]any{"table_number": number, "label": "Dekat jendela", "section": "Indoor", "capacity": 4})
		if res.Code != http.StatusCreated {
			t.Fatalf("create table failed: %d: %s", res.Code, res.Body.String())
		}
		_ = json.Unmarshal(res.Body.Bytes(), &table)
		if len(table.QRToken) != 43 {
			t.Fatalf("expected a 43-char base64url QR token, got %q", table.QRToken)
		}
		dup := call("POST", "/pos/tables", owner, map[string]any{"table_number": strings.ToLower(number)})
		if dup.Code != http.StatusConflict || !strings.Contains(dup.Body.String(), "table_number") {
			t.Fatalf("expected 409 on duplicate number (case-insensitive), got %d: %s", dup.Code, dup.Body.String())
		}
	})

	t.Run("BoardHidesTokenFromCashierAndIsTenantIsolated", func(t *testing.T) {
		if e, ok := board(cashier)[table.ID]; !ok || e.QRToken != "" || e.Status != domain.TableStatusAvailable {
			t.Fatalf("cashier board entry wrong: %+v (present=%v)", e, ok)
		}
		if e := board(owner)[table.ID]; e.QRToken != table.QRToken {
			t.Fatalf("owner must see the QR token for printing")
		}
		if _, ok := board(login("express-counter", "cashier.express@mycaffe.local"))[table.ID]; ok {
			t.Fatalf("another store must not see this table")
		}
	})

	t.Run("ResolveQRReportsPauseClosedAndOpen", func(t *testing.T) {
		if code, _ := resolve("unknown-" + strings.Repeat("x", 30)); code != http.StatusNotFound {
			t.Fatalf("unknown token must be 404, got %d", code)
		}
		inStore(centralID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO store_settings (store_id, qr_ordering_paused) VALUES ($1, true)
				ON CONFLICT (store_id) DO UPDATE SET qr_ordering_paused = true`, centralID)
			return err
		})
		code, out := resolve(table.QRToken)
		if code != http.StatusOK || out.OrderingOpen || out.ClosedReason != service.QRClosedPaused || out.TableNumber != number || out.StoreSlug != "central-cafe" {
			t.Fatalf("paused store must resolve with ordering closed: %d %+v", code, out)
		}
		inStore(centralID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE store_settings SET qr_ordering_paused = false WHERE store_id = $1`, centralID)
			return err
		})

		// An open shift makes the store "open" for QR orders (QR4).
		drawerRes := call("POST", "/pos/drawers", owner, map[string]any{"name": "Laci QR " + uuid.NewString()[:6]})
		var drawer domain.CashDrawer
		_ = json.Unmarshal(drawerRes.Body.Bytes(), &drawer)
		shiftRes := call("POST", "/pos/shifts", owner, map[string]any{"drawer_id": drawer.ID, "opening_float": 0})
		if shiftRes.Code != http.StatusCreated {
			t.Fatalf("open shift failed: %d: %s", shiftRes.Code, shiftRes.Body.String())
		}
		var opened struct {
			Shift domain.Shift `json:"shift"`
		}
		_ = json.Unmarshal(shiftRes.Body.Bytes(), &opened)
		t.Cleanup(func() {
			call("POST", fmt.Sprintf("/pos/shifts/%s/force-close", opened.Shift.ID), owner, map[string]any{"reason": "Akhir test meja"})
		})
		if code, out := resolve(table.QRToken); code != http.StatusOK || !out.OrderingOpen {
			t.Fatalf("store with an open shift must accept QR orders: %d %+v", code, out)
		} else if got := strings.Join(out.PaymentMethods, ","); got != "qris,gopay,shopeepay,bank_transfer" {
			// central-cafe also enables cash and EDC; a guest must only see the online gateway methods it can use.
			t.Fatalf("guest payment methods: got %q", got)
		} else if out.ServiceMode != domain.QRServicePickup {
			t.Fatalf("service mode must default to pickup, got %q", out.ServiceMode)
		} else if out.SplitEnabled {
			t.Fatal("bill splitting must stay off unless GUEST_SPLIT_ENABLED is set")
		}

		// A store with no open shift is closed.
		expressToken := strings.Repeat("e", 30) + uuid.NewString()[:8]
		inStore(expressID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO tables (store_id, table_number, qr_token) VALUES ($1, $2, $3)`, expressID, "E-"+uuid.NewString()[:6], expressToken)
			return err
		})
		if code, out := resolve(expressToken); code != http.StatusOK || out.OrderingOpen || out.ClosedReason != service.QRClosedNoShift {
			t.Fatalf("store without an open shift must be closed: %d %+v", code, out)
		}
	})

	t.Run("RotateQRKillsOldSticker", func(t *testing.T) {
		if res := call("POST", fmt.Sprintf("/pos/tables/%s/rotate-qr", table.ID), cashier, nil); res.Code != http.StatusForbidden {
			t.Fatalf("cashier must not rotate QR, got %d", res.Code)
		}
		res := call("POST", fmt.Sprintf("/pos/tables/%s/rotate-qr", table.ID), owner, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("rotate failed: %d: %s", res.Code, res.Body.String())
		}
		var rotated domain.Table
		_ = json.Unmarshal(res.Body.Bytes(), &rotated)
		if code, _ := resolve(table.QRToken); code != http.StatusNotFound {
			t.Fatalf("old token must stop resolving, got %d", code)
		}
		if code, _ := resolve(rotated.QRToken); code != http.StatusOK {
			t.Fatalf("new token must resolve, got %d", code)
		}
		table = rotated
	})

	t.Run("SessionLifecycleDrivesOccupancy", func(t *testing.T) {
		res := call("POST", fmt.Sprintf("/pos/tables/%s/sessions", table.ID), cashier, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("open session failed: %d: %s", res.Code, res.Body.String())
		}
		var session domain.TableSession
		_ = json.Unmarshal(res.Body.Bytes(), &session)
		var again domain.TableSession
		_ = json.Unmarshal(call("POST", fmt.Sprintf("/pos/tables/%s/sessions", table.ID), cashier, nil).Body.Bytes(), &again)
		if again.ID != session.ID {
			t.Fatalf("opening an occupied table must return the open session")
		}
		if e := board(cashier)[table.ID]; e.Status != domain.TableStatusOccupied || e.Session == nil || e.Session.ID != session.ID {
			t.Fatalf("expected occupied with the session, got %+v", e)
		}
		if res := call("POST", fmt.Sprintf("/pos/tables/%s/archive", table.ID), owner, nil); res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "TABLE_SESSION_OPEN") {
			t.Fatalf("archiving an occupied table must be 409, got %d: %s", res.Code, res.Body.String())
		}

		// An unpaid order on the session blocks "Kosongkan meja".
		orderID := uuid.New()
		inStore(centralID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO orders (id, store_id, idempotency_key, request_hash, order_number, queue_number, customer_name, order_type,
					status, payment_status, table_id, table_session_id, subtotal_amount, total_amount)
				VALUES ($1, $2, $3, $4, $5, 'Q-T', 'Tamu', 'dine_in', 'pending_payment', 'unpaid', $6, $7, 20000, 20000)`,
				orderID, centralID, "table-test-"+orderID.String(), strings.Repeat("c", 64), "TBL-"+orderID.String()[:8], table.ID, session.ID)
			return err
		})
		if e := board(cashier)[table.ID]; e.UnpaidOrders != 1 || e.UnpaidAmount != 20000 {
			t.Fatalf("board must show the unpaid order, got %+v", e)
		}
		closeURL := fmt.Sprintf("/pos/table-sessions/%s/close", session.ID)
		if res := call("POST", closeURL, cashier, nil); res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "TABLE_HAS_UNPAID_ORDERS") {
			t.Fatalf("expected 409 TABLE_HAS_UNPAID_ORDERS, got %d: %s", res.Code, res.Body.String())
		}
		inStore(centralID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE orders SET status = 'cancelled', stock_status = 'not_tracked' WHERE store_id = $1 AND id = $2`, centralID, orderID)
			return err
		})
		if res := call("POST", closeURL, cashier, nil); res.Code != http.StatusOK {
			t.Fatalf("close session failed: %d: %s", res.Code, res.Body.String())
		}
		if e := board(cashier)[table.ID]; e.Status != domain.TableStatusAvailable || e.Session != nil {
			t.Fatalf("closed session must free the table, got %+v", e)
		}
		var audits int
		inStore(centralID, func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &audits, `SELECT COUNT(*) FROM audit_logs WHERE store_id = $1 AND action = 'TABLE_SESSION_CLOSE' AND reference_id = $2`, centralID, session.ID.String())
		})
		if audits != 1 {
			t.Fatalf("expected one TABLE_SESSION_CLOSE audit, got %d", audits)
		}
	})

	t.Run("LazyAutoCloseAfterSettledIdle", func(t *testing.T) {
		var session domain.TableSession
		_ = json.Unmarshal(call("POST", fmt.Sprintf("/pos/tables/%s/sessions", table.ID), cashier, nil).Body.Bytes(), &session)
		inStore(centralID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE table_sessions SET settled_at = NOW() - INTERVAL '10 minutes' WHERE store_id = $1 AND id = $2`, centralID, session.ID)
			return err
		})
		if e := board(cashier)[table.ID]; e.Status != domain.TableStatusSettled {
			t.Fatalf("settled session inside the idle window must show settled, got %s", e.Status)
		}
		inStore(centralID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE table_sessions SET settled_at = NOW() - INTERVAL '4 hours' WHERE store_id = $1 AND id = $2`, centralID, session.ID)
			return err
		})
		if e := board(cashier)[table.ID]; e.Status != domain.TableStatusAvailable {
			t.Fatalf("settled session past the idle window must auto-close, got %s", e.Status)
		}
		var reason string
		inStore(centralID, func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &reason, `SELECT close_reason FROM table_sessions WHERE store_id = $1 AND id = $2`, centralID, session.ID)
		})
		if reason != "auto" {
			t.Fatalf("expected close_reason auto, got %q", reason)
		}
	})

	t.Run("InactiveAndArchivedTables", func(t *testing.T) {
		if res := call("PATCH", fmt.Sprintf("/pos/tables/%s", table.ID), owner, map[string]any{"is_active": false}); res.Code != http.StatusOK {
			t.Fatalf("deactivate failed: %d: %s", res.Code, res.Body.String())
		}
		if code, out := resolve(table.QRToken); code != http.StatusOK || out.OrderingOpen || out.ClosedReason != service.QRClosedInactive {
			t.Fatalf("inactive table must resolve closed: %d %+v", code, out)
		}
		if res := call("POST", fmt.Sprintf("/pos/tables/%s/sessions", table.ID), cashier, nil); res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "TABLE_INACTIVE") {
			t.Fatalf("expected 409 TABLE_INACTIVE, got %d: %s", res.Code, res.Body.String())
		}
		if res := call("POST", fmt.Sprintf("/pos/tables/%s/archive", table.ID), owner, nil); res.Code != http.StatusNoContent {
			t.Fatalf("archive failed: %d: %s", res.Code, res.Body.String())
		}
		if code, _ := resolve(table.QRToken); code != http.StatusNotFound {
			t.Fatalf("archived table must not resolve, got %d", code)
		}
		if _, ok := board(owner)[table.ID]; ok {
			t.Fatalf("archived table must leave the board")
		}
		// The number is free again once the old table is archived.
		if res := call("POST", "/pos/tables", owner, map[string]any{"table_number": number}); res.Code != http.StatusCreated {
			t.Fatalf("re-using an archived number failed: %d: %s", res.Code, res.Body.String())
		}
	})
}
