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
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/handler"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/auth"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

// TestDeviceIntegration covers v0.10 PR 10.1 (ADR-005 OF2/OF3, W19/W20): owner-only device registration,
// the device credential, offline PIN verifiers issued at login, catalog snapshots on serve, revocation
// and cross-tenant isolation of the credential.
func TestDeviceIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping device integration test")
	}
	db, err := database.NewPostgresDB(&config.Config{DatabaseURL: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	storeID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	expressID := uuid.MustParse("22222222-2222-4222-8222-222222222222")

	gin.SetMode(gin.TestMode)
	authSvc := service.NewAuthService(db, postgres.NewAuthRepository(), "integration-test-jwt-secret-at-least-32-chars", service.AuthPolicy{
		AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 12 * time.Hour, SessionIdleTimeout: 2 * time.Hour,
		MaxSessions: 3, PINMaxFailures: 5, PINLockout: 15 * time.Minute, MaxConcurrentHashes: 8,
	})
	ah := handler.NewAuthHandler(authSvc)
	dh := handler.NewDeviceHandler(service.NewDeviceService(db, service.NewCatalogService(db)))

	r := gin.New()
	r.POST("/login", ah.Login)
	devices := r.Group("/store/devices", ah.RequirePermission(rbac.DeviceManage))
	devices.GET("", dh.ListDevices)
	devices.POST("", dh.RegisterDevice)
	devices.PATCH("/:id", dh.RenameDevice)
	devices.POST("/:id/revoke", dh.RevokeDevice)
	r.GET("/device/catalog", dh.Catalog)

	call := func(method, path, authz string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		req.Header.Set("Content-Type", "application/json")
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	type session struct {
		AccessToken string                      `json:"access_token"`
		Verifier    *service.OfflinePINVerifier `json:"offline_pin_verifier"`
		Code        string                      `json:"code"`
	}
	login := func(email, credential string) (int, session) {
		t.Helper()
		body := map[string]string{"store_slug": "central-cafe", "email": email, "pin": "482913"}
		if credential != "" {
			body["device_credential"] = credential
		}
		res := call("POST", "/login", "", body)
		var s session
		_ = json.Unmarshal(res.Body.Bytes(), &s)
		return res.Code, s
	}
	bearer := func(email string) string {
		t.Helper()
		code, s := login(email, "")
		if code != http.StatusOK {
			t.Fatalf("login %s failed: %d", email, code)
		}
		return "Bearer " + s.AccessToken
	}
	inStore := func(fn func(tx *sqlx.Tx) error) {
		t.Helper()
		if err := database.WithTenantTx(ctx, db, storeID, fn); err != nil {
			t.Fatalf("tenant tx failed: %v", err)
		}
	}
	const cashierEmail = "cashier.central@mycaffe.local"
	owner, cashier := bearer("abdulaziz543333@gmail.com"), bearer(cashierEmail)
	// Devices are revoked, never deleted: a fresh prefix per run keeps reruns independent.
	prefix := "T" + strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", "")[:3])

	var device service.Device
	t.Run("OwnerRegistersDevice", func(t *testing.T) {
		if res := call("POST", "/store/devices", cashier, map[string]any{"name": "Kasir 1", "prefix": prefix}); res.Code != http.StatusForbidden {
			t.Fatalf("cashier must not register devices, got %d", res.Code)
		}
		for _, bad := range []map[string]any{{"name": "X", "prefix": "1A"}, {"name": "X", "prefix": "ABCDE"}, {"name": "", "prefix": "Z9"}, {"name": "X"}} {
			if res := call("POST", "/store/devices", owner, bad); res.Code != http.StatusUnprocessableEntity {
				t.Fatalf("%v must be 422, got %d: %s", bad, res.Code, res.Body.String())
			}
		}
		res := call("POST", "/store/devices", owner, map[string]any{"name": "Kasir depan", "prefix": strings.ToLower(prefix)})
		if res.Code != http.StatusCreated {
			t.Fatalf("register failed: %d: %s", res.Code, res.Body.String())
		}
		_ = json.Unmarshal(res.Body.Bytes(), &device)
		if device.Prefix != prefix || !strings.HasPrefix(device.Credential, storeID.String()+"."+device.ID.String()+".") {
			t.Fatalf("prefix is upper-cased and the credential carries store and device ids: %+v", device)
		}
		if res := call("POST", "/store/devices", owner, map[string]any{"name": "Lain", "prefix": prefix}); res.Code != http.StatusConflict {
			t.Fatalf("a duplicate prefix must be 409, got %d", res.Code)
		}
		list := call("GET", "/store/devices", owner, nil)
		if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), device.ID.String()) || strings.Contains(list.Body.String(), "credential") {
			t.Fatalf("the list shows the device but never a credential: %s", list.Body.String())
		}
		if res := call("PATCH", "/store/devices/"+device.ID.String(), owner, map[string]any{"name": "Kasir", "prefix": "ZZ"}); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("the prefix is fixed once printed on offline numbers, got %d", res.Code)
		}
		if res := call("PATCH", "/store/devices/"+device.ID.String(), owner, map[string]any{"name": "Kasir belakang"}); res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "Kasir belakang") {
			t.Fatalf("rename failed: %d: %s", res.Code, res.Body.String())
		}
	})
	if device.Credential == "" {
		t.Fatal("registration failed; later steps need the device")
	}
	deviceAuth := "Device " + device.Credential
	forged := strings.Replace(device.Credential, storeID.String(), expressID.String(), 1)

	t.Run("CatalogSnapshotOnServe", func(t *testing.T) {
		if res := call("GET", "/device/catalog", "", nil); res.Code != http.StatusUnauthorized {
			t.Fatalf("no credential must be 401, got %d", res.Code)
		}
		if res := call("GET", "/device/catalog", "Device "+forged, nil); res.Code != http.StatusUnauthorized {
			t.Fatalf("a credential moved to another store must be 401 (RLS), got %d", res.Code)
		}
		type served struct {
			Version       string    `json:"catalog_version"`
			FirstServedAt time.Time `json:"first_served_at"`
			Catalog       struct {
				Products []json.RawMessage `json:"products"`
			} `json:"catalog"`
		}
		get := func() served {
			t.Helper()
			res := call("GET", "/device/catalog", deviceAuth, nil)
			if res.Code != http.StatusOK {
				t.Fatalf("device catalog failed: %d: %s", res.Code, res.Body.String())
			}
			var s served
			_ = json.Unmarshal(res.Body.Bytes(), &s)
			return s
		}
		first, second := get(), get()
		if len(first.Version) != 64 || len(first.Catalog.Products) == 0 {
			t.Fatalf("expected a SHA-256 version and the POS catalog, got %q with %d products", first.Version, len(first.Catalog.Products))
		}
		// Other tests may change prices concurrently; only compare when the content is the same.
		if second.Version == first.Version && !second.FirstServedAt.Equal(first.FirstServedAt) {
			t.Fatal("an unchanged catalog keeps its version and first_served_at")
		}
		var rows int
		inStore(func(tx *sqlx.Tx) error {
			return tx.GetContext(ctx, &rows, `SELECT COUNT(*) FROM catalog_versions WHERE store_id = $1 AND version = $2`, storeID, first.Version)
		})
		if rows != 1 {
			t.Fatalf("the snapshot is stored once, got %d rows", rows)
		}
	})

	t.Run("LoginIssuesOfflineVerifier", func(t *testing.T) {
		if _, s := login(cashierEmail, ""); s.Verifier != nil {
			t.Fatal("a login without a device must not carry a verifier")
		}
		code, s := login(cashierEmail, device.Credential)
		if code != http.StatusOK || s.Verifier == nil {
			t.Fatalf("a device login must carry a verifier: %d %+v", code, s)
		}
		if !strings.HasPrefix(s.Verifier.Verifier, "$argon2id$v=19$m=65536,t=3,p=1$") {
			t.Fatalf("the verifier must use the W19 parameters: %q", s.Verifier.Verifier)
		}
		if ok, err := auth.VerifyPIN("482913", s.Verifier.Verifier); err != nil || !ok {
			t.Fatal("the verifier must verify the staff PIN")
		}
		if d := time.Until(s.Verifier.ExpiresAt); d < 71*time.Hour || d > 73*time.Hour {
			t.Fatalf("the verifier lives 72 h, got %s", d)
		}
		var issued, audits int
		inStore(func(tx *sqlx.Tx) error {
			if err := tx.GetContext(ctx, &issued, `SELECT COUNT(*) FROM device_pin_verifiers v JOIN users u ON u.store_id = v.store_id AND u.id = v.user_id
				WHERE v.store_id = $1 AND v.device_id = $2 AND u.email = $3 AND v.expires_at > NOW()`, storeID, device.ID, cashierEmail); err != nil {
				return err
			}
			return tx.GetContext(ctx, &audits, `SELECT COUNT(*) FROM audit_logs WHERE store_id = $1 AND action = 'DEVICE_PIN_VERIFIER_ISSUED' AND reference_id = $2`, storeID, device.ID.String())
		})
		if issued != 1 || audits != 1 {
			t.Fatalf("expected one verifier record and one audit, got %d and %d", issued, audits)
		}
		if code, s := login(cashierEmail, forged); code != http.StatusUnauthorized || s.Code != "DEVICE_INVALID" {
			t.Fatalf("a credential of another store must fail the login, got %d %q", code, s.Code)
		}
	})

	t.Run("RevokeStopsTheDevice", func(t *testing.T) {
		if res := call("POST", "/store/devices/"+device.ID.String()+"/revoke", cashier, nil); res.Code != http.StatusForbidden {
			t.Fatalf("cashier must not revoke devices, got %d", res.Code)
		}
		for range 2 { // idempotent
			if res := call("POST", "/store/devices/"+device.ID.String()+"/revoke", owner, nil); res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "revoked_at") {
				t.Fatalf("revoke failed: %d: %s", res.Code, res.Body.String())
			}
		}
		if res := call("GET", "/device/catalog", deviceAuth, nil); res.Code != http.StatusUnauthorized {
			t.Fatalf("a revoked device must be 401, got %d", res.Code)
		}
		if code, s := login(cashierEmail, device.Credential); code != http.StatusUnauthorized || s.Code != "DEVICE_INVALID" {
			t.Fatalf("a revoked device must fail the login, got %d %q", code, s.Code)
		}
		var verifiers, audits int
		inStore(func(tx *sqlx.Tx) error {
			if err := tx.GetContext(ctx, &verifiers, `SELECT COUNT(*) FROM device_pin_verifiers WHERE store_id = $1 AND device_id = $2`, storeID, device.ID); err != nil {
				return err
			}
			return tx.GetContext(ctx, &audits, `SELECT COUNT(*) FROM audit_logs WHERE store_id = $1 AND action = 'DEVICE_REVOKED' AND reference_id = $2`, storeID, device.ID.String())
		})
		if verifiers != 0 || audits != 1 {
			t.Fatalf("revoke drops verifiers and audits once, got %d verifiers and %d audits", verifiers, audits)
		}
	})
}
