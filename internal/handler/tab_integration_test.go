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

// TestTabLifecycleIntegration validates Iteration 4c-1 end-to-end:
// 1. RBAC Guard: Barista cannot open tabs
// 2. Cashier opens new tab
// 3. Duplicate tab label returns 409 TAB_LABEL_IN_USE
// 4. Opening with allow_duplicate_label = true succeeds
// 5. Submit Round 1 with items (pricing snapshot frozen, outbox event written)
// 6. Submit Round 2 with items accumulating totals
// 7. Get Tab Detail returns all rounds with nested items and options (read-only, no row lock)
// 8. Parallel open tab label race guarded by PostgreSQL advisory transaction lock
// 9. OpenTab idempotency: replay returns 200, concurrent race returns 201+200, key reuse with different payload returns 409
// 10. OpenTab with 100-char idempotency key does not overflow round key constraint
// 11. SubmitRound concurrent idempotency race: one round inserted, stock deducted exactly once
// 12. SubmitRound replay after tab is closed returns 200 replay (not TAB_NOT_OPEN)
// 13. Guard against paying an open tab: InitiatePayment returns 409 TAB_NOT_CLOSED, 0 payments, 0 cash movements
// 14. Cumulative tax calculation proof: Rp10.010 with 5% service & 10% tax proves single-order quote parity vs round accumulation
// 15. Non-open_tab order returns 404 on GET /pos/tabs/:id
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
	paymentRepo := postgres.NewPaymentRepository()
	tabSvc := service.NewTabService(db, tabRepo, orderRepo)
	orderSvc := service.NewOrderService(db, orderRepo)
	paymentSvc := service.NewPaymentService(db, paymentRepo, orderRepo, nil)
	settingsSvc := service.NewSettingsService(db)

	th := handler.NewTabHandler(tabSvc)
	pos := handler.NewPOSHandler(orderSvc, paymentSvc, settingsSvc)

	r := gin.New()
	r.POST("/login", ah.Login)

	// Tab routes (Iteration 4c-1)
	r.POST("/pos/tabs", ah.RequirePermission(rbac.TabManage), th.OpenTab)
	r.POST("/pos/tabs/:id/rounds", ah.RequirePermission(rbac.TabManage), th.SubmitRound)
	r.GET("/pos/tabs/:id", ah.RequireStaff(), th.GetTab)

	// POS Order & Payment routes
	r.POST("/pos/orders/:id/payments", ah.RequireStaff(), pos.InitiatePayment)
	r.POST("/pos/orders/quote", ah.RequireStaff(), pos.QuoteOrder)

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

	americanoProdID := "d0000003-0000-4000-8000-000000000003"
	latteProdID := "d0000001-0000-4000-8000-000000000001"
	croissantProdID := "d0000005-0000-4000-8000-000000000005"
	centralStoreID := uuid.MustParse("11111111-1111-4111-8111-111111111111")

	// Ensure sufficient stock for test products in test database
	_ = database.WithTenantTx(context.Background(), db, centralStoreID, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(context.Background(), `UPDATE products SET stock_quantity = 500 WHERE id = $1`, croissantProdID)
		return err
	})

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
	// Cumulative subtotal = 48,000, service 5% = 2,400, tax 10% on 50,400 = 5,040 -> Total = 55,440 IDR
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
		expectedTotal := int64(55440)
		if res.Order.TotalAmount != expectedTotal {
			t.Errorf("expected total %d, got %d", expectedTotal, res.Order.TotalAmount)
		}
	})

	// 7. Get Tab Detail returns both rounds with nested items and options
	t.Run("7_GetTabDetail_ReturnsAllRoundsWithItems", func(t *testing.T) {
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
		if len(detail.Rounds[0].Items) != 1 {
			t.Fatalf("expected 1 item in round 1, got %d", len(detail.Rounds[0].Items))
		}
		if len(detail.Rounds[0].Items[0].Options) != 2 {
			t.Errorf("expected 2 options in round 1 item, got %d", len(detail.Rounds[0].Items[0].Options))
		}
		if len(detail.Rounds[1].Items) != 1 {
			t.Fatalf("expected 1 item in round 2, got %d", len(detail.Rounds[1].Items))
		}
	})

	// 8. Parallel Open Tab Label Race: two concurrent requests with identical label
	t.Run("8_ParallelLabelLockRace_OneWinsOneConflicts", func(t *testing.T) {
		raceLabel := "Meja Race-" + uuid.NewString()[:6]
		var wg sync.WaitGroup
		results := make([]int, 2)

		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				w := call(http.MethodPost, "/pos/tabs", cashierToken, map[string]any{
					"idempotency_key": fmt.Sprintf("race-open-%d-%s", idx, uuid.NewString()[:6]),
					"table_number":    raceLabel,
					"customer_name":   "Race Guest",
				})
				results[idx] = w.Code
			}(i)
		}
		wg.Wait()

		createdCount := 0
		conflictCount := 0
		for _, code := range results {
			if code == http.StatusCreated {
				createdCount++
			} else if code == http.StatusConflict {
				conflictCount++
			}
		}

		if createdCount != 1 || conflictCount != 1 {
			t.Fatalf("expected exactly 1 Created (201) and 1 Conflict (409) under race, got results: %v", results)
		}
	})

	// 9. OpenTab Idempotency: replay returns 200, concurrent race returns 201+200, key reuse with different payload returns 409
	t.Run("9_OpenTab_IdempotencyReplay_And_Conflict", func(t *testing.T) {
		idemKey := "open-idem-" + uuid.NewString()[:8]
		tabLabel := "Meja Idem-" + uuid.NewString()[:6]

		payloadA := map[string]any{
			"idempotency_key": idemKey,
			"table_number":    tabLabel,
			"customer_name":   "Tamu Idem A",
			"notes":           "First Note",
		}

		// 9.1 Initial creation -> 201 Created
		w1 := call(http.MethodPost, "/pos/tabs", cashierToken, payloadA)
		if w1.Code != http.StatusCreated {
			t.Fatalf("expected 201 Created on first OpenTab, got %d %s", w1.Code, w1.Body.String())
		}
		var res1 service.OpenTabResult
		_ = json.Unmarshal(w1.Body.Bytes(), &res1)
		origOrderID := res1.Order.ID

		// 9.2 Replay with exact payload -> 200 OK with identical order ID
		w2 := call(http.MethodPost, "/pos/tabs", cashierToken, payloadA)
		if w2.Code != http.StatusOK {
			t.Fatalf("expected 200 OK on idempotent replay, got %d %s", w2.Code, w2.Body.String())
		}
		var res2 service.OpenTabResult
		_ = json.Unmarshal(w2.Body.Bytes(), &res2)
		if res2.Order.ID != origOrderID {
			t.Fatalf("expected replayed order ID %s, got %s", origOrderID, res2.Order.ID)
		}
		if !res2.IsIdempotentReplay {
			t.Errorf("expected is_idempotent_replay = true")
		}

		// 9.3 Key reuse with different payload -> 409 Conflict (IDEMPOTENCY_CONFLICT)
		payloadB := map[string]any{
			"idempotency_key": idemKey,
			"table_number":    tabLabel,
			"customer_name":   "Tamu Idem B Berbeda",
			"notes":           "Different Note",
		}
		w3 := call(http.MethodPost, "/pos/tabs", cashierToken, payloadB)
		if w3.Code != http.StatusConflict {
			t.Fatalf("expected 409 Conflict on idempotency key payload conflict, got %d %s", w3.Code, w3.Body.String())
		}
		if !strings.Contains(w3.Body.String(), "IDEMPOTENCY_CONFLICT") {
			t.Errorf("expected IDEMPOTENCY_CONFLICT error code, got %s", w3.Body.String())
		}

		// 9.4 Concurrent OpenTab with identical key & payload: guarded by advisory lock, returns 201 and 200
		raceKey := "race-tab-key-" + uuid.NewString()[:8]
		racePayload := map[string]any{
			"idempotency_key": raceKey,
			"table_number":    "Meja Race Parallel " + uuid.NewString()[:6],
			"customer_name":   "Parallel Guest",
		}
		var raceWg sync.WaitGroup
		raceCodes := make([]int, 2)
		var raceOrderIDs [2]string

		for i := 0; i < 2; i++ {
			raceWg.Add(1)
			go func(idx int) {
				defer raceWg.Done()
				w := call(http.MethodPost, "/pos/tabs", cashierToken, racePayload)
				raceCodes[idx] = w.Code
				var rOut service.OpenTabResult
				_ = json.Unmarshal(w.Body.Bytes(), &rOut)
				if rOut.Order != nil {
					raceOrderIDs[idx] = rOut.Order.ID.String()
				}
			}(i)
		}
		raceWg.Wait()

		has201 := raceCodes[0] == http.StatusCreated || raceCodes[1] == http.StatusCreated
		has200 := raceCodes[0] == http.StatusOK || raceCodes[1] == http.StatusOK
		if !has201 || !has200 {
			t.Fatalf("expected 201 Created and 200 OK on concurrent identical OpenTab, got %v", raceCodes)
		}
		if raceOrderIDs[0] != raceOrderIDs[1] || raceOrderIDs[0] == "" {
			t.Fatalf("expected identical order ID from concurrent OpenTab, got %v", raceOrderIDs)
		}
	})

	// 10. OpenTab with 100-character idempotency key does not overflow round key constraint
	t.Run("10_OpenTab_100CharIdempotencyKey_NoOverflow", func(t *testing.T) {
		key100 := strings.Repeat("k", 100)
		w := call(http.MethodPost, "/pos/tabs", cashierToken, map[string]any{
			"idempotency_key": key100,
			"table_number":    "Meja 100-Char",
			"customer_name":   "Pelanggan 100 Key",
			"initial_items": []map[string]any{
				{"product_id": americanoProdID, "quantity": 1},
			},
		})
		if w.Code != http.StatusCreated {
			t.Fatalf("expected 201 Created with 100-char key, got %d %s", w.Code, w.Body.String())
		}
		var res service.OpenTabResult
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		if res.FirstRound == nil || res.FirstRound.IdempotencyKey != key100 {
			t.Fatalf("expected first round idempotency_key to match 100 chars without overflow: %+v", res.FirstRound)
		}
	})

	// 11. SubmitRound concurrent idempotency race: one round inserted, stock deducted exactly once
	t.Run("11_SubmitRound_ParallelRaceAndSingleStockDeduction", func(t *testing.T) {
		// Create dedicated tab
		wTab := call(http.MethodPost, "/pos/tabs", cashierToken, map[string]any{
			"idempotency_key": "tab-stock-race-" + uuid.NewString()[:8],
			"table_number":    "Meja Stock Race",
			"customer_name":   "Stock Race Customer",
		})
		if wTab.Code != http.StatusCreated {
			t.Fatalf("create tab failed: %d %s", wTab.Code, wTab.Body.String())
		}
		var tabRes service.OpenTabResult
		_ = json.Unmarshal(wTab.Body.Bytes(), &tabRes)
		raceTabID := tabRes.Order.ID

		// Get initial stock quantity of Croissant
		var initialStock int
		err := database.WithTenantTx(context.Background(), db, centralStoreID, func(tx *sqlx.Tx) error {
			return tx.GetContext(context.Background(), &initialStock, `SELECT stock_quantity FROM products WHERE id = $1`, croissantProdID)
		})
		if err != nil {
			t.Fatalf("failed to query initial stock: %v", err)
		}

		roundKey := "round-race-idem-" + uuid.NewString()[:8]
		roundPayload := map[string]any{
			"idempotency_key": roundKey,
			"notes":           "Parallel Race Round",
			"items": []map[string]any{
				{"product_id": croissantProdID, "quantity": 2},
			},
		}

		var wg sync.WaitGroup
		codes := make([]int, 2)
		var roundIDs [2]string

		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				w := call(http.MethodPost, "/pos/tabs/"+raceTabID.String()+"/rounds", cashierToken, roundPayload)
				codes[idx] = w.Code
				var rOut service.SubmitRoundResult
				_ = json.Unmarshal(w.Body.Bytes(), &rOut)
				if rOut.Round != nil {
					roundIDs[idx] = rOut.Round.ID.String()
				}
			}(i)
		}
		wg.Wait()

		has201 := codes[0] == http.StatusCreated || codes[1] == http.StatusCreated
		has200 := codes[0] == http.StatusOK || codes[1] == http.StatusOK
		if !has201 || !has200 {
			t.Fatalf("expected 201 Created and 200 OK on concurrent SubmitRound, got %v", codes)
		}
		if roundIDs[0] != roundIDs[1] || roundIDs[0] == "" {
			t.Fatalf("expected identical round ID, got %v", roundIDs)
		}

		// Assert stock deducted EXACTLY once (2 items, initialStock - 2)
		var postStock int
		err = database.WithTenantTx(context.Background(), db, centralStoreID, func(tx *sqlx.Tx) error {
			return tx.GetContext(context.Background(), &postStock, `SELECT stock_quantity FROM products WHERE id = $1`, croissantProdID)
		})
		if err != nil {
			t.Fatalf("failed to query post stock: %v", err)
		}
		if postStock != initialStock-2 {
			t.Fatalf("expected stock deducted exactly once to %d, got %d (initial %d)", initialStock-2, postStock, initialStock)
		}

		// Assert exactly 1 outbox event was written
		var outboxCount int
		_ = database.WithTenantTx(context.Background(), db, centralStoreID, func(tx *sqlx.Tx) error {
			return tx.GetContext(context.Background(), &outboxCount, `
				SELECT COUNT(*) FROM outbox_events WHERE store_id = $1 AND event_type = 'order.round_submitted' AND aggregate_id = $2
			`, centralStoreID, raceTabID)
		})
		if outboxCount != 1 {
			t.Fatalf("expected exactly 1 outbox event, got %d", outboxCount)
		}
	})

	// 12. SubmitRound replay after tab is closed returns 200 replay (not TAB_NOT_OPEN)
	t.Run("12_SubmitRound_ReplayAfterTabClosed", func(t *testing.T) {
		// Open tab and submit round
		wTab := call(http.MethodPost, "/pos/tabs", cashierToken, map[string]any{
			"idempotency_key": "tab-close-replay-" + uuid.NewString()[:8],
			"table_number":    "Meja Close Replay",
			"customer_name":   "Close Customer",
		})
		var tabRes service.OpenTabResult
		_ = json.Unmarshal(wTab.Body.Bytes(), &tabRes)
		closeTabID := tabRes.Order.ID

		roundKey := "round-close-replay-" + uuid.NewString()[:8]
		roundPayload := map[string]any{
			"idempotency_key": roundKey,
			"items": []map[string]any{
				{"product_id": americanoProdID, "quantity": 1},
			},
		}

		// Initial submission -> 201 Created
		wR1 := call(http.MethodPost, "/pos/tabs/"+closeTabID.String()+"/rounds", cashierToken, roundPayload)
		if wR1.Code != http.StatusCreated {
			t.Fatalf("round submission failed: %d %s", wR1.Code, wR1.Body.String())
		}
		var r1Res service.SubmitRoundResult
		_ = json.Unmarshal(wR1.Body.Bytes(), &r1Res)
		origRoundID := r1Res.Round.ID

		// Transition order status to 'cancelled' (simulating closed/non-open tab)
		err := database.WithTenantTx(context.Background(), db, centralStoreID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(context.Background(), `UPDATE orders SET status = 'cancelled' WHERE id = $1`, closeTabID)
			return err
		})
		if err != nil {
			t.Fatalf("failed to update order status: %v", err)
		}

		// Replay with exact same key -> MUST return 200 OK replay (not 409 TAB_NOT_OPEN)
		wReplay := call(http.MethodPost, "/pos/tabs/"+closeTabID.String()+"/rounds", cashierToken, roundPayload)
		if wReplay.Code != http.StatusOK {
			t.Fatalf("expected 200 OK replay on closed tab, got %d %s", wReplay.Code, wReplay.Body.String())
		}
		var replayRes service.SubmitRoundResult
		_ = json.Unmarshal(wReplay.Body.Bytes(), &replayRes)
		if replayRes.Round.ID != origRoundID || !replayRes.IsIdempotentReplay {
			t.Fatalf("expected replayed round %s, got %+v", origRoundID, replayRes)
		}
	})

	// 13. Guard against paying an open tab: InitiatePayment returns 409 TAB_NOT_CLOSED, 0 payments, 0 cash movements
	t.Run("13_PayOpenTab_RejectedWith409TabNotClosed", func(t *testing.T) {
		// Open tab with initial items
		wTab := call(http.MethodPost, "/pos/tabs", cashierToken, map[string]any{
			"idempotency_key": "tab-unclosed-pay-" + uuid.NewString()[:8],
			"table_number":    "Meja Pay Guard",
			"customer_name":   "Guard Customer",
			"initial_items": []map[string]any{
				{"product_id": americanoProdID, "quantity": 1},
			},
		})
		if wTab.Code != http.StatusCreated {
			t.Fatalf("open tab failed: %d %s", wTab.Code, wTab.Body.String())
		}
		var tabRes service.OpenTabResult
		_ = json.Unmarshal(wTab.Body.Bytes(), &tabRes)
		guardOrderID := tabRes.Order.ID

		// Attempt direct tender payment while tab is still 'open'
		payKey := "pay-open-tab-" + uuid.NewString()[:8]
		wPay := call(http.MethodPost, "/pos/orders/"+guardOrderID.String()+"/payments", cashierToken, map[string]any{
			"idempotency_key":    payKey,
			"method":             "static_qris",
			"reconciliation_ref": "REF-STAT-001",
		})
		if wPay.Code != http.StatusConflict {
			t.Fatalf("expected 409 Conflict for paying open tab, got %d %s", wPay.Code, wPay.Body.String())
		}
		if !strings.Contains(wPay.Body.String(), "TAB_NOT_CLOSED") {
			t.Errorf("expected TAB_NOT_CLOSED code, got %s", wPay.Body.String())
		}

		// Verify 0 rows in payments and 0 rows in cash_movements for this order
		var payCount int
		var movCount int
		_ = database.WithTenantTx(context.Background(), db, centralStoreID, func(tx *sqlx.Tx) error {
			_ = tx.GetContext(context.Background(), &payCount, `SELECT COUNT(*) FROM payments WHERE store_id = $1 AND order_id = $2`, centralStoreID, guardOrderID)
			_ = tx.GetContext(context.Background(), &movCount, `SELECT COUNT(*) FROM cash_movements WHERE store_id = $1 AND reference_id = $2`, centralStoreID, guardOrderID.String())
			return nil
		})
		if payCount != 0 {
			t.Errorf("expected 0 payments created, got %d", payCount)
		}
		if movCount != 0 {
			t.Errorf("expected 0 cash movements created, got %d", movCount)
		}
	})

	// 14. Cumulative tax calculation proof: Rp10.010 with 5% service & 10% tax proves single-order quote parity vs round accumulation
	// Math proof:
	// Price = 10,010 IDR. Service = 5% (500 BPS), Tax = 10% (1000 BPS).
	// Per-round rounding (Legacy flawed):
	//   Round 1: Subtotal 10,010. Service = round(10,010 * 5%) = 501. Taxable = 10,511. Tax = round(10,511 * 10%) = 1,051. Total = 11,562.
	//   Round 2: Subtotal 10,010. Service = 501. Taxable = 10,511. Tax = 1,051. Total = 11,562.
	//   Flawed accumulated total = 11,562 + 11,562 = 23,124 IDR.
	// Cumulative recalculation (A4 fix & Pay-First parity):
	//   Cumulative Subtotal = 20,020.
	//   Cumulative Service = round(20,020 * 5%) = round(1001.0) = 1,001 IDR. (1 IDR lower than 501+501!)
	//   Cumulative Taxable Base = 20,020 + 1,001 = 21,021 IDR.
	//   Cumulative Tax = round(21,021 * 10%) = round(2102.1) = 2,102 IDR.
	//   Exact Cumulative Total = 20,020 + 1,001 + 2,102 = 23,123 IDR!
	// Parity assert: Tab TotalAmount MUST be 23,123 IDR, exactly matching POS Quote for 2x item.
	// Legacy accumulation (23,124 IDR) would fail this test!
	t.Run("14_MultiRound_CumulativeTotals_MathParity", func(t *testing.T) {
		fractionalProdID := uuid.New()
		err := database.WithTenantTx(context.Background(), db, centralStoreID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(context.Background(), `
				INSERT INTO products (id, store_id, name, base_price, is_available, track_stock, stock_quantity)
				VALUES ($1, $2, 'Proof Fractional Item', 10010, true, false, 0)
			`, fractionalProdID, centralStoreID)
			return err
		})
		if err != nil {
			t.Fatalf("failed to insert proof item: %v", err)
		}
		t.Cleanup(func() {
			_ = database.WithTenantTx(context.Background(), db, centralStoreID, func(tx *sqlx.Tx) error {
				_, _ = tx.ExecContext(context.Background(), `DELETE FROM products WHERE id = $1`, fractionalProdID)
				return nil
			})
		})

		// 14.1 Query POS Quote for 2x Proof Fractional Item
		wQuote := call(http.MethodPost, "/pos/orders/quote", cashierToken, map[string]any{
			"order_type":   "dine_in",
			"billing_mode": "pay_first",
			"items": []map[string]any{
				{"product_id": fractionalProdID.String(), "quantity": 2},
			},
		})
		if wQuote.Code != http.StatusOK {
			t.Fatalf("quote failed: %d %s", wQuote.Code, wQuote.Body.String())
		}
		var quoteRes struct {
			SubtotalAmount      int64 `json:"subtotal_amount"`
			ServiceChargeAmount int64 `json:"service_charge_amount"`
			TaxAmount           int64 `json:"tax_amount"`
			TotalAmount         int64 `json:"total_amount"`
		}
		_ = json.Unmarshal(wQuote.Body.Bytes(), &quoteRes)

		if quoteRes.SubtotalAmount != 20020 || quoteRes.ServiceChargeAmount != 1001 || quoteRes.TaxAmount != 2102 || quoteRes.TotalAmount != 23123 {
			t.Fatalf("unexpected quote calculation: %+v", quoteRes)
		}

		// 14.2 Submit across 2 incremental rounds
		wTab := call(http.MethodPost, "/pos/tabs", cashierToken, map[string]any{
			"idempotency_key": "calc-proof-" + uuid.NewString()[:8],
			"table_number":    "Calc Proof Table",
			"customer_name":   "Math Audit Customer",
		})
		var tabRes service.OpenTabResult
		_ = json.Unmarshal(wTab.Body.Bytes(), &tabRes)
		calcTabID := tabRes.Order.ID

		wR1 := call(http.MethodPost, "/pos/tabs/"+calcTabID.String()+"/rounds", cashierToken, map[string]any{
			"idempotency_key": "proof-r1-" + uuid.NewString()[:8],
			"items": []map[string]any{
				{"product_id": fractionalProdID.String(), "quantity": 1},
			},
		})
		if wR1.Code != http.StatusCreated {
			t.Fatalf("submit round 1 failed: %d %s", wR1.Code, wR1.Body.String())
		}

		wR2 := call(http.MethodPost, "/pos/tabs/"+calcTabID.String()+"/rounds", cashierToken, map[string]any{
			"idempotency_key": "proof-r2-" + uuid.NewString()[:8],
			"items": []map[string]any{
				{"product_id": fractionalProdID.String(), "quantity": 1},
			},
		})
		if wR2.Code != http.StatusCreated {
			t.Fatalf("submit round 2 failed: %d %s", wR2.Code, wR2.Body.String())
		}
		var r2Res service.SubmitRoundResult
		_ = json.Unmarshal(wR2.Body.Bytes(), &r2Res)

		// Assert exact parity with quote and cumulative calculation
		if r2Res.Order.SubtotalAmount != quoteRes.SubtotalAmount {
			t.Errorf("expected subtotal %d, got %d", quoteRes.SubtotalAmount, r2Res.Order.SubtotalAmount)
		}
		if r2Res.Order.ServiceChargeAmount != quoteRes.ServiceChargeAmount {
			t.Errorf("expected service charge %d, got %d", quoteRes.ServiceChargeAmount, r2Res.Order.ServiceChargeAmount)
		}
		if r2Res.Order.TaxAmount != quoteRes.TaxAmount {
			t.Errorf("expected tax %d, got %d", quoteRes.TaxAmount, r2Res.Order.TaxAmount)
		}
		if r2Res.Order.TotalAmount != quoteRes.TotalAmount {
			t.Errorf("expected total %d (flawed per-round accumulation was 23124), got %d", quoteRes.TotalAmount, r2Res.Order.TotalAmount)
		}
	})

	// 15. Non-open_tab order returns 404 on GET /pos/tabs/:id
	t.Run("15_NonOpenTabOrder_Returns404", func(t *testing.T) {
		var nonTabOrderID uuid.UUID
		err := database.WithTenantTx(context.Background(), db, centralStoreID, func(tx *sqlx.Tx) error {
			nonTabOrderID = uuid.New()
			now := time.Now().UTC()
			orderNo := fmt.Sprintf("ORD-NONTAB-%s", uuid.NewString()[:8])
			_, err := tx.ExecContext(context.Background(), `
				INSERT INTO orders (
					id, store_id, idempotency_key, request_hash, order_number, queue_number,
					customer_name, order_type, billing_mode, status, payment_status, stock_status,
					subtotal_amount, discount_amount, tax_amount, service_charge_amount, total_amount,
					paid_amount, refunded_amount, created_at, updated_at
				) VALUES (
					$1, '11111111-1111-4111-8111-111111111111', $2, 'dummyhash', $3, 'A999',
					'Pay First Customer', 'dine_in', 'pay_first', 'open', 'unpaid', 'reserved',
					10000, 0, 1000, 500, 11500, 0, 0, $4, $4
				)
			`, nonTabOrderID, "nontab-"+uuid.NewString()[:8], orderNo, now)
			return err
		})
		if err != nil {
			t.Fatalf("failed to insert pay_first order: %v", err)
		}

		w := call(http.MethodGet, "/pos/tabs/"+nonTabOrderID.String(), ownerToken, nil)
		if w.Code != http.StatusNotFound {
			t.Fatalf("expected 404 Not Found for non-open_tab order on /pos/tabs/:id, got %d %s", w.Code, w.Body.String())
		}
	})
}
