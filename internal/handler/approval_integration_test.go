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

// TestIntegrationRemoteApproval covers ADR-007: a cashier asks, the owner approves with their own PIN,
// and void, refund, stock override and shift close each consume the approval exactly once, bound to
// requester, action, reference and the recomputed amount. Rejection, expiry, the pending cap, RBAC,
// audit rows and outbox events are checked in an isolated store.
func TestIntegrationRemoteApproval(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	migratorDSN := strings.TrimSpace(os.Getenv("TEST_MIGRATOR_DATABASE_URL"))
	if dsn == "" || migratorDSN == "" {
		t.Skip("TEST_APP_DATABASE_URL / TEST_MIGRATOR_DATABASE_URL not set; skipping remote approval integration test")
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
	slug := "approval-" + suffix
	email := func(name string) string { return name + ".approval." + suffix + "@mycaffe.local" }
	storeID, ownerID, err := service.BootstrapStore(ctx, migratorDB, service.BootstrapStoreInput{
		Slug: slug, StoreName: "Kafe Approval", OwnerName: "Pemilik Approval", OwnerEmail: email("owner"), PIN: "482913",
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
	ops := service.NewOperationalService(db, orderRepo, paymentRepo, nil, nil, nil)
	pos := handler.NewPOSHandler(service.NewOrderService(db, orderRepo), service.NewPaymentService(db, paymentRepo, orderRepo, nil), service.NewSettingsService(db))
	pos.SetOperational(ops)
	sh := handler.NewShiftHandler(service.NewShiftService(db))
	aph := handler.NewApprovalHandler(service.NewApprovalService(db, ops))

	r := gin.New()
	r.POST("/login", ah.Login)
	r.POST("/pos/drawers", ah.RequirePermission(rbac.CashDrawerManage), sh.CreateDrawer)
	r.POST("/pos/shifts", ah.RequireStaff(), sh.OpenShift)
	r.POST("/pos/shifts/:id/close", ah.RequirePermission(rbac.ShiftClose), sh.CloseShift)
	r.POST("/pos/orders", ah.RequireStaff(), pos.CreateOrder)
	r.POST("/pos/orders/:id/payments", ah.RequireStaff(), pos.InitiatePayment)
	r.POST("/pos/orders/:id/void", ah.RequirePermission(rbac.OrderVoid), pos.VoidOrder)
	r.POST("/pos/orders/:id/payments/:payment_id/refund", ah.RequirePermission(rbac.PaymentRefund), pos.RefundPayment)
	r.POST("/pos/products/:id/stock-override", ah.RequirePermission(rbac.StockOverride), pos.OverrideStock)
	r.POST("/pos/approvals", ah.RequireStaff(), aph.Create)
	r.GET("/pos/approvals/:id", ah.RequireStaff(), aph.Get)
	r.GET("/approvals", ah.RequirePermission(rbac.ApprovalDecide), aph.ListPending)
	r.POST("/approvals/:id/approve", ah.RequirePermission(rbac.ApprovalDecide), aph.Approve)
	r.POST("/approvals/:id/reject", ah.RequirePermission(rbac.ApprovalDecide), aph.Reject)

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

	priceyID, stockID := uuid.New(), uuid.New()
	if err := database.WithTenantTx(ctx, db, storeID, func(tx *sqlx.Tx) error {
		for _, role := range []string{"cashier", "barista"} {
			if _, err := tx.ExecContext(ctx, `INSERT INTO users (store_id, email, full_name, role, pin_hash, is_active, pin_set_at)
				SELECT store_id, $2, $3, $3, pin_hash, true, NOW() FROM users WHERE store_id = $1 AND id = $4`,
				storeID, email(role), role, ownerID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `WITH p AS (INSERT INTO products (id, store_id, name, base_price, is_available) VALUES ($1, $2, 'Paket Besar', 0, true) RETURNING store_id, id)
			INSERT INTO product_variants (store_id, product_id, name, price, is_default) SELECT store_id, id, 'Reguler', 60000, true FROM p`, priceyID, storeID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `WITH p AS (INSERT INTO products (id, store_id, name, base_price, is_available, track_stock, stock_quantity) VALUES ($1, $2, 'Croissant', 0, true, true, 10) RETURNING store_id, id)
			INSERT INTO product_variants (store_id, product_id, name, price, is_default) SELECT store_id, id, 'Reguler', 15000, true FROM p`, stockID, storeID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	owner, cashier, barista := login("owner"), login("cashier"), login("barista")
	var drawer domain.CashDrawer
	_ = json.Unmarshal(call("POST", "/pos/drawers", owner, map[string]any{"name": "Laci Approval"}).Body.Bytes(), &drawer)
	shiftRes := call("POST", "/pos/shifts", cashier, map[string]any{"drawer_id": drawer.ID, "opening_float": 200000})
	if shiftRes.Code != http.StatusCreated {
		t.Fatalf("open shift: %d: %s", shiftRes.Code, shiftRes.Body.String())
	}
	var opened struct {
		Shift domain.Shift `json:"shift"`
	}
	_ = json.Unmarshal(shiftRes.Body.Bytes(), &opened)

	createOrder := func(productID uuid.UUID, qty int) domain.Order {
		t.Helper()
		res := call("POST", "/pos/orders", cashier, map[string]any{"idempotency_key": "ap-" + uuid.NewString(), "order_type": "takeaway",
			"items": []map[string]any{{"product_id": productID, "quantity": qty}}})
		if res.Code != http.StatusCreated {
			t.Fatalf("create order: %d: %s", res.Code, res.Body.String())
		}
		var body struct {
			Order domain.Order `json:"order"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &body)
		return body.Order
	}
	request := func(token string, body map[string]any) (*httptest.ResponseRecorder, service.Approval) {
		t.Helper()
		res := call("POST", "/pos/approvals", token, body)
		var a service.Approval
		_ = json.Unmarshal(res.Body.Bytes(), &a)
		return res, a
	}
	mustRequest := func(body map[string]any) service.Approval {
		t.Helper()
		res, a := request(cashier, body)
		if res.Code != http.StatusCreated || a.Status != "pending" {
			t.Fatalf("request approval %v: %d: %s", body, res.Code, res.Body.String())
		}
		return a
	}
	approve := func(id uuid.UUID) {
		t.Helper()
		if res := call("POST", fmt.Sprintf("/approvals/%s/approve", id), owner, map[string]any{"pin": "482913"}); res.Code != http.StatusOK ||
			!strings.Contains(res.Body.String(), `"status":"approved"`) {
			t.Fatalf("approve: %d: %s", res.Code, res.Body.String())
		}
	}
	code := func(res *httptest.ResponseRecorder) string {
		var b struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &b)
		return b.Code
	}

	t.Run("VoidAboveThresholdThroughThePhone", func(t *testing.T) {
		order := createOrder(priceyID, 2) // Rp120.000 > default Rp100.000 threshold
		voidPath := fmt.Sprintf("/pos/orders/%s/void", order.ID)
		if res := call("POST", voidPath, cashier, map[string]any{"reason": "Batal", "pin": "482913"}); code(res) != "OWNER_PIN_REQUIRED" {
			t.Fatalf("void without owner must need the owner: %d: %s", res.Code, res.Body.String())
		}
		a := mustRequest(map[string]any{"action": "void", "reference_id": order.ID, "reason": "Pelanggan batal", "amount": 1})
		if a.Amount != order.TotalAmount || a.ReferenceLabel != order.OrderNumber || a.RequestedByName != "cashier" {
			t.Fatalf("server must compute amount and label, ignoring the client: %+v (order total %d)", a, order.TotalAmount)
		}
		var pending struct {
			Items []service.Approval `json:"items"`
		}
		_ = json.Unmarshal(call("GET", "/approvals", owner, nil).Body.Bytes(), &pending)
		if len(pending.Items) != 1 || pending.Items[0].ID != a.ID {
			t.Fatalf("owner must see the pending request: %+v", pending.Items)
		}
		if res := call("POST", fmt.Sprintf("/approvals/%s/approve", a.ID), owner, map[string]any{"pin": "905172"}); code(res) != "INVALID_OWNER_PIN" {
			t.Fatalf("wrong owner PIN must be 401 INVALID_OWNER_PIN: %d: %s", res.Code, res.Body.String())
		}
		if res := call("POST", voidPath, cashier, map[string]any{"reason": "Batal", "pin": "482913", "approval_id": a.ID}); code(res) != "APPROVAL_INVALID" {
			t.Fatalf("a pending approval must not void: %d: %s", res.Code, res.Body.String())
		}
		approve(a.ID)
		if res := call("POST", fmt.Sprintf("/approvals/%s/approve", a.ID), owner, map[string]any{"pin": "482913"}); code(res) != "APPROVAL_NOT_PENDING" {
			t.Fatalf("second approve must be 409: %d", res.Code)
		}

		other := createOrder(priceyID, 2)
		if res := call("POST", fmt.Sprintf("/pos/orders/%s/void", other.ID), cashier, map[string]any{"reason": "Batal", "pin": "482913", "approval_id": a.ID}); code(res) != "APPROVAL_INVALID" {
			t.Fatalf("approval for another order must be refused: %d: %s", res.Code, res.Body.String())
		}
		if res := call("POST", voidPath, cashier, map[string]any{"reason": "Batal", "pin": "482913", "approval_id": a.ID}); res.Code != http.StatusOK {
			t.Fatalf("approved void: %d: %s", res.Code, res.Body.String())
		}
		if res := call("POST", fmt.Sprintf("/pos/orders/%s/void", other.ID), cashier, map[string]any{"reason": "Batal", "pin": "482913", "approval_id": a.ID}); code(res) != "APPROVAL_INVALID" {
			t.Fatalf("a consumed approval must not be reused: %d", res.Code)
		}
		var got service.Approval
		_ = json.Unmarshal(call("GET", "/pos/approvals/"+a.ID.String(), cashier, nil).Body.Bytes(), &got)
		if got.Status != "consumed" || got.DecidedByName != "Pemilik Approval" {
			t.Fatalf("approval after use: %+v", got)
		}
		var meta string
		if err := migratorDB.GetContext(ctx, &meta, `SELECT metadata::text FROM audit_logs WHERE store_id = $1 AND action = 'VOID_ORDER' AND reference_id = $2`,
			storeID, order.OrderNumber); err != nil || !strings.Contains(meta, a.ID.String()) || !strings.Contains(meta, ownerID.String()) {
			t.Fatalf("void audit must name the approval and the owner: %s %v", meta, err)
		}
		var events []string
		_ = migratorDB.SelectContext(ctx, &events, `SELECT event_type FROM outbox_events WHERE store_id = $1 AND aggregate_id = $2 ORDER BY store_seq`, storeID, a.ID.String())
		if strings.Join(events, ",") != "approval.requested,approval.decided" {
			t.Fatalf("outbox events: %v", events)
		}
	})

	t.Run("GuardRails", func(t *testing.T) {
		small := createOrder(stockID, 1)
		if res, _ := request(cashier, map[string]any{"action": "void", "reference_id": small.ID, "reason": "x"}); code(res) != "APPROVAL_NOT_NEEDED" {
			t.Fatalf("below threshold must be 409 APPROVAL_NOT_NEEDED: %d: %s", res.Code, res.Body.String())
		}
		if res, _ := request(owner, map[string]any{"action": "stock_override", "reference_id": stockID, "params": map[string]any{"new_quantity": 3}, "reason": "x"}); res.Code != http.StatusForbidden {
			t.Fatalf("owners do not create requests: %d", res.Code)
		}
		if res, _ := request(barista, map[string]any{"action": "void", "reference_id": small.ID, "reason": "x"}); res.Code != http.StatusForbidden {
			t.Fatalf("barista lacks order.void: %d", res.Code)
		}
		if res := call("GET", "/approvals", cashier, nil); res.Code != http.StatusForbidden {
			t.Fatalf("cashier cannot list approvals: %d", res.Code)
		}
		if res, _ := request(cashier, map[string]any{"action": "bogus", "reason": ""}); res.Code != http.StatusUnprocessableEntity ||
			!strings.Contains(res.Body.String(), `"action"`) || !strings.Contains(res.Body.String(), `"reason"`) {
			t.Fatalf("validation: %d: %s", res.Code, res.Body.String())
		}
		if res, _ := request(cashier, map[string]any{"action": "void", "reference_id": uuid.New(), "reason": "x"}); res.Code != http.StatusNotFound {
			t.Fatalf("unknown reference must be 404: %d", res.Code)
		}

		rejected := mustRequest(map[string]any{"action": "stock_override", "reference_id": stockID, "params": map[string]any{"new_quantity": 4}, "reason": "Opname"})
		if res := call("POST", fmt.Sprintf("/approvals/%s/reject", rejected.ID), owner, map[string]any{"note": "Hitung ulang"}); res.Code != http.StatusOK ||
			!strings.Contains(res.Body.String(), `"decision_note":"Hitung ulang"`) {
			t.Fatalf("reject: %d: %s", res.Code, res.Body.String())
		}
		if res := call("POST", fmt.Sprintf("/pos/products/%s/stock-override", stockID), cashier, map[string]any{"new_quantity": 4, "reason": "Opname", "approval_id": rejected.ID}); code(res) != "APPROVAL_INVALID" {
			t.Fatalf("rejected approval must not apply: %d: %s", res.Code, res.Body.String())
		}

		stale := mustRequest(map[string]any{"action": "stock_override", "reference_id": stockID, "params": map[string]any{"new_quantity": 5}, "reason": "Opname"})
		if _, err := migratorDB.ExecContext(ctx, `UPDATE approval_requests SET created_at = NOW() - interval '10 minutes', expires_at = NOW() - interval '5 minutes' WHERE id = $1`, stale.ID); err != nil {
			t.Fatal(err)
		}
		if res := call("POST", fmt.Sprintf("/approvals/%s/approve", stale.ID), owner, map[string]any{"pin": "482913"}); code(res) != "APPROVAL_EXPIRED" {
			t.Fatalf("expired request must be 409 APPROVAL_EXPIRED: %d: %s", res.Code, res.Body.String())
		}
		if res := call("GET", "/pos/approvals/"+stale.ID.String(), cashier, nil); !strings.Contains(res.Body.String(), `"status":"expired"`) {
			t.Fatalf("expired status must be derived: %s", res.Body.String())
		}

		for i := 0; i < 3; i++ {
			mustRequest(map[string]any{"action": "stock_override", "reference_id": stockID, "params": map[string]any{"new_quantity": 20 + i}, "reason": "Opname"})
		}
		if res, _ := request(cashier, map[string]any{"action": "stock_override", "reference_id": stockID, "params": map[string]any{"new_quantity": 30}, "reason": "Opname"}); code(res) != "APPROVAL_LIMIT" {
			t.Fatalf("fourth pending request must be 429 APPROVAL_LIMIT: %d: %s", res.Code, res.Body.String())
		}
		if _, err := migratorDB.ExecContext(ctx, `UPDATE approval_requests SET status = 'rejected', decided_by = $2, decided_at = NOW() WHERE store_id = $1 AND status = 'pending'`, storeID, ownerID); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("StockOverrideBoundToQuantity", func(t *testing.T) {
		a := mustRequest(map[string]any{"action": "stock_override", "reference_id": stockID, "params": map[string]any{"new_quantity": 7}, "reason": "Opname malam"})
		approve(a.ID)
		path := fmt.Sprintf("/pos/products/%s/stock-override", stockID)
		if res := call("POST", path, cashier, map[string]any{"new_quantity": 8, "reason": "Opname malam", "approval_id": a.ID}); code(res) != "APPROVAL_INVALID" {
			t.Fatalf("another quantity must be refused: %d: %s", res.Code, res.Body.String())
		}
		if res := call("POST", path, cashier, map[string]any{"new_quantity": 7, "reason": "Opname malam", "approval_id": a.ID}); res.Code != http.StatusOK {
			t.Fatalf("approved stock override: %d: %s", res.Code, res.Body.String())
		}
	})

	t.Run("RefundAboveThreshold", func(t *testing.T) {
		order := createOrder(priceyID, 2)
		payRes := call("POST", fmt.Sprintf("/pos/orders/%s/payments", order.ID), cashier,
			map[string]any{"idempotency_key": "ap-pay-" + uuid.NewString(), "method": "cash", "cash_tendered_amount": order.TotalAmount})
		var paid struct {
			Payment struct {
				ID uuid.UUID `json:"id"`
			} `json:"payment"`
		}
		_ = json.Unmarshal(payRes.Body.Bytes(), &paid)
		a := mustRequest(map[string]any{"action": "refund", "reference_id": paid.Payment.ID, "reason": "Komplain"})
		approve(a.ID)
		res := call("POST", fmt.Sprintf("/pos/orders/%s/payments/%s/refund", order.ID, paid.Payment.ID), cashier,
			map[string]any{"reason": "Komplain", "pin": "482913", "restore_stock": true, "approval_id": a.ID})
		if res.Code != http.StatusOK {
			t.Fatalf("approved refund: %d: %s", res.Code, res.Body.String())
		}
		var approvedBy uuid.UUID
		if err := migratorDB.GetContext(ctx, &approvedBy, `SELECT approved_by FROM payment_refunds WHERE store_id = $1 AND payment_id = $2`, storeID, paid.Payment.ID); err != nil || approvedBy != ownerID {
			t.Fatalf("refund ledger must record the approving owner: %s %v", approvedBy, err)
		}
	})

	t.Run("ShiftVarianceBoundToCountedCash", func(t *testing.T) {
		counted := int64(50000) // far below the expected cash: variance above the Rp5.000 tolerance
		a := mustRequest(map[string]any{"action": "shift_variance", "reference_id": opened.Shift.ID, "params": map[string]any{"counted_cash": counted}, "reason": "Uang kurang"})
		if a.Amount >= 0 || a.ReferenceLabel != "Laci Approval" {
			t.Fatalf("variance must be negative and labelled with the drawer: %+v", a)
		}
		approve(a.ID)
		path := fmt.Sprintf("/pos/shifts/%s/close", opened.Shift.ID)
		if res := call("POST", path, cashier, map[string]any{"counted_cash": counted + 1000, "variance_reason": "Uang kurang", "approval_id": a.ID}); code(res) != "APPROVAL_INVALID" {
			t.Fatalf("another counted cash must be refused: %d: %s", res.Code, res.Body.String())
		}
		if res := call("POST", path, cashier, map[string]any{"counted_cash": counted, "variance_reason": "Uang kurang", "approval_id": a.ID}); res.Code != http.StatusOK {
			t.Fatalf("approved shift close: %d: %s", res.Code, res.Body.String())
		}
	})
}
