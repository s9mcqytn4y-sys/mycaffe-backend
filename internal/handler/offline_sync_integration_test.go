package handler_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// TestOfflineSyncIntegration covers v0.10 PR 10.2 (ADR-005 OF1/OF3/OF6/OF8/OF9, W16): re-pricing from
// the served snapshot, official numbering, payment into the open shift, idempotent re-sync, rejections
// stored for the owner, owner accept/reject, offline PIN failures and cross-tenant refusal.
func TestOfflineSyncIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping offline sync integration test")
	}
	db, err := database.NewPostgresDB(&config.Config{DatabaseURL: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	storeID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	expressID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	americano := uuid.MustParse("d0000003-0000-4000-8000-000000000003")

	gin.SetMode(gin.TestMode)
	authSvc := service.NewAuthService(db, postgres.NewAuthRepository(), "integration-test-jwt-secret-at-least-32-chars", service.AuthPolicy{
		AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 12 * time.Hour, SessionIdleTimeout: 2 * time.Hour,
		MaxSessions: 3, PINMaxFailures: 5, PINLockout: 15 * time.Minute, MaxConcurrentHashes: 8,
	})
	ah := handler.NewAuthHandler(authSvc)
	orderRepo, paymentRepo := postgres.NewOrderRepository(), postgres.NewPaymentRepository()
	orders := service.NewOrderService(db, orderRepo)
	payments := service.NewPaymentService(db, paymentRepo, orderRepo, nil)
	dh := handler.NewDeviceHandler(service.NewDeviceService(db, service.NewCatalogService(db)))
	oh := handler.NewOfflineHandler(service.NewOfflineSyncService(db, orders, payments))
	sh := handler.NewShiftHandler(service.NewShiftService(db))

	r := gin.New()
	r.POST("/login", ah.Login)
	r.POST("/pos/drawers", ah.RequirePermission(rbac.CashDrawerManage), sh.CreateDrawer)
	r.POST("/pos/shifts", ah.RequireStaff(), sh.OpenShift)
	r.POST("/pos/shifts/:id/force-close", ah.RequireStaff(), sh.ForceCloseShift)
	r.POST("/store/devices", ah.RequirePermission(rbac.DeviceManage), dh.RegisterDevice)
	r.POST("/store/devices/:id/revoke", ah.RequirePermission(rbac.DeviceManage), dh.RevokeDevice)
	r.GET("/device/catalog", dh.Catalog)
	r.POST("/device/offline-orders/sync", oh.Sync)
	r.GET("/store/offline-submissions", ah.RequirePermission(rbac.DeviceManage), oh.ListSubmissions)
	r.POST("/store/offline-submissions/:id/resolve", ah.RequirePermission(rbac.DeviceManage), oh.ResolveSubmission)

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
	login := func(email, credential string) (string, uuid.UUID) {
		t.Helper()
		body := map[string]string{"store_slug": "central-cafe", "email": email, "pin": "482913"}
		if credential != "" {
			body["device_credential"] = credential
		}
		res := call("POST", "/login", "", body)
		if res.Code != http.StatusOK {
			t.Fatalf("login %s failed: %d: %s", email, res.Code, res.Body.String())
		}
		var s struct {
			AccessToken string `json:"access_token"`
			Staff       struct {
				ID uuid.UUID `json:"id"`
			} `json:"staff"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &s)
		return "Bearer " + s.AccessToken, s.Staff.ID
	}
	inStore := func(fn func(tx *sqlx.Tx) error) {
		t.Helper()
		if err := database.WithTenantTx(ctx, db, storeID, fn); err != nil {
			t.Fatalf("tenant tx failed: %v", err)
		}
	}

	owner, ownerID := login("abdulaziz543333@gmail.com", "")
	prefix := "O" + strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", "")[:3])
	var device service.Device
	if res := call("POST", "/store/devices", owner, map[string]any{"name": "Offline test", "prefix": prefix}); res.Code != http.StatusCreated {
		t.Fatalf("register device failed: %d: %s", res.Code, res.Body.String())
	} else {
		_ = json.Unmarshal(res.Body.Bytes(), &device)
	}
	t.Cleanup(func() { call("POST", "/store/devices/"+device.ID.String()+"/revoke", owner, nil) })
	deviceAuth := "Device " + device.Credential
	cashier, cashierID := login("cashier.central@mycaffe.local", device.Credential) // issues the offline verifier

	var drawer domain.CashDrawer
	_ = json.Unmarshal(call("POST", "/pos/drawers", owner, map[string]any{"name": "Laci Offline " + prefix}).Body.Bytes(), &drawer)
	var opened struct {
		Shift domain.Shift `json:"shift"`
	}
	if res := call("POST", "/pos/shifts", cashier, map[string]any{"drawer_id": drawer.ID, "opening_float": 0}); res.Code != http.StatusCreated {
		t.Fatalf("open shift failed: %d: %s", res.Code, res.Body.String())
	} else {
		_ = json.Unmarshal(res.Body.Bytes(), &opened)
	}
	t.Cleanup(func() {
		call("POST", fmt.Sprintf("/pos/shifts/%s/force-close", opened.Shift.ID), owner, map[string]any{"reason": "Akhir test offline"})
	})

	var served struct {
		Version string `json:"catalog_version"`
	}
	_ = json.Unmarshal(call("GET", "/device/catalog", deviceAuth, nil).Body.Bytes(), &served)

	// A snapshot as if the device had been served Americano at Rp12.345 (prices the server must use).
	var snapshot map[string]any
	inStore(func(tx *sqlx.Tx) error {
		var raw []byte
		if err := tx.GetContext(ctx, &raw, `SELECT snapshot FROM catalog_versions WHERE store_id = $1 AND version = $2`, storeID, served.Version); err != nil {
			return err
		}
		return json.Unmarshal(raw, &snapshot)
	})
	for _, p := range snapshot["products"].([]any) {
		if prod := p.(map[string]any); prod["id"] == americano.String() {
			for _, v := range prod["variants"].([]any) {
				v.(map[string]any)["price"] = 12345
			}
		}
	}
	oldRaw, _ := json.Marshal(snapshot)
	sum := sha256.Sum256(oldRaw)
	oldVersion := hex.EncodeToString(sum[:])
	inStore(func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO catalog_versions (store_id, version, snapshot, first_served_at) VALUES ($1, $2, $3, $4)
			ON CONFLICT DO NOTHING`, storeID, oldVersion, oldRaw, time.Now().Add(-30*time.Hour))
		return err
	})

	seq := 0
	order := func(mut func(o map[string]any)) map[string]any {
		seq++
		o := map[string]any{
			"client_order_id": uuid.NewString(), "offline_queue_number": fmt.Sprintf("%s-%03d", prefix, seq),
			"offline_created_at": time.Now(), "cashier_id": cashierID, "shift_id": opened.Shift.ID, // created after the shift opened (OF4)
			"catalog_version": served.Version, "order_type": "takeaway", "customer_name": "Rina",
			"items":   []map[string]any{{"product_id": americano, "quantity": 2}},
			"payment": map[string]any{"method": "cash", "cash_tendered_amount": 1_000_000},
		}
		if mut != nil {
			mut(o)
		}
		return o
	}
	type result struct {
		ClientOrderID uuid.UUID  `json:"client_order_id"`
		Status        string     `json:"status"`
		Reason        string     `json:"reason"`
		OrderID       *uuid.UUID `json:"order_id"`
		OrderNumber   string     `json:"order_number"`
	}
	sync := func(body map[string]any) []result {
		t.Helper()
		res := call("POST", "/device/offline-orders/sync", deviceAuth, body)
		if res.Code != http.StatusOK {
			t.Fatalf("sync failed: %d: %s", res.Code, res.Body.String())
		}
		var out struct {
			Results []result `json:"results"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &out)
		return out.Results
	}

	snapOrder := order(func(o map[string]any) { o["catalog_version"] = oldVersion })
	qrisOrder := order(func(o map[string]any) {
		o["payment"] = map[string]any{"method": "static_qris", "reference": "QR-REF-" + prefix}
	})
	var accepted []result
	t.Run("AcceptedOrdersArePricedFromTheirSnapshot", func(t *testing.T) {
		batch := map[string]any{"orders": []any{snapOrder, qrisOrder}}
		accepted = sync(batch)
		if len(accepted) != 2 || accepted[0].Status != "accepted" || accepted[1].Status != "accepted" || accepted[0].OrderNumber == "" {
			t.Fatalf("both orders must be accepted with an official number: %+v", accepted)
		}
		var row struct {
			Origin   string `db:"origin"`
			Subtotal int64  `db:"subtotal_amount"`
			Delta    int64  `db:"price_delta"`
			Stale    bool   `db:"stale_catalog"`
			Number   string `db:"offline_queue_number"`
			Paid     string `db:"payment_status"`
			ShiftID  string `db:"shift_id"`
		}
		var unreconciled bool
		inStore(func(tx *sqlx.Tx) error {
			if err := tx.GetContext(ctx, &row, `SELECT origin, subtotal_amount, price_delta, stale_catalog, offline_queue_number, payment_status, shift_id
				FROM orders WHERE store_id = $1 AND id = $2`, storeID, *accepted[0].OrderID); err != nil {
				return err
			}
			return tx.GetContext(ctx, &unreconciled, `SELECT offline_unreconciled FROM payments WHERE store_id = $1 AND order_id = $2`, storeID, *accepted[1].OrderID)
		})
		if row.Origin != "offline" || row.Subtotal != 2*12345 || !row.Stale || row.Number != snapOrder["offline_queue_number"] || row.Paid != "paid" || row.ShiftID != opened.Shift.ID.String() {
			t.Fatalf("the order must be offline, priced from its snapshot, stale, paid into the shift: %+v", row)
		}
		if row.Delta == 0 {
			t.Fatal("the difference to today's price must be recorded")
		}
		if !unreconciled {
			t.Fatal("static QRIS taken offline stays unreconciled (OF8)")
		}

		again := sync(batch)
		if again[0].OrderID == nil || *again[0].OrderID != *accepted[0].OrderID || again[0].Status != "accepted" {
			t.Fatalf("a re-sync must return the first result: %+v", again)
		}
		var orderCount, payCount, cashCount int
		inStore(func(tx *sqlx.Tx) error {
			if err := tx.GetContext(ctx, &orderCount, `SELECT COUNT(*) FROM orders WHERE store_id = $1 AND client_order_id = $2`, storeID, snapOrder["client_order_id"]); err != nil {
				return err
			}
			if err := tx.GetContext(ctx, &payCount, `SELECT COUNT(*) FROM payments WHERE store_id = $1 AND order_id = $2`, storeID, *accepted[0].OrderID); err != nil {
				return err
			}
			return tx.GetContext(ctx, &cashCount, `SELECT COUNT(*) FROM cash_movements WHERE store_id = $1 AND order_id = $2`, storeID, *accepted[0].OrderID)
		})
		if orderCount != 1 || payCount != 1 || cashCount != 1 {
			t.Fatalf("exactly one order, payment and cash movement per client_order_id, got %d/%d/%d", orderCount, payCount, cashCount)
		}
	})

	var policyRejected, versionRejected uuid.UUID
	t.Run("RejectionsAreStoredWithAReason", func(t *testing.T) {
		discount := order(func(o map[string]any) { o["discount_bps"] = 9000 })
		unknown := order(func(o map[string]any) { o["catalog_version"] = strings.Repeat("0", 64) })
		badPrefix := order(func(o map[string]any) { o["offline_queue_number"] = "ZZ-001" })
		noRef := order(func(o map[string]any) { o["payment"] = map[string]any{"method": "static_qris"} })
		noVerifier := order(func(o map[string]any) { o["cashier_id"] = ownerID })
		results := sync(map[string]any{"orders": []any{discount, unknown, badPrefix, noRef, noVerifier}})
		for i, res := range results {
			if res.Status != "rejected" || res.Reason == "" || res.OrderID != nil {
				t.Fatalf("order %d must be rejected with a reason and no order: %+v", i, res)
			}
		}
		if !strings.Contains(results[0].Reason, "batas diskon") || !strings.Contains(results[4].Reason, "verifier") {
			t.Fatalf("reasons must name the rule: %q / %q", results[0].Reason, results[4].Reason)
		}
		var stored int
		inStore(func(tx *sqlx.Tx) error {
			if err := tx.GetContext(ctx, &stored, `SELECT COUNT(*) FROM offline_order_submissions WHERE store_id = $1 AND device_id = $2 AND status = 'rejected'`, storeID, device.ID); err != nil {
				return err
			}
			if err := tx.GetContext(ctx, &policyRejected, `SELECT id FROM offline_order_submissions WHERE store_id = $1 AND client_order_id = $2`, storeID, noVerifier["client_order_id"]); err != nil {
				return err
			}
			return tx.GetContext(ctx, &versionRejected, `SELECT id FROM offline_order_submissions WHERE store_id = $1 AND client_order_id = $2`, storeID, unknown["client_order_id"])
		})
		if stored != 5 {
			t.Fatalf("every rejected order is kept for the owner, got %d", stored)
		}
	})

	t.Run("OwnerResolvesTheQueue", func(t *testing.T) {
		if res := call("GET", "/store/offline-submissions", cashier, nil); res.Code != http.StatusForbidden {
			t.Fatalf("cashier must not see the review queue, got %d", res.Code)
		}
		list := call("GET", "/store/offline-submissions", owner, nil)
		if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), policyRejected.String()) || !strings.Contains(list.Body.String(), `"device_prefix":"`+prefix+`"`) {
			t.Fatalf("the queue lists rejected submissions with their device: %d", list.Code)
		}
		if res := call("POST", "/store/offline-submissions/"+versionRejected.String()+"/resolve", owner, map[string]any{"decision": "accept"}); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("an accept that still fails must be 422, got %d: %s", res.Code, res.Body.String())
		}
		res := call("POST", "/store/offline-submissions/"+policyRejected.String()+"/resolve", owner, map[string]any{"decision": "accept", "note": "PIN owner, diterima"})
		if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "resolved_accepted") || !strings.Contains(res.Body.String(), "order_number") {
			t.Fatalf("owner accept must create the order: %d: %s", res.Code, res.Body.String())
		}
		if res := call("POST", "/store/offline-submissions/"+versionRejected.String()+"/resolve", owner, map[string]any{"decision": "reject"}); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("a reject needs a note, got %d", res.Code)
		}
		if res := call("POST", "/store/offline-submissions/"+versionRejected.String()+"/resolve", owner, map[string]any{"decision": "reject", "note": "Versi katalog hilang"}); res.Code != http.StatusOK {
			t.Fatalf("owner reject failed: %d: %s", res.Code, res.Body.String())
		}
		if res := call("POST", "/store/offline-submissions/"+versionRejected.String()+"/resolve", owner, map[string]any{"decision": "reject", "note": "lagi"}); res.Code != http.StatusConflict {
			t.Fatalf("a resolved submission cannot be decided twice, got %d", res.Code)
		}
		var audits int
		inStore(func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &audits, `SELECT COUNT(*) FROM audit_logs WHERE store_id = $1 AND action = 'OFFLINE_ORDER_RESOLVED' AND metadata::jsonb->>'submission_id' IN ($2, $3)`,
				storeID, policyRejected.String(), versionRejected.String())
		})
		if audits != 2 {
			t.Fatalf("both decisions are audited, got %d", audits)
		}
	})

	t.Run("PINFailuresAndTenantIsolation", func(t *testing.T) {
		sync(map[string]any{"orders": []any{}, "pin_failures": []any{map[string]any{"user_id": cashierID, "at": time.Now(), "attempts": 5, "locked": true}}})
		var audits int
		inStore(func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &audits, `SELECT COUNT(*) FROM audit_logs WHERE store_id = $1 AND action = 'DEVICE_PIN_FAILED' AND reference_id = $2`, storeID, device.ID.String())
		})
		if audits != 1 {
			t.Fatalf("offline PIN failures are audited, got %d", audits)
		}
		forged := strings.Replace(device.Credential, storeID.String(), expressID.String(), 1)
		if res := call("POST", "/device/offline-orders/sync", "Device "+forged, map[string]any{"orders": []any{order(nil)}}); res.Code != http.StatusUnauthorized {
			t.Fatalf("a credential moved to another store must be 401, got %d", res.Code)
		}
	})
}
