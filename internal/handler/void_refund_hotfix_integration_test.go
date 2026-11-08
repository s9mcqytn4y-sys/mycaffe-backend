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

// TestVoidRefundHotfix4d1Integration covers the v0.4.3 hotfix of the 4d void/refund flows: money
// guards on void, stock returned exactly once (products and modifier options), round cancellation +
// outbox, split-aware refund restock, explicit restore_stock, cash-only refunds, refunded amounts,
// refund counts on the X report, stock-override tracking rules, and PIN brute-force lockout.
func TestVoidRefundHotfix4d1Integration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping void/refund hotfix integration test")
	}
	db, err := database.NewPostgresDB(&config.Config{DatabaseURL: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	gin.SetMode(gin.TestMode)
	authSvc := service.NewAuthService(db, postgres.NewAuthRepository(), "integration-test-jwt-secret-at-least-32-chars", service.AuthPolicy{
		AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 12 * time.Hour, SessionIdleTimeout: 2 * time.Hour,
		MaxSessions: 3, PINMaxFailures: 5, PINLockout: 15 * time.Minute, MaxConcurrentHashes: 8,
	})
	ah := handler.NewAuthHandler(authSvc)

	orderRepo := postgres.NewOrderRepository()
	paymentRepo := postgres.NewPaymentRepository()
	orderSvc := service.NewOrderService(db, orderRepo)
	paymentSvc := service.NewPaymentService(db, paymentRepo, orderRepo, nil)
	opSvc := service.NewOperationalService(db, orderRepo, paymentRepo, nil, nil, nil)
	pos := handler.NewPOSHandler(orderSvc, paymentSvc, service.NewSettingsService(db))
	pos.SetOperational(opSvc)
	sh := handler.NewShiftHandler(service.NewShiftService(db))
	th := handler.NewTabHandler(service.NewTabService(db, postgres.NewTabRepository(), orderRepo))

	r := gin.New()
	r.POST("/login", ah.Login)
	r.POST("/pos/drawers", ah.RequirePermission(rbac.CashDrawerManage), sh.CreateDrawer)
	r.POST("/pos/shifts", ah.RequireStaff(), sh.OpenShift)
	r.POST("/pos/shifts/:id/close", ah.RequireStaff(), sh.CloseShift)
	r.GET("/pos/shifts/:id/x-report", ah.RequireStaff(), sh.GetXReport)
	r.POST("/pos/orders", ah.RequireStaff(), pos.CreateOrder)
	r.POST("/pos/orders/:id/payments", ah.RequireStaff(), pos.InitiatePayment)
	r.POST("/pos/orders/:id/void", ah.RequirePermission(rbac.OrderVoid), pos.VoidOrder)
	r.POST("/pos/orders/:id/payments/:payment_id/refund", ah.RequirePermission(rbac.PaymentRefund), pos.RefundPayment)
	r.POST("/pos/products/:id/stock-override", ah.RequirePermission(rbac.StockOverride), pos.OverrideStock)
	r.POST("/pos/tabs", ah.RequirePermission(rbac.TabManage), th.OpenTab)
	r.POST("/pos/tabs/:id/rounds", ah.RequirePermission(rbac.TabManage), th.SubmitRound)
	r.GET("/pos/tabs/:id", ah.RequireStaff(), th.GetTab)
	r.POST("/pos/tabs/:id/close", ah.RequirePermission(rbac.TabManage), th.CloseTab)

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

	storeID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	ownerToken := login("central-cafe", "abdulaziz543333@gmail.com")
	cashierToken := login("central-cafe", "cashier.central@mycaffe.local")

	// Dedicated tracked product + tracked modifier option so stock deltas are exact.
	productID, groupID, optionID := uuid.New(), uuid.New(), uuid.New()
	inStore(storeID, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE store_settings SET void_refund_owner_threshold = 100000 WHERE store_id = $1`, storeID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			WITH p AS (INSERT INTO products (id, store_id, name, base_price, is_available, track_stock, stock_quantity)
			VALUES ($1, $2, $3, 10000, true, true, 1000)
				RETURNING store_id, id, base_price)
				INSERT INTO product_variants (store_id, product_id, name, price, is_default) SELECT store_id, id, 'Reguler', base_price, true FROM p`, productID, storeID, "Hotfix Roti "+productID.String()[:6]); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO modifier_groups (id, store_id, name, selection_type, is_required, min_select, max_select)
			VALUES ($1, $2, $3, 'single', false, 0, 1)`, groupID, storeID, "Hotfix Topping "+groupID.String()[:6]); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO modifier_options (id, store_id, group_id, name, price_delta, track_stock, stock_quantity)
			VALUES ($1, $2, $3, 'Keju', 2000, true, 1000)`, optionID, storeID, groupID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO product_modifier_groups (store_id, product_id, group_id) VALUES ($1, $2, $3)`, storeID, productID, groupID)
		return err
	})

	stock := func() (int, int) {
		var p, o int
		inStore(storeID, func(tx *sqlx.Tx) error {
			if err := tx.GetContext(ctx, &p, `SELECT stock_quantity FROM products WHERE store_id = $1 AND id = $2`, storeID, productID); err != nil {
				return err
			}
			return tx.GetContext(ctx, &o, `SELECT stock_quantity FROM modifier_options WHERE store_id = $1 AND id = $2`, storeID, optionID)
		})
		return p, o
	}
	line := func(qty int, withOption bool) map[string]any {
		item := map[string]any{"product_id": productID, "quantity": qty}
		if withOption {
			item["modifiers"] = []map[string]any{{"option_id": optionID, "quantity": 1}}
		}
		return item
	}
	createOrder := func(items ...map[string]any) (uuid.UUID, int64) {
		t.Helper()
		res := call("POST", "/pos/orders", cashierToken, map[string]any{
			"idempotency_key": "hotfix-order-" + uuid.NewString(),
			"order_type":      "takeaway",
			"items":           items,
		})
		if res.Code != http.StatusCreated {
			t.Fatalf("create order failed: %d: %s", res.Code, res.Body.String())
		}
		var body struct {
			Order struct {
				ID          uuid.UUID `json:"id"`
				TotalAmount int64     `json:"total_amount"`
			} `json:"order"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &body)
		return body.Order.ID, body.Order.TotalAmount
	}
	openTab := func(items ...map[string]any) uuid.UUID {
		t.Helper()
		res := call("POST", "/pos/tabs", cashierToken, map[string]any{
			"idempotency_key": "hotfix-tab-" + uuid.NewString(),
			"table_number":    "Meja-Hotfix",
			"initial_items":   items,
		})
		if res.Code != http.StatusCreated {
			t.Fatalf("open tab failed: %d: %s", res.Code, res.Body.String())
		}
		var body struct {
			Order struct {
				ID uuid.UUID `json:"id"`
			} `json:"order"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &body)
		return body.Order.ID
	}
	closeTab := func(orderID uuid.UUID, body map[string]any) []domain.PaymentSplit {
		t.Helper()
		res := call("POST", fmt.Sprintf("/pos/tabs/%s/close", orderID), cashierToken, body)
		if res.Code != http.StatusOK {
			t.Fatalf("close tab failed: %d: %s", res.Code, res.Body.String())
		}
		var out struct {
			Splits []domain.PaymentSplit `json:"splits"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &out)
		return out.Splits
	}

	cashInDrawer := int64(1_000_000)
	pay := func(orderID uuid.UUID, split *domain.PaymentSplit, method string, amount int64) uuid.UUID {
		t.Helper()
		body := map[string]any{"idempotency_key": "hotfix-pay-" + uuid.NewString(), "method": method}
		if split != nil {
			body["split_id"] = split.ID
		}
		if method == "cash" {
			body["cash_tendered_amount"] = amount
		} else {
			body["reconciliation_ref"] = "REF-" + uuid.NewString()[:8]
		}
		res := call("POST", fmt.Sprintf("/pos/orders/%s/payments", orderID), cashierToken, body)
		if res.Code != http.StatusOK {
			t.Fatalf("%s payment failed: %d: %s", method, res.Code, res.Body.String())
		}
		if method == "cash" {
			cashInDrawer += amount
		}
		var out struct {
			Payment struct {
				ID uuid.UUID `json:"id"`
			} `json:"payment"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &out)
		return out.Payment.ID
	}
	refund := func(orderID, paymentID uuid.UUID, body map[string]any) *httptest.ResponseRecorder {
		if _, ok := body["pin"]; !ok {
			body["pin"] = "482913"
		}
		if _, ok := body["reason"]; !ok {
			body["reason"] = "Komplain pelanggan"
		}
		return call("POST", fmt.Sprintf("/pos/orders/%s/payments/%s/refund", orderID, paymentID), cashierToken, body)
	}
	void := func(orderID uuid.UUID) *httptest.ResponseRecorder {
		return call("POST", fmt.Sprintf("/pos/orders/%s/void", orderID), cashierToken, map[string]any{"reason": "Salah input", "pin": "482913"})
	}
	orderRow := func(orderID uuid.UUID) (status, paymentStatus, stockStatus string, refunded int64) {
		inStore(storeID, func(tx *sqlx.Tx) error {
			return tx.QueryRowxContext(ctx, `SELECT status, payment_status, stock_status, refunded_amount FROM orders WHERE store_id = $1 AND id = $2`,
				storeID, orderID).Scan(&status, &paymentStatus, &stockStatus, &refunded)
		})
		return
	}

	// Cash refunds and cash tenders need an open shift with enough float.
	drawerRes := call("POST", "/pos/drawers", ownerToken, map[string]any{"name": "Laci Hotfix " + uuid.NewString()[:6]})
	if drawerRes.Code != http.StatusCreated {
		t.Fatalf("create drawer failed: %d: %s", drawerRes.Code, drawerRes.Body.String())
	}
	var drawer domain.CashDrawer
	_ = json.Unmarshal(drawerRes.Body.Bytes(), &drawer)
	shiftRes := call("POST", "/pos/shifts", cashierToken, map[string]any{"drawer_id": drawer.ID, "opening_float": cashInDrawer})
	if shiftRes.Code != http.StatusCreated {
		t.Fatalf("open shift failed: %d: %s", shiftRes.Code, shiftRes.Body.String())
	}
	var opened struct {
		Shift domain.Shift `json:"shift"`
	}
	_ = json.Unmarshal(shiftRes.Body.Bytes(), &opened)
	shiftID := opened.Shift.ID
	refundsMade := 0

	t.Run("VoidReservedOrderRestoresProductAndOptionOnce", func(t *testing.T) {
		p0, o0 := stock()
		orderID, _ := createOrder(line(2, true))
		if p, o := stock(); p != p0-2 || o != o0-2 {
			t.Fatalf("order must reserve 2/2, got product %d option %d (from %d/%d)", p, o, p0, o0)
		}
		if res := void(orderID); res.Code != http.StatusOK {
			t.Fatalf("void failed: %d: %s", res.Code, res.Body.String())
		}
		if p, o := stock(); p != p0 || o != o0 {
			t.Fatalf("void must restore product and option stock, got %d/%d want %d/%d", p, o, p0, o0)
		}
		if status, _, stockStatus, _ := orderRow(orderID); status != "cancelled" || stockStatus != "released" {
			t.Fatalf("expected cancelled/released, got %s/%s", status, stockStatus)
		}
		if res := void(orderID); res.Code != http.StatusConflict {
			t.Fatalf("second void must be 409, got %d", res.Code)
		}
		if p, o := stock(); p != p0 || o != o0 {
			t.Fatalf("second void must not restore again, got %d/%d", p, o)
		}

		// A late settlement after void re-deducts once (released -> committed), never twice.
		if _, err := orderSvc.ConfirmOrderPaymentAndCommitStock(ctx, storeID, orderID); err != nil {
			t.Fatalf("late settlement failed: %v", err)
		}
		if p, o := stock(); p != p0-2 || o != o0-2 {
			t.Fatalf("late settlement must deduct exactly once, got %d/%d want %d/%d", p, o, p0-2, o0-2)
		}
	})

	t.Run("VoidTabCancelsRoundsRestoresStockAndWritesOutbox", func(t *testing.T) {
		p0, o0 := stock()
		tabID := openTab(line(2, true))
		round := call("POST", fmt.Sprintf("/pos/tabs/%s/rounds", tabID), cashierToken, map[string]any{
			"idempotency_key": "hotfix-round-" + uuid.NewString(),
			"items":           []map[string]any{line(1, false)},
		})
		if round.Code != http.StatusCreated && round.Code != http.StatusOK {
			t.Fatalf("submit round failed: %d: %s", round.Code, round.Body.String())
		}
		if p, o := stock(); p != p0-3 || o != o0-2 {
			t.Fatalf("rounds must commit 3/2, got %d/%d", p, o)
		}
		if res := void(tabID); res.Code != http.StatusOK {
			t.Fatalf("void tab failed: %d: %s", res.Code, res.Body.String())
		}
		if p, o := stock(); p != p0 || o != o0 {
			t.Fatalf("void tab must restore all rounds, got %d/%d want %d/%d", p, o, p0, o0)
		}
		var openRounds, events int
		inStore(storeID, func(tx *sqlx.Tx) error {
			if err := tx.GetContext(ctx, &openRounds, `SELECT COUNT(*) FROM order_rounds WHERE store_id = $1 AND order_id = $2 AND status <> 'cancelled'`, storeID, tabID); err != nil {
				return err
			}
			return tx.GetContext(ctx, &events, `SELECT COUNT(*) FROM outbox_events WHERE store_id = $1 AND aggregate_id = $2 AND event_type = 'order.cancelled'`, storeID, tabID.String())
		})
		if openRounds != 0 || events != 1 {
			t.Fatalf("expected all rounds cancelled and one order.cancelled event, got %d open rounds, %d events", openRounds, events)
		}
	})

	t.Run("VoidRefusedWhenOneSplitPaid", func(t *testing.T) {
		tabID := openTab(line(2, false))
		splits := closeTab(tabID, map[string]any{"strategy": "equal", "split_count": 2})
		pay(tabID, &splits[0], "cash", splits[0].TotalAmount)
		res := void(tabID)
		if res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "ORDER_HAS_PAYMENTS") {
			t.Fatalf("expected 409 ORDER_HAS_PAYMENTS, got %d: %s", res.Code, res.Body.String())
		}
		pay(tabID, &splits[1], "cash", splits[1].TotalAmount) // settle so the shift balances
	})

	t.Run("VoidRefusedWhenQRISPaymentPending", func(t *testing.T) {
		orderID, total := createOrder(line(1, false))
		paymentID := uuid.New()
		inStore(storeID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO payments (id, store_id, order_id, attempt_no, idempotency_key, request_hash,
					provider, provider_order_id, amount, method, status)
				VALUES ($1, $2, $3, 1, $4, $5, 'midtrans', $4, $6, 'qris', 'pending')`,
				paymentID, storeID, orderID, paymentID.String(), strings.Repeat("b", 64), total)
			return err
		})
		res := void(orderID)
		if res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "ORDER_HAS_PAYMENTS") {
			t.Fatalf("expected 409 ORDER_HAS_PAYMENTS, got %d: %s", res.Code, res.Body.String())
		}
	})

	t.Run("RefundRequiresExplicitRestoreStockAndCash", func(t *testing.T) {
		orderID, total := createOrder(line(1, false))
		paymentID := pay(orderID, nil, "cash", total)
		res := refund(orderID, paymentID, map[string]any{})
		if res.Code != http.StatusUnprocessableEntity || !strings.Contains(res.Body.String(), "restore_stock") {
			t.Fatalf("expected 422 with restore_stock field, got %d: %s", res.Code, res.Body.String())
		}
		p0, _ := stock()
		if res := refund(orderID, paymentID, map[string]any{"restore_stock": false}); res.Code != http.StatusOK {
			t.Fatalf("waste refund failed: %d: %s", res.Code, res.Body.String())
		}
		cashInDrawer -= total
		refundsMade++
		if p, _ := stock(); p != p0 {
			t.Fatalf("restore_stock=false must not restock, got %d want %d", p, p0)
		}
		if _, ps, ss, refunded := orderRow(orderID); ps != "refunded" || refunded != total || ss != "committed" {
			t.Fatalf("expected refunded/%d/committed, got %s/%d/%s", total, ps, refunded, ss)
		}

		edcOrder, edcTotal := createOrder(line(1, false))
		edcPayment := pay(edcOrder, nil, "edc", edcTotal)
		// RF1-c: a manual bank transfer is the owner's alone; a cashier is refused and nothing changes.
		res = refund(edcOrder, edcPayment, map[string]any{"restore_stock": true, "refund_method": "bank_transfer",
			"bank_name": "BCA", "account_last4": "1234", "transfer_reference": "TRF-1"})
		if res.Code != http.StatusForbidden {
			t.Fatalf("cashier bank-transfer refund must be 403, got %d: %s", res.Code, res.Body.String())
		}
		if _, ps, _, refunded := orderRow(edcOrder); ps != "paid" || refunded != 0 {
			t.Fatalf("refused refund must leave the order paid, got %s/%d", ps, refunded)
		}
	})

	t.Run("EqualSplitRefundRestoresStockOnceWhenFullyRefunded", func(t *testing.T) {
		tabID := openTab(line(3, true))
		splits := closeTab(tabID, map[string]any{"strategy": "equal", "split_count": 3})
		payments := make([]uuid.UUID, len(splits))
		var total int64
		for i := range splits {
			payments[i] = pay(tabID, &splits[i], "cash", splits[i].TotalAmount)
			total += splits[i].TotalAmount
		}
		p0, o0 := stock()
		for i, paymentID := range payments {
			res := refund(tabID, paymentID, map[string]any{"restore_stock": true})
			if res.Code != http.StatusOK {
				t.Fatalf("refund split %d failed: %d: %s", i+1, res.Code, res.Body.String())
			}
			cashInDrawer -= splits[i].TotalAmount
			refundsMade++
			wantP, wantO := p0, o0
			if i == len(payments)-1 {
				wantP, wantO = p0+3, o0+3
			}
			if p, o := stock(); p != wantP || o != wantO {
				t.Fatalf("after refund %d stock %d/%d, want %d/%d", i+1, p, o, wantP, wantO)
			}
		}
		if _, ps, ss, refunded := orderRow(tabID); ps != "refunded" || ss != "released" || refunded != total {
			t.Fatalf("expected refunded/released/%d, got %s/%s/%d", total, ps, ss, refunded)
		}
		var refundedSplits int
		inStore(storeID, func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &refundedSplits, `SELECT COUNT(*) FROM payment_splits WHERE store_id = $1 AND order_id = $2 AND status = 'refunded'`, storeID, tabID)
		})
		if refundedSplits != 3 {
			t.Fatalf("expected 3 refunded splits, got %d", refundedSplits)
		}
		if res := refund(tabID, payments[0], map[string]any{"restore_stock": true}); res.Code != http.StatusConflict {
			t.Fatalf("second refund of the same payment must be 409, got %d", res.Code)
		}
	})

	t.Run("ByItemTwoSplitsReleaseStockAndAuditWaste", func(t *testing.T) {
		tabID := openTab(line(2, true), line(1, false))
		detail := call("GET", fmt.Sprintf("/pos/tabs/%s", tabID), cashierToken, nil)
		var tab struct {
			Rounds []struct {
				Items []domain.OrderItem `json:"items"`
			} `json:"rounds"`
		}
		_ = json.Unmarshal(detail.Body.Bytes(), &tab)
		var withOption, plain uuid.UUID
		for _, round := range tab.Rounds {
			for _, it := range round.Items {
				if len(it.Options) > 0 {
					withOption = it.ID
				} else {
					plain = it.ID
				}
			}
		}
		if withOption == uuid.Nil || plain == uuid.Nil {
			t.Fatalf("expected one line with and one without option: %s", detail.Body.String())
		}
		splits := closeTab(tabID, map[string]any{"strategy": "by_item", "splits": []map[string]any{
			{"label": "Tamu 1", "items": []map[string]any{{"order_item_id": withOption, "quantity": 1}}},
			{"label": "Tamu 2", "items": []map[string]any{{"order_item_id": withOption, "quantity": 1}, {"order_item_id": plain, "quantity": 1}}},
		}})
		first := pay(tabID, &splits[0], "cash", splits[0].TotalAmount)
		secondPayment := pay(tabID, &splits[1], "cash", splits[1].TotalAmount)

		p0, o0 := stock()
		if res := refund(tabID, first, map[string]any{"restore_stock": true}); res.Code != http.StatusOK {
			t.Fatalf("by-item refund failed: %d: %s", res.Code, res.Body.String())
		}
		cashInDrawer -= splits[0].TotalAmount
		refundsMade++
		if p, o := stock(); p != p0+1 || o != o0+1 {
			t.Fatalf("by-item refund must restore 1/1, got %d/%d from %d/%d", p, o, p0, o0)
		}
		if _, ps, ss, refunded := orderRow(tabID); ps != "partially_refunded" || ss != "committed" || refunded != splits[0].TotalAmount {
			t.Fatalf("expected partially_refunded/committed/%d, got %s/%s/%d", splits[0].TotalAmount, ps, ss, refunded)
		}

		// F3: the last split is wasted; the order is then fully refunded with every split processed, so
		// stock_status is released without restocking again, and the audit lists the wasted lines.
		second := secondPayment
		if res := refund(tabID, second, map[string]any{"restore_stock": false}); res.Code != http.StatusOK {
			t.Fatalf("by-item waste refund failed: %d: %s", res.Code, res.Body.String())
		}
		cashInDrawer -= splits[1].TotalAmount
		refundsMade++
		if p, o := stock(); p != p0+1 || o != o0+1 {
			t.Fatalf("waste refund must not restock, got %d/%d want %d/%d", p, o, p0+1, o0+1)
		}
		if _, ps, ss, _ := orderRow(tabID); ps != "refunded" || ss != "released" {
			t.Fatalf("expected refunded/released after every split, got %s/%s", ps, ss)
		}
		var wasted map[string]int
		var stockAfter string
		inStore(storeID, func(tx *sqlx.Tx) error {
			var raw []byte
			if err := tx.QueryRowxContext(ctx, `SELECT metadata->'wasted_lines', metadata->>'stock_after' FROM audit_logs
				WHERE store_id = $1 AND action = 'REFUND' AND reference_id = $2`, storeID, second.String()).Scan(&raw, &stockAfter); err != nil {
				return err
			}
			return json.Unmarshal(raw, &wasted)
		})
		if wasted[withOption.String()] != 1 || wasted[plain.String()] != 1 || stockAfter != "released" {
			t.Fatalf("audit must record wasted lines and released stock, got %v / %s", wasted, stockAfter)
		}
	})

	t.Run("XReportShowsRefundsAndShiftBalances", func(t *testing.T) {
		res := call("GET", fmt.Sprintf("/pos/shifts/%s/x-report", shiftID), ownerToken, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("x-report failed: %d: %s", res.Code, res.Body.String())
		}
		var report domain.XReport
		_ = json.Unmarshal(res.Body.Bytes(), &report)
		if report.Summary.RefundCount != refundsMade || report.Summary.CashRefunds == 0 {
			t.Fatalf("expected %d refunds on the X report, got %+v", refundsMade, report.Summary)
		}
		closeRes := call("POST", fmt.Sprintf("/pos/shifts/%s/close", shiftID), cashierToken, map[string]any{"counted_cash": cashInDrawer})
		if closeRes.Code != http.StatusOK {
			t.Fatalf("shift must balance at %d: %d: %s", cashInDrawer, closeRes.Code, closeRes.Body.String())
		}
	})

	t.Run("StockOverrideTrackingRules", func(t *testing.T) {
		untracked := uuid.New()
		inStore(storeID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `WITH p AS (INSERT INTO products (id, store_id, name, base_price, is_available, track_stock, stock_quantity)
				VALUES ($1, $2, $3, 5000, true, false, 0)
				RETURNING store_id, id, base_price)
				INSERT INTO product_variants (store_id, product_id, name, price, is_default) SELECT store_id, id, 'Reguler', base_price, true FROM p`, untracked, storeID, "Hotfix Untracked "+untracked.String()[:6])
			return err
		})
		body := map[string]any{"new_quantity": 10, "reason": "Opname", "owner_pin": "482913"}
		res := call("POST", fmt.Sprintf("/pos/products/%s/stock-override", untracked), cashierToken, body)
		if res.Code != http.StatusUnprocessableEntity || !strings.Contains(res.Body.String(), "PRODUCT_NOT_TRACKED") {
			t.Fatalf("expected 422 PRODUCT_NOT_TRACKED, got %d: %s", res.Code, res.Body.String())
		}
		body["enable_tracking"] = true
		if res := call("POST", fmt.Sprintf("/pos/products/%s/stock-override", untracked), cashierToken, body); res.Code != http.StatusOK {
			t.Fatalf("explicit enable_tracking override failed: %d: %s", res.Code, res.Body.String())
		}
		if res := call("POST", fmt.Sprintf("/pos/products/%s/stock-override", uuid.New()), cashierToken, body); res.Code != http.StatusNotFound {
			t.Fatalf("unknown product must be 404, got %d: %s", res.Code, res.Body.String())
		}
	})

	// Brute force runs in the second seeded store so the central owner and cashier stay usable.
	t.Run("PINBruteForceLocksStaffNotOwner", func(t *testing.T) {
		expressID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
		suffix := uuid.NewString()[:8]
		ownerID, cashierID, expressProduct := uuid.New(), uuid.New(), uuid.New()
		ownerEmail, cashierEmail := "owner."+suffix+"@mycaffe.local", "cashier."+suffix+"@mycaffe.local"
		inStore(expressID, func(tx *sqlx.Tx) error {
			for _, u := range []struct {
				id          uuid.UUID
				email, role string
			}{{ownerID, ownerEmail, "owner"}, {cashierID, cashierEmail, "cashier"}} {
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO users (id, store_id, email, full_name, role, pin_hash, is_active, pin_set_at)
					SELECT $1, $2, $3, $4, $5, pin_hash, true, NOW() FROM users WHERE id = 'a0000005-0000-4000-8000-000000000005'`,
					u.id, expressID, u.email, "Hotfix "+u.role, u.role); err != nil {
					return err
				}
			}
			_, err := tx.ExecContext(ctx, `WITH p AS (INSERT INTO products (id, store_id, name, base_price, is_available, track_stock, stock_quantity)
				VALUES ($1, $2, $3, 10000, true, false, 0)
				RETURNING store_id, id, base_price)
				INSERT INTO product_variants (store_id, product_id, name, price, is_default) SELECT store_id, id, 'Reguler', base_price, true FROM p`, expressProduct, expressID, "Hotfix Express "+suffix)
			return err
		})
		token := login("express-counter", cashierEmail)

		// Owner PIN: five wrong guesses, then the per-actor cap answers 429 even for the right PIN. The owner
		// account is not locked by someone else guessing (HF5, review follow-up F1).
		override := func(pin string) *httptest.ResponseRecorder {
			return call("POST", fmt.Sprintf("/pos/products/%s/stock-override", expressProduct), token,
				map[string]any{"new_quantity": 1, "reason": "Opname", "owner_pin": pin, "enable_tracking": true})
		}
		for i := 1; i <= 5; i++ {
			if res := override("000000"); res.Code != http.StatusUnauthorized {
				t.Fatalf("wrong owner PIN #%d: expected 401, got %d: %s", i, res.Code, res.Body.String())
			}
		}
		if res := override("482913"); res.Code != http.StatusTooManyRequests {
			t.Fatalf("expected 429 after 5 wrong owner PINs, got %d: %s", res.Code, res.Body.String())
		}
		var failedAudits int
		var ownerLocked bool
		inStore(expressID, func(tx *sqlx.Tx) error {
			if err := tx.GetContext(ctx, &failedAudits, `SELECT COUNT(*) FROM audit_logs WHERE store_id = $1 AND action = 'OWNER_PIN_FAILED' AND actor_id = $2`, expressID, cashierID); err != nil {
				return err
			}
			return tx.GetContext(ctx, &ownerLocked, `SELECT COALESCE(locked_until > NOW(), false) FROM users WHERE store_id = $1 AND id = $2`, expressID, ownerID)
		})
		if failedAudits != 5 || ownerLocked {
			t.Fatalf("expected 5 OWNER_PIN_FAILED audits and an unlocked owner, got %d audits, locked=%v", failedAudits, ownerLocked)
		}
		login("express-counter", ownerEmail)

		// Own PIN: the same counter as login, so five wrong PINs lock the cashier's account.
		orderRes := call("POST", "/pos/orders", token, map[string]any{
			"idempotency_key": "hotfix-express-" + suffix, "order_type": "takeaway",
			"items": []map[string]any{{"product_id": expressProduct, "quantity": 1}},
		})
		if orderRes.Code != http.StatusCreated {
			t.Fatalf("express order failed: %d: %s", orderRes.Code, orderRes.Body.String())
		}
		var created struct {
			Order struct {
				ID uuid.UUID `json:"id"`
			} `json:"order"`
		}
		_ = json.Unmarshal(orderRes.Body.Bytes(), &created)
		voidWith := func(pin string) *httptest.ResponseRecorder {
			return call("POST", fmt.Sprintf("/pos/orders/%s/void", created.Order.ID), token, map[string]any{"reason": "Salah input", "pin": pin})
		}
		for i := 1; i <= 5; i++ {
			if res := voidWith("000000"); res.Code != http.StatusUnauthorized {
				t.Fatalf("wrong own PIN #%d: expected 401, got %d: %s", i, res.Code, res.Body.String())
			}
		}
		// The lock also ends the running session: Authenticate rejects a locked account.
		if res := voidWith("482913"); res.Code != http.StatusUnauthorized || !strings.Contains(res.Body.String(), "SESSION_INVALID") {
			t.Fatalf("expected 401 SESSION_INVALID after 5 wrong own PINs, got %d: %s", res.Code, res.Body.String())
		}
		if res := call("POST", "/login", "", map[string]string{"store_slug": "express-counter", "email": cashierEmail, "pin": "482913"}); res.Code != http.StatusUnauthorized {
			t.Fatalf("locked cashier must not log in, got %d", res.Code)
		}
	})
}
