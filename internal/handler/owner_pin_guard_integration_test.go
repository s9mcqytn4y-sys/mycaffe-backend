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
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/handler"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/mailer"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

// TestIntegrationOwnerPINGuard covers review follow-ups F1 and F2 (HF5 updated): wrong owner PINs from
// other staff never lock the owner, a store-wide cap blocks every owner-PIN approval and emails the
// owners once, an owner's own wrong PIN uses the login counter, and the caps follow the configured
// policy (here 2 failures per 10 minutes, so the store cap is 6).
func TestIntegrationOwnerPINGuard(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping owner PIN guard integration test")
	}
	db, err := database.NewPostgresDB(&config.Config{DatabaseURL: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	gin.SetMode(gin.TestMode)
	policy := service.AuthPolicy{
		AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 12 * time.Hour, SessionIdleTimeout: 2 * time.Hour,
		MaxSessions: 3, PINMaxFailures: 2, PINLockout: 10 * time.Minute, MaxConcurrentHashes: 8,
	}
	authSvc := service.NewAuthService(db, postgres.NewAuthRepository(), "integration-test-jwt-secret-at-least-32-chars", policy)
	ah := handler.NewAuthHandler(authSvc)
	orderRepo := postgres.NewOrderRepository()
	paymentRepo := postgres.NewPaymentRepository()
	opSvc := service.NewOperationalService(db, orderRepo, paymentRepo, nil, nil, nil)
	opSvc.SetPINPolicy(policy, service.NewEmailService(db, mailer.LogSender{}))
	pos := handler.NewPOSHandler(service.NewOrderService(db, orderRepo), service.NewPaymentService(db, paymentRepo, orderRepo, nil), service.NewSettingsService(db))
	pos.SetOperational(opSvc)

	r := gin.New()
	r.POST("/login", ah.Login)
	r.POST("/pos/products/:id/stock-override", ah.RequirePermission(rbac.StockOverride), pos.OverrideStock)
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
	inStore := func(storeID uuid.UUID, fn func(tx *sqlx.Tx) error) {
		t.Helper()
		if err := database.WithTenantTx(ctx, db, storeID, fn); err != nil {
			t.Fatalf("tenant tx failed: %v", err)
		}
	}

	// A fresh store per run keeps the store-wide failure count isolated from other tests and reruns.
	migratorDSN := strings.TrimSpace(os.Getenv("TEST_MIGRATOR_DATABASE_URL"))
	if migratorDSN == "" {
		t.Skip("TEST_MIGRATOR_DATABASE_URL not set; skipping owner PIN guard integration test")
	}
	migratorDB, err := database.NewPostgresDB(&config.Config{DatabaseURL: migratorDSN})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migratorDB.Close() })
	suffix := uuid.NewString()[:8]
	slug := "pinguard-" + suffix
	email := func(name string) string { return name + ".pinguard." + suffix + "@mycaffe.local" }
	storeID, ownerID, err := service.BootstrapStore(ctx, migratorDB, service.BootstrapStoreInput{
		Slug: slug, StoreName: "Kafe PIN Guard", OwnerName: "PIN Guard owner", OwnerEmail: email("owner"), PIN: "482913",
	})
	if err != nil {
		t.Fatalf("bootstrap store: %v", err)
	}
	productID := uuid.New()
	users := map[string]uuid.UUID{"owner": ownerID, "a": uuid.New(), "b": uuid.New(), "c": uuid.New(), "d": uuid.New()}
	inStore(storeID, func(tx *sqlx.Tx) error {
		for name, id := range users {
			if name == "owner" {
				continue
			}
			// Cashiers share the owner's PIN hash (PIN 482913).
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO users (id, store_id, email, full_name, role, pin_hash, is_active, pin_set_at)
				SELECT $1, $2, $3, $4, 'cashier', pin_hash, true, NOW() FROM users WHERE id = $5`,
				id, storeID, email(name), "PIN Guard "+name, ownerID); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `WITH p AS (INSERT INTO products (id, store_id, name, base_price, is_available, track_stock, stock_quantity)
			VALUES ($1, $2, $3, 10000, true, true, 5)
			RETURNING store_id, id, base_price)
			INSERT INTO product_variants (store_id, product_id, name, price, is_default) SELECT store_id, id, 'Reguler', base_price, true FROM p`, productID, storeID, "PIN Guard "+suffix)
		return err
	})
	tokens := map[string]string{}
	for name := range users {
		res := call("/login", "", map[string]string{"store_slug": slug, "email": email(name), "pin": "482913"})
		if res.Code != http.StatusOK {
			t.Fatalf("login %s failed: %d: %s", name, res.Code, res.Body.String())
		}
		var body struct {
			AccessToken string `json:"access_token"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &body)
		tokens[name] = body.AccessToken
	}
	override := func(who, pin string) *httptest.ResponseRecorder {
		return call(fmt.Sprintf("/pos/products/%s/stock-override", productID), tokens[who],
			map[string]any{"new_quantity": 3, "reason": "Opname", "owner_pin": pin})
	}
	expect := func(res *httptest.ResponseRecorder, code int, errCode string) {
		t.Helper()
		if res.Code != code || !strings.Contains(res.Body.String(), errCode) {
			t.Fatalf("expected %d %s, got %d: %s", code, errCode, res.Code, res.Body.String())
		}
	}
	ownerCounter := func() (attempts int, locked bool) {
		inStore(storeID, func(tx *sqlx.Tx) error {
			return tx.QueryRowxContext(ctx, `SELECT failed_pin_attempts, locked_until IS NOT NULL FROM users WHERE store_id = $1 AND id = $2`,
				storeID, users["owner"]).Scan(&attempts, &locked)
		})
		return attempts, locked
	}

	t.Run("OwnOwnerPINFailureUsesLoginCounter", func(t *testing.T) {
		expect(override("owner", "000000"), http.StatusUnauthorized, "INVALID_OWNER_PIN")
		if attempts, locked := ownerCounter(); attempts != 1 || locked {
			t.Fatalf("owner's own wrong PIN must advance the login counter: attempts=%d locked=%v", attempts, locked)
		}
	})

	t.Run("OwnerPINGuessDoesNotLockOwner", func(t *testing.T) {
		expect(override("a", "000000"), http.StatusUnauthorized, "INVALID_OWNER_PIN")
		expect(override("a", "000000"), http.StatusUnauthorized, "INVALID_OWNER_PIN")
		// Configured per-actor cap is 2 (F2): even the right PIN is refused now.
		expect(override("a", "482913"), http.StatusTooManyRequests, "PIN_ATTEMPTS_EXCEEDED")
		if attempts, locked := ownerCounter(); attempts != 1 || locked {
			t.Fatalf("another staff member's guesses must not touch the owner: attempts=%d locked=%v", attempts, locked)
		}
	})

	t.Run("OwnerPINStoreWideLimit", func(t *testing.T) {
		// Store failures so far: owner 1 + a 2 = 3. Three more reach the cap of 6.
		expect(override("b", "000000"), http.StatusUnauthorized, "INVALID_OWNER_PIN")
		expect(override("b", "000000"), http.StatusUnauthorized, "INVALID_OWNER_PIN")
		expect(override("c", "000000"), http.StatusUnauthorized, "INVALID_OWNER_PIN")
		// d never failed, yet every owner-PIN approval in the store is blocked.
		expect(override("d", "482913"), http.StatusTooManyRequests, "OWNER_PIN_STORE_LIMIT")
		expect(override("c", "482913"), http.StatusTooManyRequests, "OWNER_PIN_STORE_LIMIT")

		var emails int
		inStore(storeID, func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &emails, `SELECT COUNT(*) FROM email_outbox WHERE store_id = $1 AND template = $2 AND to_email = $3`,
				storeID, mailer.TemplateOwnerPINAttempts, email("owner"))
		})
		if emails != 1 {
			t.Fatalf("expected exactly 1 owner warning email, got %d", emails)
		}
		if _, locked := ownerCounter(); locked {
			t.Fatal("the store cap must not lock the owner account")
		}
	})
}
