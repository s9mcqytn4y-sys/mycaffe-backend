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
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/realtime"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/service"
)

// TestQueueDisplayIntegration covers v0.9 PR 9.3 (decision W7): owner-managed display tokens, the
// public queue with numbers and status only, the "queue" WebSocket channel, revocation and limits.
func TestQueueDisplayIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping queue display integration test")
	}
	db, err := database.NewPostgresDB(&config.Config{DatabaseURL: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	centralID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	expressID := uuid.MustParse("22222222-2222-4222-8222-222222222222")

	gin.SetMode(gin.TestMode)
	authSvc := service.NewAuthService(db, postgres.NewAuthRepository(), "integration-test-jwt-secret-at-least-32-chars", service.AuthPolicy{
		AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 12 * time.Hour, SessionIdleTimeout: 2 * time.Hour,
		MaxSessions: 3, PINMaxFailures: 5, PINLockout: 15 * time.Minute, MaxConcurrentHashes: 8,
	})
	ah := handler.NewAuthHandler(authSvc)
	rt := service.NewRealtimeService(db)
	dh := handler.NewDisplayHandler(service.NewDisplayService(db), rt)

	r := gin.New()
	r.POST("/login", ah.Login)
	displays := r.Group("/store/display-tokens", ah.RequirePermission(rbac.StoreSettingsManage))
	displays.GET("", dh.ListTokens)
	displays.POST("", dh.CreateToken)
	displays.POST("/:id/revoke", dh.RevokeToken)
	r.GET("/public/queue", dh.Queue)
	r.POST("/public/queue/ws-ticket", dh.IssueTicket)

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
		if err := database.WithTenantTx(ctx, db, centralID, fn); err != nil {
			t.Fatalf("tenant tx failed: %v", err)
		}
	}
	owner := login("abdulaziz543333@gmail.com")
	cashier := login("cashier.central@mycaffe.local")
	create := func(name string) service.DisplayToken {
		t.Helper()
		res := call("POST", "/store/display-tokens", owner, map[string]any{"name": name})
		if res.Code != http.StatusCreated {
			t.Fatalf("create display failed: %d: %s", res.Code, res.Body.String())
		}
		var tok service.DisplayToken
		_ = json.Unmarshal(res.Body.Bytes(), &tok)
		return tok
	}

	// Orders of today: one waiting at the counter, one not yet paid (never shown).
	suffix := strings.ToUpper(uuid.NewString()[:5])
	secretName := "Tamu Rahasia " + suffix
	order := func(status, queue string) uuid.UUID {
		t.Helper()
		id := uuid.New()
		inStore(func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO orders (id, store_id, idempotency_key, request_hash, order_number, queue_number, customer_name, customer_phone, order_type,
					status, payment_status, subtotal_amount, total_amount)
				VALUES ($1, $2, $3, $4, $5, $6, $7, '+6281299990000', 'takeaway', $8, 'paid', 30000, 30000)`,
				id, centralID, "tv-test-"+id.String(), strings.Repeat("d", 64), "TV-"+id.String()[:8], queue, secretName, status)
			return err
		})
		return id
	}
	readyQueue, unpaidQueue := "R"+suffix, "U"+suffix
	readyOrder := order("ready", readyQueue)
	order("pending_payment", unpaidQueue)

	var tv service.DisplayToken
	t.Run("OwnerCreatesAndListsCashierCannot", func(t *testing.T) {
		if res := call("POST", "/store/display-tokens", cashier, map[string]any{"name": "TV"}); res.Code != http.StatusForbidden {
			t.Fatalf("cashier must not manage displays, got %d", res.Code)
		}
		if res := call("POST", "/store/display-tokens", owner, map[string]any{"name": "  "}); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("blank name must be 422, got %d", res.Code)
		}
		tv = create("TV Kasir " + suffix)
		if !strings.HasPrefix(tv.Token, centralID.String()+".") {
			t.Fatalf("token must carry the store id, got %q", tv.Token)
		}
		res := call("GET", "/store/display-tokens", owner, nil)
		if res.Code != http.StatusOK || strings.Contains(res.Body.String(), tv.Token) || !strings.Contains(res.Body.String(), tv.ID.String()) {
			t.Fatalf("list must show the display without its token: %d %s", res.Code, res.Body.String())
		}
	})

	t.Run("QueueShowsNumbersAndStatusOnly", func(t *testing.T) {
		if res := call("GET", "/public/queue", "", nil); res.Code != http.StatusUnauthorized {
			t.Fatalf("missing token must be 401, got %d", res.Code)
		}
		if res := call("GET", "/public/queue", "Bearer "+tv.Token, nil); res.Code != http.StatusUnauthorized {
			t.Fatalf("wrong scheme must be 401, got %d", res.Code)
		}
		forged := strings.Replace(tv.Token, centralID.String(), expressID.String(), 1)
		if res := call("GET", "/public/queue", "Display "+forged, nil); res.Code != http.StatusUnauthorized {
			t.Fatalf("token moved to another store must be 401, got %d", res.Code)
		}
		res := call("GET", "/public/queue", "Display "+tv.Token, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("queue failed: %d: %s", res.Code, res.Body.String())
		}
		body := res.Body.String()
		if !strings.Contains(body, readyQueue) || strings.Contains(body, unpaidQueue) {
			t.Fatalf("queue must list the ready order and hide the unpaid one: %s", body)
		}
		for _, leak := range []string{secretName, "+6281299990000", "30000", "total_amount", "customer"} {
			if strings.Contains(body, leak) {
				t.Fatalf("queue leaks %q: %s", leak, body)
			}
		}
		var lastSeen *time.Time
		inStore(func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &lastSeen, `SELECT last_seen_at FROM display_tokens WHERE store_id = $1 AND id = $2`, centralID, tv.ID)
		})
		if lastSeen == nil {
			t.Fatal("reading the queue must record last_seen_at")
		}
	})

	var grant *service.WSGrant
	t.Run("WebSocketQueueChannelOnly", func(t *testing.T) {
		res := call("POST", "/public/queue/ws-ticket", "Display "+tv.Token, nil)
		if res.Code != http.StatusCreated {
			t.Fatalf("ws ticket failed: %d: %s", res.Code, res.Body.String())
		}
		var ticket service.WSTicket
		_ = json.Unmarshal(res.Body.Bytes(), &ticket)
		var err error
		if grant, err = rt.Redeem(ctx, ticket.Ticket); err != nil {
			t.Fatalf("redeem: %v", err)
		}
		if len(grant.Channels) != 1 || grant.Channels[0] != realtime.ChannelQueue || grant.DisplayTokenID == nil || *grant.DisplayTokenID != tv.ID {
			t.Fatalf("display grant must be channel queue only, got %+v", grant)
		}
		// An order event reaches the display as queue.updated with the number and status only.
		var lastSeq int64
		inStore(func(tx *sqlx.Tx) error {
			if err := tx.GetContext(ctx, &lastSeq, `SELECT COALESCE(MAX(store_seq), 0) FROM outbox_events WHERE store_id = $1`, centralID); err != nil {
				return err
			}
			return postgres.NewOrderRepository().InsertOutboxEvent(ctx, tx, &domain.OutboxEvent{
				ID: uuid.New(), StoreID: centralID, EventType: "kds.ticket_updated", AggregateType: "order",
				AggregateID: readyOrder.String(), Payload: fmt.Sprintf(`{"customer_name":%q}`, secretName), CreatedAt: time.Now().UTC(),
			})
		})
		msgs, err := rt.Replay(ctx, centralID, grant.Channels, lastSeq)
		if err != nil {
			t.Fatalf("replay: %v", err)
		}
		var found bool
		for _, m := range msgs {
			raw, _ := json.Marshal(m)
			if m.Type != "queue.updated" || strings.Contains(string(raw), secretName) {
				t.Fatalf("display channel must only get queue.updated without staff payload, got %s", raw)
			}
			if strings.Contains(string(raw), readyQueue) && strings.Contains(string(raw), `"ready"`) {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected queue.updated for %s, got %d messages", readyQueue, len(msgs))
		}
	})

	t.Run("RevokeCutsAccessAtOnce", func(t *testing.T) {
		if res := call("POST", fmt.Sprintf("/store/display-tokens/%s/revoke", tv.ID), owner, nil); res.Code != http.StatusNoContent {
			t.Fatalf("revoke failed: %d: %s", res.Code, res.Body.String())
		}
		if res := call("POST", fmt.Sprintf("/store/display-tokens/%s/revoke", tv.ID), owner, nil); res.Code != http.StatusNoContent {
			t.Fatalf("revoking twice must be a no-op, got %d", res.Code)
		}
		if res := call("POST", fmt.Sprintf("/store/display-tokens/%s/revoke", uuid.New()), owner, nil); res.Code != http.StatusNotFound {
			t.Fatalf("unknown display must be 404, got %d", res.Code)
		}
		if res := call("GET", "/public/queue", "Display "+tv.Token, nil); res.Code != http.StatusUnauthorized {
			t.Fatalf("revoked display must be 401, got %d", res.Code)
		}
		if live, err := rt.Alive(ctx, grant); err != nil || live {
			t.Fatalf("an open WebSocket of a revoked display must be closed (alive=%v err=%v)", live, err)
		}
		var audits int
		inStore(func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &audits, `SELECT COUNT(*) FROM audit_logs WHERE store_id = $1 AND reference_id = $2
				AND action IN ('DISPLAY_TOKEN_CREATED', 'DISPLAY_TOKEN_REVOKED')`, centralID, tv.ID.String())
		})
		if audits != 2 {
			t.Fatalf("expected create and revoke audits, got %d", audits)
		}
	})

	t.Run("AtMostTenActiveDisplays", func(t *testing.T) {
		var active int
		inStore(func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &active, `SELECT COUNT(*) FROM display_tokens WHERE store_id = $1 AND revoked_at IS NULL`, centralID)
		})
		var made []service.DisplayToken
		for i := active; i < 10; i++ {
			made = append(made, create(fmt.Sprintf("TV %d %s", i, suffix)))
		}
		if res := call("POST", "/store/display-tokens", owner, map[string]any{"name": "TV 11"}); res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "DISPLAY_TOKEN_LIMIT") {
			t.Fatalf("the 11th display must be 409 DISPLAY_TOKEN_LIMIT, got %d: %s", res.Code, res.Body.String())
		}
		for _, tok := range made {
			call("POST", fmt.Sprintf("/store/display-tokens/%s/revoke", tok.ID), owner, nil)
		}
	})
}
