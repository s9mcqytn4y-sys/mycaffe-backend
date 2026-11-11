package service

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
)

func TestValidateStoreSettingsQRServiceMode(t *testing.T) {
	s := domain.DefaultStoreSettings(uuid.New())
	if s.QRServiceMode != domain.QRServicePickup {
		t.Fatalf("default service mode must be pickup, got %q", s.QRServiceMode)
	}
	for _, mode := range []string{domain.QRServicePickup, domain.QRServiceTable} {
		s.QRServiceMode = mode
		if err := validateStoreSettings(s); err != nil {
			t.Fatalf("%q must be valid: %v", mode, err)
		}
	}
	for _, mode := range []string{"", "delivery", "TABLE"} {
		s.QRServiceMode = mode
		if err := validateStoreSettings(s); !errors.Is(err, ErrInvalidSettings) {
			t.Fatalf("%q must be refused, got %v", mode, err)
		}
	}
}
