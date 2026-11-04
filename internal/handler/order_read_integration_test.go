package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
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

// TestIntegrationOrderRead covers v0.7 G-1/G-2: the POS order list (filters, keyset pagination,
// open-tab view, validation, RBAC) and the order detail with allowed_actions per state, in an
// isolated store so other tests' orders never leak into the counts.
func TestIntegrationOrderRead(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	migratorDSN := strings.TrimSpace(os.Getenv("TEST_MIGRATOR_DATABASE_URL"))
	if dsn == "" || migratorDSN == "" {
		t.Skip("TEST_APP_DATABASE_URL / TEST_MIGRATOR_DATABASE_URL not set; skipping order read integration test")
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
	slug := "orders-" + suffix
	email := func(name string) string { return name + ".orders." + suffix + "@mycaffe.local" }
	storeID, ownerID, err := service.BootstrapStore(ctx, migratorDB, service.BootstrapStoreInput{
		Slug: slug, StoreName: "Kafe Daftar", OwnerName: "Pemilik Daftar", OwnerEmail: email("owner"), PIN: "482913",
	})
	if err != nil {
		t.Fatalf("bootstrap store: %v", err)
	}
	if _, err := migratorDB.ExecContext(ctx, `UPDATE stores SET enabled_payment_methods = '["cash"]'::jsonb WHERE id = $1`, storeID); err != nil {
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
	th := handler.NewTabHandler(service.NewTabService(db, postgres.NewTabRepository(), orderRepo))
	oh := handler.NewOrderReadHandler(service.NewOrderReadService(db))

	r := gin.New()
	r.POST("/login", ah.Login)
	r.POST("/pos/drawers", ah.RequirePermission(rbac.CashDrawerManage), sh.CreateDrawer)
	r.POST("/pos/shifts", ah.RequireStaff(), sh.OpenShift)
	r.POST("/pos/orders", ah.RequireStaff(), pos.CreateOrder)
	r.POST("/pos/orders/:id/payments", ah.RequireStaff(), pos.InitiatePayment)
	r.POST("/pos/orders/:id/void", ah.RequirePermission(rbac.OrderVoid), pos.VoidOrder)
	r.POST("/pos/orders/:id/payments/:payment_id/refund", ah.RequirePermission(rbac.PaymentRefund), pos.RefundPayment)
	r.POST("/pos/tabs", ah.RequirePermission(rbac.TabManage), th.OpenTab)
	r.GET("/pos/orders", ah.RequirePermission(rbac.OrderReadStore), oh.List)
	r.GET("/pos/orders/:id", ah.RequirePermission(rbac.OrderReadStore), oh.Get)

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
			t.Fatalf("login %s failed: %d: %s", who, res.Code, res.Body.String())
		}
		var body struct {
			AccessToken string `json:"access_token"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &body)
		return body.AccessToken
	}

	productID := uuid.New()
	if err := database.WithTenantTx(ctx, db, storeID, func(tx *sqlx.Tx) error {
		for _, role := range []string{"cashier", "barista"} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO users (store_id, email, full_name, role, pin_hash, is_active, pin_set_at)
				SELECT store_id, $2, $3, $3, pin_hash, true, NOW() FROM users WHERE store_id = $1 AND id = $4`,
				storeID, email(role), role, ownerID); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `WITH p AS (INSERT INTO products (id, store_id, name, base_price, is_available) VALUES ($1, $2, 'Kopi Daftar', 0, true) RETURNING store_id, id)
			INSERT INTO product_variants (store_id, product_id, name, price, is_default) SELECT store_id, id, 'Reguler', 18000, true FROM p`, productID, storeID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	owner, cashier, barista := login("owner"), login("cashier"), login("barista")
	var drawer domain.CashDrawer
	_ = json.Unmarshal(call("POST", "/pos/drawers", owner, map[string]any{"name": "Laci Daftar"}).Body.Bytes(), &drawer)
	if res := call("POST", "/pos/shifts", cashier, map[string]any{"drawer_id": drawer.ID, "opening_float": 100000}); res.Code != http.StatusCreated {
		t.Fatalf("open shift: %d: %s", res.Code, res.Body.String())
	}

	items := []map[string]any{{"product_id": productID, "quantity": 2}}
	create := func() domain.Order {
		t.Helper()
		res := call("POST", "/pos/orders", cashier, map[string]any{"idempotency_key": "list-" + uuid.NewString(), "order_type": "takeaway", "items": items})
		if res.Code != http.StatusCreated {
			t.Fatalf("create order: %d: %s", res.Code, res.Body.String())
		}
		var body struct {
			Order domain.Order `json:"order"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &body)
		return body.Order
	}
	paidOrder, voided, unpaid := create(), create(), create()
	payRes := call("POST", fmt.Sprintf("/pos/orders/%s/payments", paidOrder.ID), cashier,
		map[string]any{"idempotency_key": "list-pay-" + uuid.NewString(), "method": "cash", "cash_tendered_amount": paidOrder.TotalAmount})
	if payRes.Code != http.StatusOK {
		t.Fatalf("pay: %d: %s", payRes.Code, payRes.Body.String())
	}
	var paidBody struct {
		Payment struct {
			ID uuid.UUID `json:"id"`
		} `json:"payment"`
	}
	_ = json.Unmarshal(payRes.Body.Bytes(), &paidBody)
	if res := call("POST", fmt.Sprintf("/pos/orders/%s/void", voided.ID), owner, map[string]any{"reason": "Salah input", "pin": "482913"}); res.Code != http.StatusOK {
		t.Fatalf("void: %d: %s", res.Code, res.Body.String())
	}
	tabRes := call("POST", "/pos/tabs", cashier, map[string]any{"idempotency_key": "list-tab-" + uuid.NewString(), "table_number": "Meja-7", "initial_items": items})
	if tabRes.Code != http.StatusCreated {
		t.Fatalf("open tab: %d: %s", tabRes.Code, tabRes.Body.String())
	}
	var tabBody struct {
		Order domain.Order `json:"order"`
	}
	_ = json.Unmarshal(tabRes.Body.Bytes(), &tabBody)
	tab := tabBody.Order

	list := func(token, query string) service.OrderList {
		t.Helper()
		res := call("GET", "/pos/orders?"+query, token, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("list %q: %d: %s", query, res.Code, res.Body.String())
		}
		var out service.OrderList
		_ = json.Unmarshal(res.Body.Bytes(), &out)
		return out
	}
	ids := func(l service.OrderList) []uuid.UUID {
		out := []uuid.UUID{}
		for _, o := range l.Items {
			out = append(out, o.ID)
		}
		return out
	}

	t.Run("ListNewestFirstWithKeysetPages", func(t *testing.T) {
		all := list(cashier, "")
		want := []uuid.UUID{tab.ID, unpaid.ID, voided.ID, paidOrder.ID}
		if !slices.Equal(ids(all), want) || all.NextCursor != nil {
			t.Fatalf("want %v newest first without cursor, got %v cursor=%v", want, ids(all), all.NextCursor)
		}
		if all.Items[3].ItemCount != 2 || all.Items[3].PaymentStatus != "paid" || all.Items[3].TotalAmount != paidOrder.TotalAmount {
			t.Fatalf("summary fields wrong: %+v", all.Items[3])
		}
		if strings.Contains(fmt.Sprint(all), "customer_phone") {
			t.Fatal("list must not expose customer_phone")
		}
		p1 := list(cashier, "limit=3")
		if len(p1.Items) != 3 || p1.NextCursor == nil {
			t.Fatalf("page 1 must hold 3 items and a cursor: %v", ids(p1))
		}
		p2 := list(cashier, "limit=3&after="+url.QueryEscape(*p1.NextCursor))
		if !slices.Equal(append(ids(p1), ids(p2)...), want) || p2.NextCursor != nil {
			t.Fatalf("pages must cover every order once: %v + %v", ids(p1), ids(p2))
		}
	})

	t.Run("Filters", func(t *testing.T) {
		if got := ids(list(cashier, "billing_mode=open_tab&status=open")); !slices.Equal(got, []uuid.UUID{tab.ID}) {
			t.Fatalf("open tabs (G-2): %v", got)
		}
		if got := ids(list(cashier, "payment_status=paid")); !slices.Equal(got, []uuid.UUID{paidOrder.ID}) {
			t.Fatalf("paid filter: %v", got)
		}
		if got := ids(list(cashier, "status=cancelled,completed")); !slices.Equal(got, []uuid.UUID{voided.ID}) {
			t.Fatalf("status list filter: %v", got)
		}
		if got := ids(list(cashier, "q="+url.QueryEscape(unpaid.OrderNumber))); !slices.Equal(got, []uuid.UUID{unpaid.ID}) {
			t.Fatalf("search by order number: %v", got)
		}
		if got := list(cashier, "from=2020-01-01&to=2020-01-02"); len(got.Items) != 0 {
			t.Fatalf("past range must be empty: %v", ids(got))
		}
	})

	t.Run("ValidationAndRBAC", func(t *testing.T) {
		for query, field := range map[string]string{
			"status=bogus": "status", "payment_status=lunas": "payment_status", "billing_mode=x": "billing_mode",
			"limit=0": "limit", "limit=101": "limit", "after=bukan-cursor": "after", "from=2026-13-01": "from",
			"from=2026-01-01&to=2026-12-31": "to", "q=a%25": "q", "shift_id=1": "shift_id",
		} {
			res := call("GET", "/pos/orders?"+query, cashier, nil)
			if res.Code != http.StatusUnprocessableEntity || !strings.Contains(res.Body.String(), `"`+field+`"`) {
				t.Fatalf("%s must be 422 on %q, got %d: %s", query, field, res.Code, res.Body.String())
			}
		}
		if res := call("GET", "/pos/orders", barista, nil); res.Code != http.StatusForbidden {
			t.Fatalf("barista list must be 403, got %d", res.Code)
		}
		if res := call("GET", "/pos/orders/"+paidOrder.ID.String(), barista, nil); res.Code != http.StatusForbidden {
			t.Fatalf("barista detail must be 403, got %d", res.Code)
		}
		if res := call("GET", "/pos/orders", "", nil); res.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous list must be 401, got %d", res.Code)
		}
	})

	detail := func(token string, id uuid.UUID) (service.OrderDetail, string) {
		t.Helper()
		res := call("GET", "/pos/orders/"+id.String(), token, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("detail %s: %d: %s", id, res.Code, res.Body.String())
		}
		var out service.OrderDetail
		_ = json.Unmarshal(res.Body.Bytes(), &out)
		return out, res.Body.String()
	}

	t.Run("DetailAllowedActions", func(t *testing.T) {
		d, raw := detail(cashier, unpaid.ID)
		if !slices.Equal(d.AllowedActions, []string{"pay", "void"}) || len(d.Order.Items) != 1 || len(d.Payments) != 0 {
			t.Fatalf("unpaid order: actions %v items %d payments %d", d.AllowedActions, len(d.Order.Items), len(d.Payments))
		}
		if strings.Contains(raw, "idempotency_key") || strings.Contains(raw, "request_hash") {
			t.Fatal("detail must not expose idempotency_key or request_hash")
		}

		d, _ = detail(cashier, paidOrder.ID)
		if !slices.Equal(d.AllowedActions, []string{"refund", "print_receipt"}) || len(d.Payments) != 1 || d.Payments[0].Method != "cash" {
			t.Fatalf("paid order: actions %v payments %+v", d.AllowedActions, d.Payments)
		}

		d, _ = detail(cashier, voided.ID)
		if !slices.Equal(d.AllowedActions, []string{"print_receipt"}) || d.Order.Status != domain.OrderStatusCancelled {
			t.Fatalf("voided order: actions %v status %s", d.AllowedActions, d.Order.Status)
		}

		d, raw = detail(cashier, tab.ID)
		if !slices.Equal(d.AllowedActions, []string{"add_round", "close_tab", "void"}) || len(d.Rounds) != 1 {
			t.Fatalf("open tab: actions %v rounds %d", d.AllowedActions, len(d.Rounds))
		}
		if strings.Contains(raw, "idempotency_key") {
			t.Fatal("rounds must not expose idempotency_key")
		}
	})

	t.Run("DetailRefundsAndIsolation", func(t *testing.T) {
		res := call("POST", fmt.Sprintf("/pos/orders/%s/payments/%s/refund", paidOrder.ID, paidBody.Payment.ID), owner,
			map[string]any{"reason": "Komplain rasa", "pin": "482913", "restore_stock": true})
		if res.Code != http.StatusOK {
			t.Fatalf("refund: %d: %s", res.Code, res.Body.String())
		}
		d, _ := detail(owner, paidOrder.ID)
		if len(d.Refunds) != 1 || d.Refunds[0].Amount != paidOrder.TotalAmount || d.Refunds[0].Method != "cash" ||
			d.Refunds[0].ActorName != "Pemilik Daftar" || slices.Contains(d.AllowedActions, "refund") {
			t.Fatalf("refunded order: refunds %+v actions %v", d.Refunds, d.AllowedActions)
		}
		var foreign uuid.UUID
		_ = migratorDB.GetContext(ctx, &foreign, `SELECT id FROM orders WHERE store_id <> $1 LIMIT 1`, storeID)
		for _, id := range []uuid.UUID{uuid.New(), foreign} {
			if id == uuid.Nil {
				continue
			}
			if res := call("GET", "/pos/orders/"+id.String(), owner, nil); res.Code != http.StatusNotFound {
				t.Fatalf("order %s outside the store must be 404, got %d", id, res.Code)
			}
		}
		if res := call("GET", "/pos/orders/bukan-uuid", owner, nil); res.Code != http.StatusNotFound {
			t.Fatalf("malformed id must be 404, got %d", res.Code)
		}
	})
}
