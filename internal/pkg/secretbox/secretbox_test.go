package secretbox_test

import (
	"strings"
	"testing"

	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/secretbox"
)

func TestBoxSealOpenAndTenantAADIsolation(t *testing.T) {
	box, err := secretbox.New(strings.Repeat("b2", 32))
	if err != nil {
		t.Fatalf("unexpected key error: %v", err)
	}

	aadStoreA := "store:11111111-1111-1111-1111-111111111111:midtrans_server_key"
	aadStoreB := "store:22222222-2222-2222-8222-222222222222:midtrans_server_key"
	secret := "SB-Mid-server-TestDummyFixture0001"

	ct, err := box.Seal(secret, aadStoreA)
	if err != nil {
		t.Fatalf("seal failed: %v", err)
	}

	got, err := box.Open(ct, aadStoreA)
	if err != nil || got != secret {
		t.Fatalf("open failed: got=%q err=%v", got, err)
	}

	// Cross-tenant ciphertext transplant MUST fail authentication
	if _, err := box.Open(ct, aadStoreB); err == nil {
		t.Fatal("expected AAD mismatch error when opening Store A ciphertext under Store B context")
	}

	masked := secretbox.MaskSecret(secret)
	if masked != "SB-Mid****0001" {
		t.Fatalf("unexpected mask format: %q", masked)
	}
}

func TestFailClosedInProductionWhenKeyEmpty(t *testing.T) {
	if _, err := secretbox.NewDevFallback("", "production"); err == nil {
		t.Fatal("expected production empty key to fail-closed")
	}
	if _, err := secretbox.NewDevFallback("", "staging"); err == nil {
		t.Fatal("expected staging empty key to fail-closed")
	}
	if _, err := secretbox.NewDevFallback("", "development"); err != nil {
		t.Fatalf("expected dev fallback to succeed in development: %v", err)
	}
}
