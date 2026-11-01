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

// TestTabLifecycleIntegration tests Iteration 4c-1:
// 1. Barista cannot open tabs (RBAC guard tab.manage)
// 2. Cashier successfully opens a new tab
// 3. Attempting to open duplicate tab on label returns 409 TAB_LABEL_IN_USE
// 4. Opening with allow_duplicate_label = true succeeds
// 5. Submit Round 1 with items (locked pricing, outbox event written)
// 6. Submit Round 2 with items accumulating totals
// 7. Get Tab Detail returns all rounds with nested items and options
// 8. Parallel open tab label race guarded by PostgreSQL advisory transaction lock
// 9. OpenTab idempotency: replay returns 200, key reuse with different payload returns 409
// 10. SubmitRound idempotency: replay returns 200 (no duplicate stock deduction), different payload returns 409
// 11. Multi-round tax calculation cumulative parity with pay-first totals
// 12. Non-open_tab order returns 404 on GET /pos/tabs/:id
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

	americanoProdID := "d0000003-0000-4000-8000-000000000003"
	latteProdID := "d0000001-0000-4000-8000-000000000001"
	croissantProdID := "d0000005-0000-4000-8000-000000000005"

	// Ensure sufficient stock for test products in test database
	_ = database.WithTenantTx(context.Background(), db, uuid.MustParse("11111111-1111-4111-8111-111111111111"), func(tx *sqlx.Tx) error {
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

	// 9. OpenTab Idempotency: replay returns 200, key reuse with different payload returns 409
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
	})

	// 10. SubmitRound Idempotency: replay returns 200, key reuse with different payload returns 409
	t.Run("10_SubmitRound_IdempotencyReplay_And_Conflict", func(t *testing.T) {
		roundIdemKey := "round-idem-" + uuid.NewString()[:8]
		roundPayloadA := map[string]any{
			"idempotency_key": roundIdemKey,
			"notes":           "Idem Round Note A",
			"items": []map[string]any{
				{
					"product_id": croissantProdID,
					"quantity":   1,
				},
			},
		}

		// 10.1 First submission -> 201 Created
		w1 := call(http.MethodPost, "/pos/tabs/"+activeTabID+"/rounds", cashierToken, roundPayloadA)
		if w1.Code != http.StatusCreated {
			t.Fatalf("expected 201 Created on first SubmitRound, got %d %s", w1.Code, w1.Body.String())
		}
		var res1 service.SubmitRoundResult
		_ = json.Unmarshal(w1.Body.Bytes(), &res1)
		origRoundID := res1.Round.ID

		// 10.2 Replay exact submission -> 200 OK with identical round ID, is_idempotent_replay = true
		w2 := call(http.MethodPost, "/pos/tabs/"+activeTabID+"/rounds", cashierToken, roundPayloadA)
		if w2.Code != http.StatusOK {
			t.Fatalf("expected 200 OK on SubmitRound idempotent replay, got %d %s", w2.Code, w2.Body.String())
		}
		var res2 service.SubmitRoundResult
		_ = json.Unmarshal(w2.Body.Bytes(), &res2)
		if res2.Round.ID != origRoundID {
			t.Fatalf("expected replayed round ID %s, got %s", origRoundID, res2.Round.ID)
		}
		if !res2.IsIdempotentReplay {
			t.Errorf("expected is_idempotent_replay = true")
		}

		// 10.3 Key reuse with different payload -> 409 Conflict (IDEMPOTENCY_CONFLICT)
		roundPayloadB := map[string]any{
			"idempotency_key": roundIdemKey,
			"notes":           "Idem Round Note B Modified",
			"items": []map[string]any{
				{
					"product_id": croissantProdID,
					"quantity":   2,
				},
			},
		}
		w3 := call(http.MethodPost, "/pos/tabs/"+activeTabID+"/rounds", cashierToken, roundPayloadB)
		if w3.Code != http.StatusConflict {
			t.Fatalf("expected 409 Conflict on round idempotency conflict, got %d %s", w3.Code, w3.Body.String())
		}
		if !strings.Contains(w3.Body.String(), "IDEMPOTENCY_CONFLICT") {
			t.Errorf("expected IDEMPOTENCY_CONFLICT code, got %s", w3.Body.String())
		}
	})

	// 11. Multi-round tax calculation cumulative parity with pay-first totals
	t.Run("11_MultiRound_CumulativeTotals_Parity", func(t *testing.T) {
		// Open dedicated tab
		wOpen := call(http.MethodPost, "/pos/tabs", cashierToken, map[string]any{
			"idempotency_key": "calc-tab-" + uuid.NewString()[:8],
			"table_number":    "Calc Tab " + uuid.NewString()[:6],
			"customer_name":   "Audit Calc",
		})
		if wOpen.Code != http.StatusCreated {
			t.Fatalf("open tab for calc test failed: %d %s", wOpen.Code, wOpen.Body.String())
		}
		var openRes service.OpenTabResult
		_ = json.Unmarshal(wOpen.Body.Bytes(), &openRes)
		calcTabID := openRes.Order.ID.String()

		// Submit 2 rounds:
		// Round 1: 3x Americano (20,000 * 3 = 60,000)
		// Round 2: 2x Americano (20,000 * 2 = 40,000)
		// Total subtotal = 100,000
		// Service charge (5%) = 5,000
		// Tax base = 105,000, Tax (10%) = 10,500
		// Total = 115,500
		wR1 := call(http.MethodPost, "/pos/tabs/"+calcTabID+"/rounds", cashierToken, map[string]any{
			"idempotency_key": "calc-r1-" + uuid.NewString()[:8],
			"items": []map[string]any{
				{"product_id": americanoProdID, "quantity": 3},
			},
		})
		if wR1.Code != http.StatusCreated {
			t.Fatalf("submit round 1 failed: %d %s", wR1.Code, wR1.Body.String())
		}

		wR2 := call(http.MethodPost, "/pos/tabs/"+calcTabID+"/rounds", cashierToken, map[string]any{
			"idempotency_key": "calc-r2-" + uuid.NewString()[:8],
			"items": []map[string]any{
				{"product_id": americanoProdID, "quantity": 2},
			},
		})
		if wR2.Code != http.StatusCreated {
			t.Fatalf("submit round 2 failed: %d %s", wR2.Code, wR2.Body.String())
		}
		var r2Res service.SubmitRoundResult
		_ = json.Unmarshal(wR2.Body.Bytes(), &r2Res)

		if r2Res.Order.SubtotalAmount != 100000 {
			t.Errorf("expected cumulative subtotal 100000, got %d", r2Res.Order.SubtotalAmount)
		}
		if r2Res.Order.ServiceChargeAmount != 5000 {
			t.Errorf("expected cumulative service 5000, got %d", r2Res.Order.ServiceChargeAmount)
		}
		if r2Res.Order.TaxAmount != 10500 {
			t.Errorf("expected cumulative tax 10500, got %d", r2Res.Order.TaxAmount)
		}
		if r2Res.Order.TotalAmount != 115500 {
			t.Errorf("expected cumulative total 115500, got %d", r2Res.Order.TotalAmount)
		}
	})

	// 12. Non-open_tab order returns 404 on GET /pos/tabs/:id
	t.Run("12_NonOpenTabOrder_Returns404", func(t *testing.T) {
		// Create a pay_first order
		centralStoreID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
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
