package config_test

import (
	"os"
	"testing"

	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
)

func TestValidateConfigProductionFailClosed(t *testing.T) {
	// Unknown APP_ENV must fail
	cfgUnknown := &config.Config{
		AppEnv: "uat_environment_unsupported",
	}
	if err := config.ValidateConfig(cfgUnknown); err == nil {
		t.Errorf("expected ValidateConfig to fail on unknown APP_ENV, got nil")
	}

	// Normalized variations of production must pass environment check and enforce production security
	cfgVariations := []string{"Production", "  production  ", "PRODUCTION"}
	for _, envVar := range cfgVariations {
		cfg := &config.Config{
			AppEnv:         envVar,
			JWTSecret:      "weak_key",
			DatabaseURL:    "postgresql://postgres:pass@remote.db.com:5432/db",
			AllowedOrigins: []string{"https://mycaffe.com"},
		}
		if err := config.ValidateConfig(cfg); err == nil {
			t.Errorf("expected ValidateConfig to fail for weak JWT in %q environment, got nil", envVar)
		}
	}

	// Weak JWT Secret in production must fail
	cfg := &config.Config{
		AppEnv:         "production",
		JWTSecret:      "some_insecure_key_change_me_later_please",
		DatabaseURL:    "postgresql://postgres:pass@remote.db.com:5432/db",
		AllowedOrigins: []string{"https://mycaffe.com"},
	}

	if err := config.ValidateConfig(cfg); err == nil {
		t.Errorf("expected ValidateConfig to fail for change_me JWT secret in production, got nil")
	}

	// Empty DatabaseURL in production must fail
	cfg.JWTSecret = "strong_production_secret_key_that_is_at_least_32_characters_long"
	cfg.DatabaseURL = ""
	if err := config.ValidateConfig(cfg); err == nil {
		t.Errorf("expected ValidateConfig to fail for empty DatabaseURL in production, got nil")
	}

	// MidtransIsProduction=false in production must fail
	cfg.DatabaseURL = "postgresql://postgres:pass@remote.db.com:5432/db"
	cfg.MidtransIsProduction = false
	cfg.MidtransServerKey = "Mid-server-production-valid-key-999"
	if err := config.ValidateConfig(cfg); err == nil {
		t.Errorf("expected ValidateConfig to fail when MidtransIsProduction is false in production, got nil")
	}

	// Midtrans sandbox key with MidtransIsProduction=true must fail
	cfg.MidtransIsProduction = true
	cfg.MidtransServerKey = "SB-Mid-server-xxxx"
	if err := config.ValidateConfig(cfg); err == nil {
		t.Errorf("expected ValidateConfig to fail for sandbox key when MidtransIsProduction is true, got nil")
	}

	// Empty Midtrans server key in production must fail
	cfg.MidtransServerKey = ""
	if err := config.ValidateConfig(cfg); err == nil {
		t.Errorf("expected ValidateConfig to fail for empty Midtrans server key in production, got nil")
	}

	// Wildcard '*' origin in production must fail
	cfg.MidtransServerKey = "Mid-server-production-valid-key-999"
	cfg.AllowedOrigins = []string{"*"}
	if err := config.ValidateConfig(cfg); err == nil {
		t.Errorf("expected ValidateConfig to fail for wildcard '*' origin in production, got nil")
	}

	// Localhost origin in production must fail
	cfg.AllowedOrigins = []string{"http://localhost:3000"}
	if err := config.ValidateConfig(cfg); err == nil {
		t.Errorf("expected ValidateConfig to fail for localhost origin in production, got nil")
	}

	// Valid production configuration must pass
	cfg.AllowedOrigins = []string{"https://app.mycaffe.com", "https://pos.mycaffe.com"}
	if err := config.ValidateConfig(cfg); err != nil {
		t.Errorf("expected valid production config to pass, got error: %v", err)
	}
}

func TestLoadConfigStrictParsing(t *testing.T) {
	// Preserve original env
	origEnv, hasEnv := os.LookupEnv("APP_ENV")
	defer func() {
		if hasEnv {
			os.Setenv("APP_ENV", origEnv)
		} else {
			os.Unsetenv("APP_ENV")
		}
	}()

	// 1. Unset APP_ENV must fail
	os.Unsetenv("APP_ENV")
	if _, err := config.LoadConfig(); err == nil {
		t.Errorf("expected LoadConfig to fail when APP_ENV is unset, got nil")
	}

	// 2. Empty APP_ENV must fail
	os.Setenv("APP_ENV", "")
	if _, err := config.LoadConfig(); err == nil {
		t.Errorf("expected LoadConfig to fail when APP_ENV is empty, got nil")
	}

	// 3. Whitespace-only APP_ENV must fail
	os.Setenv("APP_ENV", "   ")
	if _, err := config.LoadConfig(); err == nil {
		t.Errorf("expected LoadConfig to fail when APP_ENV is whitespace only, got nil")
	}

	// 4. Invalid APP_ENV enum must fail
	os.Setenv("APP_ENV", "custom_sandbox_env")
	if _, err := config.LoadConfig(); err == nil {
		t.Errorf("expected LoadConfig to fail when APP_ENV is unknown enum, got nil")
	}

	// Set valid APP_ENV for subsequent strict parsing tests
	os.Setenv("APP_ENV", "development")

	// Invalid MIDTRANS_IS_PRODUCTION boolean must return error
	os.Setenv("MIDTRANS_IS_PRODUCTION", "not_a_boolean")
	defer os.Unsetenv("MIDTRANS_IS_PRODUCTION")

	_, err := config.LoadConfig()
	if err == nil {
		t.Errorf("expected LoadConfig to fail on invalid boolean string, got nil")
	}

	// Invalid JWT_EXPIRED_HOURS integer must return error
	os.Setenv("MIDTRANS_IS_PRODUCTION", "false")
	os.Setenv("JWT_EXPIRED_HOURS", "-5")
	defer os.Unsetenv("JWT_EXPIRED_HOURS")

	_, err = config.LoadConfig()
	if err == nil {
		t.Errorf("expected LoadConfig to fail on negative JWT_EXPIRED_HOURS, got nil")
	}
}

func TestIsOriginAllowed(t *testing.T) {
	cfg := &config.Config{
		AllowedOrigins: []string{"http://localhost:3000", "https://app.mycaffe.com"},
	}

	if !cfg.IsOriginAllowed("http://localhost:3000") {
		t.Errorf("expected localhost:3000 to be allowed")
	}

	if !cfg.IsOriginAllowed("https://app.mycaffe.com") {
		t.Errorf("expected app.mycaffe.com to be allowed")
	}

	if cfg.IsOriginAllowed("https://malicious-site.com") {
		t.Errorf("expected malicious-site.com to be blocked")
	}

	if cfg.IsOriginAllowed("") {
		t.Errorf("expected empty origin to be blocked")
	}
}
