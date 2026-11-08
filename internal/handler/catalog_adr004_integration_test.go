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
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/config"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/handler"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/service"
)

// TestCatalogADR004Integration: the variant is the only price source (products.base_price is
// ignored by pricing) and the database rejects single-choice modifier groups with max_select <> 1.
func TestCatalogADR004Integration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping ADR-004 catalog integration test")
	}
	db, err := database.NewPostgresDB(&config.Config{DatabaseURL: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	storeID := uuid.MustParse("11111111-1111-4111-8111-111111111111")

	gin.SetMode(gin.TestMode)
	authSvc := service.NewAuthService(db, postgres.NewAuthRepository(), "integration-test-jwt-secret-at-least-32-chars", service.AuthPolicy{
		AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 12 * time.Hour, SessionIdleTimeout: 2 * time.Hour,
		MaxSessions: 3, PINMaxFailures: 5, PINLockout: 15 * time.Minute, MaxConcurrentHashes: 8,
	})
	ah := handler.NewAuthHandler(authSvc)
	orderRepo := postgres.NewOrderRepository()
	pos := handler.NewPOSHandler(service.NewOrderService(db, orderRepo), service.NewPaymentService(db, postgres.NewPaymentRepository(), orderRepo, nil), service.NewSettingsService(db))
	r := gin.New()
	r.POST("/login", ah.Login)
	r.POST("/pos/orders", ah.RequireStaff(), pos.CreateOrder)
	call := func(path, token string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		_ = json.NewEncoder(&buf).Encode(body)
		req := httptest.NewRequest(http.MethodPost, path, &buf)
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	loginRes := call("/login", "", map[string]string{"store_slug": "central-cafe", "email": "cashier.central@mycaffe.local", "pin": "482913"})
	if loginRes.Code != http.StatusOK {
		t.Fatalf("login failed: %d: %s", loginRes.Code, loginRes.Body.String())
	}
	var session struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal(loginRes.Body.Bytes(), &session)

	t.Run("OrderPricesFromVariantNotBasePrice", func(t *testing.T) {
		productID := uuid.New()
		if err := database.WithTenantTx(ctx, db, storeID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `
				WITH p AS (INSERT INTO products (id, store_id, name, base_price, is_available) VALUES ($1, $2, $3, 1, true) RETURNING store_id, id)
				INSERT INTO product_variants (store_id, product_id, name, price, is_default) SELECT store_id, id, 'Reguler', 15000, true FROM p`,
				productID, storeID, "ADR004 Teh "+productID.String()[:6])
			return err
		}); err != nil {
			t.Fatalf("seed product: %v", err)
		}
		res := call("/pos/orders", session.AccessToken, map[string]any{
			"idempotency_key": "adr004-" + uuid.NewString(),
			"order_type":      "takeaway",
			"items":           []map[string]any{{"product_id": productID, "quantity": 2}},
		})
		if res.Code != http.StatusCreated {
			t.Fatalf("create order failed: %d: %s", res.Code, res.Body.String())
		}
		var body struct {
			Order struct {
				SubtotalAmount int64 `json:"subtotal_amount"`
			} `json:"order"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &body)
		if body.Order.SubtotalAmount != 30000 {
			t.Fatalf("subtotal must come from the variant (2 x 15000), got %d", body.Order.SubtotalAmount)
		}
	})

	t.Run("SingleChoiceGroupRejectsMaxSelectAboveOne", func(t *testing.T) {
		err := database.WithTenantTx(ctx, db, storeID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO modifier_groups (store_id, name, selection_type, min_select, max_select)
				VALUES ($1, $2, 'single', 0, 2)`, storeID, "ADR004 Bad "+uuid.NewString()[:6])
			return err
		})
		if err == nil || !strings.Contains(err.Error(), "chk_modifier_groups_single_max") {
			t.Fatalf("expected chk_modifier_groups_single_max violation, got %v", err)
		}
	})
}
