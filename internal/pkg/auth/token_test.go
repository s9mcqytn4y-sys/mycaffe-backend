package auth_test

import (
	"testing"
	"time"

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

	// 2. Empty token
	_, err = auth.ValidateToken("", secretKey)
	if err == nil {
		t.Errorf("expected error for empty token, got nil")
	}

	// 3. Malformed token
	_, err = auth.ValidateToken("this.is.not.a.jwt", secretKey)
	if err == nil {
		t.Errorf("expected error for malformed token, got nil")
	}

	// 4. Expired token
	expiredToken, err := auth.GenerateTestToken(storeID, userID, "cashier", secretKey, -1*time.Minute)
	if err != nil {
		t.Fatalf("failed to generate expired test token: %v", err)
	}
	_, err = auth.ValidateToken(expiredToken, secretKey)
	if err == nil {
		t.Errorf("expected error for expired token, got nil")
	}

	// 5. Wrong secret key signature
	_, err = auth.ValidateToken(validToken, "wrong_secret_key_that_does_not_match_original_signature")
	if err == nil {
		t.Errorf("expected error for token validated with wrong secret key, got nil")
	}
}
