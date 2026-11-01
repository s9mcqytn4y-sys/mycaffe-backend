package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
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

// TestTabLifecycleIntegration tests Iteration 4c-1:
// 1. Cashier opens a new tab on "Meja 12"
// 2. Attempting to open another tab on "meja 12" without allow_duplicate_label returns 409 TAB_LABEL_IN_USE
// 3. Opening tab with allow_duplicate_label=true succeeds (table joining / separate guest groups)
// 4. Barista cannot open tabs (RBAC guard tab.manage)
// 5. Submit Round 1 with items (committed stock, frozen pricing snapshot, round status queued)
// 6. Outbox event 'order.round_submitted' written inside the ACID transaction
// 7. Submit Round 2 with additional items accumulating parent order totals
// 8. GetTab returns complete history of submitted rounds
func TestTabLifecycleIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping Tab Lifecycle integration test")
	}
	db, err := database.NewPostgresDB(&config.Config{DatabaseURL: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	gin.SetMode(gin.TestMode)
	authSvc := service.NewAuthService(db, postgres.NewAuthRepository(), "integration-test-jwt-secret-at-least-32-chars", service.AuthPolicy{
		AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 12 * time.Hour, SessionIdleTimeout: 2 * time.Hour,
		MaxSessions: 3, PINMaxFailures: 5, PINLockout: 15 * time.Minute, MaxConcurrentHashes: 8,
	})
	ah := handler.NewAuthHandler(authSvc)
	ah.DefaultStoreSlug = "central-cafe"

	orderRepo := postgres.NewOrderRepository()
	tabRepo := postgres.NewTabRepository()
	tabSvc := service.NewTabService(db, tabRepo, orderRepo)
	th := handler.NewTabHandler(tabSvc)

	r := gin.New()
	r.POST("/login", ah.Login)

	// Tab routes (Iteration 4c-1)
	r.POST("/pos/tabs", ah.RequirePermission(rbac.TabManage), th.OpenTab)
	r.POST("/pos/tabs/:id/rounds", ah.RequirePermission(rbac.TabManage), th.SubmitRound)
	r.GET("/pos/tabs/:id", ah.RequireStaff(), th.GetTab)

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
		w := call(http.MethodPost, "/login", "", map[string]string{"email": email, "pin": "482913"})
		if w.Code != http.StatusOK {
			t.Fatalf("login %s failed: %d %s", email, w.Code, w.Body.String())
		}
		var out struct {
			AccessToken string `json:"access_token"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out.AccessToken
	}

	ownerToken := login("abdulaziz543333@gmail.com")
	cashierToken := login("cashier.central@mycaffe.local")
	baristaToken := login("barista.central@mycaffe.local")

	latteProdID := "d0000001-0000-4000-8000-000000000001"
	croissantProdID := "d0000005-0000-4000-8000-000000000005"

	var activeTabID string

	tblNumber := "Meja " + uuid.NewString()[:6]

	// 1. RBAC Guard: Barista cannot open tabs
	t.Run("1_BaristaCannotOpenTab_RBACGuard", func(t *testing.T) {
		w := call(http.MethodPost, "/pos/tabs", baristaToken, map[string]any{
			"idempotency_key": "tab-barista-" + uuid.NewString()[:8],
			"table_number":    "Meja 99",
			"customer_name":   "Barista Guest",
		})
		if w.Code != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden for barista opening tab, got %d %s", w.Code, w.Body.String())
		}
	})

	// 2. Cashier successfully opens a new tab on tblNumber
	t.Run("2_CashierOpensTab", func(t *testing.T) {
		w := call(http.MethodPost, "/pos/tabs", cashierToken, map[string]any{
			"idempotency_key": "tab-open-ok-" + uuid.NewString()[:8],
			"table_number":    tblNumber,
			"customer_name":   "Pak Hadi Tab",
			"notes":           "VIP Sofa",
		})
		if w.Code != http.StatusCreated {
			t.Fatalf("open tab failed: %d %s", w.Code, w.Body.String())
		}
		var res service.OpenTabResult
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		if res.Order == nil || res.Order.ID == uuid.Nil {
			t.Fatal("expected non-nil order in open tab response")
		}
		if res.Order.Status != domain.OrderStatusOpen || res.Order.BillingMode != domain.BillingModeOpenTab {
			t.Fatalf("unexpected order status/mode: status=%s mode=%s", res.Order.Status, res.Order.BillingMode)
		}
		if res.Order.TotalAmount != 0 {
			t.Fatalf("empty open tab must have total 0, got %d", res.Order.TotalAmount)
		}
		activeTabID = res.Order.ID.String()
	})

	// 3. Attempting to open duplicate tab on tblNumber (case-insensitive normalized) returns 409 TAB_LABEL_IN_USE
	t.Run("3_DuplicateTabLabelReturns409", func(t *testing.T) {
		w := call(http.MethodPost, "/pos/tabs", cashierToken, map[string]any{
			"idempotency_key": "tab-dup-" + uuid.NewString()[:8],
			"table_number":    "  " + strings.ToLower(tblNumber) + "  ",
			"customer_name":   "Tamu Lain",
		})
		if w.Code != http.StatusConflict {
			t.Fatalf("expected 409 Conflict for duplicate label, got %d %s", w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), "TAB_LABEL_IN_USE") {
			t.Errorf("expected error code TAB_LABEL_IN_USE, got %s", w.Body.String())
		}
		var errRes struct {
			ExistingOrderID string `json:"existing_order_id"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &errRes)
		if errRes.ExistingOrderID != activeTabID {
			t.Errorf("expected existing_order_id=%s, got %s", activeTabID, errRes.ExistingOrderID)
		}
	})

	// 4. Opening with allow_duplicate_label = true succeeds
	t.Run("4_AllowDuplicateLabel_Succeeds", func(t *testing.T) {
		w := call(http.MethodPost, "/pos/tabs", cashierToken, map[string]any{
			"idempotency_key":       "tab-dup-allow-" + uuid.NewString()[:8],
			"table_number":          tblNumber,
			"customer_name":         "Grup Terpisah Meja",
			"allow_duplicate_label": true,
		})
		if w.Code != http.StatusCreated {
			t.Fatalf("expected 201 Created with allow_duplicate_label=true, got %d %s", w.Code, w.Body.String())
		}
	})

	// 5. Submit Round 1 with items (Classic Iced Latte: 24,000 + 5% service (1,200) + 10% tax (2,520) = 27,720 IDR)
	var round1ID string
	t.Run("5_SubmitRound1_LocksPricing_WritesOutboxEvent", func(t *testing.T) {
		w := call(http.MethodPost, "/pos/tabs/"+activeTabID+"/rounds", cashierToken, map[string]any{
			"idempotency_key": "rnd-1-" + uuid.NewString()[:8],
			"notes":           "Less ice please",
			"items": []map[string]any{
				{
					"product_id": latteProdID,
					"quantity":   1,
					"modifiers": []map[string]any{
						{"option_id": "20000001-0000-4000-8000-000000000001", "quantity": 1},
						{"option_id": "20000003-0000-4000-8000-000000000003", "quantity": 1},
					},
				},
			},
		})
		if w.Code != http.StatusCreated {
			t.Fatalf("submit round 1 failed: %d %s", w.Code, w.Body.String())
		}
		var res service.SubmitRoundResult
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		if res.Round == nil || res.Round.RoundNumber != 1 {
			t.Fatalf("expected round_number=1, got %+v", res.Round)
		}
		if res.Round.Status != domain.OrderRoundStatusQueued {
			t.Errorf("expected round status 'queued', got %s", res.Round.Status)
		}
		if res.Order.TotalAmount != 27720 {
			t.Errorf("expected accumulated order total 27720, got %d", res.Order.TotalAmount)
		}
		round1ID = res.Round.ID.String()

		// Verify outbox_events record written
		var eventCount int
		err := database.WithTenantTx(context.Background(), db, res.Order.StoreID, func(tx *sqlx.Tx) error {
			return tx.GetContext(context.Background(), &eventCount, `
				SELECT COUNT(*)
				FROM outbox_events
				WHERE store_id = $1 AND event_type = 'order.round_submitted' AND aggregate_id = $2
			`, res.Order.StoreID, activeTabID)
		})
		if err != nil {
			t.Fatalf("query outbox_events failed: %v", err)
		}
		if eventCount < 1 {
			t.Errorf("expected at least 1 'order.round_submitted' outbox event, got %d", eventCount)
		}
	})

	// 6. Submit Round 2 with Butter Croissant (24,000 + 5% service (1,200) + 10% tax (2,520) = 27,720 IDR)
	// New total = 27,720 + 27,720 = 55,440 IDR
	t.Run("6_SubmitRound2_AccumulatesTotals", func(t *testing.T) {
		w := call(http.MethodPost, "/pos/tabs/"+activeTabID+"/rounds", cashierToken, map[string]any{
			"idempotency_key": "rnd-2-" + uuid.NewString()[:8],
			"notes":           "Warm up croissant",
			"items": []map[string]any{
				{
					"product_id": croissantProdID,
					"quantity":   1,
				},
			},
		})
		if w.Code != http.StatusCreated {
			t.Fatalf("submit round 2 failed: %d %s", w.Code, w.Body.String())
		}
		var res service.SubmitRoundResult
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		if res.Round == nil || res.Round.RoundNumber != 2 {
			t.Fatalf("expected round_number=2, got %+v", res.Round)
		}
		expectedTotal := int64(27720 + 27720)
		if res.Order.TotalAmount != expectedTotal {
			t.Errorf("expected total %d, got %d", expectedTotal, res.Order.TotalAmount)
		}
	})

	// 7. Get Tab Detail returns both rounds
	t.Run("7_GetTabDetail_ReturnsAllRounds", func(t *testing.T) {
		w := call(http.MethodGet, "/pos/tabs/"+activeTabID, ownerToken, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("get tab failed: %d %s", w.Code, w.Body.String())
		}
		var detail service.TabDetailResult
		_ = json.Unmarshal(w.Body.Bytes(), &detail)
		if detail.Order == nil || detail.Order.ID.String() != activeTabID {
			t.Fatalf("unexpected tab detail order: %+v", detail.Order)
		}
		if len(detail.Rounds) != 2 {
			t.Fatalf("expected 2 rounds, got %d", len(detail.Rounds))
		}
		if detail.Rounds[0].ID.String() != round1ID {
			t.Errorf("expected round 1 ID %s, got %s", round1ID, detail.Rounds[0].ID)
		}
		if detail.Rounds[0].RoundNumber != 1 || detail.Rounds[1].RoundNumber != 2 {
			t.Errorf("unexpected round numbers: %d, %d", detail.Rounds[0].RoundNumber, detail.Rounds[1].RoundNumber)
		}
	})
}
