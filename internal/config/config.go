package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	AppEnv               string
	Port                 string
	MetricsAddr          string
	FrontendURL          string
	AllowedOrigins       []string
	TrustedProxies       []string
	DatabaseURL          string
	DatabaseMigrationURL string
	DBAppUser            string
	DBAppPassword        string
	JWTSecret            string
	JWTExpiryHours       int

	// Staff authentication (ADR-003 D3/D4, owner decision O3)
	StaffAccessTokenTTL     time.Duration
	StaffRefreshTokenTTL    time.Duration
	StaffSessionIdleTimeout time.Duration
	StaffMaxSessions        int
	StaffPINMaxFailures     int
	StaffPINLockout         time.Duration

	// Email (owner decision E5): "log" writes emails to the server log (dev/test only),
	// "smtp" delivers through SMTP (Gmail app password for dev/demo, Hostinger for production),
	// "disabled" turns off email delivery outside production.
	EmailDriver string
	// SMSDriver is "log" (development/test only: OTPs are secrets), "email" (free SMS-to-Email SMTP bridge),
	// "webhook" (HTTP POST gateway for Telegram/WhatsApp/Fonnte), or "disabled" (V4, V5).
	SMSDriver       string
	SMSEmailTo      string
	SMSWebhookURL   string
	SMSWebhookToken string
	// WhatsApp Cloud API receipts (decision W9); off until the access token and phone number id are set.
	WhatsAppAPIVersion       string
	WhatsAppPhoneNumberID    string
	WhatsAppAccessToken      string
	WhatsAppReceiptTemplate  string
	WhatsAppTemplateLanguage string
	// WhatsAppDriver is "cloud" (Cloud API), "bridge" (cmd/wa-bridge via whatsmeow, demo/development only,
	// decision W12) or "disabled"; empty picks cloud when the Cloud API token is set.
	WhatsAppDriver string
	// GuestSplitEnabled exposes per-person payment links on QR orders (ADR-010).
	GuestSplitEnabled   bool
	WhatsAppBridgeURL   string
	WhatsAppBridgeToken string
	SMTPHost            string
	SMTPPort            int
	SMTPUsername        string
	SMTPPassword        string
	SMTPTLSMode         string // "starttls" (587) or "implicit" (465)
	EmailFromEmail      string
	EmailFromName       string
	// AppPublicURL is the frontend origin used in links inside emails.
	AppPublicURL string

	// DefaultStoreSlug lets a single-cafe deployment omit the store code on login and forgot-PIN
	// (owner decision Q3, single-tenant MVP). Multi-branch deployments leave it empty.
	DefaultStoreSlug string

	// Midtrans Core API (Default Store / Tenant Fallback)
	MidtransMerchantID   string
	MidtransServerKey    string
	MidtransClientKey    string
	MidtransIsProduction bool

	// Multi-Tenant SaaS Governance & Dual-Lane Billing (ADR-009)
	// CredentialEncryptionKey seals tenant BYOK Midtrans, SMTP, and WhatsApp secrets via AES-256-GCM.
	CredentialEncryptionKey string
	// PlatformAdminAPIKey guards /api/v1/platform/* Superadmin endpoints (fails closed 503 if empty).
	PlatformAdminAPIKey string
	// PlatformMidtrans* powers Lane 1 (AzureDevID SaaS Subscription Invoices), isolated from tenant F&B funds.
	PlatformMidtransServerKey    string
	PlatformMidtransClientKey    string
	PlatformMidtransIsProduction bool

	// Cloudflare R2 / Storage
	StorageDriver       string
	R2AccountID         string
	R2AccessKeyID       string
	R2SecretAccessKey   string
	R2BucketName        string
	R2PrivateBucketName string
	R2PublicURL         string

	// Sentry
	SentryDSN         string
	SentryEnvironment string
}

