package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/handler"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/realtime"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

type wsEvent struct {
	Seq     int64          `json:"seq"`
	Type    string         `json:"type"`
	StoreID string         `json:"store_id"`
	Payload map[string]any `json:"payload"`
}

// TestRealtimeIteration5cIntegration covers the outbox relay, per-store sequence, channel routing
// (POS, KDS station, guest), replay by last_seq, resync_required, and the single-use ticket rules.
func TestRealtimeIteration5cIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping realtime integration test")
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
	orderRepo := postgres.NewOrderRepository()
	orderSvc := service.NewOrderService(db, orderRepo)
	paymentSvc := service.NewPaymentService(db, postgres.NewPaymentRepository(), orderRepo, qrisGateway{})
	gh := handler.NewGuestHandler(service.NewGuestService(db, orderSvc, paymentSvc))
	sh := handler.NewShiftHandler(service.NewShiftService(db))
	hub := realtime.NewHub([]string{"http://localhost:3000"})
	// Short re-check interval so revocation tests close connections within a second.
	hub.SetLimits(realtime.DefaultLimits, 200*time.Millisecond)
	go hub.Run()
	t.Cleanup(hub.Shutdown)
	rtSvc := service.NewRealtimeService(db)
	rh := handler.NewRealtimeHandler(hub, rtSvc)

	r := gin.New()
	r.POST("/login", ah.Login)
	r.POST("/pos/drawers", ah.RequirePermission(rbac.CashDrawerManage), sh.CreateDrawer)
	r.POST("/pos/shifts", ah.RequireStaff(), sh.OpenShift)
	r.POST("/pos/shifts/:id/force-close", ah.RequireStaff(), sh.ForceCloseShift)
	r.POST("/pos/ws-ticket", ah.RequireStaff(), rh.IssueStaffTicket)
	r.POST("/public/qr/:token/session", gh.StartSession)
	guest := r.Group("/public/guest", gh.RequireGuest())
	guest.POST("/orders", gh.CreateOrder)
	guest.POST("/ws-ticket", rh.IssueGuestTicket)
	r.GET("/ws", rh.Connect)
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)

	call := func(method, path, bearer, cookie string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(handler.CSRFHeader, "1")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: handler.GuestCookieName, Value: cookie})
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	login := func(slug, email string) string {
		t.Helper()
		res := call("POST", "/login", "", "", map[string]string{"store_slug": slug, "email": email, "pin": "482913"})
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
	ticket := func(path, bearer, cookie string) service.WSTicket {
		t.Helper()
		res := call("POST", path, bearer, cookie, nil)
		if res.Code != http.StatusCreated {
			t.Fatalf("ticket failed: %d: %s", res.Code, res.Body.String())
		}
		var tk service.WSTicket
		_ = json.Unmarshal(res.Body.Bytes(), &tk)
		return tk
	}
	dial := func(tk string, lastSeq int64) (*websocket.Conn, int) {
		url := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws?ticket=" + tk
		if lastSeq >= 0 {
			url += fmt.Sprintf("&last_seq=%d", lastSeq)
		}
		conn, resp, err := websocket.DefaultDialer.Dial(url, http.Header{"Origin": {"http://localhost:3000"}})
		if err != nil {
			if resp != nil {
				return nil, resp.StatusCode
			}
			t.Fatalf("dial failed: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn, http.StatusSwitchingProtocols
	}
	// next waits for an event matching pred; it fails after 3 s.
	next := func(conn *websocket.Conn, pred func(wsEvent) bool) wsEvent {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			_ = conn.SetReadDeadline(deadline)
			var ev wsEvent
			if err := conn.ReadJSON(&ev); err != nil {
				t.Fatalf("no matching event: %v", err)
			}
			if pred(ev) {
				return ev
			}
		}
	}
	relay := func() {
		t.Helper()
		if err := rtSvc.RelayOnce(ctx, hub); err != nil {
			t.Fatalf("relay failed: %v", err)
		}
	}
	// drain relays the backlog other packages left in the shared store before a client connects: a backlog
	// above the client's 64-message buffer would otherwise crowd out the event this test waits for.
	drain := func() {
		t.Helper()
		for range 20 {
			relay()
			var pending int
			inStore(centralID, func(tx *sqlx.Tx) error {
				return tx.GetContext(ctx, &pending, `SELECT COUNT(*) FROM outbox_events WHERE store_id = $1 AND processed_at IS NULL`, centralID)
			})
			if pending == 0 {
				return
			}
		}
	}

	productID := uuid.New()
	inStore(centralID, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `
			WITH p AS (INSERT INTO products (id, store_id, name, base_price, is_available) VALUES ($1, $2, $3, 0, true) RETURNING store_id, id)
			INSERT INTO product_variants (store_id, product_id, name, price, is_default) SELECT store_id, id, 'Reguler', 15000, true FROM p`,
			productID, centralID, "RT Teh "+productID.String()[:6])
		return err
	})
	createOrder := func() *service.CreateOrderResult {
		t.Helper()
		res, err := orderSvc.CreateOrder(ctx, centralID, service.OrderCreationInput{
			IdempotencyKey: "rt-" + uuid.NewString(), CustomerName: "RT", OrderType: domain.OrderTypeTakeaway,
			Items: []service.OrderItemInput{{ProductID: productID, Quantity: 1}},
		})
		if err != nil {
			t.Fatalf("create order: %v", err)
		}
		return res
	}
	isOrder := func(typ string, id uuid.UUID) func(wsEvent) bool {
		return func(ev wsEvent) bool { return ev.Type == typ && ev.Payload["order_id"] == id.String() }
	}

	// newGuest opens a QR guest session on a new table and returns its cookie.
	newGuest := func() string {
		t.Helper()
		qr := "rt" + strings.ReplaceAll(uuid.NewString(), "-", "")
		inStore(centralID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO tables (store_id, table_number, qr_token) VALUES ($1, $2, $3)`, centralID, "RT-"+qr[:6], qr)
			return err
		})
		start := call("POST", "/public/qr/"+qr+"/session", "", "", nil)
		for _, ck := range start.Result().Cookies() {
			if ck.Name == handler.GuestCookieName {
				return ck.Value
			}
		}
		t.Fatalf("guest session failed: %d: %s", start.Code, start.Body.String())
		return ""
	}
	// closesWithin reports whether the server closes conn within d (events received meanwhile are skipped).
	closesWithin := func(conn *websocket.Conn, d time.Duration) bool {
		_ = conn.SetReadDeadline(time.Now().Add(d))
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				var netErr interface{ Timeout() bool }
				return !(errors.As(err, &netErr) && netErr.Timeout())
			}
		}
	}

	cashier := login("central-cafe", "cashier.central@mycaffe.local")
	kitchen := login("central-cafe", "kitchen.central@mycaffe.local")

	var firstSeq int64
	t.Run("RelayDeliversToPOSWithSequence", func(t *testing.T) {
		tk := ticket("/pos/ws-ticket", cashier, "")
		if !strings.Contains(strings.Join(tk.Channels, ","), realtime.ChannelPOS) {
			t.Fatalf("cashier must get the POS channel, got %v", tk.Channels)
		}
		drain()
		conn, code := dial(tk.Ticket, -1)
		if code != http.StatusSwitchingProtocols {
			t.Fatalf("dial failed with %d", code)
		}
		if _, code := dial(tk.Ticket, -1); code != http.StatusUnauthorized {
			t.Fatalf("a ticket is single-use, second dial got %d", code)
		}
		res := createOrder()
		relay()
		ev := next(conn, isOrder("order.created", res.Order.ID))
		if ev.Seq <= 0 || ev.StoreID != centralID.String() {
			t.Fatalf("event must carry store and sequence: %+v", ev)
		}
		firstSeq = ev.Seq
		second := createOrder()
		relay()
		if ev2 := next(conn, isOrder("order.created", second.Order.ID)); ev2.Seq <= firstSeq {
			t.Fatalf("sequence must increase: %d then %d", firstSeq, ev2.Seq)
		}
	})

	t.Run("KitchenChannelGetsOnlyKDSEvents", func(t *testing.T) {
		tk := ticket("/pos/ws-ticket", kitchen, "")
		if strings.Contains(strings.Join(tk.Channels, ","), realtime.ChannelPOS) {
			t.Fatalf("kitchen must not get the POS channel, got %v", tk.Channels)
		}
		drain()
		conn, _ := dial(tk.Ticket, -1)
		res := createOrder()
		if _, err := orderSvc.CancelOrExpireUnpaidOrder(ctx, centralID, res.Order.ID, domain.OrderPaymentStatusUnpaid, "test"); err != nil {
			t.Fatalf("cancel: %v", err)
		}
		relay()
		// Events arrive in sequence order, so order.created (seq n) would come before order.cancelled (n+1).
		next(conn, func(ev wsEvent) bool {
			if isOrder("order.created", res.Order.ID)(ev) {
				t.Fatalf("kitchen must not receive order.created")
			}
			return isOrder("order.cancelled", res.Order.ID)(ev)
		})
	})

	t.Run("ReplayAfterLastSeq", func(t *testing.T) {
		// Other packages write events to this store in parallel; replay from an event created just now
		// so the gap stays far below the 500-event resync limit.
		res := createOrder()
		relay()
		var seq int64
		inStore(centralID, func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &seq, `SELECT store_seq FROM outbox_events WHERE store_id = $1 AND event_type = 'order.created' AND aggregate_id = $2`,
				centralID, res.Order.ID.String())
		})
		conn, _ := dial(ticket("/pos/ws-ticket", cashier, "").Ticket, seq-1)
		ev := next(conn, func(ev wsEvent) bool {
			if ev.Type == "resync_required" {
				t.Fatalf("replay of one event must not need a resync")
			}
			return isOrder("order.created", res.Order.ID)(ev)
		})
		if ev.Seq != seq {
			t.Fatalf("replay must resend seq %d, got %d", seq, ev.Seq)
		}
	})

	t.Run("ResyncWhenTooFarBehind", func(t *testing.T) {
		inStore(expressID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO outbox_events (store_id, event_type, aggregate_type, aggregate_id, payload, processed_at)
				SELECT $1, 'test.bulk', 'order', gen_random_uuid()::text, '{}'::jsonb, NOW() FROM generate_series(1, 501)`, expressID)
			return err
		})
		express := login("express-counter", "cashier.express@mycaffe.local")
		conn, _ := dial(ticket("/pos/ws-ticket", express, "").Ticket, 0)
		next(conn, func(ev wsEvent) bool { return ev.Type == "resync_required" })
	})

	t.Run("ExpiredTicketAndRevokedSessionAreRejected", func(t *testing.T) {
		tk := ticket("/pos/ws-ticket", cashier, "")
		ticketID := strings.Split(tk.Ticket, ".")[1]
		inStore(centralID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE ws_tickets SET created_at = NOW() - INTERVAL '2 minutes', expires_at = NOW() - INTERVAL '1 minute'
				WHERE store_id = $1 AND id = $2`, centralID, ticketID)
			return err
		})
		if _, code := dial(tk.Ticket, -1); code != http.StatusUnauthorized {
			t.Fatalf("expired ticket must be 401, got %d", code)
		}
		fresh := login("central-cafe", "cashier.central@mycaffe.local")
		tk = ticket("/pos/ws-ticket", fresh, "")
		inStore(centralID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE auth_sessions SET revoked_at = NOW(), revoked_reason = 'logout'
				WHERE store_id = $1 AND id = (SELECT auth_session_id FROM ws_tickets WHERE store_id = $1 AND id = $2)`,
				centralID, strings.Split(tk.Ticket, ".")[1])
			return err
		})
		if _, code := dial(tk.Ticket, -1); code != http.StatusUnauthorized {
			t.Fatalf("ticket of a revoked session must be 401, got %d", code)
		}
	})

	t.Run("GuestSeesOnlyOwnOrderWithMinimalPayload", func(t *testing.T) {
		owner := login("central-cafe", "abdulaziz543333@gmail.com")
		var drawer domain.CashDrawer
		_ = json.Unmarshal(call("POST", "/pos/drawers", owner, "", map[string]any{"name": "Laci RT " + uuid.NewString()[:6]}).Body.Bytes(), &drawer)
		var opened struct {
			Shift domain.Shift `json:"shift"`
		}
		_ = json.Unmarshal(call("POST", "/pos/shifts", owner, "", map[string]any{"drawer_id": drawer.ID, "opening_float": 0}).Body.Bytes(), &opened)
		t.Cleanup(func() {
			call("POST", fmt.Sprintf("/pos/shifts/%s/force-close", opened.Shift.ID), owner, "", map[string]any{"reason": "Akhir test realtime"})
		})
		cookie := newGuest()
		tk := ticket("/public/guest/ws-ticket", "", cookie)
		if len(tk.Channels) != 1 || !strings.HasPrefix(tk.Channels[0], realtime.ChannelGuestPrefix) {
			t.Fatalf("guest must get only its own channel, got %v", tk.Channels)
		}
		drain()
		conn, _ := dial(tk.Ticket, -1)

		other := createOrder()
		orderRes := call("POST", "/public/guest/orders", "", cookie, map[string]any{
			"idempotency_key": "rt-guest", "items": []map[string]any{{"product_id": productID, "quantity": 1}},
		})
		if orderRes.Code != http.StatusCreated {
			t.Fatalf("guest order failed: %d: %s", orderRes.Code, orderRes.Body.String())
		}
		var placed struct {
			Order domain.Order `json:"order"`
		}
		_ = json.Unmarshal(orderRes.Body.Bytes(), &placed)
		relay()
		ev := next(conn, func(ev wsEvent) bool { return ev.Type == "order.created" })
		if ev.Payload["order_id"] != placed.Order.ID.String() {
			t.Fatalf("guest received someone else's order: %+v (other order %s)", ev, other.Order.ID)
		}
		if _, leaked := ev.Payload["total_amount"]; leaked {
			t.Fatalf("guest payload must be minimal, got %+v", ev.Payload)
		}
		if ev.Payload["status"] != string(domain.OrderStatusPendingPayment) {
			t.Fatalf("guest payload must carry the order status, got %+v", ev.Payload)
		}
	})

	// Review follow-up: open connections close once the session behind them ends (re-check every 200 ms here).
	t.Run("RevokedStaffSessionClosesOpenConnection", func(t *testing.T) {
		fresh := login("central-cafe", "cashier.central@mycaffe.local")
		tk := ticket("/pos/ws-ticket", fresh, "")
		drain()
		conn, _ := dial(tk.Ticket, -1)
		inStore(centralID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE auth_sessions SET revoked_at = NOW(), revoked_reason = 'logout'
				WHERE store_id = $1 AND id = (SELECT auth_session_id FROM ws_tickets WHERE store_id = $1 AND id = $2)`,
				centralID, strings.Split(tk.Ticket, ".")[1])
			return err
		})
		if !closesWithin(conn, 2*time.Second) {
			t.Fatal("connection of a revoked staff session must close")
		}
	})

	t.Run("DeactivatedStaffClosesOpenConnection", func(t *testing.T) {
		email := "rt.deactivate." + uuid.NewString()[:8] + "@mycaffe.local"
		inStore(centralID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO users (store_id, email, full_name, role, pin_hash, is_active, pin_set_at)
				SELECT store_id, $2, 'RT Nonaktif', 'cashier', pin_hash, true, NOW() FROM users WHERE store_id = $1 AND email = 'cashier.central@mycaffe.local'`,
				centralID, email)
			return err
		})
		drain()
		conn, _ := dial(ticket("/pos/ws-ticket", login("central-cafe", email), "").Ticket, -1)
		inStore(centralID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE users SET is_active = false WHERE store_id = $1 AND email = $2`, centralID, email)
			return err
		})
		if !closesWithin(conn, 2*time.Second) {
			t.Fatal("connection of a deactivated staff member must close")
		}
	})

	t.Run("GuestConnectionCapAndEndedSessionCloses", func(t *testing.T) {
		cookie := newGuest()
		var conns []*websocket.Conn
		for i := 0; i < realtime.DefaultLimits.PerGuest; i++ {
			conn, code := dial(ticket("/public/guest/ws-ticket", "", cookie).Ticket, -1)
			if code != http.StatusSwitchingProtocols {
				t.Fatalf("guest connection %d refused: %d", i+1, code)
			}
			conns = append(conns, conn)
		}
		// The hub registers a connection asynchronously; retry briefly until the cap applies.
		deadline := time.Now().Add(time.Second)
		for {
			_, code := dial(ticket("/public/guest/ws-ticket", "", cookie).Ticket, -1)
			if code == http.StatusTooManyRequests {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("connection over the per-guest cap must be 429, got %d", code)
			}
			time.Sleep(20 * time.Millisecond)
		}
		inStore(centralID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE guest_sessions SET ended_at = NOW()
				WHERE store_id = $1 AND id = (SELECT guest_session_id FROM ws_tickets WHERE store_id = $1 AND guest_session_id IS NOT NULL ORDER BY created_at DESC LIMIT 1)`,
				centralID)
			return err
		})
		for i, conn := range conns {
			if !closesWithin(conn, 2*time.Second) {
				t.Fatalf("guest connection %d must close after the guest session ended", i+1)
			}
		}
	})
}
