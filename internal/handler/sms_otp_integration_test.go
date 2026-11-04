package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/handler"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/sms"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

// TestIntegrationSMSOTP covers decision V4: staff PIN reset by SMS (no enumeration, one code per minute,
// wrong codes spend attempts, a valid code resets the PIN and revokes sessions), the owner setting a
// staff phone, customer phone verification at the POS, and SMS_DRIVER=disabled answering 503.
func TestIntegrationSMSOTP(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	migratorDSN := strings.TrimSpace(os.Getenv("TEST_MIGRATOR_DATABASE_URL"))
	if dsn == "" || migratorDSN == "" {
		t.Skip("TEST_APP_DATABASE_URL / TEST_MIGRATOR_DATABASE_URL not set; skipping SMS OTP integration test")
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
	slug := "sms-" + suffix
	ownerEmail := "owner.sms." + suffix + "@mycaffe.local"
	storeID, ownerID, err := service.BootstrapStore(ctx, migratorDB, service.BootstrapStoreInput{
		Slug: slug, StoreName: "Kafe SMS", OwnerName: "Pemilik SMS", OwnerEmail: ownerEmail, PIN: "482913",
	})
	if err != nil {
		t.Fatalf("bootstrap store: %v", err)
	}
	cashierEmail := "cashier.sms." + suffix + "@mycaffe.local"
	if _, err := migratorDB.ExecContext(ctx, `INSERT INTO users (store_id, email, full_name, role, pin_hash, is_active, pin_set_at)
		SELECT store_id, $2, 'Kasir SMS', 'cashier', pin_hash, true, NOW() FROM users WHERE id = $1`, ownerID, cashierEmail); err != nil {
		t.Fatal(err)
	}
	var cashierID uuid.UUID
	_ = migratorDB.GetContext(ctx, &cashierID, `SELECT id FROM users WHERE store_id = $1 AND email = $2`, storeID, cashierEmail)

	gin.SetMode(gin.TestMode)
	policy := service.AuthPolicy{AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 12 * time.Hour, SessionIdleTimeout: 2 * time.Hour,
		MaxSessions: 3, PINMaxFailures: 5, PINLockout: 15 * time.Minute, MaxConcurrentHashes: 8}
	router := func(sender sms.Sender) *gin.Engine {
		messages := service.NewMessageService(db, sender)
		ah := handler.NewAuthHandler(service.NewAuthService(db, postgres.NewAuthRepository(), "integration-test-jwt-secret-at-least-32-chars", policy,
			service.WithAuthSMS(messages)))
		ch := handler.NewCustomerHandler(service.NewCustomerService(db, messages))
		r := gin.New()
		r.POST("/login", ah.Login)
		r.POST("/refresh", ah.Refresh)
		r.POST("/pin/forgot-sms", ah.ForgotPINSMS)
		r.POST("/pin/reset-sms", ah.ResetPINSMS)
		r.POST("/staff/:id/phone", ah.RequirePermission(rbac.StaffManage), ah.SetStaffPhone)
		r.POST("/pos/customers/verify", ah.RequirePermission(rbac.OrderPOSCreate), ch.StartVerification)
		r.POST("/pos/customers/verify/:id/confirm", ah.RequirePermission(rbac.OrderPOSCreate), ch.ConfirmVerification)
		return r
	}
	enabled, disabled := router(sms.LogSender{}), router(nil)
	call := func(r *gin.Engine, method, path, token string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		_ = json.NewEncoder(&buf).Encode(body)
		req := httptest.NewRequest(method, path, &buf)
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "198.51.100." + fmt.Sprint(time.Now().UnixNano()%200+1) + ":1234" // fresh per-IP budget
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	login := func(email, pin string) (string, int) {
		res := call(enabled, "POST", "/login", "", map[string]string{"store_slug": slug, "email": email, "pin": pin})
		var b struct {
			AccessToken string `json:"access_token"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &b)
		return b.AccessToken, res.Code
	}
	codeRE := regexp.MustCompile(`\b\d{6}\b`)
	latestCode := func(phone string) string {
		t.Helper()
		var body string
		if err := migratorDB.GetContext(ctx, &body, `SELECT body FROM message_outbox WHERE store_id = $1 AND to_phone = $2 ORDER BY created_at DESC LIMIT 1`, storeID, phone); err != nil {
			t.Fatalf("no sms queued for %s: %v", phone, err)
		}
		return codeRE.FindString(body)
	}
	queued := func(phone string) int {
		var n int
		_ = migratorDB.GetContext(ctx, &n, `SELECT COUNT(*) FROM message_outbox WHERE store_id = $1 AND to_phone = $2`, storeID, phone)
		return n
	}
	owner, _ := login(ownerEmail, "482913")
	cashier, _ := login(cashierEmail, "482913")
	digits := fmt.Sprintf("%09d", time.Now().UnixNano()%1_000_000_000)
	staffPhone, staffLocal := "+628"+digits, "08"+digits

	t.Run("OwnerSetsStaffPhone", func(t *testing.T) {
		if res := call(enabled, "POST", "/staff/"+cashierID.String()+"/phone", cashier, map[string]string{"phone": staffLocal}); res.Code != http.StatusForbidden {
			t.Fatalf("cashier cannot set phones: %d", res.Code)
		}
		if res := call(enabled, "POST", "/staff/"+cashierID.String()+"/phone", owner, map[string]string{"phone": "12345"}); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid phone must be 422: %d: %s", res.Code, res.Body.String())
		}
		res := call(enabled, "POST", "/staff/"+cashierID.String()+"/phone", owner, map[string]string{"phone": staffLocal})
		if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), staffPhone) {
			t.Fatalf("set phone: %d: %s", res.Code, res.Body.String())
		}
		if res := call(enabled, "POST", "/staff/"+ownerID.String()+"/phone", owner, map[string]string{"phone": staffPhone}); res.Code != http.StatusConflict {
			t.Fatalf("a phone belongs to one staff member: %d", res.Code)
		}
	})

	t.Run("StaffPINResetBySMS", func(t *testing.T) {
		unknown := call(enabled, "POST", "/pin/forgot-sms", "", map[string]string{"store_slug": slug, "phone": "081100000000"})
		known := call(enabled, "POST", "/pin/forgot-sms", "", map[string]string{"store_slug": slug, "phone": staffLocal})
		if unknown.Code != http.StatusAccepted || known.Code != http.StatusAccepted || unknown.Body.String() != known.Body.String() {
			t.Fatalf("forgot-sms must not reveal phones: %d %d", unknown.Code, known.Code)
		}
		if queued("+6281100000000") != 0 || queued(staffPhone) != 1 {
			t.Fatal("only the registered phone gets an SMS")
		}
		call(enabled, "POST", "/pin/forgot-sms", "", map[string]string{"store_slug": slug, "phone": staffLocal})
		if queued(staffPhone) != 1 {
			t.Fatal("a second request within a minute must not send another SMS")
		}
		code := latestCode(staffPhone)
		wrong := call(enabled, "POST", "/pin/reset-sms", "", map[string]string{"store_slug": slug, "phone": staffLocal, "code": "000000", "pin": "905172"})
		if wrong.Code != http.StatusBadRequest || !strings.Contains(wrong.Body.String(), "OTP_INVALID") || code == "000000" {
			t.Fatalf("wrong code: %d: %s", wrong.Code, wrong.Body.String())
		}
		var attempts int
		_ = migratorDB.GetContext(ctx, &attempts, `SELECT attempts FROM otp_challenges WHERE store_id = $1 AND phone = $2`, storeID, staffPhone)
		if attempts != 1 {
			t.Fatalf("a wrong code must spend one attempt, got %d", attempts)
		}
		if res := call(enabled, "POST", "/pin/reset-sms", "", map[string]string{"store_slug": slug, "phone": staffLocal, "code": code, "pin": "123456"}); res.Code != http.StatusBadRequest ||
			!strings.Contains(res.Body.String(), "PIN_REJECTED") {
			t.Fatalf("weak PIN must be rejected before the code is spent: %d: %s", res.Code, res.Body.String())
		}
		res := call(enabled, "POST", "/pin/reset-sms", "", map[string]string{"store_slug": slug, "phone": staffLocal, "code": code, "pin": "905172"})
		if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), cashierEmail) {
			t.Fatalf("reset with code: %d: %s", res.Code, res.Body.String())
		}
		if again := call(enabled, "POST", "/pin/reset-sms", "", map[string]string{"store_slug": slug, "phone": staffLocal, "code": code, "pin": "731904"}); again.Code != http.StatusBadRequest {
			t.Fatalf("a code works once: %d", again.Code)
		}
		if _, status := login(cashierEmail, "482913"); status == http.StatusOK {
			t.Fatal("the old PIN must stop working")
		}
		if _, status := login(cashierEmail, "905172"); status != http.StatusOK {
			t.Fatalf("login with the new PIN: %d", status)
		}
		var audit int
		_ = migratorDB.GetContext(ctx, &audit, `SELECT COUNT(*) FROM audit_logs WHERE store_id = $1 AND action = 'STAFF_PIN_SET' AND metadata->>'purpose' = 'sms_reset'`, storeID)
		if audit != 1 {
			t.Fatalf("one STAFF_PIN_SET audit with purpose sms_reset expected, got %d", audit)
		}
	})

	t.Run("CustomerPhoneVerification", func(t *testing.T) {
		customerPhone := "+628" + fmt.Sprintf("%09d", (time.Now().UnixNano()+7)%1_000_000_000)
		if res := call(enabled, "POST", "/pos/customers/verify", owner, map[string]string{"phone": "bukan nomor"}); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid customer phone must be 422: %d", res.Code)
		}
		res := call(enabled, "POST", "/pos/customers/verify", owner, map[string]string{"phone": customerPhone, "name": "Budi"})
		if res.Code != http.StatusAccepted {
			t.Fatalf("start verification: %d: %s", res.Code, res.Body.String())
		}
		var v service.CustomerVerification
		_ = json.Unmarshal(res.Body.Bytes(), &v)
		if again := call(enabled, "POST", "/pos/customers/verify", owner, map[string]string{"phone": customerPhone}); again.Code != http.StatusTooManyRequests {
			t.Fatalf("second code within a minute must be 429: %d", again.Code)
		}
		path := fmt.Sprintf("/pos/customers/verify/%s/confirm", v.ChallengeID)
		if res := call(enabled, "POST", path, owner, map[string]string{"code": "111111"}); res.Code != http.StatusBadRequest {
			t.Fatalf("wrong code: %d", res.Code)
		}
		res = call(enabled, "POST", path, owner, map[string]string{"code": latestCode(customerPhone)})
		if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"phone_verified_at"`) || !strings.Contains(res.Body.String(), "Budi") {
			t.Fatalf("confirm: %d: %s", res.Code, res.Body.String())
		}
		if res := call(enabled, "POST", "/pos/customers/verify/"+uuid.NewString()+"/confirm", owner, map[string]string{"code": "123456"}); res.Code != http.StatusNotFound {
			t.Fatalf("unknown challenge must be 404: %d", res.Code)
		}
	})

	t.Run("DisabledDriverAnswers503", func(t *testing.T) {
		for _, res := range []*httptest.ResponseRecorder{
			call(disabled, "POST", "/pin/forgot-sms", "", map[string]string{"store_slug": slug, "phone": staffLocal}),
			call(disabled, "POST", "/pos/customers/verify", owner, map[string]string{"phone": "081299999999"}),
		} {
			if res.Code != http.StatusServiceUnavailable || !strings.Contains(res.Body.String(), "SMS_UNAVAILABLE") {
				t.Fatalf("disabled SMS must answer 503 SMS_UNAVAILABLE: %d: %s", res.Code, res.Body.String())
			}
		}
	})
}
