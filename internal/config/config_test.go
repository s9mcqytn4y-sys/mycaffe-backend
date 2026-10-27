package config_test

import (
	"testing"

	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
)

func TestValidateConfigProductionFailClosed(t *testing.T) {
	// Weak JWT Secret in production must fail
	cfg := &config.Config{
		AppEnv:         "production",
		JWTSecret:      "default_secret_key_change_me_in_production",
		DatabaseURL:    "postgresql://postgres:pass@localhost:5432/db",
		AllowedOrigins: []string{"https://mycaffe.com"},
	}

	if err := config.ValidateConfig(cfg); err == nil {
		t.Errorf("expected ValidateConfig to fail for weak JWT secret in production, got nil")
	}

	// Empty DatabaseURL in production must fail
	cfg.JWTSecret = "strong_production_secret_key_that_is_at_least_32_characters_long"
	cfg.DatabaseURL = ""
	if err := config.ValidateConfig(cfg); err == nil {
		t.Errorf("expected ValidateConfig to fail for empty DatabaseURL in production, got nil")
	}

	// Midtrans sandbox key with MidtransIsProduction=true must fail
	cfg.DatabaseURL = "postgresql://postgres:pass@localhost:5432/db"
	cfg.MidtransIsProduction = true
	cfg.MidtransServerKey = "SB-Mid-server-xxxx"
	if err := config.ValidateConfig(cfg); err == nil {
		t.Errorf("expected ValidateConfig to fail for sandbox key when MidtransIsProduction is true, got nil")
	}

	// Valid production configuration must pass
	cfg.MidtransServerKey = "Mid-server-production-valid-key-999"
	if err := config.ValidateConfig(cfg); err != nil {
		t.Errorf("expected valid production config to pass, got error: %v", err)
	}
}

func TestIsOriginAllowed(t *testing.T) {
	cfg := &config.Config{
		AllowedOrigins: []string{"http://localhost:3000", "https://app.kafesenja.com"},
	}

	if !cfg.IsOriginAllowed("http://localhost:3000") {
		t.Errorf("expected localhost:3000 to be allowed")
	}

	if !cfg.IsOriginAllowed("https://app.kafesenja.com") {
		t.Errorf("expected app.kafesenja.com to be allowed")
	}

	if cfg.IsOriginAllowed("https://malicious-site.com") {
		t.Errorf("expected malicious-site.com to be blocked")
	}

	if cfg.IsOriginAllowed("") {
		t.Errorf("expected empty origin to be blocked")
	}
}
