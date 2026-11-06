package auth_test

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/auth"
)

func TestValidatePINPolicy(t *testing.T) {
	cases := map[string]error{
		"482913":     nil,
		"905172":     nil,
		"9037451268": auth.ErrPINFormat,
		"12345":      auth.ErrPINFormat,
		"1234567":    auth.ErrPINFormat,
		"48a913":     auth.ErrPINFormat,
		"000000":     auth.ErrPINTooWeak,
		"123456":     auth.ErrPINTooWeak,
		"654321":     auth.ErrPINTooWeak,
		"789012":     auth.ErrPINTooWeak,
		"121212":     auth.ErrPINTooWeak,
		"198519":     auth.ErrPINTooWeak,
	}
	for pin, want := range cases {
		if got := auth.ValidatePINPolicy(pin); got != want {
			t.Errorf("ValidatePINPolicy(%q) = %v, want %v", pin, got, want)
		}
	}
}

func TestHashAndVerifyPIN(t *testing.T) {
	hash, err := auth.HashPIN("482913")
	if err != nil {
		t.Fatalf("HashPIN: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("unexpected PHC prefix: %s", hash)
	}
	other, _ := auth.HashPIN("482913")
	if other == hash {
		t.Fatal("two hashes of the same PIN must use different salts")
	}
	if ok, err := auth.VerifyPIN("482913", hash); err != nil || !ok {
		t.Fatalf("correct PIN rejected: ok=%v err=%v", ok, err)
	}
	if ok, _ := auth.VerifyPIN("482914", hash); ok {
		t.Fatal("wrong PIN accepted")
	}
	for _, bad := range []string{"", "$2a$10$bcrypt", "$argon2id$v=19$m=999999999,t=2,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA"} {
		if _, err := auth.VerifyPIN("482913", bad); err != auth.ErrPINHashFormat {
			t.Errorf("VerifyPIN(%q) err = %v, want ErrPINHashFormat", bad, err)
		}
	}
}

func TestStaffAccessTokenRoundTrip(t *testing.T) {
	secret := "unit-test-secret-key-with-at-least-32-chars"
	storeID, userID, sessionID := uuid.New(), uuid.New(), uuid.New()
	now := time.Now()

	token, exp, err := auth.IssueStaffAccessToken(secret, storeID, userID, sessionID, "cashier", now, 15*time.Minute)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if !exp.Equal(now.Add(15 * time.Minute)) {
		t.Fatalf("unexpected expiry %v", exp)
	}
	claims, err := auth.ValidateStaffAccessToken(token, secret)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if claims.Subject != userID.String() || claims.StoreID != storeID.String() || claims.SessionID != sessionID.String() || claims.Role != "cashier" {
		t.Fatalf("claims mismatch: %+v", claims)
	}

	if _, err := auth.ValidateStaffAccessToken(token, secret+"x"); err == nil {
		t.Fatal("token verified with the wrong secret")
	}
	expired, _, _ := auth.IssueStaffAccessToken(secret, storeID, userID, sessionID, "cashier", now.Add(-time.Hour), 15*time.Minute)
	if _, err := auth.ValidateStaffAccessToken(expired, secret); err != auth.ErrTokenExpired {
		t.Fatalf("expired token err = %v", err)
	}

	// Legacy test tokens (no typ/sid) must not pass as staff access tokens.
	legacy, _ := auth.GenerateTestToken(storeID.String(), userID.String(), "owner", secret, time.Hour)
	if _, err := auth.ValidateStaffAccessToken(legacy, secret); err == nil {
		t.Fatal("legacy token without kid/typ accepted as staff access token")
	}

	// A correctly signed token with another purpose must be rejected.
	guest := jwt.NewWithClaims(jwt.SigningMethodHS256, auth.StaffClaims{
		Type: "guest_session", StoreID: storeID.String(), Role: "cashier", SessionID: sessionID.String(),
		RegisteredClaims: jwt.RegisteredClaims{
			Subject: userID.String(), Issuer: auth.ExpectedIssuer, Audience: jwt.ClaimStrings{auth.ExpectedAudience},
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
	})
	guest.Header["kid"] = "k1"
	guestToken, _ := guest.SignedString([]byte(secret))
	if _, err := auth.ValidateStaffAccessToken(guestToken, secret); err != auth.ErrTokenTypeInvalid {
		t.Fatalf("guest-typed token err = %v, want ErrTokenTypeInvalid", err)
	}
}

func TestOfflinePINVerifier(t *testing.T) {
	v1, err := auth.OfflinePINVerifier("482913")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(v1, "$argon2id$v=19$m=65536,t=3,p=1$") {
		t.Fatalf("verifier must carry the W19 parameters, got %q", v1)
	}
	if ok, err := auth.VerifyPIN("482913", v1); err != nil || !ok {
		t.Fatalf("verifier must verify its PIN: ok=%v err=%v", ok, err)
	}
	if ok, _ := auth.VerifyPIN("482914", v1); ok {
		t.Fatal("verifier must reject another PIN")
	}
	if v2, _ := auth.OfflinePINVerifier("482913"); v2 == v1 {
		t.Fatal("each issue must use a fresh salt")
	}
}
