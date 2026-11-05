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
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/handler"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

// TestIntegrationStaffBadges covers decision V14: the owner issues a QR badge (token shown once, hash
// stored), badge + PIN logs in without store code or e-mail, a wrong PIN or a tampered, reissued or
// revoked badge is refused like a wrong credential, and only owners manage badges.
func TestIntegrationStaffBadges(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	migratorDSN := strings.TrimSpace(os.Getenv("TEST_MIGRATOR_DATABASE_URL"))
	if dsn == "" || migratorDSN == "" {
		t.Skip("TEST_APP_DATABASE_URL / TEST_MIGRATOR_DATABASE_URL not set; skipping staff badge integration test")
	}
	db, err := database.NewPostgresDB(&config.Config{DatabaseURL: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	migratorDB, err := database.NewPostgresDB(&config.Config{DatabaseURL: migratorDSN})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = migratorDB.Close() })
	ctx := context.Background()

	suffix := uuid.NewString()[:8]
	slug := "badge-" + suffix
	email := func(name string) string { return name + ".badge." + suffix + "@mycaffe.local" }
	_, ownerID, err := service.BootstrapStore(ctx, migratorDB, service.BootstrapStoreInput{
		Slug: slug, StoreName: "Kafe Kartu", OwnerName: "Pemilik Kartu", OwnerEmail: email("owner"), PIN: "482913",
	})
	if err != nil {
		t.Fatal(err)
	}
	var cashierID uuid.UUID
	if err := migratorDB.GetContext(ctx, &cashierID, `INSERT INTO users (store_id, email, full_name, role, pin_hash, is_active, pin_set_at)
		SELECT store_id, $2, 'Kasir Kartu', 'cashier', pin_hash, true, NOW() FROM users WHERE id = $1 RETURNING id`, ownerID, email("cashier")); err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	ah := handler.NewAuthHandler(service.NewAuthService(db, postgres.NewAuthRepository(), "integration-test-jwt-secret-at-least-32-chars", service.AuthPolicy{
		AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 12 * time.Hour, SessionIdleTimeout: 2 * time.Hour,
		MaxSessions: 3, PINMaxFailures: 5, PINLockout: 15 * time.Minute, MaxConcurrentHashes: 8,
	}))
	r := gin.New()
	r.POST("/login", ah.Login)
	r.POST("/staff/:id/badge", ah.RequirePermission(rbac.StaffManage), ah.IssueBadge)
	r.POST("/staff/:id/badge/revoke", ah.RequirePermission(rbac.StaffManage), ah.RevokeBadge)
	call := func(path, token string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		_ = json.NewEncoder(&buf).Encode(body)
		req := httptest.NewRequest("POST", path, &buf)
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	accessToken := func(res *httptest.ResponseRecorder) string {
		var b struct {
			AccessToken string `json:"access_token"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &b)
		return b.AccessToken
	}
	owner := accessToken(call("/login", "", map[string]string{"store_slug": slug, "email": email("owner"), "pin": "482913"}))
	cashier := accessToken(call("/login", "", map[string]string{"store_slug": slug, "email": email("cashier"), "pin": "482913"}))
	issue := func() string {
		t.Helper()
		res := call("/staff/"+cashierID.String()+"/badge", owner, nil)
		var b service.StaffBadge
		_ = json.Unmarshal(res.Body.Bytes(), &b)
		if res.Code != http.StatusCreated || strings.Count(b.Token, ".") != 2 {
			t.Fatalf("issue badge: %d %s", res.Code, res.Body.String())
		}
		return b.Token
	}
	badgeLogin := func(badge, pin string) int {
		return call("/login", "", map[string]string{"badge": badge, "pin": pin}).Code
	}

	if res := call("/staff/"+cashierID.String()+"/badge", cashier, nil); res.Code != http.StatusForbidden {
		t.Fatalf("only owners issue badges: %d", res.Code)
	}
	first := issue()
	var stored string
	_ = migratorDB.GetContext(ctx, &stored, `SELECT token_hash FROM staff_badges WHERE user_id = $1 AND revoked_at IS NULL`, cashierID)
	if strings.Contains(first, stored) || len(stored) != 64 {
		t.Fatal("only the SHA-256 of the secret may be stored")
	}
	if code := badgeLogin(first, "482913"); code != http.StatusOK {
		t.Fatalf("badge + PIN must log in: %d", code)
	}
	if code := badgeLogin(first, "905172"); code != http.StatusUnauthorized {
		t.Fatalf("badge with a wrong PIN: %d", code)
	}
	if code := badgeLogin(first[:len(first)-2]+"xx", "482913"); code != http.StatusUnauthorized {
		t.Fatalf("tampered badge: %d", code)
	}
	if code := call("/login", "", map[string]string{"pin": "482913"}).Code; code != http.StatusBadRequest {
		t.Fatalf("PIN alone must be refused: %d", code)
	}
	second := issue()
	if code := badgeLogin(first, "482913"); code != http.StatusUnauthorized {
		t.Fatalf("a reissued card must invalidate the old one: %d", code)
	}
	if res := call("/staff/"+cashierID.String()+"/badge/revoke", owner, nil); res.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", res.Code, res.Body.String())
	}
	if code := badgeLogin(second, "482913"); code != http.StatusUnauthorized {
		t.Fatalf("a revoked card must stop working: %d", code)
	}
	if res := call("/staff/"+cashierID.String()+"/badge/revoke", owner, nil); res.Code != http.StatusNotFound {
		t.Fatalf("nothing left to revoke: %d", res.Code)
	}
	var audits int
	_ = migratorDB.GetContext(ctx, &audits, `SELECT COUNT(*) FROM audit_logs WHERE reference_id = $1 AND action IN ('STAFF_BADGE_ISSUED', 'STAFF_BADGE_REVOKED')`, cashierID.String())
	if audits != 3 {
		t.Fatalf("two issues and one revoke must be audited, got %d", audits)
	}
}
