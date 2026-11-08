package handler_test

import (
	"bytes"
	"context"
	"encoding/base64"
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

// TestPrintQueueIntegration covers v0.9 PR 9.4 (decision W8): printers per station, jobs rendered
// when an order is paid (exactly once), the agent claim/ack protocol with retries, lease and
// dead-letter, token rotation, inactive printers and reprints.
func TestPrintQueueIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping print queue integration test")
	}
	db, err := database.NewPostgresDB(&config.Config{DatabaseURL: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	storeID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	americano := uuid.MustParse("d0000003-0000-4000-8000-000000000003") // bar
	croissant := uuid.MustParse("d0000005-0000-4000-8000-000000000005") // kitchen

	gin.SetMode(gin.TestMode)
	authSvc := service.NewAuthService(db, postgres.NewAuthRepository(), "integration-test-jwt-secret-at-least-32-chars", service.AuthPolicy{
		AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 12 * time.Hour, SessionIdleTimeout: 2 * time.Hour,
		MaxSessions: 3, PINMaxFailures: 5, PINLockout: 15 * time.Minute, MaxConcurrentHashes: 8,
	})
	ah := handler.NewAuthHandler(authSvc)
	orderRepo, paymentRepo := postgres.NewOrderRepository(), postgres.NewPaymentRepository()
	pos := handler.NewPOSHandler(service.NewOrderService(db, orderRepo), service.NewPaymentService(db, paymentRepo, orderRepo, nil), service.NewSettingsService(db))
	sh := handler.NewShiftHandler(service.NewShiftService(db))
	ph := handler.NewPrintHandler(service.NewPrintService(db))
	tabs := handler.NewTabHandler(service.NewTabService(db, postgres.NewTabRepository(), orderRepo))

	r := gin.New()
	r.POST("/login", ah.Login)
	r.POST("/pos/drawers", ah.RequirePermission(rbac.CashDrawerManage), sh.CreateDrawer)
	r.POST("/pos/shifts", ah.RequireStaff(), sh.OpenShift)
	r.POST("/pos/shifts/:id/force-close", ah.RequireStaff(), sh.ForceCloseShift)
	r.POST("/pos/orders", ah.RequireStaff(), pos.CreateOrder)
	r.POST("/pos/orders/:id/payments", ah.RequireStaff(), pos.InitiatePayment)
	r.POST("/pos/orders/:id/reprint", ah.RequirePermission(rbac.OrderPOSCreate), ph.Reprint)
	r.POST("/pos/tabs", ah.RequirePermission(rbac.TabManage), tabs.OpenTab)
	printers := r.Group("/store/printers", ah.RequirePermission(rbac.StoreSettingsManage))
	printers.GET("", ph.ListPrinters)
	printers.POST("", ph.CreatePrinter)
	printers.PATCH("/:id", ph.UpdatePrinter)
	printers.POST("/:id/rotate-token", ph.RotateToken)
	r.POST("/print-agent/jobs/claim", ph.Claim)
	r.POST("/print-agent/jobs/:id/ack", ph.Ack)

	call := func(method, path, auth string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", auth)
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
		return "Bearer " + body.AccessToken
	}
	inStore := func(fn func(tx *sqlx.Tx) error) {
		t.Helper()
		if err := database.WithTenantTx(ctx, db, storeID, fn); err != nil {
			t.Fatalf("tenant tx failed: %v", err)
		}
	}
	owner, cashier := login("abdulaziz543333@gmail.com"), login("cashier.central@mycaffe.local")

	newPrinter := func(body map[string]any) service.Printer {
		t.Helper()
		res := call("POST", "/store/printers", owner, body)
		if res.Code != http.StatusCreated {
			t.Fatalf("create printer failed: %d: %s", res.Code, res.Body.String())
		}
		var p service.Printer
		_ = json.Unmarshal(res.Body.Bytes(), &p)
		// Other tests pay orders in this store: never leave these printers active.
		t.Cleanup(func() { call("PATCH", "/store/printers/"+p.ID.String(), owner, map[string]any{"is_active": false}) })
		return p
	}
	suffix := uuid.NewString()[:6]
	var bar, kitchen, checker, label service.Printer
	t.Run("OwnerConfiguresPrinters", func(t *testing.T) {
		if res := call("POST", "/store/printers", cashier, map[string]any{"name": "Bar", "station": "bar"}); res.Code != http.StatusForbidden {
			t.Fatalf("cashier must not configure printers, got %d", res.Code)
		}
		for _, bad := range []map[string]any{
			{"name": "X", "station": "bar", "paper_width_mm": 70},
			{"name": "X", "station": "receipt"},
			{"name": "X", "station": "label", "label_height_mm": 5},
			{"name": "X", "station": "bar", "copies": 6},
		} {
			if res := call("POST", "/store/printers", owner, bad); res.Code != http.StatusUnprocessableEntity {
				t.Fatalf("%v must be 422, got %d: %s", bad, res.Code, res.Body.String())
			}
		}
		bar = newPrinter(map[string]any{"name": "Bar " + suffix, "station": "bar", "copies": 2, "footer": "Bar utama"})
		kitchen = newPrinter(map[string]any{"name": "Dapur " + suffix, "station": "kitchen", "paper_width_mm": 80})
		checker = newPrinter(map[string]any{"name": "Checker " + suffix, "station": "checker"})
		label = newPrinter(map[string]any{"name": "Label " + suffix, "station": "label"})
		if bar.Format != "escpos" || bar.PaperWidthMM != 58 || label.Format != "tspl" || label.PaperWidthMM != 40 || label.LabelHeightMM == nil || *label.LabelHeightMM != 30 {
			t.Fatalf("defaults must follow the station: bar %+v label %+v", bar, label)
		}
		if bar.AgentToken == "" || !strings.HasPrefix(bar.AgentToken, storeID.String()+".") {
			t.Fatal("the agent token is returned once and carries the store id")
		}
		res := call("GET", "/store/printers", owner, nil)
		if res.Code != http.StatusOK || strings.Contains(res.Body.String(), bar.AgentToken) || strings.Contains(res.Body.String(), "agent_token") {
			t.Fatalf("the list must never show agent tokens: %s", res.Body.String())
		}
	})

	// A paid order: two Americano (bar) and one Croissant (kitchen).
	var drawer domain.CashDrawer
	_ = json.Unmarshal(call("POST", "/pos/drawers", owner, map[string]any{"name": "Laci Print " + suffix}).Body.Bytes(), &drawer)
	var opened struct {
		Shift domain.Shift `json:"shift"`
	}
	if res := call("POST", "/pos/shifts", cashier, map[string]any{"drawer_id": drawer.ID, "opening_float": 0}); res.Code != http.StatusCreated {
		t.Fatalf("open shift failed: %d: %s", res.Code, res.Body.String())
	} else {
		_ = json.Unmarshal(res.Body.Bytes(), &opened)
	}
	t.Cleanup(func() {
		call("POST", fmt.Sprintf("/pos/shifts/%s/force-close", opened.Shift.ID), owner, map[string]any{"reason": "Akhir test cetak"})
	})
	res := call("POST", "/pos/orders", cashier, map[string]any{"idempotency_key": "print-" + uuid.NewString(), "order_type": "takeaway",
		"customer_name": "Dina", "items": []map[string]any{{"product_id": americano, "quantity": 2}, {"product_id": croissant, "quantity": 1}}})
	if res.Code != http.StatusCreated {
		t.Fatalf("create order failed: %d: %s", res.Code, res.Body.String())
	}
	var created struct {
		Order domain.Order `json:"order"`
	}
	_ = json.Unmarshal(res.Body.Bytes(), &created)
	order := created.Order
	payKey := "print-pay-" + uuid.NewString()
	pay := func() {
		t.Helper()
		if res := call("POST", fmt.Sprintf("/pos/orders/%s/payments", order.ID), cashier, map[string]any{
			"idempotency_key": payKey, "method": "cash", "cash_tendered_amount": order.TotalAmount}); res.Code != http.StatusOK {
			t.Fatalf("cash payment failed: %d: %s", res.Code, res.Body.String())
		}
	}
	type jobRow struct {
		ID        uuid.UUID `db:"id"`
		PrinterID uuid.UUID `db:"printer_id"`
		Kind      string    `db:"kind"`
		Payload   []byte    `db:"payload"`
		Status    string    `db:"status"`
		Attempts  int       `db:"attempts"`
	}
	jobs := func() map[uuid.UUID]jobRow {
		t.Helper()
		var rows []jobRow
		inStore(func(tx *sqlx.Tx) error {
			return tx.SelectContext(ctx, &rows, `SELECT id, printer_id, kind, payload, status, attempts FROM print_jobs
				WHERE store_id = $1 AND order_id = $2 AND reprinted_by_user_id IS NULL`, storeID, order.ID)
		})
		out := map[uuid.UUID]jobRow{}
		for _, j := range rows {
			out[j.PrinterID] = j
		}
		if len(out) != len(rows) {
			t.Fatalf("one job per printer expected, got %d jobs on %d printers", len(rows), len(out))
		}
		return out
	}

	t.Run("PaymentPrintsEachStationOnce", func(t *testing.T) {
		pay()
		pay() // idempotent replay must not print again
		js := jobs()
		if len(js) != 4 {
			t.Fatalf("expected bar, kitchen, checker and label jobs, got %d", len(js))
		}
		b, k, c, l := string(js[bar.ID].Payload), string(js[kitchen.ID].Payload), string(js[checker.ID].Payload), string(js[label.ID].Payload)
		if js[bar.ID].Kind != "ticket" || !strings.Contains(b, "2x Americano") || strings.Contains(b, "Croissant") || strings.Count(b, "Bar utama") != 2 {
			t.Fatalf("bar ticket must hold only bar lines, twice with its footer: %q", b)
		}
		if !strings.Contains(k, "Croissant") || strings.Contains(k, "Americano") || !strings.Contains(k, "DAPUR") {
			t.Fatalf("kitchen ticket must hold only kitchen lines: %q", k)
		}
		if js[checker.ID].Kind != "checker" || !strings.Contains(c, "Americano") || !strings.Contains(c, "Croissant") {
			t.Fatalf("checker must hold every line: %q", c)
		}
		if js[label.ID].Kind != "label" || strings.Count(l, "PRINT 1,1") != 2 || !strings.Contains(l, order.QueueNumber+"  2/2") {
			t.Fatalf("one label per Americano cup expected: %q", l)
		}
		for _, p := range []string{b, k, c, l} {
			if strings.Contains(p, "Rp") {
				t.Fatal("print jobs never show prices")
			}
		}
	})

	// claimOwn claims until it gets a job of this order (other packages may pay orders meanwhile).
	claimOwn := func(token string) (int, service.AgentJob) {
		t.Helper()
		mine := map[uuid.UUID]bool{}
		for _, j := range jobs() {
			mine[j.ID] = true
		}
		for i := 0; i < 50; i++ {
			res := call("POST", "/print-agent/jobs/claim", "Printer "+token, nil)
			if res.Code != http.StatusOK {
				return res.Code, service.AgentJob{}
			}
			var job service.AgentJob
			_ = json.Unmarshal(res.Body.Bytes(), &job)
			if mine[job.ID] {
				return res.Code, job
			}
			call("POST", fmt.Sprintf("/print-agent/jobs/%s/ack", job.ID), "Printer "+token, map[string]any{"status": "printed"})
		}
		t.Fatal("never claimed a job of this order")
		return 0, service.AgentJob{}
	}
	events := func(event string, jobID uuid.UUID) int {
		var n int
		inStore(func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &n, `SELECT COUNT(*) FROM outbox_events WHERE store_id = $1 AND event_type = $2 AND aggregate_id = $3`, storeID, event, jobID.String())
		})
		return n
	}

	t.Run("OpenTabRoundPrintsWithTableAndRound", func(t *testing.T) {
		res := call("POST", "/pos/tabs", cashier, map[string]any{"idempotency_key": "print-tab-" + uuid.NewString(), "table_number": "P" + suffix,
			"initial_items": []map[string]any{{"product_id": americano, "quantity": 1}}})
		if res.Code != http.StatusCreated {
			t.Fatalf("open tab failed: %d: %s", res.Code, res.Body.String())
		}
		var tab struct {
			Order domain.Order `json:"order"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &tab)
		var payloads [][]byte
		inStore(func(tx *sqlx.Tx) error {
			return tx.SelectContext(ctx, &payloads, `SELECT payload FROM print_jobs
				WHERE store_id = $1 AND order_id = $2 AND printer_id = $3 AND round_id IS NOT NULL`, storeID, tab.Order.ID, bar.ID)
		})
		if len(payloads) != 1 || !strings.Contains(string(payloads[0]), "MEJA P"+suffix) || !strings.Contains(string(payloads[0]), "Ronde") {
			t.Fatalf("the first round must print one bar ticket with table and round, got %d", len(payloads))
		}
	})

	t.Run("AgentPrintsAndAcknowledges", func(t *testing.T) {
		if res := call("POST", "/print-agent/jobs/claim", "Printer "+storeID.String()+"."+bar.ID.String()+".wrong", nil); res.Code != http.StatusUnauthorized {
			t.Fatalf("wrong secret must be 401, got %d", res.Code)
		}
		code, job := claimOwn(bar.AgentToken)
		raw, _ := base64.StdEncoding.DecodeString(job.Payload)
		if code != http.StatusOK || job.Format != "escpos" || job.Kind != "ticket" || job.Attempts != 1 || !strings.Contains(string(raw), "Americano") {
			t.Fatalf("unexpected claimed job %+v", job)
		}
		if res := call("POST", fmt.Sprintf("/print-agent/jobs/%s/ack", job.ID), "Printer "+kitchen.AgentToken, map[string]any{"status": "printed"}); res.Code != http.StatusNotFound {
			t.Fatalf("another printer must not acknowledge this job, got %d", res.Code)
		}
		for i := 0; i < 2; i++ { // the second ack is a no-op
			if res := call("POST", fmt.Sprintf("/print-agent/jobs/%s/ack", job.ID), "Printer "+bar.AgentToken, map[string]any{"status": "printed"}); res.Code != http.StatusNoContent {
				t.Fatalf("ack failed: %d: %s", res.Code, res.Body.String())
			}
		}
		if st := jobs()[bar.ID].Status; st != "printed" || events("print_job.printed", job.ID) != 1 {
			t.Fatalf("expected printed with one event, got %s", st)
		}
	})

	t.Run("FailuresRetryThenGoDead", func(t *testing.T) {
		_, job := claimOwn(kitchen.AgentToken)
		if res := call("POST", fmt.Sprintf("/print-agent/jobs/%s/ack", job.ID), "Printer "+kitchen.AgentToken, map[string]any{"status": "failed", "error": "paper out"}); res.Code != http.StatusNoContent {
			t.Fatalf("failed ack: %d", res.Code)
		}
		if j := jobs()[kitchen.ID]; j.Status != "pending" || j.Attempts != 1 {
			t.Fatalf("a failure must retry later, got %+v", j)
		}
		inStore(func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE print_jobs SET attempts = 4, next_attempt_at = NOW() - INTERVAL '1 second' WHERE store_id = $1 AND id = $2`, storeID, job.ID)
			return err
		})
		_, job = claimOwn(kitchen.AgentToken)
		call("POST", fmt.Sprintf("/print-agent/jobs/%s/ack", job.ID), "Printer "+kitchen.AgentToken, map[string]any{"status": "failed", "error": "paper out"})
		if j := jobs()[kitchen.ID]; j.Status != "dead" || events("print_job.failed", job.ID) != 1 {
			t.Fatalf("the fifth failure must go dead with an event, got %+v", j)
		}
	})

	t.Run("UnacknowledgedJobIsReclaimedAfterLease", func(t *testing.T) {
		_, first := claimOwn(checker.AgentToken)
		inStore(func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE print_jobs SET claimed_at = NOW() - INTERVAL '2 minutes' WHERE store_id = $1 AND id = $2`, storeID, first.ID)
			return err
		})
		_, again := claimOwn(checker.AgentToken)
		if again.ID != first.ID || again.Attempts != 2 {
			t.Fatalf("an expired lease must hand the same job out again, got %+v", again)
		}
	})

	t.Run("RotateAndDeactivate", func(t *testing.T) {
		res := call("POST", "/store/printers/"+label.ID.String()+"/rotate-token", owner, nil)
		var rotated service.Printer
		_ = json.Unmarshal(res.Body.Bytes(), &rotated)
		if res.Code != http.StatusOK || rotated.AgentToken == "" || rotated.AgentToken == label.AgentToken {
			t.Fatalf("rotate failed: %d", res.Code)
		}
		if code, _ := claimOwn(label.AgentToken); code != http.StatusUnauthorized {
			t.Fatalf("the old token must stop at once, got %d", code)
		}
		if code, job := claimOwn(rotated.AgentToken); code != http.StatusOK || job.Format != "tspl" {
			t.Fatalf("the new token must work, got %d %+v", code, job)
		}
		call("PATCH", "/store/printers/"+label.ID.String(), owner, map[string]any{"is_active": false})
		if code, _ := claimOwn(rotated.AgentToken); code != http.StatusUnauthorized {
			t.Fatalf("an inactive printer must be refused, got %d", code)
		}
	})

	t.Run("Reprint", func(t *testing.T) {
		if res := call("POST", fmt.Sprintf("/pos/orders/%s/reprint", order.ID), cashier, map[string]any{"kind": "receipt"}); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("unknown kind must be 422, got %d", res.Code)
		}
		res := call("POST", fmt.Sprintf("/pos/orders/%s/reprint", order.ID), cashier, map[string]any{"kind": "ticket"})
		if res.Code != http.StatusAccepted || !strings.Contains(res.Body.String(), `"jobs":2`) {
			t.Fatalf("reprint must queue the bar and kitchen tickets: %d %s", res.Code, res.Body.String())
		}
		var reprints []jobRow
		inStore(func(tx *sqlx.Tx) error {
			return tx.SelectContext(ctx, &reprints, `SELECT id, printer_id, kind, payload, status, attempts FROM print_jobs
				WHERE store_id = $1 AND order_id = $2 AND reprinted_by_user_id IS NOT NULL`, storeID, order.ID)
		})
		if len(reprints) != 2 || !strings.Contains(string(reprints[0].Payload), "CETAK ULANG") {
			t.Fatalf("expected two marked reprints, got %d", len(reprints))
		}
		if res := call("POST", fmt.Sprintf("/pos/orders/%s/reprint", order.ID), cashier, map[string]any{"kind": "label"}); res.Code != http.StatusConflict {
			t.Fatalf("no active label printer must be 409, got %d", res.Code)
		}
		var audits int
		inStore(func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &audits, `SELECT COUNT(*) FROM audit_logs WHERE store_id = $1 AND action = 'PRINT_REPRINT' AND reference_id = $2`, storeID, order.ID.String())
		})
		if audits != 1 {
			t.Fatalf("expected one PRINT_REPRINT audit, got %d", audits)
		}
	})
}
