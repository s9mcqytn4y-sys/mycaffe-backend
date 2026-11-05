package config_test

import (
	"os"
	"strings"
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

	// Placeholder DatabaseURL in production must fail
	cfg.AllowedOrigins = []string{"https://app.mycaffe.com"}
	cfg.DatabaseURL = "postgres://mycaffe_app:change_me_in_production@postgres:5432/mycaffe_pos"
	if err := config.ValidateConfig(cfg); err == nil {
		t.Errorf("expected ValidateConfig to fail for placeholder DatabaseURL in production, got nil")
	}

	// Placeholder MidtransServerKey in production must fail
	cfg.DatabaseURL = "postgresql://mycaffe_app:strong_prod_db_pass_99@postgres:5432/mycaffe_pos"
	cfg.MidtransServerKey = "Mid-server-placeholder-key"
	if err := config.ValidateConfig(cfg); err == nil {
		t.Errorf("expected ValidateConfig to fail for placeholder MidtransServerKey in production, got nil")
	}

	// Valid production configuration must pass (with a full SMTP mailer)
	cfg.EmailDriver, cfg.SMTPHost, cfg.SMTPUsername, cfg.SMTPPassword = "smtp", "smtp.hostinger.com", "noreply@mycaffe.com", "smtp-mailbox-secret"
	cfg.EmailFromEmail, cfg.SMTPTLSMode, cfg.AppPublicURL = "noreply@mycaffe.com", "implicit", "https://pos.mycaffe.com"
	cfg.MidtransServerKey = "Mid-server-production-valid-key-999"
	cfg.AllowedOrigins = []string{"https://app.mycaffe.com", "https://pos.mycaffe.com"}
	if err := config.ValidateConfig(cfg); err != nil {
		t.Errorf("expected valid production config to pass, got error: %v", err)
	}

	// The log mailer prints link secrets and must be rejected in an otherwise valid production config.
	cfg.EmailDriver = "log"
	if err := config.ValidateConfig(cfg); err == nil || !strings.Contains(err.Error(), "EMAIL_DRIVER") {
		t.Errorf("expected EMAIL_DRIVER=log to fail closed in production, got %v", err)
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

func TestValidateMigrateConfigLeastPrivilege(t *testing.T) {
	// Migrate config in production must succeed WITHOUT JWT_SECRET_KEY or MIDTRANS_SERVER_KEY
	cfg := &config.Config{
		AppEnv:               "production",
		DatabaseMigrationURL: "postgres://mycaffe_migrator:strong_prod_mig_pass_99@postgres:5432/mycaffe_pos?sslmode=disable",
		DBAppUser:            "mycaffe_app",
		DBAppPassword:        "strong_prod_app_pass_99",
	}
	if err := config.ValidateMigrateConfig(cfg); err != nil {
		t.Fatalf("expected ValidateMigrateConfig to succeed without JWT/Midtrans secrets, got: %v", err)
	}

	// Placeholder migration password in production must fail
	cfgPlaceholder := &config.Config{
		AppEnv:               "production",
		DatabaseMigrationURL: "postgres://mycaffe_migrator:change_me_in_production@postgres:5432/mycaffe_pos",
	}
	if err := config.ValidateMigrateConfig(cfgPlaceholder); err == nil {
		t.Fatal("expected ValidateMigrateConfig to reject placeholder DSN in production")
	}
}

func TestLoadConfigTrustedProxies(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("TRUSTED_PROXIES", " 172.18.0.0/16 , ,10.0.0.5")

	cfg, err := config.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if len(cfg.TrustedProxies) != 2 || cfg.TrustedProxies[0] != "172.18.0.0/16" || cfg.TrustedProxies[1] != "10.0.0.5" {
		t.Fatalf("unexpected TrustedProxies: %q", cfg.TrustedProxies)
	}

	t.Setenv("TRUSTED_PROXIES", "")
	cfg, err = config.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if len(cfg.TrustedProxies) != 0 {
		t.Fatalf("expected no trusted proxies by default, got %q", cfg.TrustedProxies)
	}
}

func TestLoadConfigEmailDriver(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	cfg, err := config.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.EmailDriver != "log" {
		t.Fatalf("development must default to the log driver, got %q", cfg.EmailDriver)
	}

	// Hostinger: port 465 implies implicit TLS.
	t.Setenv("EMAIL_DRIVER", "smtp")
	t.Setenv("SMTP_HOST", "smtp.hostinger.com")
	t.Setenv("SMTP_PORT", "465")
	t.Setenv("SMTP_USERNAME", "noreply@kafe.test")
	t.Setenv("SMTP_PASSWORD", "mailbox-secret")
	t.Setenv("APP_PUBLIC_URL", "https://pos.kafe.test/")
	cfg, err = config.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig smtp: %v", err)
	}
	if cfg.SMTPTLSMode != "implicit" || cfg.EmailFromEmail != "noreply@kafe.test" || cfg.AppPublicURL != "https://pos.kafe.test" {
		t.Fatalf("unexpected smtp config: mode=%s from=%s url=%s", cfg.SMTPTLSMode, cfg.EmailFromEmail, cfg.AppPublicURL)
	}

	t.Setenv("SMTP_PASSWORD", "")
	if _, err := config.LoadConfig(); err == nil {
		t.Fatal("smtp driver without a password must fail closed")
	}

}

// L-2: the staging demo is reachable through a public tunnel, so it needs a real JWT secret too.
func TestValidateConfigStagingRequiresStrongJWTSecret(t *testing.T) {
	for _, secret := range []string{
		"",
		"short-secret",
		"demo-only-jwt-secret-key-minimum-32-characters",
		"replace-with-a-very-long-random-secret-key-min-32-chars",
	} {
		if err := config.ValidateConfig(&config.Config{AppEnv: "staging", JWTSecret: secret}); err == nil {
			t.Errorf("staging accepted JWT secret %q", secret)
		}
	}
	if err := config.ValidateConfig(&config.Config{AppEnv: "staging", JWTSecret: strings.Repeat("strong", 6), EmailDriver: "log"}); err != nil {
		t.Errorf("staging rejected a strong secret: %v", err)
	}
}

// V4/V5: the log driver prints OTP codes, so only development and test may use it.
// email and webhook drivers are allowed in staging/production when properly configured.
func TestValidateConfigWhatsAppDriver(t *testing.T) {
	strong := strings.Repeat("strong", 6)
	token := strings.Repeat("t", 32)
	base := func(env string) *config.Config {
		return &config.Config{AppEnv: env, JWTSecret: strong, EmailDriver: "log"}
	}
	c := base("staging")
	c.WhatsAppDriver, c.WhatsAppBridgeURL, c.WhatsAppBridgeToken = "bridge", "http://wa-bridge:8090", token
	if err := config.ValidateConfig(c); err != nil {
		t.Errorf("staging with the bridge should pass: %v", err)
	}
	c.WhatsAppBridgeToken = "short"
	if err := config.ValidateConfig(c); err == nil {
		t.Error("a short bridge token must be refused")
	}
	c = base("staging")
	c.WhatsAppDriver = "cloud"
	if err := config.ValidateConfig(c); err == nil {
		t.Error("cloud without token must be refused")
	}
	c.WhatsAppDriver = "carrier-pigeon"
	if err := config.ValidateConfig(c); err == nil {
		t.Error("unknown driver must be refused")
	}
}

func TestValidateConfigSMSDriver(t *testing.T) {
	strong := strings.Repeat("strong", 6)
	if err := config.ValidateConfig(&config.Config{AppEnv: "staging", JWTSecret: strong, EmailDriver: "log", SMSDriver: "log"}); err == nil {
		t.Error("staging must refuse SMS_DRIVER=log")
	}
	if err := config.ValidateConfig(&config.Config{AppEnv: "staging", JWTSecret: strong, EmailDriver: "log", SMSDriver: "disabled"}); err != nil {
		t.Errorf("staging with SMS disabled: %v", err)
	}
	if err := config.ValidateConfig(&config.Config{
		AppEnv:         "staging",
		JWTSecret:      strong,
		EmailDriver:    "smtp",
		SMTPHost:       "smtp.gmail.com",
		SMTPUsername:   "owner@abbdullaziz.online",
		SMTPPassword:   "app-pass",
		EmailFromEmail: "owner@abbdullaziz.online",
		SMTPTLSMode:    "starttls",
		AppPublicURL:   "https://demo.abbdullaziz.online",
		SMSDriver:      "email",
		SMSEmailTo:     "owner@abbdullaziz.online",
	}); err != nil {
		t.Errorf("staging with SMS_DRIVER=email should pass: %v", err)
	}
	if err := config.ValidateConfig(&config.Config{
		AppEnv:        "staging",
		JWTSecret:     strong,
		EmailDriver:   "log",
		SMSDriver:     "webhook",
		SMSWebhookURL: "https://gateway.abbdullaziz.online/sms",
	}); err != nil {
		t.Errorf("staging with SMS_DRIVER=webhook should pass: %v", err)
	}
	if err := config.ValidateConfig(&config.Config{AppEnv: "development", EmailDriver: "log", SMSDriver: "carrier-pigeon"}); err == nil {
		t.Error("unknown SMS driver must be refused")
	}
}
