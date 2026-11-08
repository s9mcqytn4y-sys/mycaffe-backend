package handler_test

import (
	"bytes"
	"context"
	"encoding/csv"
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

// TestIntegrationReports covers B2 in a store of its own: cash figures equal the Z-report, the business
// day follows the store time zone, groups by day/hour/method/category/item (category from the sale-time
// snapshot), voids and refunds, CSV export, RBAC and range validation.
func TestIntegrationReports(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	migratorDSN := strings.TrimSpace(os.Getenv("TEST_MIGRATOR_DATABASE_URL"))
	if dsn == "" || migratorDSN == "" {
		t.Skip("TEST_APP_DATABASE_URL / TEST_MIGRATOR_DATABASE_URL not set; skipping report integration test")
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
	slug := "laporan-" + suffix
	email := func(name string) string { return name + ".laporan." + suffix + "@mycaffe.local" }
	storeID, ownerID, err := service.BootstrapStore(ctx, migratorDB, service.BootstrapStoreInput{
		Slug: slug, StoreName: "Kafe Laporan", OwnerName: "Pemilik Laporan", OwnerEmail: email("owner"), PIN: "482913",
	})
	if err != nil {
		t.Fatalf("bootstrap store: %v", err)
	}
	if _, err := migratorDB.ExecContext(ctx, `UPDATE stores SET enabled_payment_methods = '["cash","edc"]'::jsonb WHERE id = $1`, storeID); err != nil {
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
	rp := handler.NewReportHandler(service.NewReportService(db))

	r := gin.New()
	r.POST("/login", ah.Login)
	r.POST("/pos/drawers", ah.RequirePermission(rbac.CashDrawerManage), sh.CreateDrawer)
	r.POST("/pos/shifts", ah.RequireStaff(), sh.OpenShift)
	r.POST("/pos/shifts/:id/close", ah.RequireStaff(), sh.CloseShift)
	r.GET("/pos/shifts/:id/z-report", ah.RequireStaff(), sh.GetZReport)
	r.POST("/pos/orders", ah.RequireStaff(), pos.CreateOrder)
	r.POST("/pos/orders/:id/payments", ah.RequireStaff(), pos.InitiatePayment)
	r.POST("/pos/orders/:id/void", ah.RequirePermission(rbac.OrderVoid), pos.VoidOrder)
	r.POST("/pos/orders/:id/payments/:payment_id/refund", ah.RequirePermission(rbac.PaymentRefund), pos.RefundPayment)
	reports := r.Group("/reports", ah.RequirePermission(rbac.ReportFinanceRead))
	reports.GET("/sales", rp.Sales)
	reports.GET("/summary", rp.Summary)

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
	inStore := func(fn func(tx *sqlx.Tx) error) {
		t.Helper()
		if err := database.WithTenantTx(ctx, db, storeID, fn); err != nil {
			t.Fatalf("tenant tx failed: %v", err)
		}
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

	coffee, pastry, kopiCat, rotiCat := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	inStore(func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO users (store_id, email, full_name, role, pin_hash, is_active, pin_set_at)
			SELECT store_id, $2, 'Kasir Laporan', 'cashier', pin_hash, true, NOW() FROM users WHERE store_id = $1 AND id = $3`,
			storeID, email("cashier"), ownerID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO categories (id, store_id, name) VALUES ($1, $3, 'Kopi'), ($2, $3, 'Roti')`, kopiCat, rotiCat, storeID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `WITH p AS (INSERT INTO products (id, store_id, category_id, name, base_price, is_available)
			VALUES ($1, $3, $4, 'Latte', 0, true), ($2, $3, $5, 'Croissant', 0, true) RETURNING store_id, id, name)
			INSERT INTO product_variants (store_id, product_id, name, price, is_default)
			SELECT store_id, id, 'Reguler', CASE WHEN name = 'Latte' THEN 20000 ELSE 15000 END, true FROM p`,
			coffee, pastry, storeID, kopiCat, rotiCat)
		return err
	})
	owner, cashier := login("owner"), login("cashier")

	var drawer domain.CashDrawer
	_ = json.Unmarshal(call("POST", "/pos/drawers", owner, map[string]any{"name": "Laci Laporan"}).Body.Bytes(), &drawer)
	var opened struct {
		Shift domain.Shift `json:"shift"`
	}
	_ = json.Unmarshal(call("POST", "/pos/shifts", cashier, map[string]any{"drawer_id": drawer.ID, "opening_float": 100000}).Body.Bytes(), &opened)

	createOrder := func(product uuid.UUID, qty int) domain.Order {
		t.Helper()
		res := call("POST", "/pos/orders", cashier, map[string]any{"idempotency_key": "lap-" + uuid.NewString(), "order_type": "takeaway",
			"items": []map[string]any{{"product_id": product, "quantity": qty}}})
		if res.Code != http.StatusCreated {
			t.Fatalf("create order failed: %d: %s", res.Code, res.Body.String())
		}
		var body struct {
			Order domain.Order `json:"order"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &body)
		return body.Order
	}
	pay := func(o domain.Order, method string) uuid.UUID {
		t.Helper()
		body := map[string]any{"idempotency_key": "lap-pay-" + uuid.NewString(), "method": method}
		if method == "cash" {
			body["cash_tendered_amount"] = o.TotalAmount
		} else {
			body["reconciliation_ref"] = "EDC-" + uuid.NewString()[:8]
		}
		res := call("POST", fmt.Sprintf("/pos/orders/%s/payments", o.ID), cashier, body)
		if res.Code != http.StatusOK {
			t.Fatalf("%s payment failed: %d: %s", method, res.Code, res.Body.String())
		}
		var out struct {
			Payment struct {
				ID uuid.UUID `json:"id"`
			} `json:"payment"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &out)
		return out.Payment.ID
	}
	today := time.Now().In(time.FixedZone("WIB", 7*3600)).Format(time.DateOnly)
	summary := func(query string) service.ReportSummary {
		t.Helper()
		res := call("GET", "/reports/summary?"+query, owner, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("summary failed: %d: %s", res.Code, res.Body.String())
		}
		var s service.ReportSummary
		_ = json.Unmarshal(res.Body.Bytes(), &s)
		return s
	}
	sales := func(query string) service.SalesReport {
		t.Helper()
		res := call("GET", "/reports/sales?"+query, owner, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("sales failed: %d: %s", res.Code, res.Body.String())
		}
		var s service.SalesReport
		_ = json.Unmarshal(res.Body.Bytes(), &s)
		return s
	}

	// Today: two cash latte orders (one refunded), one EDC croissant order, one voided order.
	latte1, latte2, croissant := createOrder(coffee, 1), createOrder(coffee, 2), createOrder(pastry, 1)
	refundedPayment := pay(latte1, "cash")
	pay(latte2, "cash")
	pay(croissant, "edc")
	if res := call("POST", fmt.Sprintf("/pos/orders/%s/payments/%s/refund", latte1.ID, refundedPayment), cashier,
		map[string]any{"reason": "Salah pesan", "pin": "482913", "owner_pin": "482913", "restore_stock": true}); res.Code != http.StatusOK {
		t.Fatalf("refund failed: %d: %s", res.Code, res.Body.String())
	}
	voided := createOrder(pastry, 2)
	if res := call("POST", fmt.Sprintf("/pos/orders/%s/void", voided.ID), cashier, map[string]any{"reason": "Batal", "pin": "482913", "owner_pin": "482913"}); res.Code != http.StatusOK {
		t.Fatalf("void failed: %d: %s", res.Code, res.Body.String())
	}

	// An order paid at 2026-01-15 23:30 WIB (16:30 UTC) belongs to the 15th, not the 16th.
	late := createOrder(pastry, 1)
	pay(late, "edc")
	if _, err := migratorDB.ExecContext(ctx, `UPDATE orders SET created_at = '2026-01-15 16:30:00+00' WHERE id = $1`, late.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := migratorDB.ExecContext(ctx, `UPDATE payments SET paid_at = '2026-01-15 16:31:00+00' WHERE order_id = $1`, late.ID); err != nil {
		t.Fatal(err)
	}

	t.Run("MatchZReport", func(t *testing.T) {
		s := summary("from=" + today)
		expectedCash := 100000 + latte1.TotalAmount + latte2.TotalAmount - latte1.TotalAmount
		if res := call("POST", fmt.Sprintf("/pos/shifts/%s/close", opened.Shift.ID), cashier, map[string]any{"counted_cash": expectedCash}); res.Code != http.StatusOK {
			t.Fatalf("close shift failed: %d: %s", res.Code, res.Body.String())
		}
		var z domain.ZReport
		_ = json.Unmarshal(call("GET", fmt.Sprintf("/pos/shifts/%s/z-report", opened.Shift.ID), owner, nil).Body.Bytes(), &z)
		if s.CashSales != z.Summary.CashSales || s.CashRefunds != z.Summary.CashRefunds || s.CashNet != z.Summary.CashSales-z.Summary.CashRefunds {
			t.Fatalf("report cash %d/%d/%d must equal Z-report %d/%d", s.CashSales, s.CashRefunds, s.CashNet, z.Summary.CashSales, z.Summary.CashRefunds)
		}
		if s.CashNet != latte2.TotalAmount {
			t.Fatalf("cash net must be the unrefunded cash order %d, got %d", latte2.TotalAmount, s.CashNet)
		}
	})

	t.Run("VoidRefundTotals", func(t *testing.T) {
		s := summary("from=" + today + "&to=" + today)
		wantTotal := latte1.TotalAmount + latte2.TotalAmount + croissant.TotalAmount
		if s.Orders != 3 || s.Total != wantTotal || s.Refunded != latte1.TotalAmount || s.Net != wantTotal-latte1.TotalAmount {
			t.Fatalf("totals wrong: %+v (want 3 orders, total %d)", s, wantTotal)
		}
		if s.GrossSales != latte1.SubtotalAmount+latte2.SubtotalAmount+croissant.SubtotalAmount || s.Tax != latte1.TaxAmount+latte2.TaxAmount+croissant.TaxAmount {
			t.Fatalf("gross/tax must sum the order snapshots: %+v", s)
		}
		if s.VoidCount != 1 || s.VoidAmount != voided.TotalAmount {
			t.Fatalf("void must count once at %d: %+v", voided.TotalAmount, s)
		}
		if len(s.RefundsBy) != 1 || s.RefundsBy[0].Method != "cash" || s.RefundsBy[0].Amount != latte1.TotalAmount {
			t.Fatalf("refunds by method wrong: %+v", s.RefundsBy)
		}
	})

	t.Run("Groups", func(t *testing.T) {
		// Moving Latte to Roti after the sale must not move its revenue (category snapshot).
		inStore(func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE products SET category_id = $3 WHERE store_id = $1 AND id = $2`, storeID, coffee, rotiCat)
			return err
		})
		cat := sales("from=" + today + "&group=category")
		got := map[string]service.SalesRow{}
		for _, row := range cat.Rows {
			got[row.Key] = row
		}
		if got["Kopi"].Quantity != 3 || got["Kopi"].GrossSales != 60000 || got["Roti"].Quantity != 1 || got["Roti"].GrossSales != 15000 {
			t.Fatalf("category rows must follow the sale-time snapshot: %+v", cat.Rows)
		}
		items := sales("from=" + today + "&group=item")
		if len(items.Rows) != 2 || items.Rows[0].Key != "Croissant (Reguler)" || items.Rows[1].Key != "Latte (Reguler)" || items.Rows[1].Quantity != 3 {
			t.Fatalf("item rows wrong: %+v", items.Rows)
		}
		methods := sales("from=" + today + "&group=method")
		if len(methods.Rows) != 2 || methods.Rows[0].Key != "cash" || methods.Rows[0].Payments != 2 || methods.Rows[0].Net != latte2.TotalAmount ||
			methods.Rows[1].Key != "edc" || methods.Rows[1].Net != croissant.TotalAmount {
			t.Fatalf("method rows wrong: %+v", methods.Rows)
		}
		day := sales("from=" + today + "&group=day")
		if len(day.Rows) != 1 || day.Rows[0].Key != today || day.Rows[0].Orders != 3 {
			t.Fatalf("day rows wrong: %+v", day.Rows)
		}
		hour := sales("from=" + today + "&group=hour")
		var hourOrders int64
		for _, row := range hour.Rows {
			hourOrders += row.Orders
		}
		if hourOrders != 3 {
			t.Fatalf("hour rows must cover the 3 paid orders: %+v", hour.Rows)
		}
	})

	t.Run("BusinessDayTimezone", func(t *testing.T) {
		if s := summary("from=2026-01-15"); s.Orders != 1 || s.Total != late.TotalAmount {
			t.Fatalf("23:30 WIB order must count on its local day: %+v", s)
		}
		if s := summary("from=2026-01-16"); s.Orders != 0 {
			t.Fatalf("it must not count on the next (UTC) day: %+v", s)
		}
		if m := sales("from=2026-01-15&group=method"); len(m.Rows) != 1 || m.Rows[0].Key != "edc" {
			t.Fatalf("payment must count on its local day: %+v", m.Rows)
		}
		if h := sales("from=2026-01-15&group=hour"); len(h.Rows) != 1 || h.Rows[0].Key != "23" {
			t.Fatalf("hour must be local (23): %+v", h.Rows)
		}
	})

	t.Run("CSV", func(t *testing.T) {
		res := call("GET", "/reports/sales?from="+today+"&group=method&format=csv", owner, nil)
		if res.Code != http.StatusOK || !strings.HasPrefix(res.Header().Get("Content-Type"), "text/csv") ||
			!strings.Contains(res.Header().Get("Content-Disposition"), "penjualan-method-"+today) {
			t.Fatalf("CSV response wrong: %d %v", res.Code, res.Header())
		}
		rows, err := csv.NewReader(res.Body).ReadAll()
		if err != nil || len(rows) != 3 || rows[0][0] != "metode" || rows[1][0] != "cash" || rows[1][10] != fmt.Sprint(latte2.TotalAmount) {
			t.Fatalf("CSV content wrong: %v %v", rows, err)
		}
		sum := call("GET", "/reports/summary?from="+today+"&format=csv", owner, nil)
		if sum.Code != http.StatusOK || !strings.Contains(sum.Body.String(), "kas_bersih,"+fmt.Sprint(latte2.TotalAmount)) {
			t.Fatalf("summary CSV wrong: %d %s", sum.Code, sum.Body.String())
		}
	})

	t.Run("RBAC", func(t *testing.T) {
		for _, path := range []string{"/reports/sales", "/reports/summary"} {
			if res := call("GET", path, cashier, nil); res.Code != http.StatusForbidden {
				t.Fatalf("cashier must get 403 on %s, got %d", path, res.Code)
			}
		}
	})

	t.Run("RangeValidation", func(t *testing.T) {
		for query, field := range map[string]string{
			"from=2026-01-10&to=2026-01-01": `"to"`,
			"from=2026-01-01&to=2026-04-03": `"to"`,
			"from=15-01-2026":               `"from"`,
			"group=week":                    `"group"`,
			"format=xlsx":                   `"format"`,
		} {
			res := call("GET", "/reports/sales?"+query, owner, nil)
			if res.Code != http.StatusUnprocessableEntity || !strings.Contains(res.Body.String(), field) {
				t.Fatalf("%s: expected 422 on %s, got %d: %s", query, field, res.Code, res.Body.String())
			}
		}
		if res := call("GET", "/reports/sales?from=2026-01-01&to=2026-04-02", owner, nil); res.Code != http.StatusOK {
			t.Fatalf("a 92-day range must be accepted, got %d: %s", res.Code, res.Body.String())
		}
	})
}
