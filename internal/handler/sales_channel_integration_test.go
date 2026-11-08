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

// TestSalesChannelsIntegration covers v0.11.1 PR 11.7 (X12-X15): a delivery-app channel with its own prices,
// orders priced at them without tax or service, paid only by the platform method, and the channel report.
func TestSalesChannelsIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping sales channel integration test")
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
	orderRepo, paymentRepo := postgres.NewOrderRepository(), postgres.NewPaymentRepository()
	pos := handler.NewPOSHandler(service.NewOrderService(db, orderRepo), service.NewPaymentService(db, paymentRepo, orderRepo, nil), service.NewSettingsService(db))
	ch := handler.NewCatalogHandler(service.NewCatalogService(db))
	sh := handler.NewShiftHandler(service.NewShiftService(db))

	r := gin.New()
	r.POST("/login", ah.Login)
	r.POST("/pos/drawers", ah.RequirePermission(rbac.CashDrawerManage), sh.CreateDrawer)
	r.POST("/pos/shifts", ah.RequireStaff(), sh.OpenShift)
	r.POST("/pos/shifts/:id/force-close", ah.RequireStaff(), sh.ForceCloseShift)
	r.POST("/pos/orders", ah.RequirePermission(rbac.OrderPOSCreate), pos.CreateOrder)
	r.POST("/pos/orders/quote", ah.RequirePermission(rbac.OrderPOSCreate), pos.QuoteOrder)
	r.POST("/pos/orders/:id/payments", ah.RequireStaff(), pos.InitiatePayment)
	r.GET("/pos/sales-channels", ah.RequirePermission(rbac.OrderPOSCreate), ch.ListSalesChannels(true))
	r.POST("/catalog/products", ah.RequirePermission(rbac.CatalogManage), ch.CreateProduct)
	r.DELETE("/catalog/products/:id", ah.RequirePermission(rbac.CatalogManage), ch.ArchiveProduct)
	manage := ah.RequirePermission(rbac.CatalogManage)
	r.GET("/catalog/sales-channels", manage, ch.ListSalesChannels(false))
	r.POST("/catalog/sales-channels", manage, ch.CreateSalesChannel)
	r.PATCH("/catalog/sales-channels/:id", manage, ch.UpdateSalesChannel)
	r.PUT("/catalog/sales-channels/:id/prices", manage, ch.SetChannelPrices)
	r.GET("/reports/channels", ah.RequirePermission(rbac.ReportFinanceRead), handler.NewReportHandler(service.NewReportService(db)).Channels)

	call := func(method, path, authz string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		req.Header.Set("Content-Type", "application/json")
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	login := func(email string) string {
		t.Helper()
		res := call("POST", "/login", "", map[string]string{"store_slug": "central-cafe", "email": email, "pin": "482913"})
		if res.Code != http.StatusOK {
			t.Fatalf("login %s failed: %d", email, res.Code)
		}
		var s struct {
			AccessToken string `json:"access_token"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &s)
		return "Bearer " + s.AccessToken
	}
	inStore := func(fn func(tx *sqlx.Tx) error) {
		t.Helper()
		if err := database.WithTenantTx(ctx, db, storeID, fn); err != nil {
			t.Fatalf("tenant tx failed: %v", err)
		}
	}
	clientError := func(res *httptest.ResponseRecorder, what string) {
		t.Helper()
		if res.Code < 400 || res.Code >= 500 {
			t.Fatalf("%s must be refused with a 4xx, got %d: %s", what, res.Code, res.Body.String())
		}
	}
	owner, cashier := login("abdulaziz543333@gmail.com"), login("cashier.central@mycaffe.local")
	suffix := uuid.NewString()[:6]

	type product struct {
		ID       uuid.UUID `json:"id"`
		Variants []struct {
			ID uuid.UUID `json:"id"`
		} `json:"variants"`
	}
	newProduct := func(name string, price int64) product {
		t.Helper()
		res := call("POST", "/catalog/products", owner, map[string]any{"name": name + " " + suffix, "product_type": "prepared",
			"prep_station": "bar", "variants": []map[string]any{{"name": "Reguler", "price": price, "is_default": true}}})
		if res.Code != http.StatusCreated {
			t.Fatalf("create product failed: %d: %s", res.Code, res.Body.String())
		}
		var p product
		_ = json.Unmarshal(res.Body.Bytes(), &p)
		t.Cleanup(func() { call("DELETE", "/catalog/products/"+p.ID.String(), owner, nil) })
		return p
	}
	latte, tea := newProduct("Latte Kanal", 30000), newProduct("Teh Kanal", 20000)

	var channel struct {
		ID     uuid.UUID `json:"id"`
		Prices []struct {
			VariantID uuid.UUID `json:"variant_id"`
			Price     int64     `json:"price"`
		} `json:"prices"`
	}
	atEnd := t.Cleanup
	t.Run("OwnerManagesChannelsAndPrices", func(t *testing.T) {
		if res := call("POST", "/catalog/sales-channels", cashier, map[string]any{"name": "X"}); res.Code != http.StatusForbidden {
			t.Fatalf("cashier must not manage channels, got %d", res.Code)
		}
		res := call("POST", "/catalog/sales-channels", owner, map[string]any{"name": "GoFood " + suffix, "commission_bps": 2000})
		if res.Code != http.StatusCreated {
			t.Fatalf("create channel failed: %d: %s", res.Code, res.Body.String())
		}
		_ = json.Unmarshal(res.Body.Bytes(), &channel)
		atEnd(func() { // the parent test's cleanup: later subtests still order on this channel
			call("PATCH", "/catalog/sales-channels/"+channel.ID.String(), owner, map[string]any{"is_active": false})
		})
		if res := call("POST", "/catalog/sales-channels", owner, map[string]any{"name": "GoFood " + suffix}); res.Code != http.StatusConflict {
			t.Fatalf("a duplicate channel name must be 409, got %d", res.Code)
		}
		if res := call("POST", "/catalog/sales-channels", owner, map[string]any{"name": "Y " + suffix, "commission_bps": 10001}); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("commission above 100%% must be 422, got %d", res.Code)
		}
		res = call("PUT", "/catalog/sales-channels/"+channel.ID.String()+"/prices", owner, map[string]any{"prices": []map[string]any{
			{"variant_id": latte.Variants[0].ID, "price": 36000}, {"variant_id": tea.Variants[0].ID, "price": 25000}}})
		if res.Code != http.StatusOK {
			t.Fatalf("set channel prices failed: %d: %s", res.Code, res.Body.String())
		}
		// Removing the tea price puts it back on its normal price.
		res = call("PUT", "/catalog/sales-channels/"+channel.ID.String()+"/prices", owner, map[string]any{"prices": []map[string]any{
			{"variant_id": tea.Variants[0].ID, "price": nil}}})
		_ = json.Unmarshal(res.Body.Bytes(), &channel)
		if res.Code != http.StatusOK || len(channel.Prices) != 1 || channel.Prices[0].Price != 36000 {
			t.Fatalf("expected only the latte channel price left: %d: %s", res.Code, res.Body.String())
		}
		if res := call("GET", "/pos/sales-channels", cashier, nil); !strings.Contains(res.Body.String(), channel.ID.String()) {
			t.Fatalf("the POS lists active channels with prices: %d: %s", res.Code, res.Body.String())
		}
	})

	items := []map[string]any{{"product_id": latte.ID, "quantity": 1}, {"product_id": tea.ID, "quantity": 1}}
	t.Run("ChannelPricesWithoutTaxOrService", func(t *testing.T) {
		res := call("POST", "/pos/orders/quote", cashier, map[string]any{"order_type": "takeaway", "sales_channel_id": channel.ID, "items": items})
		var q struct {
			Subtotal int64 `json:"subtotal_amount"`
			Service  int64 `json:"service_charge_amount"`
			Tax      int64 `json:"tax_amount"`
			Total    int64 `json:"total_amount"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &q)
		if res.Code != http.StatusOK || q.Subtotal != 56000 || q.Service != 0 || q.Tax != 0 || q.Total != 56000 {
			t.Fatalf("channel quote is 36.000 + 20.000 with nothing on top: %d: %s", res.Code, res.Body.String())
		}
	})

	var drawer domain.CashDrawer
	_ = json.Unmarshal(call("POST", "/pos/drawers", owner, map[string]any{"name": "Laci Kanal " + suffix}).Body.Bytes(), &drawer)
	var opened struct {
		Shift domain.Shift `json:"shift"`
	}
	if res := call("POST", "/pos/shifts", cashier, map[string]any{"drawer_id": drawer.ID, "opening_float": 0}); res.Code != http.StatusCreated {
		t.Fatalf("open shift failed: %d: %s", res.Code, res.Body.String())
	} else {
		_ = json.Unmarshal(res.Body.Bytes(), &opened)
	}
	t.Cleanup(func() {
		call("POST", fmt.Sprintf("/pos/shifts/%s/force-close", opened.Shift.ID), owner, map[string]any{"reason": "Akhir test kanal"})
	})
	createOrder := func(body map[string]any) domain.Order {
		t.Helper()
		body["idempotency_key"], body["order_type"], body["items"] = "channel-"+uuid.NewString(), "takeaway", items
		res := call("POST", "/pos/orders", cashier, body)
		if res.Code != http.StatusCreated {
			t.Fatalf("create order failed: %d: %s", res.Code, res.Body.String())
		}
		var out struct {
			Order domain.Order `json:"order"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &out)
		return out.Order
	}
	pay := func(orderID uuid.UUID, body map[string]any) *httptest.ResponseRecorder {
		body["idempotency_key"] = "pay-" + uuid.NewString()
		return call("POST", fmt.Sprintf("/pos/orders/%s/payments", orderID), cashier, body)
	}

	t.Run("ChannelOrderPaidOnlyByPlatform", func(t *testing.T) {
		order := createOrder(map[string]any{"customer_name": "GoFood 123", "sales_channel_id": channel.ID})
		if order.TotalAmount != 56000 || order.TaxAmount != 0 {
			t.Fatalf("channel order total must be 56.000 without tax, got %+v", order)
		}
		var name string
		var commission int64
		inStore(func(tx *sqlx.Tx) error {
			return tx.QueryRowxContext(ctx, `SELECT snapshot_channel_name, channel_commission_amount FROM orders WHERE store_id = $1 AND id = $2`,
				storeID, order.ID).Scan(&name, &commission)
		})
		if name != "GoFood "+suffix || commission != 11200 {
			t.Fatalf("the order keeps its channel and 20%% commission: %q %d", name, commission)
		}
		clientError(pay(order.ID, map[string]any{"method": "cash", "cash_tendered_amount": 100000}), "cash on a channel order")
		clientError(pay(order.ID, map[string]any{"method": "platform"}), "platform without the platform order number")
		if res := pay(order.ID, map[string]any{"method": "platform", "reconciliation_ref": "GF-" + suffix}); res.Code != http.StatusOK {
			t.Fatalf("platform payment failed: %d: %s", res.Code, res.Body.String())
		}
		var cashMoves int
		inStore(func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &cashMoves, `SELECT COUNT(*) FROM cash_movements WHERE store_id = $1 AND order_id = $2`, storeID, order.ID)
		})
		if cashMoves != 0 {
			t.Fatal("a platform payment never touches the cash drawer")
		}

		direct := createOrder(map[string]any{"customer_name": "Langsung"})
		clientError(pay(direct.ID, map[string]any{"method": "platform", "reconciliation_ref": "X"}), "platform on a direct order")
	})

	t.Run("InactiveChannelAndReport", func(t *testing.T) {
		other := call("POST", "/catalog/sales-channels", owner, map[string]any{"name": "Grab " + suffix, "is_active": false})
		var off struct {
			ID uuid.UUID `json:"id"`
		}
		_ = json.Unmarshal(other.Body.Bytes(), &off)
		res := call("POST", "/pos/orders", cashier, map[string]any{"idempotency_key": "off-" + uuid.NewString(), "order_type": "takeaway",
			"customer_name": "Grab", "sales_channel_id": off.ID, "items": items})
		clientError(res, "an order on an inactive channel")

		var report struct {
			Rows []struct {
				Channel    string `json:"channel"`
				Orders     int64  `json:"orders"`
				Gross      int64  `json:"gross"`
				Commission int64  `json:"commission"`
				Net        int64  `json:"net"`
			} `json:"rows"`
		}
		res = call("GET", "/reports/channels", owner, nil)
		_ = json.Unmarshal(res.Body.Bytes(), &report)
		for _, row := range report.Rows {
			if row.Channel == "GoFood "+suffix {
				if row.Orders != 1 || row.Gross != 56000 || row.Commission != 11200 || row.Net != 44800 {
					t.Fatalf("unexpected channel row: %+v", row)
				}
				return
			}
		}
		t.Fatalf("no row for the channel: %d: %s", res.Code, res.Body.String())
	})
}
