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
	"sync"
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

// TestTableTimerIntegration covers v0.9 PR 9.2 (decision W6): settings validation, the timer set
// when a session opens, board states, exactly-once warning and expiry events (also with two workers
// at once), extending with audit, and sessions without a timer.
func TestTableTimerIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping table timer integration test")
	}
	db, err := database.NewPostgresDB(&config.Config{DatabaseURL: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	centralID := uuid.MustParse("11111111-1111-4111-8111-111111111111")

	gin.SetMode(gin.TestMode)
	authSvc := service.NewAuthService(db, postgres.NewAuthRepository(), "integration-test-jwt-secret-at-least-32-chars", service.AuthPolicy{
		AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 12 * time.Hour, SessionIdleTimeout: 2 * time.Hour,
		MaxSessions: 3, PINMaxFailures: 5, PINLockout: 15 * time.Minute, MaxConcurrentHashes: 8,
	})
	ah := handler.NewAuthHandler(authSvc)
	tables := service.NewTableService(db)
	th := handler.NewTableHandler(tables)
	orderRepo, paymentRepo := postgres.NewOrderRepository(), postgres.NewPaymentRepository()
	pos := handler.NewPOSHandler(service.NewOrderService(db, orderRepo), service.NewPaymentService(db, paymentRepo, orderRepo, nil), service.NewSettingsService(db))

	r := gin.New()
	r.POST("/login", ah.Login)
	r.PATCH("/store/settings", ah.RequirePermission(rbac.StoreSettingsManage), pos.UpdateSettings)
	r.GET("/pos/tables", ah.RequireStaff(), th.ListTables)
	r.POST("/pos/tables", ah.RequirePermission(rbac.TableManage), th.CreateTable)
	r.POST("/pos/tables/:id/sessions", ah.RequirePermission(rbac.TabManage), th.OpenSession)
	r.POST("/pos/table-sessions/:id/extend", ah.RequirePermission(rbac.TabManage), th.ExtendTimer)

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
		return body.AccessToken
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if err := database.WithTenantTx(ctx, db, centralID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, query, args...)
			return err
		}); err != nil {
			t.Fatalf("exec failed: %v", err)
		}
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := database.WithTenantTx(ctx, db, centralID, func(tx *sqlx.Tx) error { return tx.GetContext(ctx, &n, query, args...) }); err != nil {
			t.Fatalf("count failed: %v", err)
		}
		return n
	}
	owner := login("abdulaziz543333@gmail.com")
	cashier := login("cashier.central@mycaffe.local")
	setLimit := func(limit, warning int) *httptest.ResponseRecorder {
		return call("PATCH", "/store/settings", owner, map[string]any{"table_time_limit_minutes": limit, "table_timer_warning_minutes": warning})
	}
	// Other tests open sessions in this store: turn the limit off again whatever happens.
	t.Cleanup(func() {
		exec(`UPDATE store_settings SET table_time_limit_minutes = 0, table_timer_warning_minutes = 10 WHERE store_id = $1`, centralID)
	})

	suffix := uuid.NewString()[:6]
	newSession := func(name string) (domain.Table, domain.TableSession) {
		t.Helper()
		res := call("POST", "/pos/tables", owner, map[string]any{"table_number": name + "-" + suffix})
		if res.Code != http.StatusCreated {
			t.Fatalf("create table failed: %d: %s", res.Code, res.Body.String())
		}
		var tb domain.Table
		_ = json.Unmarshal(res.Body.Bytes(), &tb)
		res = call("POST", fmt.Sprintf("/pos/tables/%s/sessions", tb.ID), cashier, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("open session failed: %d: %s", res.Code, res.Body.String())
		}
		var s domain.TableSession
		_ = json.Unmarshal(res.Body.Bytes(), &s)
		return tb, s
	}
	timerState := func(tableID uuid.UUID) string {
		t.Helper()
		res := call("GET", "/pos/tables", cashier, nil)
		var body struct {
			Tables []domain.TableBoardEntry `json:"tables"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &body)
		for _, e := range body.Tables {
			if e.ID == tableID {
				return e.TimerState
			}
		}
		t.Fatalf("table %s not on the board", tableID)
		return ""
	}
	events := func(event string, sessionID uuid.UUID) int {
		return count(`SELECT COUNT(*) FROM outbox_events WHERE store_id = $1 AND event_type = $2 AND aggregate_id = $3`, centralID, event, sessionID.String())
	}
	process := func() {
		t.Helper()
		if err := tables.ProcessTimers(ctx); err != nil {
			t.Fatalf("process timers: %v", err)
		}
	}

	t.Run("SettingsValidation", func(t *testing.T) {
		for _, bad := range [][2]int{{10, 5}, {481, 10}, {60, 60}, {60, 0}} {
			if res := setLimit(bad[0], bad[1]); res.Code == http.StatusOK {
				t.Fatalf("limit %d warning %d must be rejected", bad[0], bad[1])
			}
		}
	})

	t.Run("NoLimitMeansNoTimer", func(t *testing.T) {
		if res := setLimit(0, 10); res.Code != http.StatusOK {
			t.Fatalf("turn off failed: %d: %s", res.Code, res.Body.String())
		}
		tb, s := newSession("NT")
		if s.EndsAt != nil || timerState(tb.ID) != domain.TimerNone {
			t.Fatalf("session opened without a limit must have no timer, got %v", s.EndsAt)
		}
		if res := call("POST", fmt.Sprintf("/pos/table-sessions/%s/extend", s.ID), cashier, map[string]any{"minutes": 15}); res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "TABLE_TIMER_OFF") {
			t.Fatalf("extending a session without timer must be 409 TABLE_TIMER_OFF, got %d: %s", res.Code, res.Body.String())
		}
	})

	if res := setLimit(90, 10); res.Code != http.StatusOK {
		t.Fatalf("set limit failed: %d: %s", res.Code, res.Body.String())
	}
	tb, s := newSession("TM")

	t.Run("OpenStartsTimer", func(t *testing.T) {
		if s.EndsAt == nil {
			t.Fatal("session opened under a 90 minute limit must have ends_at")
		}
		// PostgreSQL keeps microseconds; the returned opened_at still has Go nanoseconds.
		if d := s.EndsAt.Sub(s.OpenedAt) - 90*time.Minute; d < -time.Millisecond || d > time.Millisecond {
			t.Fatalf("ends_at must be opened_at + 90m, off by %v", d)
		}
		if st := timerState(tb.ID); st != domain.TimerOK {
			t.Fatalf("expected ok, got %s", st)
		}
	})

	t.Run("WarningOnceEvenWithTwoWorkers", func(t *testing.T) {
		exec(`UPDATE table_sessions SET ends_at = NOW() + INTERVAL '5 minutes' WHERE store_id = $1 AND id = $2`, centralID, s.ID)
		if st := timerState(tb.ID); st != domain.TimerWarning {
			t.Fatalf("expected warning, got %s", st)
		}
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); process() }()
		}
		wg.Wait()
		process()
		if n := events("table.timer_warning", s.ID); n != 1 {
			t.Fatalf("expected exactly one table.timer_warning, got %d", n)
		}
	})

	t.Run("ExpiryOnceAndOrdersStillAllowed", func(t *testing.T) {
		exec(`UPDATE table_sessions SET ends_at = NOW() - INTERVAL '1 minute' WHERE store_id = $1 AND id = $2`, centralID, s.ID)
		process()
		process()
		if n := events("table.timer_expired", s.ID); n != 1 {
			t.Fatalf("expected exactly one table.timer_expired, got %d", n)
		}
		if st := timerState(tb.ID); st != domain.TimerExpired {
			t.Fatalf("expected expired, got %s", st)
		}
		if n := count(`SELECT COUNT(*) FROM table_sessions WHERE store_id = $1 AND id = $2 AND status = 'open'`, centralID, s.ID); n != 1 {
			t.Fatal("an expired timer must never close the session")
		}
	})

	t.Run("ExtendRearmsTimer", func(t *testing.T) {
		if res := call("POST", fmt.Sprintf("/pos/table-sessions/%s/extend", s.ID), cashier, map[string]any{"minutes": 241}); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("241 minutes must be rejected, got %d", res.Code)
		}
		before := time.Now()
		res := call("POST", fmt.Sprintf("/pos/table-sessions/%s/extend", s.ID), cashier, map[string]any{"minutes": 30})
		if res.Code != http.StatusOK {
			t.Fatalf("extend failed: %d: %s", res.Code, res.Body.String())
		}
		var out domain.TableSession
		_ = json.Unmarshal(res.Body.Bytes(), &out)
		// The timer had already run out, so the extension counts from now, not from the old end.
		if out.EndsAt == nil || out.EndsAt.Before(before.Add(29*time.Minute)) || out.EndsAt.After(time.Now().Add(31*time.Minute)) {
			t.Fatalf("extended ends_at must be about now + 30m, got %v", out.EndsAt)
		}
		if out.WarnedAt != nil || out.ExpiredAt != nil || out.ExtendedMinutes != 30 {
			t.Fatalf("extend must re-arm the timer and count minutes, got %+v", out)
		}
		if st := timerState(tb.ID); st != domain.TimerOK {
			t.Fatalf("expected ok after extending, got %s", st)
		}
		if n := count(`SELECT COUNT(*) FROM audit_logs WHERE store_id = $1 AND action = 'TABLE_TIMER_EXTEND' AND reference_id = $2`, centralID, s.ID.String()); n != 1 {
			t.Fatalf("expected one TABLE_TIMER_EXTEND audit, got %d", n)
		}
		if n := events("table.timer_extended", s.ID); n != 1 {
			t.Fatalf("expected one table.timer_extended event, got %d", n)
		}
		// Re-armed: crossing the end again publishes a second expiry.
		exec(`UPDATE table_sessions SET ends_at = NOW() - INTERVAL '1 minute' WHERE store_id = $1 AND id = $2`, centralID, s.ID)
		process()
		if n := events("table.timer_expired", s.ID); n != 2 {
			t.Fatalf("expected a second table.timer_expired after re-arming, got %d", n)
		}
	})
}
