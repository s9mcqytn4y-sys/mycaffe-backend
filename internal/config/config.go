package config

import (
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	AppEnv         string
	Port           string
	FrontendURL    string
	AllowedOrigins []string
	DatabaseURL    string
	JWTSecret      string
	JWTExpiryHours int

	// Midtrans Core API
	MidtransMerchantID   string
	MidtransServerKey    string
	MidtransClientKey    string
	MidtransIsProduction bool

	// Cloudflare R2 / Storage
	StorageDriver     string
	R2AccountID       string
	R2AccessKeyID     string
	R2SecretAccessKey string
	R2BucketName      string
	R2PublicURL       string

	// Sentry
	SentryDSN         string
	SentryEnvironment string
}

func LoadConfig() (*Config, error) {
	// Load .env if present (ignored in production environments where env is injected)
	_ = godotenv.Load()

	isProd, _ := strconv.ParseBool(getEnv("MIDTRANS_IS_PRODUCTION", "false"))
	jwtHours, _ := strconv.Atoi(getEnv("JWT_EXPIRED_HOURS", "24"))

	frontendURL := getEnv("FRONTEND_URL", "http://localhost:3000")
	allowedOriginsRaw := getEnv("ALLOWED_ORIGINS", frontendURL)
	var allowedOrigins []string
	for _, o := range strings.Split(allowedOriginsRaw, ",") {
		trimmed := strings.TrimSpace(o)
		if trimmed != "" {
			allowedOrigins = append(allowedOrigins, trimmed)
		}
	}

	cfg := &Config{
		AppEnv:               getEnv("APP_ENV", "development"),
		Port:                 getEnv("PORT", "8080"),
		FrontendURL:          frontendURL,
		AllowedOrigins:       allowedOrigins,
		DatabaseURL:          getEnv("DATABASE_URL", ""),
		JWTSecret:            getEnv("JWT_SECRET_KEY", "default_secret_key_change_me_in_production"),
		JWTExpiryHours:       jwtHours,
		MidtransMerchantID:   getEnv("MIDTRANS_MERCHANT_ID", ""),
		MidtransServerKey:    getEnv("MIDTRANS_SERVER_KEY", ""),
		MidtransClientKey:    getEnv("MIDTRANS_CLIENT_KEY", ""),
		MidtransIsProduction: isProd,
		StorageDriver:        getEnv("STORAGE_DRIVER", "local"),
		R2AccountID:          getEnv("R2_ACCOUNT_ID", ""),
		R2AccessKeyID:        getEnv("R2_ACCESS_KEY_ID", ""),
		R2SecretAccessKey:    getEnv("R2_SECRET_ACCESS_KEY", ""),
		R2BucketName:         getEnv("R2_BUCKET_NAME", ""),
		R2PublicURL:          getEnv("R2_PUBLIC_URL", ""),
		SentryDSN:            getEnv("SENTRY_DSN", ""),
		SentryEnvironment:    getEnv("SENTRY_ENVIRONMENT", "development"),
	}

	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

// ValidateConfig enforces fail-closed security rules for configuration
func ValidateConfig(cfg *Config) error {
	if cfg.AppEnv == "production" {
		if cfg.JWTSecret == "" ||
			cfg.JWTSecret == "default_secret_key_change_me_in_production" ||
			cfg.JWTSecret == "super_secret_jwt_key_kafe_pos_2026_x7!" ||
			len(cfg.JWTSecret) < 32 {
			return errors.New("[SECURITY FAIL-CLOSED] production requires a strong JWT_SECRET_KEY of at least 32 characters")
		}

		if cfg.DatabaseURL == "" {
			return errors.New("[SECURITY FAIL-CLOSED] production requires a valid DATABASE_URL")
		}

		if cfg.MidtransIsProduction {
			if cfg.MidtransServerKey == "" || strings.HasPrefix(cfg.MidtransServerKey, "SB-") {
				return errors.New("[SECURITY FAIL-CLOSED] production cannot run with sandbox Midtrans server key")
			}
		}

		if len(cfg.AllowedOrigins) == 0 {
			return errors.New("[SECURITY FAIL-CLOSED] production requires explicit ALLOWED_ORIGINS")
		}
	}

	return nil
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
