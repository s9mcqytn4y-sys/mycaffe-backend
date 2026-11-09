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

// TestStampCardIntegration covers v0.11.1 PR 11.8 (X16, X17): the program, phone lookup, earning on paid
// orders at or above the minimum, redeeming one free unit, and stamps of refunded orders no longer counting.
func TestStampCardIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping stamp card integration test")
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
	cu := handler.NewCustomerHandler(service.NewCustomerService(db, nil))

	r := gin.New()
	create := ah.RequirePermission(rbac.OrderPOSCreate)
	manage := ah.RequirePermission(rbac.CatalogManage)
	r.POST("/login", ah.Login)
	r.POST("/pos/drawers", ah.RequirePermission(rbac.CashDrawerManage), sh.CreateDrawer)
	r.POST("/pos/shifts", ah.RequireStaff(), sh.OpenShift)
	r.POST("/pos/shifts/:id/force-close", ah.RequireStaff(), sh.ForceCloseShift)
	r.POST("/pos/orders", create, pos.CreateOrder)
	r.POST("/pos/orders/quote", create, pos.QuoteOrder)
	r.POST("/pos/orders/:id/payments", ah.RequireStaff(), pos.InitiatePayment)
	r.POST("/pos/customers/lookup", create, cu.LookupCustomer)
	r.POST("/catalog/products", manage, ch.CreateProduct)
	r.DELETE("/catalog/products/:id", manage, ch.ArchiveProduct)
	r.GET("/catalog/stamp-program", manage, ch.GetStampProgram)
	r.PUT("/catalog/stamp-program", manage, ch.SetStampProgram)

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
	owner, cashier := login("abdulaziz543333@gmail.com"), login("cashier.central@mycaffe.local")
	suffix := uuid.NewString()[:6]

	res := call("POST", "/catalog/products", owner, map[string]any{"name": "Kopi Stamp " + suffix, "product_type": "prepared",
		"prep_station": "bar", "variants": []map[string]any{{"name": "Reguler", "price": 30000, "is_default": true}}})
	if res.Code != http.StatusCreated {
		t.Fatalf("create product failed: %d: %s", res.Code, res.Body.String())
	}
	var product struct {
		ID uuid.UUID `json:"id"`
	}
	_ = json.Unmarshal(res.Body.Bytes(), &product)
	t.Cleanup(func() { call("DELETE", "/catalog/products/"+product.ID.String(), owner, nil) })

	program := func(active bool, rewards []uuid.UUID) map[string]any {
		return map[string]any{"is_active": active, "stamps_required": 2, "min_order_amount": 25000, "stamp_valid_days": 30, "reward_product_ids": rewards}
	}
	t.Cleanup(func() { call("PUT", "/catalog/stamp-program", owner, program(false, []uuid.UUID{})) })
	t.Run("OwnerSetsTheProgram", func(t *testing.T) {
		if res := call("PUT", "/catalog/stamp-program", cashier, program(true, []uuid.UUID{product.ID})); res.Code != http.StatusForbidden {
			t.Fatalf("cashier must not manage the program, got %d", res.Code)
		}
		if res := call("PUT", "/catalog/stamp-program", owner, program(true, []uuid.UUID{})); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("an active program needs a reward, got %d", res.Code)
		}
		if res := call("PUT", "/catalog/stamp-program", owner, program(true, []uuid.UUID{uuid.New()})); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("an unknown reward product must be 422, got %d", res.Code)
		}
		if res := call("PUT", "/catalog/stamp-program", owner, program(true, []uuid.UUID{product.ID})); res.Code != http.StatusOK {
			t.Fatalf("set program failed: %d: %s", res.Code, res.Body.String())
		}
	})

	digits := fmt.Sprintf("%08d", time.Now().UnixNano()%100_000_000)
	newCustomer := func(prefix string, verified bool) uuid.UUID {
		t.Helper()
		id := uuid.New()
		inStore(func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO customers (id, store_id, phone, name, phone_verified_at)
				VALUES ($1, $2, $3, 'Pelanggan Stamp', CASE WHEN $4 THEN NOW() END)`, id, storeID, "+62"+prefix+digits, verified)
			return err
		})
		return id
	}
	customer, unverified := newCustomer("812", true), newCustomer("813", false)
	type stamps struct {
		Stamps      int64 `json:"stamps"`
		RewardReady bool  `json:"reward_ready"`
	}
	lookup := func() stamps {
		t.Helper()
		res := call("POST", "/pos/customers/lookup", cashier, map[string]string{"phone": "0812" + digits})
		if res.Code != http.StatusOK {
			t.Fatalf("lookup failed: %d: %s", res.Code, res.Body.String())
		}
		var out stamps
		_ = json.Unmarshal(res.Body.Bytes(), &out)
		return out
	}

	var drawer domain.CashDrawer
	_ = json.Unmarshal(call("POST", "/pos/drawers", owner, map[string]any{"name": "Laci Stamp " + suffix}).Body.Bytes(), &drawer)
	var opened struct {
		Shift domain.Shift `json:"shift"`
	}
	if res := call("POST", "/pos/shifts", cashier, map[string]any{"drawer_id": drawer.ID, "opening_float": 0}); res.Code != http.StatusCreated {
		t.Fatalf("open shift failed: %d: %s", res.Code, res.Body.String())
	} else {
		_ = json.Unmarshal(res.Body.Bytes(), &opened)
	}
	t.Cleanup(func() {
		call("POST", fmt.Sprintf("/pos/shifts/%s/force-close", opened.Shift.ID), owner, map[string]any{"reason": "Akhir test stamp"})
	})

	order := func(extra map[string]any, quantity int) *httptest.ResponseRecorder {
		body := map[string]any{"idempotency_key": "stamp-" + uuid.NewString(), "order_type": "takeaway", "customer_name": "Stamp",
			"items": []map[string]any{{"product_id": product.ID, "quantity": quantity}}}
		for k, v := range extra {
			body[k] = v
		}
		return call("POST", "/pos/orders", cashier, body)
	}
	paidOrder := func(extra map[string]any, quantity int) domain.Order {
		t.Helper()
		res := order(extra, quantity)
		if res.Code != http.StatusCreated {
			t.Fatalf("create order failed: %d: %s", res.Code, res.Body.String())
		}
		var out struct {
			Order domain.Order `json:"order"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &out)
		if res := call("POST", fmt.Sprintf("/pos/orders/%s/payments", out.Order.ID), cashier, map[string]any{
			"idempotency_key": "pay-" + uuid.NewString(), "method": "cash", "cash_tendered_amount": 1_000_000}); res.Code != http.StatusOK {
			t.Fatalf("payment failed: %d: %s", res.Code, res.Body.String())
		}
		return out.Order
	}

	t.Run("PaidOrdersEarnOneStampEach", func(t *testing.T) {
		if res := order(map[string]any{"customer_id": unverified}, 1); res.Code < 400 || res.Code >= 500 {
			t.Fatalf("an unverified customer must be refused, got %d", res.Code)
		}
		if res := order(map[string]any{"customer_id": customer, "reward_item_index": 0}, 1); res.Code < 400 || res.Code >= 500 {
			t.Fatalf("a reward without enough stamps must be refused, got %d", res.Code)
		}
		paidOrder(map[string]any{}, 1) // no customer: no stamp anywhere
		paidOrder(map[string]any{"customer_id": customer}, 1)
		if got := lookup(); got.Stamps != 1 || got.RewardReady {
			t.Fatalf("one paid order is one stamp: %+v", got)
		}
		paidOrder(map[string]any{"customer_id": customer}, 2)
		if got := lookup(); got.Stamps != 2 || !got.RewardReady {
			t.Fatalf("two stamps make the reward ready: %+v", got)
		}
	})

	t.Run("RewardGivesOneUnitFreeAndSpendsTheStamps", func(t *testing.T) {
		q := call("POST", "/pos/orders/quote", cashier, map[string]any{"order_type": "takeaway", "customer_id": customer, "reward_item_index": 0,
			"items": []map[string]any{{"product_id": product.ID, "quantity": 2}}})
		var quote struct {
			Subtotal int64 `json:"subtotal_amount"`
		}
		_ = json.Unmarshal(q.Body.Bytes(), &quote)
		if q.Code != http.StatusOK || quote.Subtotal != 30000 {
			t.Fatalf("two units with one free cost 30.000: %d: %s", q.Code, q.Body.String())
		}
		o := paidOrder(map[string]any{"customer_id": customer, "reward_item_index": 0}, 2)
		if o.SubtotalAmount != 30000 {
			t.Fatalf("reward order subtotal must be 30.000, got %d", o.SubtotalAmount)
		}
		// Both old stamps are spent; the reward order itself is 30.000, above the minimum, and earns one.
		if got := lookup(); got.Stamps != 1 || got.RewardReady {
			t.Fatalf("expected the two stamps spent and one new: %+v", got)
		}
		var redeemed, audits int
		inStore(func(tx *sqlx.Tx) error {
			if err := tx.GetContext(ctx, &redeemed, `SELECT COUNT(*) FROM customer_stamps WHERE store_id = $1 AND redeemed_order_id = $2`, storeID, o.ID); err != nil {
				return err
			}
			return tx.GetContext(ctx, &audits, `SELECT COUNT(*) FROM audit_logs WHERE store_id = $1 AND action = 'STAMP_REWARD_REDEEMED' AND reference_id = $2`, storeID, o.OrderNumber)
		})
		if redeemed != 2 || audits != 1 {
			t.Fatalf("expected 2 redeemed stamps and 1 audit, got %d and %d", redeemed, audits)
		}
	})

	t.Run("BelowMinimumEarnsNothingAndRefundsStopCounting", func(t *testing.T) {
		paidOrder(map[string]any{"customer_id": customer}, 1)
		before := lookup().Stamps
		if before != 2 {
			t.Fatalf("expected 2 stamps before the refund, got %d", before)
		}
		var last uuid.UUID
		inStore(func(tx *sqlx.Tx) error {
			if err := tx.GetContext(ctx, &last, `SELECT order_id FROM customer_stamps WHERE store_id = $1 AND customer_id = $2 AND redeemed_order_id IS NULL
				ORDER BY earned_at DESC LIMIT 1`, storeID, customer); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `UPDATE orders SET payment_status = 'refunded', refunded_amount = total_amount WHERE store_id = $1 AND id = $2`, storeID, last)
			return err
		})
		if got := lookup(); got.Stamps != 1 {
			t.Fatalf("a fully refunded order no longer counts: %+v", got)
		}
		if res := call("PUT", "/catalog/stamp-program", owner, map[string]any{"is_active": true, "stamps_required": 2, "min_order_amount": 99_000_000,
			"stamp_valid_days": 30, "reward_product_ids": []uuid.UUID{product.ID}}); res.Code != http.StatusOK {
			t.Fatalf("raise minimum failed: %d: %s", res.Code, res.Body.String())
		}
		paidOrder(map[string]any{"customer_id": customer}, 1)
		if got := lookup(); got.Stamps != 1 {
			t.Fatalf("an order below the minimum earns nothing: %+v", got)
		}
	})
}
