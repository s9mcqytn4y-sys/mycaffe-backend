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

// TestKDSIteration5dIntegration covers ticket creation per (order/round) x station, station
// permissions, optimistic transitions, derived order/round status, table settled_at, and the
// void rule that restocks unstarted lines and records started ones as waste.
func TestKDSIteration5dIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping KDS integration test")
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
	orderRepo := postgres.NewOrderRepository()
	paymentRepo := postgres.NewPaymentRepository()
	orderSvc := service.NewOrderService(db, orderRepo)
	pos := handler.NewPOSHandler(orderSvc, service.NewPaymentService(db, paymentRepo, orderRepo, nil), service.NewSettingsService(db))
	pos.SetOperational(service.NewOperationalService(db, orderRepo, paymentRepo, nil, nil, nil))
	sh := handler.NewShiftHandler(service.NewShiftService(db))
	th := handler.NewTabHandler(service.NewTabService(db, postgres.NewTabRepository(), orderRepo))
	kh := handler.NewKDSHandler(service.NewKDSService(db))

	r := gin.New()
	r.POST("/login", ah.Login)
	r.POST("/pos/drawers", ah.RequirePermission(rbac.CashDrawerManage), sh.CreateDrawer)
	r.POST("/pos/shifts", ah.RequireStaff(), sh.OpenShift)
	r.POST("/pos/shifts/:id/force-close", ah.RequireStaff(), sh.ForceCloseShift)
	r.POST("/pos/orders", ah.RequireStaff(), pos.CreateOrder)
	r.POST("/pos/orders/:id/payments", ah.RequireStaff(), pos.InitiatePayment)
	r.POST("/pos/orders/:id/void", ah.RequirePermission(rbac.OrderVoid), pos.VoidOrder)
	r.POST("/pos/tabs", ah.RequirePermission(rbac.TabManage), th.OpenTab)
	r.GET("/kds/tickets", ah.RequireStaff(), kh.ListTickets)
	r.POST("/kds/tickets/:id/transition", ah.RequireStaff(), kh.Transition)

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
	inStore := func(fn func(tx *sqlx.Tx) error) {
		t.Helper()
		if err := database.WithTenantTx(ctx, db, storeID, fn); err != nil {
			t.Fatalf("tenant tx failed: %v", err)
		}
	}

	owner, cashier := login("abdulaziz543333@gmail.com"), login("cashier.central@mycaffe.local")
	barista, kitchen := login("barista.central@mycaffe.local"), login("kitchen.central@mycaffe.local")

	// One product per station, all stock-tracked.
	coffee, toast, cookie := uuid.New(), uuid.New(), uuid.New()
	inStore(func(tx *sqlx.Tx) error {
		for _, p := range []struct {
			id      uuid.UUID
			station string
		}{{coffee, "bar"}, {toast, "kitchen"}, {cookie, "none"}} {
			if _, err := tx.ExecContext(ctx, `
				WITH p AS (INSERT INTO products (id, store_id, name, base_price, is_available, track_stock, stock_quantity, prep_station)
					VALUES ($1, $2, $3, 0, true, true, 100, $4) RETURNING store_id, id)
				INSERT INTO product_variants (store_id, product_id, name, price, is_default) SELECT store_id, id, 'Reguler', 10000, true FROM p`,
				p.id, storeID, "KDS "+p.station+" "+p.id.String()[:6], p.station); err != nil {
				return err
			}
		}
		return nil
	})
	stock := func(id uuid.UUID) int {
		var n int
		inStore(func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &n, `SELECT stock_quantity FROM products WHERE store_id = $1 AND id = $2`, storeID, id)
		})
		return n
	}

	// Cash tender needs an open shift.
	var drawer domain.CashDrawer
	_ = json.Unmarshal(call("POST", "/pos/drawers", owner, map[string]any{"name": "Laci KDS " + uuid.NewString()[:6]}).Body.Bytes(), &drawer)
	var opened struct {
		Shift domain.Shift `json:"shift"`
	}
	shiftRes := call("POST", "/pos/shifts", cashier, map[string]any{"drawer_id": drawer.ID, "opening_float": 0})
	if shiftRes.Code != http.StatusCreated {
		t.Fatalf("open shift failed: %d: %s", shiftRes.Code, shiftRes.Body.String())
	}
	_ = json.Unmarshal(shiftRes.Body.Bytes(), &opened)
	t.Cleanup(func() {
		call("POST", fmt.Sprintf("/pos/shifts/%s/force-close", opened.Shift.ID), owner, map[string]any{"reason": "Akhir test KDS"})
	})

	paidOrder := func(products ...uuid.UUID) uuid.UUID {
		t.Helper()
		items := []map[string]any{}
		for _, p := range products {
			items = append(items, map[string]any{"product_id": p, "quantity": 1})
		}
		res := call("POST", "/pos/orders", cashier, map[string]any{"idempotency_key": "kds-" + uuid.NewString(), "order_type": "takeaway", "items": items})
		if res.Code != http.StatusCreated {
			t.Fatalf("create order failed: %d: %s", res.Code, res.Body.String())
		}
		var created struct {
			Order domain.Order `json:"order"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &created)
		pay := call("POST", fmt.Sprintf("/pos/orders/%s/payments", created.Order.ID), cashier, map[string]any{
			"idempotency_key": "kds-pay-" + uuid.NewString(), "method": "cash", "cash_tendered_amount": created.Order.TotalAmount,
		})
		if pay.Code != http.StatusOK {
			t.Fatalf("cash payment failed: %d: %s", pay.Code, pay.Body.String())
		}
		return created.Order.ID
	}
	orderStatus := func(id uuid.UUID) string {
		var s string
		inStore(func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &s, `SELECT status FROM orders WHERE store_id = $1 AND id = $2`, storeID, id)
		})
		return s
	}
	tickets := func(token, station string) []service.KDSTicket {
		t.Helper()
		res := call("GET", "/kds/tickets?station="+station, token, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("list %s tickets failed: %d: %s", station, res.Code, res.Body.String())
		}
		var body struct {
			Tickets []service.KDSTicket `json:"tickets"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &body)
		return body.Tickets
	}
	ticketFor := func(token, station string, orderID uuid.UUID) service.KDSTicket {
		t.Helper()
		for _, tk := range tickets(token, station) {
			if tk.OrderID == orderID {
				return tk
			}
		}
		t.Fatalf("no %s ticket for order %s", station, orderID)
		return service.KDSTicket{}
	}
	move := func(token string, id uuid.UUID, from, to string) *httptest.ResponseRecorder {
		return call("POST", fmt.Sprintf("/kds/tickets/%s/transition", id), token, map[string]any{"to": to, "expected_status": from})
	}
	mustMove := func(token string, id uuid.UUID, from, to string) {
		t.Helper()
		if res := move(token, id, from, to); res.Code != http.StatusOK {
			t.Fatalf("%s -> %s failed: %d: %s", from, to, res.Code, res.Body.String())
		}
	}

	t.Run("PaidMixedOrderGetsOneTicketPerStation", func(t *testing.T) {
		orderID := paidOrder(coffee, toast, cookie)
		bar, kit := ticketFor(barista, "bar", orderID), ticketFor(kitchen, "kitchen", orderID)
		if len(bar.Lines) != 1 || len(kit.Lines) != 1 || bar.Status != service.KDSQueued || bar.LatePayment {
			t.Fatalf("expected one queued line per station: bar=%+v kitchen=%+v", bar, kit)
		}
		var created int
		inStore(func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &created, `SELECT COUNT(*) FROM outbox_events WHERE store_id = $1 AND aggregate_id = $2 AND event_type = 'kds.ticket_created'`, storeID, orderID.String())
		})
		if created != 2 || orderStatus(orderID) != "queued" {
			t.Fatalf("expected 2 kds.ticket_created events and a queued order, got %d / %s", created, orderStatus(orderID))
		}
		if onlyReady := paidOrder(cookie); orderStatus(onlyReady) != "ready" {
			t.Fatalf("an order with nothing to prepare must be ready at once, got %s", orderStatus(onlyReady))
		}
	})

	t.Run("StationPermissionsAndOptimisticTransitions", func(t *testing.T) {
		orderID := paidOrder(coffee, toast)
		bar, kit := ticketFor(barista, "bar", orderID), ticketFor(kitchen, "kitchen", orderID)
		if res := call("GET", "/kds/tickets?station=kitchen", barista, nil); res.Code != http.StatusForbidden {
			t.Fatalf("barista must not read the kitchen screen, got %d", res.Code)
		}
		if res := move(kitchen, bar.ID, "queued", "preparing"); res.Code != http.StatusForbidden {
			t.Fatalf("kitchen must not move a bar ticket, got %d", res.Code)
		}
		if res := move(barista, bar.ID, "preparing", "ready"); res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "KDS_STALE_STATUS") {
			t.Fatalf("stale expected_status must be 409 KDS_STALE_STATUS, got %d: %s", res.Code, res.Body.String())
		}
		if res := move(barista, bar.ID, "queued", "served"); res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "KDS_TRANSITION_NOT_ALLOWED") {
			t.Fatalf("skipping steps must be 409, got %d: %s", res.Code, res.Body.String())
		}

		mustMove(barista, bar.ID, "queued", "preparing")
		if s := orderStatus(orderID); s != "preparing" {
			t.Fatalf("order must be preparing once a station starts, got %s", s)
		}
		mustMove(barista, bar.ID, "preparing", "ready")
		mustMove(kitchen, kit.ID, "queued", "preparing")
		mustMove(kitchen, kit.ID, "preparing", "ready")
		if s := orderStatus(orderID); s != "ready" {
			t.Fatalf("order must be ready when every station is ready, got %s", s)
		}
		mustMove(barista, bar.ID, "ready", "preparing") // recall
		if s := orderStatus(orderID); s != "preparing" {
			t.Fatalf("a recall must move the order back to preparing, got %s", s)
		}
		mustMove(barista, bar.ID, "preparing", "ready")

		// Bind the order to a table session to check settled_at after the last serve.
		sessionID := uuid.New()
		inStore(func(tx *sqlx.Tx) error {
			var tableID uuid.UUID
			if err := tx.GetContext(ctx, &tableID, `INSERT INTO tables (store_id, table_number, qr_token) VALUES ($1, $2, $3) RETURNING id`,
				storeID, "K-"+sessionID.String()[:6], "kds"+strings.ReplaceAll(sessionID.String(), "-", "")); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO table_sessions (id, store_id, table_id, opened_via) VALUES ($1, $2, $3, 'staff')`, sessionID, storeID, tableID); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `UPDATE orders SET table_id = $3, table_session_id = $4 WHERE store_id = $1 AND id = $2`, storeID, orderID, tableID, sessionID)
			return err
		})
		mustMove(barista, bar.ID, "ready", "served")
		var settled *time.Time
		inStore(func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &settled, `SELECT settled_at FROM table_sessions WHERE store_id = $1 AND id = $2`, storeID, sessionID)
		})
		if settled != nil {
			t.Fatalf("table must not settle while a station is still serving")
		}
		mustMove(kitchen, kit.ID, "ready", "served")
		inStore(func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &settled, `SELECT settled_at FROM table_sessions WHERE store_id = $1 AND id = $2`, storeID, sessionID)
		})
		if s := orderStatus(orderID); s != "served" || settled == nil {
			t.Fatalf("expected served order and a settled table, got %s / %v", s, settled)
		}
	})

	t.Run("TabRoundTicketsAndVoidRestocksOnlyUnstartedLines", func(t *testing.T) {
		res := call("POST", "/pos/tabs", cashier, map[string]any{
			"idempotency_key": "kds-tab-" + uuid.NewString(), "table_number": "KDS-TAB",
			"initial_items": []map[string]any{{"product_id": coffee, "quantity": 1}, {"product_id": toast, "quantity": 1}},
		})
		if res.Code != http.StatusCreated {
			t.Fatalf("open tab failed: %d: %s", res.Code, res.Body.String())
		}
		var tab struct {
			Order domain.Order `json:"order"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &tab)
		bar, kit := ticketFor(barista, "bar", tab.Order.ID), ticketFor(kitchen, "kitchen", tab.Order.ID)
		if bar.RoundID == nil || bar.RoundNumber == nil || *bar.RoundNumber != 1 {
			t.Fatalf("tab tickets must belong to round 1: %+v", bar)
		}
		mustMove(kitchen, kit.ID, "queued", "preparing")
		var roundStatus string
		inStore(func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &roundStatus, `SELECT status FROM order_rounds WHERE store_id = $1 AND id = $2`, storeID, *kit.RoundID)
		})
		if roundStatus != "preparing" || orderStatus(tab.Order.ID) != "open" {
			t.Fatalf("progress lives on the round while the tab stays open, got round=%s order=%s", roundStatus, orderStatus(tab.Order.ID))
		}

		coffeeBefore, toastBefore := stock(coffee), stock(toast)
		if res := call("POST", fmt.Sprintf("/pos/orders/%s/void", tab.Order.ID), cashier, map[string]any{"reason": "Tamu pergi", "pin": "482913"}); res.Code != http.StatusOK {
			t.Fatalf("void tab failed: %d: %s", res.Code, res.Body.String())
		}
		if stock(coffee) != coffeeBefore+1 || stock(toast) != toastBefore {
			t.Fatalf("queued bar line must be restocked and started kitchen line wasted: coffee %d->%d toast %d->%d",
				coffeeBefore, stock(coffee), toastBefore, stock(toast))
		}
		var open int
		var wasted string
		inStore(func(tx *sqlx.Tx) error {
			if err := tx.GetContext(ctx, &open, `SELECT COUNT(*) FROM kds_tickets WHERE store_id = $1 AND order_id = $2 AND status <> 'cancelled'`, storeID, tab.Order.ID); err != nil {
				return err
			}
			return tx.GetContext(ctx, &wasted, `SELECT metadata->>'wasted_lines' FROM audit_logs WHERE store_id = $1 AND action = 'VOID_ORDER'
				AND metadata->>'order_id' = $2`, storeID, tab.Order.ID.String())
		})
		if open != 0 || !strings.Contains(wasted, "-") {
			t.Fatalf("void must cancel every ticket and audit the wasted line, got open=%d wasted=%s", open, wasted)
		}
	})
}
