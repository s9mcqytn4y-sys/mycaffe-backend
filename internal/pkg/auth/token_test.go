package auth_test

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/auth"
)

func TestValidateToken(t *testing.T) {
	secretKey := "super_strong_secret_key_for_testing_purposes_only_32_chars"
	storeID := uuid.New().String()
	userID := uuid.New().String()

	// 1. Valid token
	validToken, err := auth.GenerateTestToken(storeID, userID, "cashier", secretKey, 1*time.Hour)
	if err != nil {
		t.Fatalf("failed to generate test token: %v", err)
	}

	claims, err := auth.ValidateToken(validToken, secretKey)
	if err != nil {
		t.Errorf("expected valid token to pass, got error: %v", err)
	}
	if claims.StoreID != storeID {
		t.Errorf("expected store_id %s, got %s", storeID, claims.StoreID)
	}
	if claims.UserID != userID {
		t.Errorf("expected user_id %s, got %s", userID, claims.UserID)
	}
	if claims.Role != "cashier" {
		t.Errorf("expected role cashier, got %s", claims.Role)
	}

	// 2. Empty token
	if _, err := auth.ValidateToken("", secretKey); err == nil {
		t.Errorf("expected error for empty token, got nil")
	}

	// 3. Malformed token
	if _, err := auth.ValidateToken("this.is.not.a.jwt", secretKey); err == nil {
		t.Errorf("expected error for malformed token, got nil")
	}

	// 4. Expired token
	expiredToken, err := auth.GenerateTestToken(storeID, userID, "cashier", secretKey, -1*time.Minute)
	if err != nil {
		t.Fatalf("failed to generate expired test token: %v", err)
	}
	if _, err := auth.ValidateToken(expiredToken, secretKey); err == nil {
		t.Errorf("expected error for expired token, got nil")
	}

	// 5. Wrong secret key signature
	if _, err := auth.ValidateToken(validToken, "wrong_secret_key_that_does_not_match_original_signature"); err == nil {
		t.Errorf("expected error for token validated with wrong secret key, got nil")
	}

	// 6. Missing exp claim
	claimsNoExp := auth.CustomClaims{
		StoreID: storeID,
		UserID:  userID,
		Role:    "barista",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:   auth.ExpectedIssuer,
			Audience: jwt.ClaimStrings{auth.ExpectedAudience},
		},
	}
	tknNoExp, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claimsNoExp).SignedString([]byte(secretKey))
	if _, err := auth.ValidateToken(tknNoExp, secretKey); err == nil {
		t.Errorf("expected error for missing exp claim, got nil")
	}

	// 7. Wrong issuer
	claimsWrongIssuer := auth.CustomClaims{
		StoreID: storeID,
		UserID:  userID,
		Role:    "kitchen",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
			Issuer:    "unknown-rogue-backend",
			Audience:  jwt.ClaimStrings{auth.ExpectedAudience},
		},
	}
	tknWrongIssuer, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claimsWrongIssuer).SignedString([]byte(secretKey))
	if _, err := auth.ValidateToken(tknWrongIssuer, secretKey); err == nil {
		t.Errorf("expected error for wrong issuer, got nil")
	}

	// 8. Wrong audience
	claimsWrongAud := auth.CustomClaims{
		StoreID: storeID,
		UserID:  userID,
		Role:    "kitchen",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
			Issuer:    auth.ExpectedIssuer,
			Audience:  jwt.ClaimStrings{"other-application"},
		},
	}
	tknWrongAud, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claimsWrongAud).SignedString([]byte(secretKey))
	if _, err := auth.ValidateToken(tknWrongAud, secretKey); err == nil {
		t.Errorf("expected error for wrong audience, got nil")
	}

	// 9. Invalid StoreID format
	claimsBadStore := auth.CustomClaims{
		StoreID: "not-a-valid-uuid",
		UserID:  userID,
		Role:    "owner",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
			Issuer:    auth.ExpectedIssuer,
			Audience:  jwt.ClaimStrings{auth.ExpectedAudience},
		},
	}
	tknBadStore, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claimsBadStore).SignedString([]byte(secretKey))
	if _, err := auth.ValidateToken(tknBadStore, secretKey); err == nil {
		t.Errorf("expected error for bad store UUID, got nil")
	}

	// 10. Invalid UserID format
	claimsBadUser := auth.CustomClaims{
		StoreID: storeID,
		UserID:  "invalid-user-format",
		Role:    "owner",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
			Issuer:    auth.ExpectedIssuer,
			Audience:  jwt.ClaimStrings{auth.ExpectedAudience},
		},
	}
	tknBadUser, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claimsBadUser).SignedString([]byte(secretKey))
	if _, err := auth.ValidateToken(tknBadUser, secretKey); err == nil {
		t.Errorf("expected error for bad user UUID, got nil")
	}

	// 11. Invalid Role
	claimsBadRole := auth.CustomClaims{
		StoreID: storeID,
		UserID:  userID,
		Role:    "superadmin_unauthorized",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
			Issuer:    auth.ExpectedIssuer,
			Audience:  jwt.ClaimStrings{auth.ExpectedAudience},
		},
	}
	tknBadRole, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claimsBadRole).SignedString([]byte(secretKey))
	if _, err := auth.ValidateToken(tknBadRole, secretKey); err == nil {
		t.Errorf("expected error for invalid role, got nil")
	}

	// 12. Unsupported Algorithm (HS512 instead of HS256)
	tknWrongAlg, _ := jwt.NewWithClaims(jwt.SigningMethodHS512, auth.CustomClaims{
		StoreID: storeID,
		UserID:  userID,
		Role:    "owner",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
			Issuer:    auth.ExpectedIssuer,
			Audience:  jwt.ClaimStrings{auth.ExpectedAudience},
		},
	}).SignedString([]byte(secretKey))
	if _, err := auth.ValidateToken(tknWrongAlg, secretKey); err == nil {
		t.Errorf("expected error for unsupported algorithm, got nil")
	}
}