func LoadConfig() (*Config, error) {
	// Load .env if present (ignored in production if injected via container/host)
	_ = godotenv.Load()

	// Strict boolean parsing for MIDTRANS_IS_PRODUCTION
	midtransProdStr := getEnv("MIDTRANS_IS_PRODUCTION", "false")
	isProd, err := strconv.ParseBool(midtransProdStr)
	if err != nil {
		return nil, fmt.Errorf("invalid boolean for MIDTRANS_IS_PRODUCTION: %w", err)
	}

	platformMidtransProdStr := getEnv("PLATFORM_MIDTRANS_IS_PRODUCTION", "false")
	platformIsProd, err := strconv.ParseBool(platformMidtransProdStr)
	if err != nil {
		return nil, fmt.Errorf("invalid boolean for PLATFORM_MIDTRANS_IS_PRODUCTION: %w", err)
	}

	// Strict integer parsing for JWT_EXPIRED_HOURS
	jwtHoursStr := getEnv("JWT_EXPIRED_HOURS", "24")
	jwtHours, err := strconv.Atoi(jwtHoursStr)
	if err != nil || jwtHours <= 0 {
		return nil, fmt.Errorf("invalid positive integer for JWT_EXPIRED_HOURS: %w", err)
	}

	// Explicitly require APP_ENV (reject unset and empty values, normalize case/whitespace)
	rawAppEnv, exists := os.LookupEnv("APP_ENV")
	if !exists {
		return nil, errors.New("APP_ENV is not set: environment must be explicitly defined (development, test, staging, production)")
	}
	appEnv := strings.ToLower(strings.TrimSpace(rawAppEnv))
	if appEnv == "" {
		return nil, errors.New("APP_ENV cannot be empty: environment must be explicitly defined (development, test, staging, production)")
	}

	var parseErrs []error
	duration := func(key, def string) time.Duration {
		d, err := time.ParseDuration(getEnv(key, def))
		if err != nil || d <= 0 {
			parseErrs = append(parseErrs, fmt.Errorf("invalid positive duration for %s", key))
		}
		return d
	}
	positiveInt := func(key, def string) int {
		n, err := strconv.Atoi(getEnv(key, def))
		if err != nil || n <= 0 {
			parseErrs = append(parseErrs, fmt.Errorf("invalid positive integer for %s", key))
		}
		return n
	}
	staffAccessTTL := duration("STAFF_ACCESS_TOKEN_TTL", "15m")
	staffRefreshTTL := duration("STAFF_REFRESH_TOKEN_TTL", "12h")
	staffIdle := duration("STAFF_SESSION_IDLE_TIMEOUT", "2h")
	staffMaxSessions := positiveInt("STAFF_MAX_SESSIONS_PER_USER", "3")
	staffPINMaxFailures := positiveInt("STAFF_PIN_MAX_FAILURES", "5")
	staffPINLockout := duration("STAFF_PIN_LOCKOUT", "15m")
	smtpPort := positiveInt("SMTP_PORT", "587")
	defaultEmailDriver := "smtp"
	if appEnv == "development" || appEnv == "test" {
		defaultEmailDriver = "log"
	}
	emailDriver := strings.ToLower(strings.TrimSpace(getEnv("EMAIL_DRIVER", defaultEmailDriver)))
	defaultSMSDriver := "disabled"
	if appEnv == "development" || appEnv == "test" {
		defaultSMSDriver = "log"
	}
	smsDriver := strings.ToLower(strings.TrimSpace(getEnv("SMS_DRIVER", defaultSMSDriver)))
	smtpTLSMode := strings.ToLower(strings.TrimSpace(getEnv("SMTP_TLS_MODE", "")))
	if smtpTLSMode == "" {
		smtpTLSMode = "starttls"
		if smtpPort == 465 {
			smtpTLSMode = "implicit"
		}
	}
	if err := errors.Join(parseErrs...); err != nil {
		return nil, err
	}

	frontendURL := getEnv("FRONTEND_URL", "http://localhost:3000")

	// Strict origin parsing
	allowedOrigins := splitCSV(getEnv("ALLOWED_ORIGINS", ""))
	if len(allowedOrigins) == 0 && appEnv != "production" {
		allowedOrigins = append(allowedOrigins, frontendURL)
	}

	cfg := &Config{
		AppEnv:                       appEnv,
		Port:                         getEnv("PORT", "8080"),
		MetricsAddr:                  getEnv("METRICS_ADDR", "127.0.0.1:9091"),
		FrontendURL:                  frontendURL,
		AllowedOrigins:               allowedOrigins,
		TrustedProxies:               splitCSV(getEnv("TRUSTED_PROXIES", "")),
		DatabaseURL:                  getEnv("DATABASE_URL", ""),
		DatabaseMigrationURL:         getEnv("DATABASE_MIGRATION_URL", getEnv("DATABASE_URL", "")),
		DBAppUser:                    getEnv("DB_APP_USER", "mycaffe_app"),
		DBAppPassword:                getEnv("DB_APP_PASSWORD", ""),
		JWTSecret:                    getEnv("JWT_SECRET_KEY", ""),
		JWTExpiryHours:               jwtHours,
		StaffAccessTokenTTL:          staffAccessTTL,
		StaffRefreshTokenTTL:         staffRefreshTTL,
		StaffSessionIdleTimeout:      staffIdle,
		StaffMaxSessions:             staffMaxSessions,
		StaffPINMaxFailures:          staffPINMaxFailures,
		StaffPINLockout:              staffPINLockout,
		EmailDriver:                  emailDriver,
		SMSDriver:                    smsDriver,
		SMSEmailTo:                   strings.TrimSpace(getEnv("SMS_EMAIL_TO", getEnv("EMAIL_FROM_ADDRESS", getEnv("SMTP_USERNAME", "")))),
		SMSWebhookURL:                strings.TrimSpace(getEnv("SMS_WEBHOOK_URL", "")),
		SMSWebhookToken:              strings.TrimSpace(getEnv("SMS_WEBHOOK_TOKEN", "")),
		WhatsAppAPIVersion:           strings.TrimSpace(getEnv("WHATSAPP_API_VERSION", "v21.0")),
		WhatsAppPhoneNumberID:        strings.TrimSpace(getEnv("WHATSAPP_PHONE_NUMBER_ID", "")),
		WhatsAppAccessToken:          strings.TrimSpace(getEnv("WHATSAPP_ACCESS_TOKEN", "")),
		WhatsAppReceiptTemplate:      strings.TrimSpace(getEnv("WHATSAPP_RECEIPT_TEMPLATE", "struk_digital")),
		WhatsAppTemplateLanguage:     strings.TrimSpace(getEnv("WHATSAPP_TEMPLATE_LANGUAGE", "id")),
		GuestSplitEnabled:            getEnv("GUEST_SPLIT_ENABLED", "false") == "true",
		WhatsAppDriver:               strings.ToLower(strings.TrimSpace(getEnv("WHATSAPP_DRIVER", ""))),
		WhatsAppBridgeURL:            strings.TrimSpace(getEnv("WHATSAPP_BRIDGE_URL", "")),
		WhatsAppBridgeToken:          strings.TrimSpace(getEnv("WHATSAPP_BRIDGE_TOKEN", "")),
		SMTPHost:                     getEnv("SMTP_HOST", ""),
		SMTPPort:                     smtpPort,
		SMTPUsername:                 getEnv("SMTP_USERNAME", ""),
		SMTPPassword:                 getEnv("SMTP_PASSWORD", ""),
		SMTPTLSMode:                  smtpTLSMode,
		EmailFromEmail:               getEnv("EMAIL_FROM_ADDRESS", getEnv("SMTP_USERNAME", "")),
		EmailFromName:                getEnv("EMAIL_FROM_NAME", "MyCaffe"),
		AppPublicURL:                 strings.TrimRight(getEnv("APP_PUBLIC_URL", frontendURL), "/"),
		DefaultStoreSlug:             strings.ToLower(strings.TrimSpace(getEnv("DEFAULT_STORE_SLUG", ""))),
		MidtransMerchantID:           getEnv("MIDTRANS_MERCHANT_ID", ""),
		MidtransServerKey:            getEnv("MIDTRANS_SERVER_KEY", ""),
		MidtransClientKey:            getEnv("MIDTRANS_CLIENT_KEY", ""),
		MidtransIsProduction:         isProd,
		CredentialEncryptionKey:      strings.TrimSpace(getEnv("CREDENTIAL_ENCRYPTION_KEY", "")),
		PlatformAdminAPIKey:          strings.TrimSpace(getEnv("PLATFORM_ADMIN_API_KEY", "")),
		PlatformMidtransServerKey:    strings.TrimSpace(getEnv("PLATFORM_MIDTRANS_SERVER_KEY", getEnv("MIDTRANS_SERVER_KEY", ""))),
		PlatformMidtransClientKey:    strings.TrimSpace(getEnv("PLATFORM_MIDTRANS_CLIENT_KEY", getEnv("MIDTRANS_CLIENT_KEY", ""))),
		PlatformMidtransIsProduction: platformIsProd,
		StorageDriver:                getEnv("STORAGE_DRIVER", "local"),
		R2AccountID:                  getEnv("R2_ACCOUNT_ID", ""),
		R2AccessKeyID:                getEnv("R2_ACCESS_KEY_ID", ""),
		R2SecretAccessKey:            getEnv("R2_SECRET_ACCESS_KEY", ""),
		R2BucketName:                 getEnv("R2_BUCKET_NAME", ""),
		R2PrivateBucketName:          getEnv("R2_PRIVATE_BUCKET_NAME", ""),
		R2PublicURL:                  getEnv("R2_PUBLIC_URL", ""),
		SentryDSN:                    getEnv("SENTRY_DSN", ""),
		SentryEnvironment:            getEnv("SENTRY_ENVIRONMENT", appEnv),
	}

	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// LoadMigrateConfig loads and validates only the database migration configuration needed by cmd/migrate.
// This allows the migration job container to run with least privilege without requiring JWT or Midtrans runtime secrets.
func LoadMigrateConfig() (*Config, error) {
	_ = godotenv.Load()

	rawAppEnv, exists := os.LookupEnv("APP_ENV")
	if !exists {
		return nil, errors.New("APP_ENV is not set: environment must be explicitly defined (development, test, staging, production)")
	}
	appEnv := strings.ToLower(strings.TrimSpace(rawAppEnv))
	if appEnv == "" {
		return nil, errors.New("APP_ENV cannot be empty: environment must be explicitly defined (development, test, staging, production)")
	}

	cfg := &Config{
		AppEnv:               appEnv,
		DatabaseURL:          getEnv("DATABASE_URL", ""),
		DatabaseMigrationURL: getEnv("DATABASE_MIGRATION_URL", getEnv("DATABASE_URL", "")),
		DBAppUser:            getEnv("DB_APP_USER", "mycaffe_app"),
		DBAppPassword:        getEnv("DB_APP_PASSWORD", ""),
	}

	if err := ValidateMigrateConfig(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// ValidateMigrateConfig enforces environment and database DSN safety rules for the database migration CLI.
func ValidateMigrateConfig(cfg *Config) error {
	cfg.AppEnv = strings.ToLower(strings.TrimSpace(cfg.AppEnv))
	validEnvs := []string{"development", "test", "staging", "production"}
	isValidEnv := false
	for _, env := range validEnvs {
		if cfg.AppEnv == env {
			isValidEnv = true
			break
		}
	}
	if !isValidEnv {
		return fmt.Errorf("[SECURITY FAIL-CLOSED] invalid APP_ENV %q: allowed values are development, test, staging, production", cfg.AppEnv)
	}

	dsn := cfg.DatabaseMigrationURL
	if dsn == "" {
		dsn = cfg.DatabaseURL
	}
	if dsn == "" {
		return errors.New("[SECURITY FAIL-CLOSED] DATABASE_MIGRATION_URL or DATABASE_URL must be set")
	}

	if cfg.AppEnv == "production" {
		if containsPlaceholderPattern(dsn) {
			return errors.New("[SECURITY FAIL-CLOSED] production DATABASE_MIGRATION_URL cannot contain placeholder credentials")
		}
		if cfg.DBAppPassword != "" && containsPlaceholderPattern(cfg.DBAppPassword) {
			return errors.New("[SECURITY FAIL-CLOSED] production DB_APP_PASSWORD cannot contain placeholder credentials")
		}
	}

	return nil
}

// ValidateConfig enforces fail-closed security rules without embedding actual secrets in code
func ValidateConfig(cfg *Config) error {
	// Whitelist valid environments
	cfg.AppEnv = strings.ToLower(strings.TrimSpace(cfg.AppEnv))
	validEnvs := []string{"development", "test", "staging", "production"}
	isValidEnv := false
	for _, env := range validEnvs {
		if cfg.AppEnv == env {
			isValidEnv = true
			break
		}
	}
	if !isValidEnv {
		return fmt.Errorf("[SECURITY FAIL-CLOSED] invalid APP_ENV %q: allowed values are development, test, staging, production", cfg.AppEnv)
	}

	// staging is the publicly tunnelled demo (L-2): a guessable JWT secret there lets anyone forge staff tokens.
	if cfg.AppEnv == "production" || cfg.AppEnv == "staging" {
		if containsPlaceholderPattern(cfg.JWTSecret) {
			return fmt.Errorf("[SECURITY FAIL-CLOSED] %s requires a genuine, unguessable JWT_SECRET_KEY", cfg.AppEnv)
		}
		if len(cfg.JWTSecret) < 32 {
			return fmt.Errorf("[SECURITY FAIL-CLOSED] %s requires a strong JWT_SECRET_KEY of at least 32 characters", cfg.AppEnv)
		}
		if cfg.CredentialEncryptionKey != "" {
			if containsPlaceholderPattern(cfg.CredentialEncryptionKey) || len(cfg.CredentialEncryptionKey) < 32 {
				return fmt.Errorf("[SECURITY FAIL-CLOSED] %s CREDENTIAL_ENCRYPTION_KEY must be non-placeholder and at least 32 characters", cfg.AppEnv)
			}
		}
		if cfg.PlatformAdminAPIKey != "" {
			if containsPlaceholderPattern(cfg.PlatformAdminAPIKey) || len(cfg.PlatformAdminAPIKey) < 32 {
				return fmt.Errorf("[SECURITY FAIL-CLOSED] %s PLATFORM_ADMIN_API_KEY must be non-placeholder and at least 32 characters", cfg.AppEnv)
			}
		}
	}

	// Generic validation: reject obviously insecure patterns in production
	if cfg.AppEnv == "production" {
		if cfg.StaffAccessTokenTTL > time.Hour || cfg.StaffRefreshTokenTTL > 24*time.Hour {
			return errors.New("[SECURITY FAIL-CLOSED] production staff token TTLs cannot exceed 1h (access) / 24h (refresh)")
		}

		if cfg.DatabaseURL == "" || containsPlaceholderPattern(cfg.DatabaseURL) {
			return errors.New("[SECURITY FAIL-CLOSED] production requires a valid, non-placeholder DATABASE_URL")
		}
		if cfg.DatabaseMigrationURL != "" && containsPlaceholderPattern(cfg.DatabaseMigrationURL) {
			return errors.New("[SECURITY FAIL-CLOSED] production DATABASE_MIGRATION_URL cannot contain placeholder credentials")
		}

		if !cfg.MidtransIsProduction {
			return errors.New("[SECURITY FAIL-CLOSED] production requires MIDTRANS_IS_PRODUCTION=true")
		}
		if cfg.MidtransServerKey == "" || strings.HasPrefix(cfg.MidtransServerKey, "SB-") || containsPlaceholderPattern(cfg.MidtransServerKey) {
			return errors.New("[SECURITY FAIL-CLOSED] production cannot run with empty, placeholder, or sandbox Midtrans server key")
		}
		if strings.HasPrefix(cfg.MidtransClientKey, "SB-") || (cfg.MidtransClientKey != "" && containsPlaceholderPattern(cfg.MidtransClientKey)) {
			return errors.New("[SECURITY FAIL-CLOSED] production cannot run with placeholder or sandbox Midtrans client key")
		}
		if cfg.MidtransMerchantID != "" && containsPlaceholderPattern(cfg.MidtransMerchantID) {
			return errors.New("[SECURITY FAIL-CLOSED] production cannot run with placeholder MIDTRANS_MERCHANT_ID")
		}

		if len(cfg.AllowedOrigins) == 0 {
			return errors.New("[SECURITY FAIL-CLOSED] production requires explicit ALLOWED_ORIGINS; cannot be empty")
		}

		// Validate origins in production: no wildcards, no localhost, must be valid HTTPS
		for _, origin := range cfg.AllowedOrigins {
			if origin == "*" {
				return errors.New("[SECURITY FAIL-CLOSED] wildcard '*' is forbidden in production ALLOWED_ORIGINS")
			}
			parsed, err := url.Parse(origin)
			if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
				return fmt.Errorf("[SECURITY FAIL-CLOSED] invalid production origin %q: must be a valid https URL", origin)
			}
			if strings.EqualFold(parsed.Hostname(), "localhost") || parsed.Hostname() == "127.0.0.1" {
				return fmt.Errorf("[SECURITY FAIL-CLOSED] localhost origin %q is forbidden in production", origin)
			}
		}
	}

	if err := validateSMSConfig(cfg); err != nil {
		return err
	}
	if err := validateWhatsAppConfig(cfg); err != nil {
		return err
	}
	return validateEmailConfig(cfg)
}

// validateWhatsAppConfig keeps the unofficial whatsmeow bridge out of production (decision W12).
func validateWhatsAppConfig(cfg *Config) error {
	switch cfg.WhatsAppDriver {
	case "", "disabled":
		return nil
	case "cloud":
		if cfg.WhatsAppAccessToken == "" || cfg.WhatsAppPhoneNumberID == "" {
			return errors.New("WHATSAPP_DRIVER=cloud requires WHATSAPP_ACCESS_TOKEN and WHATSAPP_PHONE_NUMBER_ID")
		}
		return nil
	case "bridge":
		u, err := url.Parse(strings.TrimSpace(cfg.WhatsAppBridgeURL))
		if err != nil || u.Scheme == "" || u.Host == "" {
			return errors.New("WHATSAPP_DRIVER=bridge requires a valid absolute WHATSAPP_BRIDGE_URL")
		}
		if len(cfg.WhatsAppBridgeToken) < 32 {
			return errors.New("WHATSAPP_DRIVER=bridge requires WHATSAPP_BRIDGE_TOKEN of at least 32 characters")
		}
		if cfg.AppEnv == "production" && containsPlaceholderPattern(cfg.WhatsAppBridgeToken) {
			return errors.New("[SECURITY FAIL-CLOSED] production WHATSAPP_BRIDGE_TOKEN cannot contain placeholder credentials")
		}
		return nil
	}
	return fmt.Errorf("invalid WHATSAPP_DRIVER %q: allowed values are cloud, bridge, disabled", cfg.WhatsAppDriver)
}

func validateSMSConfig(cfg *Config) error {
	switch cfg.SMSDriver {
	case "", "disabled":
		return nil
	case "log":
		if cfg.AppEnv == "staging" || cfg.AppEnv == "production" {
			return fmt.Errorf("[SECURITY FAIL-CLOSED] SMS_DRIVER=log would print OTP codes; not allowed in %s", cfg.AppEnv)
		}
		return nil
	case "email":
		if cfg.EmailDriver != "smtp" && cfg.EmailDriver != "log" {
			return errors.New("SMS_DRIVER=email requires EMAIL_DRIVER=smtp (or EMAIL_DRIVER=log in development/test)")
		}
		recipient := strings.TrimSpace(cfg.SMSEmailTo)
		if recipient == "" {
			recipient = strings.TrimSpace(cfg.EmailFromEmail)
		}
		if recipient == "" || containsPlaceholderPattern(recipient) {
			return errors.New("SMS_DRIVER=email requires a valid non-placeholder SMS_EMAIL_TO or EMAIL_FROM_ADDRESS")
		}
		return nil
	case "webhook":
		u, err := url.Parse(strings.TrimSpace(cfg.SMSWebhookURL))
		if err != nil || u.Scheme == "" || u.Host == "" {
			return errors.New("SMS_DRIVER=webhook requires a valid absolute SMS_WEBHOOK_URL")
		}
		if cfg.AppEnv == "production" && u.Scheme != "https" {
			return errors.New("[SECURITY FAIL-CLOSED] production SMS_WEBHOOK_URL must use https")
		}
		return nil
	}
	return fmt.Errorf("invalid SMS_DRIVER %q: allowed values are log, email, webhook, disabled", cfg.SMSDriver)
}

func containsPlaceholderPattern(val string) bool {
	lower := strings.ToLower(strings.TrimSpace(val))
	patterns := []string{
		"change_me",
		"default_secret",
		"placeholder",
		"your-",
		"local-dev",
		"dev_secret",
		"example_key",
		"replace-with",
		"demo-only",
	}
	for _, p := range patterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// IsOriginAllowed checks if an incoming origin is in the allowed whitelist
func (c *Config) IsOriginAllowed(origin string) bool {
	if origin == "" {
		return false
	}
	for _, allowed := range c.AllowedOrigins {
		if strings.EqualFold(strings.TrimRight(allowed, "/"), strings.TrimRight(origin, "/")) {
			return true
		}
	}
	return false
}

func getEnv(key, defaultVal string) string {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		return val
	}
	return defaultVal
}

func splitCSV(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// validateEmailConfig fails closed on an unusable mailer: the "log" driver prints link secrets
// and is only allowed in development/test, and SMTP needs a full set of credentials.
func validateEmailConfig(cfg *Config) error {
	switch cfg.EmailDriver {
	case "log":
		if cfg.AppEnv == "production" {
			return errors.New("[SECURITY FAIL-CLOSED] EMAIL_DRIVER=log is not allowed in production")
		}
		return nil
	case "disabled":
		if cfg.AppEnv == "production" {
			return errors.New("[SECURITY FAIL-CLOSED] EMAIL_DRIVER=disabled is not allowed in production")
		}
		return nil
	case "smtp":
	default:
		return fmt.Errorf("invalid EMAIL_DRIVER %q: allowed values are log, smtp, disabled", cfg.EmailDriver)
	}
	if cfg.SMTPHost == "" || cfg.SMTPUsername == "" || cfg.SMTPPassword == "" || cfg.EmailFromEmail == "" {
		return errors.New("EMAIL_DRIVER=smtp requires SMTP_HOST, SMTP_USERNAME, SMTP_PASSWORD and EMAIL_FROM_ADDRESS (or set EMAIL_DRIVER=log outside production)")
	}
	if cfg.SMTPTLSMode != "starttls" && cfg.SMTPTLSMode != "implicit" {
		return fmt.Errorf("invalid SMTP_TLS_MODE %q: allowed values are starttls, implicit", cfg.SMTPTLSMode)
	}
	if u, err := url.Parse(cfg.AppPublicURL); err != nil || u.Scheme == "" || u.Host == "" {
		return errors.New("APP_PUBLIC_URL (or FRONTEND_URL) must be an absolute URL for links in emails")
	}
	if cfg.AppEnv == "production" && !strings.HasPrefix(cfg.AppPublicURL, "https://") {
		return errors.New("[SECURITY FAIL-CLOSED] production APP_PUBLIC_URL must use https")
	}
	return nil
}
