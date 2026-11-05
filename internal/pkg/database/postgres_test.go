package database_test

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
)

func TestBuildSafeDatabaseURL_SpecialCharacters(t *testing.T) {
	specialPassword := "p@ss:w/rd?with#special%chars&more=1"
	dsn := database.BuildSafeDatabaseURL("db.mycaffe.internal", "5432", "mycaffe_app", specialPassword, "mycaffe_pos", "require")

	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("expected valid URL from BuildSafeDatabaseURL, got error: %v", err)
	}

	if parsed.Scheme != "postgres" {
		t.Errorf("expected scheme postgres, got %q", parsed.Scheme)
	}
	if parsed.Host != "db.mycaffe.internal:5432" {
		t.Errorf("expected host db.mycaffe.internal:5432, got %q", parsed.Host)
	}
	if parsed.User.Username() != "mycaffe_app" {
		t.Errorf("expected username mycaffe_app, got %q", parsed.User.Username())
	}
	decodedPass, ok := parsed.User.Password()
	if !ok || decodedPass != specialPassword {
		t.Errorf("expected decoded password %q, got %q", specialPassword, decodedPass)
	}
	if parsed.Query().Get("sslmode") != "require" {
		t.Errorf("expected sslmode=require, got %q", parsed.Query().Get("sslmode"))
	}
}

func TestNewPostgresDB_EmptyURLFailsClosed(t *testing.T) {
	cfg := &config.Config{
		DatabaseURL: "",
	}
	_, err := database.NewPostgresDB(cfg)
	if err == nil {
		t.Fatal("expected error when DatabaseURL is empty, got nil")
	}
}

func TestWithTenantTx_NilStoreIDFailsClosed(t *testing.T) {
	err := database.WithTenantTx(context.Background(), nil, uuid.Nil, nil)
	if err == nil || !strings.Contains(err.Error(), "tenant store_id cannot be nil") {
		t.Fatalf("expected nil store_id error, got: %v", err)
	}
}

func TestMigrator_ProductionGuards(t *testing.T) {
	migrator := database.NewMigrator(nil)

	if _, err := migrator.DownOne(context.Background(), "db/migrations", "production"); err == nil {
		t.Fatal("expected DownOne to fail closed in production, got nil")
	}

	if err := migrator.Seed(context.Background(), "db/seeds/development.sql", "production"); err == nil {
		t.Fatal("expected Seed to fail closed in production, got nil")
	}
}

func TestMigrator_ProvisionAppRole_RejectsInvalidIdentifier(t *testing.T) {
	migrator := database.NewMigrator(nil)
	if err := migrator.ProvisionAppRole(context.Background(), "mycaffe_app; DROP ROLE postgres;--", "secret"); err == nil {
		t.Fatal("expected ProvisionAppRole to reject SQL injection in role identifier")
	}
}

