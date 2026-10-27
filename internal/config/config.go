package config

import (
	"errors"
	"fmt"
	"net/url"
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
	// Load .env if present (ignored in production if injected via container/host)
	_ = godotenv.Load()

	// Strict boolean parsing for MIDTRANS_IS_PRODUCTION
	midtransProdStr := getEnv("MIDTRANS_IS_PRODUCTION", "false")
	isProd, err := strconv.ParseBool(midtransProdStr)
	if err != nil {
		return nil, fmt.Errorf("invalid boolean for MIDTRANS_IS_PRODUCTION: %w", err)
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

	frontendURL := getEnv("FRONTEND_URL", "http://localhost:3000")

	// Strict origin parsing
	allowedOriginsRaw := getEnv("ALLOWED_ORIGINS", "")
	var allowedOrigins []string
	if allowedOriginsRaw != "" {
		for _, o := range strings.Split(allowedOriginsRaw, ",") {
			trimmed := strings.TrimSpace(o)
			if trimmed != "" {
				allowedOrigins = append(allowedOrigins, trimmed)
			}
		}
	} else if appEnv != "production" {
		allowedOrigins = append(allowedOrigins, frontendURL)
	}

	cfg := &Config{
		AppEnv:               appEnv,
		Port:                 getEnv("PORT", "8080"),
		FrontendURL:          frontendURL,
		AllowedOrigins:       allowedOrigins,
		DatabaseURL:          getEnv("DATABASE_URL", ""),
		JWTSecret:            getEnv("JWT_SECRET_KEY", ""),
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

	// Generic validation: reject obviously insecure patterns
	secretLower := strings.ToLower(cfg.JWTSecret)
	if strings.Contains(secretLower, "change_me") ||
		strings.Contains(secretLower, "default_secret") ||
		strings.Contains(secretLower, "placeholder") {
		if cfg.AppEnv == "production" {
			return errors.New("[SECURITY FAIL-CLOSED] production requires a genuine, unguessable JWT_SECRET_KEY")
		}
	}

	if cfg.AppEnv == "production" {
		if len(cfg.JWTSecret) < 32 {
			return errors.New("[SECURITY FAIL-CLOSED] production requires a strong JWT_SECRET_KEY of at least 32 characters")
		}

		if cfg.DatabaseURL == "" {
			return errors.New("[SECURITY FAIL-CLOSED] production requires a valid DATABASE_URL")
		}

		if !cfg.MidtransIsProduction {
			return errors.New("[SECURITY FAIL-CLOSED] production requires MIDTRANS_IS_PRODUCTION=true")
		}
		if cfg.MidtransServerKey == "" || strings.HasPrefix(cfg.MidtransServerKey, "SB-") {
			return errors.New("[SECURITY FAIL-CLOSED] production cannot run with empty or sandbox Midtrans server key")
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
