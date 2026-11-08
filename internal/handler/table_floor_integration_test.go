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
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/config"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/handler"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/service"
)

// TestTableFloorIntegration covers v0.9 PR 9.1 (decision W5): area filter, merge into one shared
// session, move a whole session, split a merged table, concurrent merges and tenant isolation.
func TestTableFloorIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping table floor integration test")
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
	th := handler.NewTableHandler(service.NewTableService(db))

	r := gin.New()
	r.POST("/login", ah.Login)
	r.GET("/pos/tables", ah.RequireStaff(), th.ListTables)
	r.POST("/pos/tables", ah.RequirePermission(rbac.TableManage), th.CreateTable)
	r.POST("/pos/tables/:id/sessions", ah.RequirePermission(rbac.TabManage), th.OpenSession)
	r.POST("/pos/table-sessions/:id/close", ah.RequirePermission(rbac.TabManage), th.CloseSession)
	r.POST("/pos/table-sessions/:id/move", ah.RequirePermission(rbac.TabManage), th.MoveSession)
	r.POST("/pos/table-sessions/:id/merge", ah.RequirePermission(rbac.TabManage), th.MergeSession)
	r.POST("/pos/table-sessions/:id/split", ah.RequirePermission(rbac.TabManage), th.SplitTable)

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
	inStore := func(fn func(tx *sqlx.Tx) error) {
		t.Helper()
		if err := database.WithTenantTx(ctx, db, centralID, fn); err != nil {
			t.Fatalf("tenant tx failed: %v", err)
		}
	}
	var cashier string
	board := func(query string) map[uuid.UUID]domain.TableBoardEntry {
		t.Helper()
		res := call("GET", "/pos/tables"+query, cashier, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("list tables failed: %d: %s", res.Code, res.Body.String())
		}
		var body struct {
			Tables []domain.TableBoardEntry `json:"tables"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &body)
		out := map[uuid.UUID]domain.TableBoardEntry{}
		for _, e := range body.Tables {
			out[e.ID] = e
		}
		return out
	}

	owner := login("central-cafe", "abdulaziz543333@gmail.com")
	cashier = login("central-cafe", "cashier.central@mycaffe.local")
	suffix := uuid.NewString()[:6]
	area := "Teras-" + suffix

	newTable := func(name, section string) domain.Table {
		t.Helper()
		res := call("POST", "/pos/tables", owner, map[string]any{"table_number": name + "-" + suffix, "section": section, "capacity": 4})
		if res.Code != http.StatusCreated {
			t.Fatalf("create table failed: %d: %s", res.Code, res.Body.String())
		}
		var tb domain.Table
		_ = json.Unmarshal(res.Body.Bytes(), &tb)
		return tb
	}
	seat := func(tb domain.Table) domain.TableSession {
		t.Helper()
		res := call("POST", fmt.Sprintf("/pos/tables/%s/sessions", tb.ID), cashier, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("open session failed: %d: %s", res.Code, res.Body.String())
		}
		var s domain.TableSession
		_ = json.Unmarshal(res.Body.Bytes(), &s)
		return s
	}
	unpaidOrder := func(tb domain.Table, session domain.TableSession, amount int64) uuid.UUID {
		t.Helper()
		id := uuid.New()
		inStore(func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO orders (id, store_id, idempotency_key, request_hash, order_number, queue_number, customer_name, order_type,
					status, payment_status, table_id, table_number, table_session_id, subtotal_amount, total_amount)
				VALUES ($1, $2, $3, $4, $5, 'Q-F', 'Tamu', 'dine_in', 'pending_payment', 'unpaid', $6, $7, $8, $9, $9)`,
				id, centralID, "floor-test-"+id.String(), strings.Repeat("f", 64), "FLR-"+id.String()[:8], tb.ID, tb.TableNumber, session.ID, amount)
			return err
		})
		return id
	}
	expectCode := func(res *httptest.ResponseRecorder, code int, errCode string) {
		t.Helper()
		if res.Code != code || (errCode != "" && !strings.Contains(res.Body.String(), errCode)) {
			t.Fatalf("expected %d %s, got %d: %s", code, errCode, res.Code, res.Body.String())
		}
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		inStore(func(tx *sqlx.Tx) error { return tx.GetContext(ctx, &n, query, args...) })
		return n
	}

	a, b := newTable("A", area), newTable("B", area)
	c, d := newTable("C", "Dalam-"+suffix), newTable("D", "Dalam-"+suffix)

	t.Run("AreaFilterIsCaseInsensitive", func(t *testing.T) {
		got := board("?section=" + strings.ToLower(area))
		if len(got) != 2 || got[a.ID].ID != a.ID || got[b.ID].ID != b.ID {
			t.Fatalf("expected exactly tables A and B in %s, got %d entries", area, len(got))
		}
	})

	sessA, sessB := seat(a), seat(b)
	orderA := unpaidOrder(a, sessA, 20000)
	orderB := unpaidOrder(b, sessB, 15000)
	// A guest seated at A (scanned table recorded) and a guest at B from before 000033 (no table_id).
	guest := func(session domain.TableSession, table *uuid.UUID) uuid.UUID {
		t.Helper()
		id, hash := uuid.New(), uuid.New()
		inStore(func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO guest_sessions (id, store_id, table_session_id, table_id, token_hash, expires_at)
				VALUES ($1, $2, $3, $4, $5, NOW() + INTERVAL '1 hour')`, id, centralID, session.ID, table, append(hash[:], hash[:]...))
			return err
		})
		return id
	}
	guestTable := func(id uuid.UUID) (uuid.UUID, uuid.UUID) {
		t.Helper()
		var row *postgres.GuestSessionRow
		inStore(func(tx *sqlx.Tx) error {
			var err error
			row, err = postgres.NewGuestRepository().GetGuestSession(ctx, tx, centralID, id, false)
			return err
		})
		return row.TableSessionID, row.TableID
	}
	guestA, guestB := guest(sessA, &a.ID), guest(sessB, nil)

	t.Run("MergeMakesOneSharedSession", func(t *testing.T) {
		expectCode(call("POST", fmt.Sprintf("/pos/table-sessions/%s/merge", sessA.ID), cashier, map[string]any{"session_id": sessA.ID}),
			http.StatusUnprocessableEntity, "")
		expectCode(call("POST", fmt.Sprintf("/pos/table-sessions/%s/merge", sessA.ID), cashier, map[string]any{"session_id": sessB.ID}),
			http.StatusOK, "")

		bd := board("")
		ea, eb := bd[a.ID], bd[b.ID]
		if eb.Session == nil || eb.Session.ID != sessA.ID || eb.MergedIntoTableID == nil || *eb.MergedIntoTableID != a.ID {
			t.Fatalf("table B must show the shared session of A, got %+v", eb)
		}
		if ea.MergedIntoTableID != nil || ea.UnpaidAmount != 35000 || eb.UnpaidAmount != 35000 {
			t.Fatalf("shared bill must total both tables (35000), got A=%d B=%d", ea.UnpaidAmount, eb.UnpaidAmount)
		}
		if n := count(`SELECT COUNT(*) FROM orders WHERE store_id = $1 AND id = $2 AND table_session_id = $3 AND table_id = $4`, centralID, orderB, sessA.ID, b.ID); n != 1 {
			t.Fatalf("order of table B must move to the shared session and keep its table")
		}
		// Guests of B now bill on the shared session but keep table B for kitchen delivery.
		if sess, tb := guestTable(guestB); sess != sessA.ID || tb != b.ID {
			t.Fatalf("guest of merged table B must order into %s at table B, got session %s table %s", sessA.ID, sess, tb)
		}
		if n := count(`SELECT COUNT(*) FROM table_sessions WHERE store_id = $1 AND id = $2 AND status = 'closed' AND close_reason = 'merged'`, centralID, sessB.ID); n != 1 {
			t.Fatalf("merged session must close with reason merged")
		}
		// Seating at (or a guest scanning) table B now lands in the shared session.
		if again := seat(b); again.ID != sessA.ID {
			t.Fatalf("opening merged table B must return the shared session %s, got %s", sessA.ID, again.ID)
		}
		if n := count(`SELECT COUNT(*) FROM outbox_events WHERE store_id = $1 AND event_type = 'table.merged' AND aggregate_id = $2`, centralID, sessA.ID.String()); n != 1 {
			t.Fatalf("expected one table.merged event, got %d", n)
		}
		if n := count(`SELECT COUNT(*) FROM audit_logs WHERE store_id = $1 AND action = 'TABLE_MERGE' AND reference_id = $2`, centralID, sessA.ID.String()); n != 1 {
			t.Fatalf("expected one TABLE_MERGE audit, got %d", n)
		}
		expectCode(call("POST", fmt.Sprintf("/pos/table-sessions/%s/merge", sessA.ID), cashier, map[string]any{"session_id": sessB.ID}),
			http.StatusConflict, "TABLE_SESSION_NOT_OPEN")
	})

	t.Run("MoveTakesTheWholeSession", func(t *testing.T) {
		expectCode(call("POST", fmt.Sprintf("/pos/table-sessions/%s/move", sessA.ID), cashier, map[string]any{"to_table_id": b.ID}),
			http.StatusConflict, "TABLE_SESSION_OPEN")
		expectCode(call("POST", fmt.Sprintf("/pos/table-sessions/%s/move", sessA.ID), cashier, map[string]any{"to_table_id": c.ID}),
			http.StatusOK, "")
		bd := board("")
		if bd[a.ID].Status != domain.TableStatusAvailable || bd[a.ID].Session != nil {
			t.Fatalf("table A must be free after the move, got %+v", bd[a.ID])
		}
		if bd[c.ID].Session == nil || bd[c.ID].Session.ID != sessA.ID {
			t.Fatalf("table C must hold the moved session, got %+v", bd[c.ID])
		}
		if m := bd[b.ID].MergedIntoTableID; m == nil || *m != c.ID {
			t.Fatalf("table B must stay merged and follow the session to C, got %v", m)
		}
		if n := count(`SELECT COUNT(*) FROM orders WHERE store_id = $1 AND id = $2 AND table_id = $3 AND table_number = $4`, centralID, orderA, c.ID, c.TableNumber); n != 1 {
			t.Fatalf("order placed at A must follow the session to C")
		}
		if n := count(`SELECT COUNT(*) FROM orders WHERE store_id = $1 AND id = $2 AND table_id = $3`, centralID, orderB, b.ID); n != 1 {
			t.Fatalf("order placed at merged table B must keep table B")
		}
		if _, tb := guestTable(guestA); tb != c.ID {
			t.Fatalf("guest seated at A must move to C, got %s", tb)
		}
		if _, tb := guestTable(guestB); tb != b.ID {
			t.Fatalf("guest of merged table B must stay at B, got %s", tb)
		}
	})

	t.Run("SplitFreesTheMergedTable", func(t *testing.T) {
		expectCode(call("POST", fmt.Sprintf("/pos/table-sessions/%s/split", sessA.ID), cashier, map[string]any{"table_id": b.ID}),
			http.StatusOK, "")
		if e := board("")[b.ID]; e.Status != domain.TableStatusAvailable || e.Session != nil {
			t.Fatalf("split table B must be free, got %+v", e)
		}
		expectCode(call("POST", fmt.Sprintf("/pos/table-sessions/%s/split", sessA.ID), cashier, map[string]any{"table_id": b.ID}),
			http.StatusConflict, "TABLE_NOT_MERGED")
		if n := count(`SELECT COUNT(*) FROM orders WHERE store_id = $1 AND id = $2 AND table_session_id = $3`, centralID, orderB, sessA.ID); n != 1 {
			t.Fatalf("orders stay on the shared bill after a split")
		}
	})

	t.Run("ConcurrentCrossMergeHasOneWinner", func(t *testing.T) {
		sessD := seat(d)
		codes := make([]int, 2)
		var wg sync.WaitGroup
		for i, pair := range [][2]uuid.UUID{{sessA.ID, sessD.ID}, {sessD.ID, sessA.ID}} {
			wg.Add(1)
			go func(i int, primary, other uuid.UUID) {
				defer wg.Done()
				codes[i] = call("POST", fmt.Sprintf("/pos/table-sessions/%s/merge", primary), cashier, map[string]any{"session_id": other}).Code
			}(i, pair[0], pair[1])
		}
		wg.Wait()
		if !(codes[0] == http.StatusOK && codes[1] == http.StatusConflict) && !(codes[0] == http.StatusConflict && codes[1] == http.StatusOK) {
			t.Fatalf("exactly one concurrent merge must win, got %v", codes)
		}
	})

	t.Run("ClosingTheSharedSessionFreesEveryTable", func(t *testing.T) {
		inStore(func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE orders SET status = 'cancelled', stock_status = 'not_tracked' WHERE store_id = $1 AND id = ANY($2)`,
				centralID, []uuid.UUID{orderA, orderB})
			return err
		})
		bd := board("")
		var open uuid.UUID
		for _, tb := range []domain.Table{c, d} {
			if e := bd[tb.ID]; e.Session != nil && e.MergedIntoTableID == nil {
				open = e.Session.ID
			}
		}
		expectCode(call("POST", fmt.Sprintf("/pos/table-sessions/%s/close", open), cashier, nil), http.StatusOK, "")
		bd = board("")
		for _, tb := range []domain.Table{a, b, c, d} {
			if e := bd[tb.ID]; e.Status != domain.TableStatusAvailable || e.Session != nil {
				t.Fatalf("table %s must be free after closing the shared session, got %+v", tb.TableNumber, e)
			}
		}
		if n := count(`SELECT COUNT(*) FROM table_session_links WHERE store_id = $1 AND primary_session_id = $2 AND unlinked_at IS NULL`, centralID, open); n != 0 {
			t.Fatalf("closing must unlink merged tables, %d still linked", n)
		}
	})

	t.Run("OtherStoreCannotTouchSessions", func(t *testing.T) {
		express := login("express-counter", "cashier.express@mycaffe.local")
		expectCode(call("POST", fmt.Sprintf("/pos/table-sessions/%s/move", sessA.ID), express, map[string]any{"to_table_id": a.ID}),
			http.StatusNotFound, "")
	})
}
