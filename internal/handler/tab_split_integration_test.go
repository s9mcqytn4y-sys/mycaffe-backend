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

// TestTabSplitLifecycleIntegration verifies Iteration 4c-2 split bill requirements:
// 1. Close -> round baru ditolak 409 TAB_CLOSED; reopen sebelum bayar OK; reopen setelah 1 split paid ditolak 409 SPLIT_ALREADY_PAID.
// 2. Tab single-payer: close membuat 1 split full, bayar tunai -> order completed.
// 3. Equal N (N=3) untuk total ganjil: sisa rupiah ke split 1, sum(splits.total) = order.total.
// 4. By item dengan pembulatan pajak/service: sum(splits.total) = order.total persis; alokasi kurang/lebih -> 422.
// 5. Split dengan payment pending lalu ditender tunai -> 409 SPLIT_PAYMENT_PENDING.
// 6. Dua initiate payment paralel pada split yang sama -> tepat satu sukses.
// 7. Tab dibuka di shift A, dibayar di shift B -> payments.shift_id terikat ke shift B, omzet tercatat di X/Z shift B.
// 8. Tutup shift tidak diblokir oleh open tabs; X/Z menampilkan unsettled_open_tabs_count dan daftar nomor pesanan.
func TestTabSplitLifecycleIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping Tab Split Lifecycle integration test")
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
	shiftSvc := service.NewShiftService(db)
	settingsSvc := service.NewSettingsService(db)

	th := handler.NewTabHandler(tabSvc)
	pos := handler.NewPOSHandler(orderSvc, paymentSvc, settingsSvc)
	sh := handler.NewShiftHandler(shiftSvc)

	r := gin.New()
	r.POST("/login", ah.Login)

	// Tab routes (Iteration 4c-1 & 4c-2)
	r.POST("/pos/tabs", ah.RequirePermission(rbac.TabManage), th.OpenTab)
	r.POST("/pos/tabs/:id/rounds", ah.RequirePermission(rbac.TabManage), th.SubmitRound)
	r.GET("/pos/tabs/:id", ah.RequireStaff(), th.GetTab)
	r.POST("/pos/tabs/:id/close", ah.RequirePermission(rbac.TabManage), th.CloseTab)
	r.POST("/pos/tabs/:id/reopen", ah.RequirePermission(rbac.TabManage), th.ReopenTab)

	// POS Order & Payment routes
	r.POST("/pos/orders/:id/payments", ah.RequireStaff(), pos.InitiatePayment)

	// Drawer & Shift routes
	r.POST("/pos/drawers", ah.RequirePermission(rbac.CashDrawerManage), sh.CreateDrawer)
	r.GET("/pos/drawers", ah.RequireStaff(), sh.ListDrawers)
	r.POST("/pos/shifts", ah.RequireStaff(), sh.OpenShift)
	r.GET("/pos/shifts/current", ah.RequireStaff(), sh.GetCurrentShift)
	r.POST("/pos/shifts/:id/close", ah.RequireStaff(), sh.CloseShift)
	r.GET("/pos/shifts/:id/x-report", ah.RequireStaff(), sh.GetXReport)
	r.GET("/pos/shifts/:id/z-report", ah.RequireStaff(), sh.GetZReport)

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

	// 1. Login staff: Owner and Cashier
	ownerLogin := call("POST", "/login", "", map[string]string{
		"store_slug": "central-cafe", "email": "abdulaziz543333@gmail.com", "pin": "482913",
	})
	if ownerLogin.Code != http.StatusOK {
		t.Fatalf("Owner login failed: %d: %s", ownerLogin.Code, ownerLogin.Body.String())
	}
	var ownerResp struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal(ownerLogin.Body.Bytes(), &ownerResp)

	cashierLogin := call("POST", "/login", "", map[string]string{
		"store_slug": "central-cafe", "email": "cashier.central@mycaffe.local", "pin": "482913",
	})
	if cashierLogin.Code != http.StatusOK {
		t.Fatalf("Cashier login failed: %d: %s", cashierLogin.Code, cashierLogin.Body.String())
	}
	var cashierResp struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal(cashierLogin.Body.Bytes(), &cashierResp)

	centralStoreID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	productID := uuid.MustParse("d0000003-0000-4000-8000-000000000003")

	// Ensure sufficient stock and odd base price to verify remainder allocation
	ctx := context.Background()
	_ = database.WithTenantTx(ctx, db, centralStoreID, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, `WITH p AS (UPDATE products SET stock_quantity = 1000 WHERE id = $1) UPDATE product_variants SET price = 19999 WHERE product_id = $1 AND is_default`, productID)
		return err
	})

	// Create a dedicated drawer for cashier in this integration test
	drawerRes := call("POST", "/pos/drawers", ownerResp.AccessToken, map[string]any{
		"name":       "Laci Split POS " + uuid.NewString()[:8],
		"is_default": false,
	})
	if drawerRes.Code != http.StatusCreated {
		t.Fatalf("Failed to create test cash drawer: %d %s", drawerRes.Code, drawerRes.Body.String())
	}
	var drawer domain.CashDrawer
	_ = json.Unmarshal(drawerRes.Body.Bytes(), &drawer)
	drawerID := drawer.ID

	// Open cashier shift if none open
	call("POST", "/pos/shifts", cashierResp.AccessToken, map[string]any{
		"drawer_id":     drawerID,
		"opening_float": int64(100000),
	})

	// -------------------------------------------------------------
	// Test 1: Close and Reopen Lifecycle & Guards (P10)
	// -------------------------------------------------------------
	t.Run("1_Close_And_Reopen_Lifecycle_Guards", func(t *testing.T) {
		tabKey := "tab-life-" + uuid.NewString()[:8]
		openRes := call("POST", "/pos/tabs", cashierResp.AccessToken, map[string]any{
			"idempotency_key": tabKey,
			"table_number":    "Meja-LC-1",
			"initial_items": []map[string]any{
				{"product_id": productID, "quantity": 1},
			},
		})
		if openRes.Code != http.StatusCreated {
			t.Fatalf("open tab failed: %d: %s", openRes.Code, openRes.Body.String())
		}
		var tab struct {
			Order struct {
				ID          uuid.UUID `json:"id"`
				OrderNumber string    `json:"order_number"`
				Status      string    `json:"status"`
			} `json:"order"`
		}
		_ = json.Unmarshal(openRes.Body.Bytes(), &tab)

		// Close tab without split config -> 1 full split created, status becomes pending_payment
		closeRes := call("POST", fmt.Sprintf("/pos/tabs/%s/close", tab.Order.ID), cashierResp.AccessToken, nil)
		if closeRes.Code != http.StatusOK {
			t.Fatalf("close tab failed: %d: %s", closeRes.Code, closeRes.Body.String())
		}
		var closeData struct {
			Order struct {
				Status string `json:"status"`
			} `json:"order"`
			Splits []domain.PaymentSplit `json:"splits"`
		}
		_ = json.Unmarshal(closeRes.Body.Bytes(), &closeData)
		if closeData.Order.Status != "pending_payment" {
			t.Fatalf("expected order status pending_payment, got %s", closeData.Order.Status)
		}
		if len(closeData.Splits) != 1 {
			t.Fatalf("expected 1 full split, got %d", len(closeData.Splits))
		}

		// Guard: New round after close MUST be rejected with 409 TAB_CLOSED
		rndRes := call("POST", fmt.Sprintf("/pos/tabs/%s/rounds", tab.Order.ID), cashierResp.AccessToken, map[string]any{
			"idempotency_key": "rnd-blocked-" + uuid.NewString()[:8],
			"items": []map[string]any{
				{"product_id": productID, "quantity": 1},
			},
		})
		if rndRes.Code != http.StatusConflict || !strings.Contains(rndRes.Body.String(), "TAB_CLOSED") {
			t.Fatalf("expected 409 TAB_CLOSED on closed tab, got %d: %s", rndRes.Code, rndRes.Body.String())
		}

		// Reopen tab before any payment -> status back to open, split cancelled
		reopenRes := call("POST", fmt.Sprintf("/pos/tabs/%s/reopen", tab.Order.ID), cashierResp.AccessToken, nil)
		if reopenRes.Code != http.StatusOK {
			t.Fatalf("reopen tab failed: %d: %s", reopenRes.Code, reopenRes.Body.String())
		}
		var reopenData struct {
			Order struct {
				Status string `json:"status"`
			} `json:"order"`
		}
		_ = json.Unmarshal(reopenRes.Body.Bytes(), &reopenData)
		if reopenData.Order.Status != "open" {
			t.Fatalf("expected status open after reopen, got %s", reopenData.Order.Status)
		}

		// Now submit round succeeds after reopen
		rndOk := call("POST", fmt.Sprintf("/pos/tabs/%s/rounds", tab.Order.ID), cashierResp.AccessToken, map[string]any{
			"idempotency_key": "rnd-ok-" + uuid.NewString()[:8],
			"items": []map[string]any{
				{"product_id": productID, "quantity": 1},
			},
		})
		if rndOk.Code != http.StatusCreated {
			t.Fatalf("submit round after reopen failed: %d: %s", rndOk.Code, rndOk.Body.String())
		}

		// Close again with equal split 2
		close2 := call("POST", fmt.Sprintf("/pos/tabs/%s/close", tab.Order.ID), cashierResp.AccessToken, map[string]any{
			"strategy":    "equal",
			"split_count": 2,
		})
		if close2.Code != http.StatusOK {
			t.Fatalf("close with equal split failed: %d: %s", close2.Code, close2.Body.String())
		}
		var close2Data struct {
			Splits []domain.PaymentSplit `json:"splits"`
		}
		_ = json.Unmarshal(close2.Body.Bytes(), &close2Data)
		if len(close2Data.Splits) != 2 {
			t.Fatalf("expected 2 splits, got %d", len(close2Data.Splits))
		}

		// Pay split 1 via Cash
		paySplit1 := call("POST", fmt.Sprintf("/pos/orders/%s/payments", tab.Order.ID), cashierResp.AccessToken, map[string]any{
			"idempotency_key":      "pay-sp1-" + uuid.NewString()[:8],
			"split_id":             close2Data.Splits[0].ID,
			"method":               "cash",
			"cash_tendered_amount": close2Data.Splits[0].TotalAmount + 10000,
		})
		if paySplit1.Code != http.StatusOK {
			t.Fatalf("payment split 1 failed: %d: %s", paySplit1.Code, paySplit1.Body.String())
		}

		// Guard: Reopen MUST FAIL with 409 SPLIT_ALREADY_PAID once a split is paid!
		reopenFail := call("POST", fmt.Sprintf("/pos/tabs/%s/reopen", tab.Order.ID), cashierResp.AccessToken, nil)
		if reopenFail.Code != http.StatusConflict || !strings.Contains(reopenFail.Body.String(), "SPLIT_ALREADY_PAID") {
			t.Fatalf("expected 409 SPLIT_ALREADY_PAID, got %d: %s", reopenFail.Code, reopenFail.Body.String())
		}
	})

	// -------------------------------------------------------------
	// Test 2: Tab Single-Payer Full Settlement (P10, P12)
	// -------------------------------------------------------------
	t.Run("2_Tab_Single_Payer_Full_Settlement", func(t *testing.T) {
		tabKey := "tab-sp-" + uuid.NewString()[:8]
		openRes := call("POST", "/pos/tabs", cashierResp.AccessToken, map[string]any{
			"idempotency_key": tabKey,
			"table_number":    "Meja-SP-1",
			"initial_items": []map[string]any{
				{"product_id": productID, "quantity": 1},
			},
		})
		if openRes.Code != http.StatusCreated {
			t.Fatalf("open tab failed: %d: %s", openRes.Code, openRes.Body.String())
		}
		var tab struct {
			Order struct {
				ID          uuid.UUID `json:"id"`
				TotalAmount int64     `json:"total_amount"`
			} `json:"order"`
		}
		_ = json.Unmarshal(openRes.Body.Bytes(), &tab)

		// Close without split parameters -> 1 full split
		closeRes := call("POST", fmt.Sprintf("/pos/tabs/%s/close", tab.Order.ID), cashierResp.AccessToken, nil)
		if closeRes.Code != http.StatusOK {
			t.Fatalf("close tab failed: %d: %s", closeRes.Code, closeRes.Body.String())
		}
		var closeData struct {
			Splits []domain.PaymentSplit `json:"splits"`
		}
		_ = json.Unmarshal(closeRes.Body.Bytes(), &closeData)
		if len(closeData.Splits) != 1 || closeData.Splits[0].TotalAmount != tab.Order.TotalAmount {
			t.Fatalf("expected 1 split with amount %d, got %+v", tab.Order.TotalAmount, closeData.Splits)
		}

		// Pay single full split with cash tender
		payRes := call("POST", fmt.Sprintf("/pos/orders/%s/payments", tab.Order.ID), cashierResp.AccessToken, map[string]any{
			"idempotency_key":      "pay-sp-full-" + uuid.NewString()[:8],
			"split_id":             closeData.Splits[0].ID,
			"method":               "cash",
			"cash_tendered_amount": tab.Order.TotalAmount,
		})
		if payRes.Code != http.StatusOK {
			t.Fatalf("pay full split failed: %d: %s", payRes.Code, payRes.Body.String())
		}
		var payResult struct {
			Order struct {
				Status        string `json:"status"`
				PaymentStatus string `json:"payment_status"`
				PaidAmount    int64  `json:"paid_amount"`
			} `json:"order"`
		}
		_ = json.Unmarshal(payRes.Body.Bytes(), &payResult)
		if payResult.Order.Status != "completed" || payResult.Order.PaymentStatus != "paid" || payResult.Order.PaidAmount != tab.Order.TotalAmount {
			t.Fatalf("expected order completed and fully paid, got status=%s payment_status=%s paid_amount=%d",
				payResult.Order.Status, payResult.Order.PaymentStatus, payResult.Order.PaidAmount)
		}
	})

	// -------------------------------------------------------------
	// Test 3: Equal Split N=3 Deterministic Remainder to Split 1 (P11)
	// -------------------------------------------------------------
	t.Run("3_Equal_Split_3_Deterministic_Remainder_To_Split_1", func(t *testing.T) {
		tabKey := "tab-eq3-" + uuid.NewString()[:8]
		openRes := call("POST", "/pos/tabs", cashierResp.AccessToken, map[string]any{
			"idempotency_key": tabKey,
			"table_number":    "Meja-EQ3-1",
			"initial_items": []map[string]any{
				{"product_id": productID, "quantity": 1},
			},
		})
		if openRes.Code != http.StatusCreated {
			t.Fatalf("open tab failed: %d: %s", openRes.Code, openRes.Body.String())
		}
		var tab struct {
			Order struct {
				ID          uuid.UUID `json:"id"`
				TotalAmount int64     `json:"total_amount"`
			} `json:"order"`
		}
		_ = json.Unmarshal(openRes.Body.Bytes(), &tab)

		closeRes := call("POST", fmt.Sprintf("/pos/tabs/%s/close", tab.Order.ID), cashierResp.AccessToken, map[string]any{
			"strategy":    "equal",
			"split_count": 3,
		})
		if closeRes.Code != http.StatusOK {
			t.Fatalf("equal split 3 close failed: %d: %s", closeRes.Code, closeRes.Body.String())
		}
		var closeData struct {
			Splits []domain.PaymentSplit `json:"splits"`
		}
		_ = json.Unmarshal(closeRes.Body.Bytes(), &closeData)
		if len(closeData.Splits) != 3 {
			t.Fatalf("expected 3 splits, got %d", len(closeData.Splits))
		}

		var sumTotals int64
		for _, sp := range closeData.Splits {
			sumTotals += sp.TotalAmount
		}
		if sumTotals != tab.Order.TotalAmount {
			t.Fatalf("sum of splits (%d) != order total (%d)", sumTotals, tab.Order.TotalAmount)
		}

		// Split 2 and Split 3 must have identical base totals
		if closeData.Splits[1].TotalAmount != closeData.Splits[2].TotalAmount {
			t.Fatalf("split 2 (%d) and split 3 (%d) should be equal base amounts",
				closeData.Splits[1].TotalAmount, closeData.Splits[2].TotalAmount)
		}
		// Split 1 must absorb remainder (>= Split 2)
		if closeData.Splits[0].TotalAmount < closeData.Splits[1].TotalAmount {
			t.Fatalf("split 1 (%d) should be >= split 2 (%d) due to remainder allocation",
				closeData.Splits[0].TotalAmount, closeData.Splits[1].TotalAmount)
		}
	})

	// -------------------------------------------------------------
	// Test 4: Split by Item with Proportional Taxes & Allocation Bounds (P11)
	// -------------------------------------------------------------
	t.Run("4_Split_By_Item_Proportional_Taxes_And_Validation", func(t *testing.T) {
		tabKey := "tab-byitem-" + uuid.NewString()[:8]
		openRes := call("POST", "/pos/tabs", cashierResp.AccessToken, map[string]any{
			"idempotency_key": tabKey,
			"table_number":    "Meja-Item-1",
			"initial_items": []map[string]any{
				{"product_id": productID, "quantity": 3},
			},
		})
		if openRes.Code != http.StatusCreated {
			t.Fatalf("open tab failed: %d: %s", openRes.Code, openRes.Body.String())
		}
		var tab struct {
			Order struct {
				ID          uuid.UUID `json:"id"`
				TotalAmount int64     `json:"total_amount"`
			} `json:"order"`
		}
		_ = json.Unmarshal(openRes.Body.Bytes(), &tab)

		detailRes := call("GET", fmt.Sprintf("/pos/tabs/%s", tab.Order.ID), cashierResp.AccessToken, nil)
		var detail struct {
			Rounds []struct {
				Items []domain.OrderItem `json:"items"`
			} `json:"rounds"`
		}
		_ = json.Unmarshal(detailRes.Body.Bytes(), &detail)
		if len(detail.Rounds) == 0 || len(detail.Rounds[0].Items) == 0 {
			t.Fatalf("tab has no items")
		}
		item := detail.Rounds[0].Items[0]

		// 4A: Under-allocation (allocating 2 out of 3) must be rejected with 422
		underAlloc := call("POST", fmt.Sprintf("/pos/tabs/%s/close", tab.Order.ID), cashierResp.AccessToken, map[string]any{
			"strategy": "by_item",
			"splits": []map[string]any{
				{"label": "Tamu 1", "items": []map[string]any{{"order_item_id": item.ID, "quantity": 1}}},
				{"label": "Tamu 2", "items": []map[string]any{{"order_item_id": item.ID, "quantity": 1}}},
			},
		})
		if underAlloc.Code != http.StatusUnprocessableEntity || !strings.Contains(underAlloc.Body.String(), "ITEM_ALLOCATION_MISMATCH") {
			t.Fatalf("expected 422 ITEM_ALLOCATION_MISMATCH for under-allocation, got %d: %s", underAlloc.Code, underAlloc.Body.String())
		}

		// 4B: Over-allocation (allocating 4 out of 3) must be rejected with 422
		overAlloc := call("POST", fmt.Sprintf("/pos/tabs/%s/close", tab.Order.ID), cashierResp.AccessToken, map[string]any{
			"strategy": "by_item",
			"splits": []map[string]any{
				{"label": "Tamu 1", "items": []map[string]any{{"order_item_id": item.ID, "quantity": 2}}},
				{"label": "Tamu 2", "items": []map[string]any{{"order_item_id": item.ID, "quantity": 2}}},
			},
		})
		if overAlloc.Code != http.StatusUnprocessableEntity || !strings.Contains(overAlloc.Body.String(), "ITEM_ALLOCATION_MISMATCH") {
			t.Fatalf("expected 422 ITEM_ALLOCATION_MISMATCH for over-allocation, got %d: %s", overAlloc.Code, overAlloc.Body.String())
		}

		// 4C: Exact allocation (1 unit to Tamu 1, 2 units to Tamu 2) must succeed with exact mathematical parity
		exactAlloc := call("POST", fmt.Sprintf("/pos/tabs/%s/close", tab.Order.ID), cashierResp.AccessToken, map[string]any{
			"strategy": "by_item",
			"splits": []map[string]any{
				{"label": "Tamu 1", "items": []map[string]any{{"order_item_id": item.ID, "quantity": 1}}},
				{"label": "Tamu 2", "items": []map[string]any{{"order_item_id": item.ID, "quantity": 2}}},
			},
		})
		if exactAlloc.Code != http.StatusOK {
			t.Fatalf("exact by_item allocation failed: %d: %s", exactAlloc.Code, exactAlloc.Body.String())
		}
		var splitData struct {
			Splits []domain.PaymentSplit `json:"splits"`
		}
		_ = json.Unmarshal(exactAlloc.Body.Bytes(), &splitData)
		if len(splitData.Splits) != 2 {
			t.Fatalf("expected 2 splits, got %d", len(splitData.Splits))
		}
		sumSplits := splitData.Splits[0].TotalAmount + splitData.Splits[1].TotalAmount
		if sumSplits != tab.Order.TotalAmount {
			t.Fatalf("by_item split sum (%d) != order total (%d)", sumSplits, tab.Order.TotalAmount)
		}
	})

	// -------------------------------------------------------------
	// Test 5: Concurrency Protection on Split Payments (P12)
	// -------------------------------------------------------------
	t.Run("5_Split_Concurrency_And_Pending_Payment_Protection", func(t *testing.T) {
		tabKey := "tab-conc-" + uuid.NewString()[:8]
		openRes := call("POST", "/pos/tabs", cashierResp.AccessToken, map[string]any{
			"idempotency_key": tabKey,
			"table_number":    "Meja-Conc-1",
			"initial_items": []map[string]any{
				{"product_id": productID, "quantity": 2},
			},
		})
		if openRes.Code != http.StatusCreated {
			t.Fatalf("open tab failed: %d: %s", openRes.Code, openRes.Body.String())
		}
		var tab struct {
			Order struct {
				ID          uuid.UUID `json:"id"`
				TotalAmount int64     `json:"total_amount"`
			} `json:"order"`
		}
		_ = json.Unmarshal(openRes.Body.Bytes(), &tab)

		closeRes := call("POST", fmt.Sprintf("/pos/tabs/%s/close", tab.Order.ID), cashierResp.AccessToken, map[string]any{
			"strategy":    "equal",
			"split_count": 2,
		})
		if closeRes.Code != http.StatusOK {
			t.Fatalf("close failed: %d: %s", closeRes.Code, closeRes.Body.String())
		}
		var closeData struct {
			Splits []domain.PaymentSplit `json:"splits"`
		}
		_ = json.Unmarshal(closeRes.Body.Bytes(), &closeData)
		split1 := closeData.Splits[0]

		// Simulate inserting a pending payment attempt on split1 directly into database
		pendingPaymentID := uuid.New()
		now := time.Now().UTC()
		dummyHash := strings.Repeat("a", 64)
		err = database.WithTenantTx(ctx, db, centralStoreID, func(tx *sqlx.Tx) error {
			_, e := tx.ExecContext(ctx, `
				INSERT INTO payments (
					id, store_id, order_id, split_id, attempt_no, idempotency_key, request_hash,
					provider, provider_order_id, amount, method, status, created_at, updated_at
				) VALUES (
					$1, $2, $3, $4, 1, 'pending-key-test', $5,
					'midtrans', $6, $7, 'qris', 'pending', $8, $8
				)
			`, pendingPaymentID, centralStoreID, tab.Order.ID, split1.ID, dummyHash, pendingPaymentID.String(), split1.TotalAmount, now)
			return e
		})
		if err != nil {
			t.Fatalf("failed to insert pending mock payment: %v", err)
		}

		// Now cashier tries to tender cash on split1 -> MUST BE REJECTED with 409 SPLIT_PAYMENT_PENDING
		tenderRes := call("POST", fmt.Sprintf("/pos/orders/%s/payments", tab.Order.ID), cashierResp.AccessToken, map[string]any{
			"idempotency_key":      "cash-try-pending-" + uuid.NewString()[:8],
			"split_id":             split1.ID,
			"method":               "cash",
			"cash_tendered_amount": split1.TotalAmount,
		})
		if tenderRes.Code != http.StatusConflict || !strings.Contains(tenderRes.Body.String(), "SPLIT_PAYMENT_PENDING") {
			t.Fatalf("expected 409 SPLIT_PAYMENT_PENDING, got %d: %s", tenderRes.Code, tenderRes.Body.String())
		}

		// Expire the pending payment attempt
		err = database.WithTenantTx(ctx, db, centralStoreID, func(tx *sqlx.Tx) error {
			_, e := tx.ExecContext(ctx, `
				UPDATE payments SET status = 'expired' WHERE id = $1 AND store_id = $2
			`, pendingPaymentID, centralStoreID)
			return e
		})
		if err != nil {
			t.Fatalf("failed to expire pending payment: %v", err)
		}

		// Now cashier tenders cash on split1 -> SUCCEEDS
		tenderOk := call("POST", fmt.Sprintf("/pos/orders/%s/payments", tab.Order.ID), cashierResp.AccessToken, map[string]any{
			"idempotency_key":      "cash-after-expired-" + uuid.NewString()[:8],
			"split_id":             split1.ID,
			"method":               "cash",
			"cash_tendered_amount": split1.TotalAmount,
		})
		if tenderOk.Code != http.StatusOK {
			t.Fatalf("tender after expired failed: %d: %s", tenderOk.Code, tenderOk.Body.String())
		}
	})

	// -------------------------------------------------------------
	// Test 6: Parallel Payment Race on the Same Split (P12)
	// -------------------------------------------------------------
	t.Run("6_Parallel_Payment_Race_On_Same_Split", func(t *testing.T) {
		tabKey := "tab-race-" + uuid.NewString()[:8]
		openRes := call("POST", "/pos/tabs", cashierResp.AccessToken, map[string]any{
			"idempotency_key": tabKey,
			"table_number":    "Meja-Race-1",
			"initial_items": []map[string]any{
				{"product_id": productID, "quantity": 1},
			},
		})
		if openRes.Code != http.StatusCreated {
			t.Fatalf("open tab failed: %d: %s", openRes.Code, openRes.Body.String())
		}
		var tab struct {
			Order struct {
				ID          uuid.UUID `json:"id"`
				TotalAmount int64     `json:"total_amount"`
			} `json:"order"`
		}
		_ = json.Unmarshal(openRes.Body.Bytes(), &tab)

		closeRes := call("POST", fmt.Sprintf("/pos/tabs/%s/close", tab.Order.ID), cashierResp.AccessToken, nil)
		if closeRes.Code != http.StatusOK {
			t.Fatalf("close failed: %d: %s", closeRes.Code, closeRes.Body.String())
		}
		var closeData struct {
			Splits []domain.PaymentSplit `json:"splits"`
		}
		_ = json.Unmarshal(closeRes.Body.Bytes(), &closeData)
		targetSplit := closeData.Splits[0]

		var wg sync.WaitGroup
		results := make([]int, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				resp := call("POST", fmt.Sprintf("/pos/orders/%s/payments", tab.Order.ID), cashierResp.AccessToken, map[string]any{
					"idempotency_key":      fmt.Sprintf("race-pay-%d-%s", idx, uuid.NewString()[:8]),
					"split_id":             targetSplit.ID,
					"method":               "cash",
					"cash_tendered_amount": targetSplit.TotalAmount,
				})
				results[idx] = resp.Code
			}(i)
		}
		wg.Wait()

		// Exactly one goroutine must succeed with 200, the other must fail with 409
		successCount := 0
		conflictCount := 0
		for _, code := range results {
			if code == http.StatusOK {
				successCount++
			} else if code == http.StatusConflict {
				conflictCount++
			}
		}
		if successCount != 1 || conflictCount != 1 {
			t.Fatalf("expected 1 success (200) and 1 conflict (409), got results: %v", results)
		}
	})

	// -------------------------------------------------------------
	// Test 7: Cross-Shift Tab Attribution & X/Z Isolation (P13)
	// -------------------------------------------------------------
	t.Run("7_Cross_Shift_Tab_Attribution", func(t *testing.T) {
		// Cashier opens Tab 1 on Shift A
		tabKey := "tab-cross-" + uuid.NewString()[:8]
		openRes := call("POST", "/pos/tabs", cashierResp.AccessToken, map[string]any{
			"idempotency_key": tabKey,
			"table_number":    "Meja-Shift-1",
			"initial_items": []map[string]any{
				{"product_id": productID, "quantity": 1},
			},
		})
		if openRes.Code != http.StatusCreated {
			t.Fatalf("open tab failed: %d: %s", openRes.Code, openRes.Body.String())
		}
		var tab struct {
			Order struct {
				ID          uuid.UUID `json:"id"`
				TotalAmount int64     `json:"total_amount"`
			} `json:"order"`
		}
		_ = json.Unmarshal(openRes.Body.Bytes(), &tab)

		// Close tab to pending_payment
		closeRes := call("POST", fmt.Sprintf("/pos/tabs/%s/close", tab.Order.ID), cashierResp.AccessToken, nil)
		if closeRes.Code != http.StatusOK {
			t.Fatalf("close failed: %d: %s", closeRes.Code, closeRes.Body.String())
		}
		var closeData struct {
			Splits []domain.PaymentSplit `json:"splits"`
		}
		_ = json.Unmarshal(closeRes.Body.Bytes(), &closeData)
		sp := closeData.Splits[0]

		// Get Shift A ID and Close Shift A
		curShiftA := call("GET", "/pos/shifts/current?drawer_id="+drawerID.String(), cashierResp.AccessToken, nil)
		if curShiftA.Code != http.StatusOK {
			t.Fatalf("get current shift A failed: %d: %s", curShiftA.Code, curShiftA.Body.String())
		}
		var shiftA domain.Shift
		_ = json.Unmarshal(curShiftA.Body.Bytes(), &shiftA)

		xRepA := call("GET", fmt.Sprintf("/pos/shifts/%s/x-report", shiftA.ID), ownerResp.AccessToken, nil)
		var xReportA domain.XReport
		_ = json.Unmarshal(xRepA.Body.Bytes(), &xReportA)
		cashSalesBeforeTab1 := xReportA.Summary.CashSales

		closeShiftARes := call("POST", fmt.Sprintf("/pos/shifts/%s/close", shiftA.ID), cashierResp.AccessToken, map[string]any{
			"counted_cash":    shiftA.OpeningFloat + cashSalesBeforeTab1,
			"owner_pin":       "482913",
			"variance_reason": "Penutupan shift integrasi A",
		})
		if closeShiftARes.Code != http.StatusOK {
			t.Fatalf("close shift A failed: %d: %s", closeShiftARes.Code, closeShiftARes.Body.String())
		}
		var shiftAData struct {
			Report *domain.ZReport `json:"report"`
		}
		_ = json.Unmarshal(closeShiftARes.Body.Bytes(), &shiftAData)
		if shiftAData.Report.Summary.CashSales != cashSalesBeforeTab1 {
			t.Fatalf("shift A cash sales should remain %d before Tab 1 settlement, got %d", cashSalesBeforeTab1, shiftAData.Report.Summary.CashSales)
		}

		// Open Shift B
		openShiftB := call("POST", "/pos/shifts", cashierResp.AccessToken, map[string]any{
			"drawer_id":     drawerID,
			"opening_float": int64(100000),
		})
		if openShiftB.Code != http.StatusCreated {
			t.Fatalf("open shift B failed: %d: %s", openShiftB.Code, openShiftB.Body.String())
		}
		var shiftBInfo struct {
			Shift struct {
				ID uuid.UUID `json:"id"`
			} `json:"shift"`
		}
		_ = json.Unmarshal(openShiftB.Body.Bytes(), &shiftBInfo)

		// Pay Tab 1 on Shift B
		payRes := call("POST", fmt.Sprintf("/pos/orders/%s/payments", tab.Order.ID), cashierResp.AccessToken, map[string]any{
			"idempotency_key":      "pay-shift-b-" + uuid.NewString()[:8],
			"split_id":             sp.ID,
			"method":               "cash",
			"cash_tendered_amount": sp.TotalAmount,
		})
		if payRes.Code != http.StatusOK {
			t.Fatalf("pay tab on shift B failed: %d: %s", payRes.Code, payRes.Body.String())
		}

		// Check X-Report of Shift B: cash_sales must include Tab 1 payment!
		xRepRes := call("GET", fmt.Sprintf("/pos/shifts/%s/x-report", shiftBInfo.Shift.ID), ownerResp.AccessToken, nil)
		if xRepRes.Code != http.StatusOK {
			t.Fatalf("get X report failed: %d: %s", xRepRes.Code, xRepRes.Body.String())
		}
		var xReport domain.XReport
		_ = json.Unmarshal(xRepRes.Body.Bytes(), &xReport)
		if xReport.Summary.CashSales != sp.TotalAmount {
			t.Fatalf("expected Shift B cash sales %d, got %d", sp.TotalAmount, xReport.Summary.CashSales)
		}
	})

	// -------------------------------------------------------------
	// Test 8: Unsettled Open Tabs Warning on Close Shift (P14)
	// -------------------------------------------------------------
	t.Run("8_Unsettled_Open_Tabs_Warning_On_Shift_Close", func(t *testing.T) {
		// Open two tabs that remain unsettled (one open, one pending_payment)
		tab1Key := "tab-uns-1-" + uuid.NewString()[:8]
		t1 := call("POST", "/pos/tabs", cashierResp.AccessToken, map[string]any{
			"idempotency_key": tab1Key,
			"table_number":    "Meja-Unsettled-A",
			"initial_items": []map[string]any{
				{"product_id": productID, "quantity": 1},
			},
		})
		if t1.Code != http.StatusCreated {
			t.Fatalf("open unsettled tab 1 failed: %d: %s", t1.Code, t1.Body.String())
		}

		tab2Key := "tab-uns-2-" + uuid.NewString()[:8]
		t2 := call("POST", "/pos/tabs", cashierResp.AccessToken, map[string]any{
			"idempotency_key": tab2Key,
			"table_number":    "Meja-Unsettled-B",
			"initial_items": []map[string]any{
				{"product_id": productID, "quantity": 1},
			},
		})
		if t2.Code != http.StatusCreated {
			t.Fatalf("open unsettled tab 2 failed: %d: %s", t2.Code, t2.Body.String())
		}
		var t2Data struct {
			Order struct {
				ID uuid.UUID `json:"id"`
			} `json:"order"`
		}
		_ = json.Unmarshal(t2.Body.Bytes(), &t2Data)

		// Close tab 2 to pending_payment
		call("POST", fmt.Sprintf("/pos/tabs/%s/close", t2Data.Order.ID), cashierResp.AccessToken, nil)

		// Close current shift: MUST SUCCEED (not blocked), with unsettled_open_tabs populated
		curShiftRes := call("GET", "/pos/shifts/current?drawer_id="+drawerID.String(), cashierResp.AccessToken, nil)
		if curShiftRes.Code != http.StatusOK {
			t.Fatalf("get current shift failed: %d: %s", curShiftRes.Code, curShiftRes.Body.String())
		}
		var curShift domain.Shift
		_ = json.Unmarshal(curShiftRes.Body.Bytes(), &curShift)

		xRepB := call("GET", fmt.Sprintf("/pos/shifts/%s/x-report", curShift.ID), ownerResp.AccessToken, nil)
		var xReportB domain.XReport
		_ = json.Unmarshal(xRepB.Body.Bytes(), &xReportB)

		closeShiftRes := call("POST", fmt.Sprintf("/pos/shifts/%s/close", curShift.ID), cashierResp.AccessToken, map[string]any{
			"counted_cash":    curShift.OpeningFloat + xReportB.Summary.CashSales,
			"owner_pin":       "482913",
			"variance_reason": "Penutupan shift integrasi B dengan tab gantung",
		})
		if closeShiftRes.Code != http.StatusOK {
			t.Fatalf("expected close shift to succeed with unsettled open tabs, got %d: %s", closeShiftRes.Code, closeShiftRes.Body.String())
		}
		var closeResult struct {
			Report *domain.ZReport `json:"report"`
		}
		_ = json.Unmarshal(closeShiftRes.Body.Bytes(), &closeResult)
		if closeResult.Report.UnsettledOpenTabsCount < 2 {
			t.Fatalf("expected at least 2 unsettled open tabs in ZReport, got %d", closeResult.Report.UnsettledOpenTabsCount)
		}
		if closeResult.Report.UnsettledOpenTabsTotalAmount <= 0 {
			t.Fatalf("expected positive unsettled open tabs total amount, got %d", closeResult.Report.UnsettledOpenTabsTotalAmount)
		}
	})
}
