package qr_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/qr"
)

func TestTokenResolver_Roundtrip(t *testing.T) {
	resolver, err := qr.NewTokenResolver("my-very-secure-hmac-secret-123456")
	if err != nil {
		t.Fatalf("unexpected error creating resolver: %v", err)
	}

	storeID := uuid.New()
	tableID := uuid.New()
	token, err := resolver.GenerateSignedToken(storeID, tableID, "Meja 07", 1*time.Hour)
	if err != nil {
		t.Fatalf("unexpected error generating token: %v", err)
	}

	decoded, err := resolver.VerifyAndDecode(token)
	if err != nil {
		t.Fatalf("unexpected error decoding token: %v", err)
	}

	if decoded.StoreID != storeID {
		t.Errorf("expected store ID %v, got %v", storeID, decoded.StoreID)
	}
	if decoded.TableID != tableID {
		t.Errorf("expected table ID %v, got %v", tableID, decoded.TableID)
	}
	if decoded.TableNo != "Meja 07" {
		t.Errorf("expected table no 'Meja 07', got %s", decoded.TableNo)
	}
}

func TestTokenResolver_TamperedSignature(t *testing.T) {
	resolver, err := qr.NewTokenResolver("my-very-secure-hmac-secret-123456")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	token, _ := resolver.GenerateSignedToken(uuid.New(), uuid.New(), "Meja 01", 1*time.Hour)
	tampered := token + "evil"

	_, err = resolver.VerifyAndDecode(tampered)
	if !errors.Is(err, qr.ErrSignatureMismatch) {
		t.Errorf("expected ErrSignatureMismatch, got: %v", err)
	}
}

func TestTokenResolver_Expired(t *testing.T) {
	resolver, _ := qr.NewTokenResolver("my-very-secure-hmac-secret-123456")
	token, _ := resolver.GenerateSignedToken(uuid.New(), uuid.New(), "Meja 01", -1*time.Minute)

	_, err := resolver.VerifyAndDecode(token)
	if !errors.Is(err, qr.ErrExpiredQRToken) {
		t.Errorf("expected ErrExpiredQRToken, got: %v", err)
	}
}
