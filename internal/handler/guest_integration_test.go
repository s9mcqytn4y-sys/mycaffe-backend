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
	"github.com/jmoiron/sqlx"
	"github.com/midtrans/midtrans-go/coreapi"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/handler"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/midtrans"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

// qrisGateway answers every charge with a pending dynamic QRIS, like the Midtrans sandbox.
type qrisGateway struct{}

func (qrisGateway) ChargeCoreAPI(_ context.Context, in midtrans.ChargeRequest) (*coreapi.ChargeResponse, error) {
	return &coreapi.ChargeResponse{
		StatusCode: "201", TransactionID: "tx-" + in.OrderID, OrderID: in.OrderID,
		GrossAmount: fmt.Sprintf("%d.00", in.Amount), PaymentType: string(in.Method), TransactionStatus: "pending",
		FraudStatus: "accept", QRString: "00020101021226610014COM.GO-JEK.WWW" + in.OrderID,
	}, nil
}

func (qrisGateway) CheckTransaction(context.Context, string) (*coreapi.TransactionStatusResponse, error) {
	return nil, errors.New("not found")
}

// TestGuestQROrderingIteration5bIntegration covers guest sessions (Q6), pay-first QR orders (T2), the
// pause / store-closed refusals (QR4), unpaid caps and validation (QR5), own-orders isolation,
// QRIS payment ownership, and the public tracker (QR6).
func TestGuestQROrderingIteration5bIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping guest QR ordering integration test")
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

	r := gin.New()
	r.POST("/login", ah.Login)
	r.POST("/pos/drawers", ah.RequirePermission(rbac.CashDrawerManage), sh.CreateDrawer)
	r.POST("/pos/shifts", ah.RequireStaff(), sh.OpenShift)
	r.POST("/pos/shifts/:id/force-close", ah.RequireStaff(), sh.ForceCloseShift)
	r.POST("/public/qr/:token/session", gh.StartSession)
	r.GET("/public/orders/track/:token", gh.Track)
	guest := r.Group("/public/guest", gh.RequireGuest())
	guest.GET("/session", gh.Session)
	guest.GET("/orders", gh.ListOrders)
	guest.POST("/orders", gh.CreateOrder)
	guest.POST("/orders/:id/payments", gh.InitiatePayment)

	type req struct {
		cookie, bearer string
		csrf           bool
	}
	call := func(method, path string, o req, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		r0 := httptest.NewRequest(method, path, &buf)
		r0.Header.Set("Content-Type", "application/json")
		if o.csrf {
			r0.Header.Set(handler.CSRFHeader, "1")
		}
		if o.cookie != "" {
			r0.AddCookie(&http.Cookie{Name: handler.GuestCookieName, Value: o.cookie})
		}
		if o.bearer != "" {
			r0.Header.Set("Authorization", "Bearer "+o.bearer)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, r0)
		return w
	}
	inStore := func(storeID uuid.UUID, fn func(tx *sqlx.Tx) error) {
		t.Helper()
		if err := database.WithTenantTx(ctx, db, storeID, fn); err != nil {
			t.Fatalf("tenant tx failed: %v", err)
		}
	}
	startSession := func(qr string) (int, string, string) {
		res := call("POST", "/public/qr/"+qr+"/session", req{csrf: true}, nil)
		for _, ck := range res.Result().Cookies() {
			if ck.Name == handler.GuestCookieName {
				if !ck.HttpOnly || !ck.Secure || ck.SameSite != http.SameSiteStrictMode || ck.Path != "/api/v1/public" {
					t.Fatalf("guest cookie flags wrong: %+v", ck)
				}
				return res.Code, ck.Value, res.Body.String()
			}
		}
		return res.Code, "", res.Body.String()
	}

	// Store setup: a tracked product, a central table, an open shift (QR4), and an express table.
	productID, qrToken, expressQR := uuid.New(), "gq"+strings.ReplaceAll(uuid.NewString(), "-", ""), "gx"+strings.ReplaceAll(uuid.NewString(), "-", "")
	inStore(centralID, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			WITH p AS (INSERT INTO products (id, store_id, name, base_price, is_available, track_stock, stock_quantity)
				VALUES ($1, $2, $3, 0, true, true, 100) RETURNING store_id, id)
			INSERT INTO product_variants (store_id, product_id, name, price, is_default) SELECT store_id, id, 'Reguler', 18000, true FROM p`,
			productID, centralID, "Guest Kopi "+productID.String()[:6]); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO tables (store_id, table_number, qr_token) VALUES ($1, $2, $3)`, centralID, "G-"+qrToken[:6], qrToken)
		return err
	})
	inStore(expressID, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO tables (store_id, table_number, qr_token) VALUES ($1, $2, $3)`, expressID, "GX-"+expressQR[:6], expressQR)
		return err
	})
	loginRes := call("POST", "/login", req{}, map[string]string{"store_slug": "central-cafe", "email": "owner.central@mycaffe.local", "pin": "482913"})
	var owner struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal(loginRes.Body.Bytes(), &owner)
	var drawer domain.CashDrawer
	_ = json.Unmarshal(call("POST", "/pos/drawers", req{bearer: owner.AccessToken}, map[string]any{"name": "Laci Tamu " + uuid.NewString()[:6]}).Body.Bytes(), &drawer)
	shiftRes := call("POST", "/pos/shifts", req{bearer: owner.AccessToken}, map[string]any{"drawer_id": drawer.ID, "opening_float": 0})
	if shiftRes.Code != http.StatusCreated {
		t.Fatalf("open shift failed: %d: %s", shiftRes.Code, shiftRes.Body.String())
	}
	var opened struct {
		Shift domain.Shift `json:"shift"`
	}
	_ = json.Unmarshal(shiftRes.Body.Bytes(), &opened)
	t.Cleanup(func() {
		call("POST", fmt.Sprintf("/pos/shifts/%s/force-close", opened.Shift.ID), req{bearer: owner.AccessToken}, map[string]any{"reason": "Akhir test tamu"})
	})

	stock := func() int {
		var n int
		inStore(centralID, func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &n, `SELECT stock_quantity FROM products WHERE store_id = $1 AND id = $2`, centralID, productID)
		})
		return n
	}
	order := func(cookie, key string, qty int) *httptest.ResponseRecorder {
		return call("POST", "/public/guest/orders", req{cookie: cookie, csrf: true}, map[string]any{
			"idempotency_key": key, "items": []map[string]any{{"product_id": productID, "quantity": qty}},
		})
	}

	var deviceA, deviceB string
	t.Run("SessionRefusalsAndStart", func(t *testing.T) {
		if res := call("POST", "/public/qr/"+qrToken+"/session", req{}, nil); res.Code != http.StatusForbidden {
			t.Fatalf("missing CSRF header must be 403, got %d", res.Code)
		}
		if code, _, _ := startSession("nope-" + strings.Repeat("z", 30)); code != http.StatusNotFound {
			t.Fatalf("unknown QR must be 404, got %d", code)
		}
		if code, _, body := startSession(expressQR); code != http.StatusConflict || !strings.Contains(body, "STORE_CLOSED") {
			t.Fatalf("store without an open shift must be 409 STORE_CLOSED, got %d: %s", code, body)
		}
		inStore(centralID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO store_settings (store_id, qr_ordering_paused) VALUES ($1, true)
				ON CONFLICT (store_id) DO UPDATE SET qr_ordering_paused = true`, centralID)
			return err
		})
		code, _, body := startSession(qrToken)
		inStore(centralID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE store_settings SET qr_ordering_paused = false WHERE store_id = $1`, centralID)
			return err
		})
		if code != http.StatusConflict || !strings.Contains(body, "QR_ORDERING_PAUSED") {
			t.Fatalf("paused store must be 409 QR_ORDERING_PAUSED, got %d: %s", code, body)
		}

		if code, deviceA, body = startSession(qrToken); code != http.StatusCreated || deviceA == "" {
			t.Fatalf("start session failed: %d: %s", code, body)
		}
		if code, deviceB, body = startSession(qrToken); code != http.StatusCreated {
			t.Fatalf("second device failed: %d: %s", code, body)
		}
		var sessions int
		inStore(centralID, func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &sessions, `SELECT COUNT(DISTINCT g.table_session_id) FROM guest_sessions g
				JOIN table_sessions ts ON ts.id = g.table_session_id AND ts.store_id = g.store_id
				JOIN tables t ON t.id = ts.table_id AND t.store_id = ts.store_id
				WHERE g.store_id = $1 AND t.qr_token = $2 AND ts.status = 'open'`, centralID, qrToken)
		})
		if sessions != 1 {
			t.Fatalf("both devices must join one open table session, got %d", sessions)
		}
	})

	t.Run("CookieIsRequiredAndUnforgeable", func(t *testing.T) {
		if res := call("GET", "/public/guest/session", req{cookie: deviceA}, nil); res.Code != http.StatusOK {
			t.Fatalf("session read failed: %d: %s", res.Code, res.Body.String())
		}
		if res := call("GET", "/public/guest/session", req{}, nil); res.Code != http.StatusUnauthorized {
			t.Fatalf("missing cookie must be 401, got %d", res.Code)
		}
		forged := deviceA[:strings.LastIndex(deviceA, ".")+1] + strings.Repeat("A", 43)
		if res := call("GET", "/public/guest/session", req{cookie: forged}, nil); res.Code != http.StatusUnauthorized {
			t.Fatalf("forged secret must be 401, got %d", res.Code)
		}
	})

	var orderA1 uuid.UUID
	var trackingToken string
	t.Run("PayFirstOrderReservesStockAndBindsTable", func(t *testing.T) {
		if res := call("POST", "/public/guest/orders", req{cookie: deviceA}, map[string]any{}); res.Code != http.StatusForbidden {
			t.Fatalf("missing CSRF on order must be 403, got %d", res.Code)
		}
		if res := order(deviceA, "too-many", 21); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("quantity above 20 must be 422, got %d: %s", res.Code, res.Body.String())
		}
		before := stock()
		res := order(deviceA, "a-1", 2)
		if res.Code != http.StatusCreated {
			t.Fatalf("guest order failed: %d: %s", res.Code, res.Body.String())
		}
		var out struct {
			Order         domain.Order `json:"order"`
			TrackingToken string       `json:"tracking_token"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &out)
		orderA1, trackingToken = out.Order.ID, out.TrackingToken
		if out.Order.Status != domain.OrderStatusPendingPayment || out.Order.BillingMode != domain.BillingModePayFirst ||
			out.Order.TableID == nil || out.Order.GuestSessionID == nil || len(trackingToken) != 43 {
			t.Fatalf("expected a pay-first order bound to the table with a tracking token: %+v token=%q", out.Order, trackingToken)
		}
		if after := stock(); after != before-2 {
			t.Fatalf("stock must be reserved: %d -> %d", before, after)
		}
		replay := order(deviceA, "a-1", 2)
		var replayed struct {
			Order              domain.Order `json:"order"`
			TrackingToken      string       `json:"tracking_token"`
			IsIdempotentReplay bool         `json:"is_idempotent_replay"`
		}
		_ = json.Unmarshal(replay.Body.Bytes(), &replayed)
		if replay.Code != http.StatusOK || !replayed.IsIdempotentReplay || replayed.Order.ID != orderA1 || replayed.TrackingToken != "" {
			t.Fatalf("replay must return the same order without a new token: %d %s", replay.Code, replay.Body.String())
		}
		// The same client key on another device is a different order (keys are namespaced per session).
		if res := order(deviceB, "a-1", 1); res.Code != http.StatusCreated {
			t.Fatalf("device B order failed: %d: %s", res.Code, res.Body.String())
		}
	})

	t.Run("UnpaidCapAndOwnOrdersOnly", func(t *testing.T) {
		if res := order(deviceA, "a-2", 1); res.Code != http.StatusCreated {
			t.Fatalf("second unpaid order must pass: %d: %s", res.Code, res.Body.String())
		}
		if res := order(deviceA, "a-3", 1); res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "UNPAID_ORDER_LIMIT") {
			t.Fatalf("third unpaid order must be 409 UNPAID_ORDER_LIMIT, got %d: %s", res.Code, res.Body.String())
		}
		list := func(cookie string) int {
			var body struct {
				Orders []json.RawMessage `json:"orders"`
			}
			_ = json.Unmarshal(call("GET", "/public/guest/orders", req{cookie: cookie}, nil).Body.Bytes(), &body)
			return len(body.Orders)
		}
		if a, b := list(deviceA), list(deviceB); a != 2 || b != 1 {
			t.Fatalf("each device must see only its own orders, got A=%d B=%d", a, b)
		}
	})

	t.Run("QRISPaymentOnlyForOwnOrder", func(t *testing.T) {
		url := fmt.Sprintf("/public/guest/orders/%s/payments", orderA1)
		if res := call("POST", url, req{cookie: deviceB, csrf: true}, map[string]any{"idempotency_key": "pay-1"}); res.Code != http.StatusNotFound {
			t.Fatalf("another device must not pay this order, got %d: %s", res.Code, res.Body.String())
		}
		res := call("POST", url, req{cookie: deviceA, csrf: true}, map[string]any{"idempotency_key": "pay-1"})
		if res.Code != http.StatusOK {
			t.Fatalf("QRIS payment failed: %d: %s", res.Code, res.Body.String())
		}
		var out struct {
			Payment struct {
				Method string `json:"method"`
				Status string `json:"status"`
			} `json:"payment"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &out)
		if out.Payment.Method != "qris" || out.Payment.Status != "pending" {
			t.Fatalf("expected a pending dynamic QRIS payment, got %+v", out.Payment)
		}
	})

	t.Run("PublicTracker", func(t *testing.T) {
		res := call("GET", "/public/orders/track/"+trackingToken, req{}, nil)
		var tracked postgres.TrackedOrder
		_ = json.Unmarshal(res.Body.Bytes(), &tracked)
		if res.Code != http.StatusOK || tracked.Status != string(domain.OrderStatusPendingPayment) || tracked.OrderNumber == "" {
			t.Fatalf("tracker failed: %d: %s", res.Code, res.Body.String())
		}
		if res := call("GET", "/public/orders/track/"+strings.Repeat("x", 43), req{}, nil); res.Code != http.StatusNotFound {
			t.Fatalf("unknown tracking token must be 404, got %d", res.Code)
		}
	})

	t.Run("ClosedTableOrExpiredSessionEndsGuestAccess", func(t *testing.T) {
		parts := strings.Split(deviceB, ".")
		inStore(centralID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE guest_sessions SET created_at = NOW() - INTERVAL '4 hours', expires_at = NOW() - INTERVAL '1 hour' WHERE store_id = $1 AND id = $2`, centralID, parts[1])
			return err
		})
		if res := call("GET", "/public/guest/session", req{cookie: deviceB}, nil); res.Code != http.StatusUnauthorized || !strings.Contains(res.Body.String(), "GUEST_SESSION_EXPIRED") {
			t.Fatalf("expired session must be 401 GUEST_SESSION_EXPIRED, got %d", res.Code)
		}
		inStore(centralID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE table_sessions SET status = 'closed', closed_at = NOW(), close_reason = 'cashier'
				WHERE store_id = $1 AND id = (SELECT table_session_id FROM guest_sessions WHERE store_id = $1 AND id = $2)`, centralID, strings.Split(deviceA, ".")[1])
			return err
		})
		if res := call("GET", "/public/guest/orders", req{cookie: deviceA}, nil); res.Code != http.StatusUnauthorized {
			t.Fatalf("closed table session must end guest access, got %d", res.Code)
		}
	})
}