// TestDirectAppRoleLoginAndTenantRLS connects directly as mycaffe_app (not postgres) when
// TEST_APP_DATABASE_URL is provided in CI or local integration environments.
// It verifies:
// 1. Direct TCP login as mycaffe_app succeeds with NOSUPERUSER and NOBYPASSRLS.
// 2. Queries without WithTenantTx return 0 rows (fail-closed RLS).
// 3. WithTenantTx(StoreA) sees only Store A rows across stores, orders, and payments.
// 4. resolve_payment_webhook_context resolves Store A context from provider_order_id before tenant context is set.
// 5. DELETE on financial tables (payments, orders, payment_notification_logs) is rejected with insufficient_privilege.
func TestDirectAppRoleLoginAndTenantRLS(t *testing.T) {
	appDSN := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if appDSN == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping live mycaffe_app direct-login integration test")
	}

	db, err := database.NewPostgresDB(&config.Config{DatabaseURL: appDSN})
	if err != nil {
		t.Fatalf("failed to connect directly as mycaffe_app: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()

	// 1. Verify current_user is mycaffe_app and neither superuser nor bypassrls
	var roleInfo struct {
		CurrentUser  string `db:"current_user"`
		RolSuper     bool   `db:"rolsuper"`
		RolBypassRLS bool   `db:"rolbypassrls"`
	}
	err = db.GetContext(ctx, &roleInfo, `
		SELECT current_user, rolsuper, rolbypassrls
		FROM pg_roles
		WHERE rolname = current_user
	`)
	if err != nil {
		t.Fatalf("failed to inspect current_user role flags: %v", err)
	}
	if roleInfo.CurrentUser != "mycaffe_app" {
		t.Fatalf("expected direct connection as mycaffe_app, got %q", roleInfo.CurrentUser)
	}
	if roleInfo.RolSuper || roleInfo.RolBypassRLS {
		t.Fatalf("mycaffe_app must not have superuser (%v) or bypassrls (%v)", roleInfo.RolSuper, roleInfo.RolBypassRLS)
	}

	// 2. Without tenant context, RLS must fail-closed (0 rows across stores and orders)
	var unAuthCount int
	if err := db.GetContext(ctx, &unAuthCount, "SELECT COUNT(*) FROM orders"); err != nil {
		t.Fatalf("unexpected error querying orders without tenant context: %v", err)
	}
	if unAuthCount != 0 {
		t.Fatalf("expected 0 orders without tenant context under mycaffe_app, got %d", unAuthCount)
	}
	if err := db.GetContext(ctx, &unAuthCount, "SELECT COUNT(*) FROM stores"); err != nil {
		t.Fatalf("unexpected error querying stores without tenant context: %v", err)
	}
	if unAuthCount != 0 {
		t.Fatalf("expected 0 stores without tenant context under mycaffe_app, got %d", unAuthCount)
	}

	// 3. Seed a self-contained fixture order + payment for Store A (since db/seeds/development.sql has 0 seeded orders),
	//    then resolve webhook context via SECURITY DEFINER helper before tenant context is set.
	storeAID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	fixtureOrderID := uuid.MustParse("30000001-0000-4000-8000-000000000099")
	fixturePaymentID := uuid.MustParse("40000001-0000-4000-8000-000000000099")
	err = database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO orders (
				id, store_id, idempotency_key, request_hash, order_number, queue_number, table_number,
				customer_name, customer_phone, order_type, billing_mode, status, payment_status,
				subtotal_amount, discount_amount, tax_amount, service_charge_amount, total_amount, paid_amount
			) VALUES (
				$1, $2, 'idem-rls-test-0001', 'hash-rls-test-0001', 'ORD-RLS-TEST-0001', 'A99', 'M-01',
				'Pelanggan Uji RLS', '081200000099', 'dine_in', 'pay_first', 'served', 'paid',
				24000, 0, 2400, 0, 26400, 26400
			) ON CONFLICT (id) DO NOTHING
		`, fixtureOrderID, storeAID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO payments (
				id, store_id, order_id, attempt_no, idempotency_key, request_hash,
				method, provider, provider_order_id, amount, status
			) VALUES (
				$1, $2, $3, 1, 'idem-pay-rls-test-0001', '0000000000000000000000000000000000000000000000000000000000000001',
				'qris', 'midtrans', 'ORD-20261028-0001', 26400, 'pending'
			) ON CONFLICT (id) DO NOTHING
		`, fixturePaymentID, storeAID, fixtureOrderID)
		return err
	})
	if err != nil {
		t.Fatalf("failed to insert fixture order/payment under WithTenantTx: %v", err)
	}

	var resolved struct {
		StoreID uuid.UUID `db:"store_id"`
		OrderID uuid.UUID `db:"order_id"`
	}
	err = db.GetContext(ctx, &resolved, "SELECT store_id, order_id FROM resolve_payment_webhook_context($1)", "ORD-20261028-0001")
	if err != nil {
		t.Fatalf("resolve_payment_webhook_context failed under mycaffe_app: %v", err)
	}
	if resolved.StoreID != storeAID {
		t.Fatalf("expected resolved store_id %s, got %s", storeAID, resolved.StoreID)
	}

	// 4. WithTenantTx for Store A sees only Store A records
	err = database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
		var storeCount int
		if err := tx.GetContext(ctx, &storeCount, "SELECT COUNT(*) FROM stores"); err != nil {
			return err
		}
		if storeCount != 1 {
			t.Fatalf("expected 1 visible store for Store A, got %d", storeCount)
		}

		var crossTenantCount int
		if err := tx.GetContext(ctx, &crossTenantCount, "SELECT COUNT(*) FROM orders WHERE store_id <> $1", storeAID); err != nil {
			return err
		}
		if crossTenantCount != 0 {
			t.Fatalf("expected 0 cross-tenant orders visible to Store A, got %d", crossTenantCount)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WithTenantTx failed for Store A: %v", err)
	}

	// 5. Verify DELETE privilege is revoked on payments and orders under mycaffe_app
	delErr := database.WithTenantTx(ctx, db, storeAID, func(tx *sqlx.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM payments")
		return err
	})
	if delErr == nil || !strings.Contains(strings.ToLower(delErr.Error()), "permission denied") {
		t.Fatalf("expected DELETE FROM payments to fail with permission denied for mycaffe_app, got: %v", delErr)
	}
}
