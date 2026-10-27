package config

import (
	"log"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	AppEnv         string
	Port           string
	FrontendURL    string
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

func LoadConfig() *Config {
	// Load .env if present (ignored in production if env is injected)
	_ = godotenv.Load()

	isProd, _ := strconv.ParseBool(getEnv("MIDTRANS_IS_PRODUCTION", "false"))
	jwtHours, _ := strconv.Atoi(getEnv("JWT_EXPIRED_HOURS", "24"))

	cfg := &Config{
		AppEnv:               getEnv("APP_ENV", "development"),
		Port:                 getEnv("PORT", "8080"),
		FrontendURL:          getEnv("FRONTEND_URL", "http://localhost:3000"),
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

	if cfg.DatabaseURL == "" {
		log.Println("[WARN] DATABASE_URL is not set. Database connections may fail.")
	}

	return cfg
}

func getEnv(key, defaultVal string) string {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		return val
	}
	return defaultVal
}
