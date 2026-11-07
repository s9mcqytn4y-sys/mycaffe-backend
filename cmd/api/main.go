package main

import (
	"context"
	"database/sql"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/handler"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/mailer"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/metrics"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/sms"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/realtime"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

// @title MyCaffe Core POS API
// @version 1.0
// @description Backend API untuk Multi-Tenant QR Ordering, POS, KDS, & TV Queue Display (MyCaffe Platform).
// @BasePath /
func main() {
	// JSON logs; slog.SetDefault also routes every existing log.Printf through this handler.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	cfg, err := config.LoadConfig()
	if err != nil {
		log.Fatalf("[FATAL CONFIG] %v", err)
	}

	// Initialize Sentry error monitoring with Free Plan quota protection & PII scrubbing (R6)
	if cfg.SentryDSN != "" && cfg.SentryDSN != "https://your-public-key@o0.ingest.sentry.io/projectId" {
		if err := sentry.Init(handler.SentryClientOptions(cfg.SentryDSN, cfg.SentryEnvironment)); err != nil {
			log.Printf("[SENTRY] Initialization failed: %v", err)
		} else {
			defer sentry.Flush(2 * time.Second)
			log.Printf("[SENTRY] Initialized successfully (env=%s)", cfg.SentryEnvironment)
		}
	}

	// Initialize PostgreSQL 18 Connection Pool
	var db *sqlx.DB
	if cfg.DatabaseURL != "" {
		dbConn, err := database.NewPostgresDB(cfg)
		if err != nil {
			log.Fatalf("[FATAL DATABASE] %v", err)
		}
		db = dbConn
		defer func() {
			if err := db.Close(); err != nil {
				log.Printf("[DATABASE] Error closing pool: %v", err)
			}
		}()
		log.Println("[DATABASE] PostgreSQL pool initialized and verified")
	} else if cfg.AppEnv == "production" || cfg.AppEnv == "staging" {
		log.Fatalf("[FATAL DATABASE] DATABASE_URL is required in %s environment", cfg.AppEnv)
	}

	// Background worker lifecycle context (stopped cleanly on SIGINT/SIGTERM)
	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()

	// Initialize Realtime Hub with allowed origins
	hub := realtime.NewHub(cfg.AllowedOrigins)
	go hub.RunWithContext(workerCtx)

	// Wire domain services, handlers, and HTTP router
	handlers := wireHandlers(workerCtx, cfg, db, hub)
	router := newRouter(cfg, handlers)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           router,
		ReadTimeout:       15 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		MaxHeaderBytes:    64 << 10,
		// Payment initiation may spend a 12s Midtrans charge plus a 12s CheckTransaction fallback;
		// keep this above that budget and at or below Nginx proxy_read_timeout (30s) for /api.
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Internal metrics listener (IN3): separate port, not routed by Nginx, scraped by Grafana Alloy.
	if cfg.MetricsAddr != "" {
		var sqlDB *sql.DB
		if db != nil {
			sqlDB = db.DB
		}
		metricsSrv := &http.Server{Addr: cfg.MetricsAddr, Handler: metrics.Handler(sqlDB), ReadHeaderTimeout: 5 * time.Second}
		go func() {
			log.Printf("[METRICS] Serving /metrics on %s", cfg.MetricsAddr)
			if err := metricsSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("[METRICS] listener stopped: %v", err)
			}
		}()
		defer metricsSrv.Close()
	}

	// Graceful shutdown setup
	go func() {
		log.Printf("[SERVER] Starting MyCaffe Backend on port :%s (env: %s)", cfg.Port, cfg.AppEnv)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[SERVER] Listen error: %s\n", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("[SERVER] Shutting down server gracefully...")
	workerCancel()
	hub.Shutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("[SERVER] Server forced to shutdown: %v", err)
	}

	log.Println("[SERVER] Server exited successfully.")
}

func newRouter(cfg *config.Config, h *handlerSet) *gin.Engine {
	// Debug mode only on developer machines; the staging demo is public and must not log routes.
	if cfg.AppEnv == "development" || cfg.AppEnv == "test" {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()
	router.Use(handler.Recover(), handler.AccessLog())
	// Without this Gin trusts X-Forwarded-For from any peer, so ClientIP() is spoofable.
	// Empty TRUSTED_PROXIES trusts none; set it to the Nginx network CIDR behind the reverse proxy.
	if err := router.SetTrustedProxies(cfg.TrustedProxies); err != nil {
		log.Fatalf("[FATAL CONFIG] invalid TRUSTED_PROXIES: %v", err)
	}
	router.Use(handler.RequestID(), handler.LimitBody(1<<20), corsMiddleware(cfg))

	registerRoutes(router, h)
	return router
}

// corsMiddleware enforces strict origin-validated CORS without wildcard + credentials violations.
func corsMiddleware(cfg *config.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")

		if cfg.IsOriginAllowed(origin) {
			c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
			c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
			c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Protection, X-Platform-Admin-Key, Authorization, Accept, Origin, Cache-Control, X-Requested-With")
			c.Writer.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS, GET, PUT, DELETE, PATCH")
		}

		if c.Request.Method == "OPTIONS" {
			if cfg.IsOriginAllowed(origin) {
				c.AbortWithStatus(http.StatusNoContent)
			} else {
				c.AbortWithStatus(http.StatusForbidden)
			}
			return
		}

		c.Next()
	}
}

// buildMailSender returns the configured mailer.Sender (log or smtp).
func buildMailSender(cfg *config.Config) mailer.Sender {
	if cfg.EmailDriver == "smtp" {
		return mailer.NewSMTPSender(mailer.SMTPConfig{
			Host: cfg.SMTPHost, Port: cfg.SMTPPort, Username: cfg.SMTPUsername, Password: cfg.SMTPPassword,
			TLSMode: cfg.SMTPTLSMode, From: cfg.EmailFromEmail, FromName: cfg.EmailFromName,
		})
	}
	return mailer.LogSender{}
}

// newEmailService picks the mail driver from config (log for dev/test, SMTP otherwise) and
// starts the outbox worker on the shared worker context.
func newEmailService(cfg *config.Config, db *sqlx.DB, ctx context.Context, sender mailer.Sender) *service.EmailService {
	emailService := service.NewEmailService(db, sender)
	go emailService.StartWorker(ctx, 10*time.Second)
	log.Printf("[EMAIL] driver=%s worker started", cfg.EmailDriver)
	return emailService
}

// newMessageService picks the SMS driver (decision V5: log, email bridge, webhook, or disabled)
// and starts its outbox worker. Disabled means no sender: SMS flows answer 503 SMS_UNAVAILABLE.
func newMessageService(cfg *config.Config, db *sqlx.DB, ctx context.Context, mailSender mailer.Sender) *service.MessageService {
	var sender sms.Sender
	switch cfg.SMSDriver {
	case "log":
		sender = sms.LogSender{}
	case "email":
		recipient := strings.TrimSpace(cfg.SMSEmailTo)
		if recipient == "" {
			recipient = strings.TrimSpace(cfg.EmailFromEmail)
		}
		emailSender, err := sms.NewEmailSender(mailSender, recipient)
		if err != nil {
			log.Fatalf("[FATAL CONFIG] SMS_DRIVER=email initialization failed: %v", err)
		}
		sender = emailSender
	case "webhook":
		webhookSender, err := sms.NewWebhookSender(cfg.SMSWebhookURL, cfg.SMSWebhookToken)
		if err != nil {
			log.Fatalf("[FATAL CONFIG] SMS_DRIVER=webhook initialization failed: %v", err)
		}
		sender = webhookSender
	}
	messages := service.NewMessageService(db, sender)
	// WhatsApp receipts: Cloud API (W9) in production; the whatsmeow bridge (W12) only for demo and
	// development, refused in production by config validation.
	whatsapp := "disabled"
	switch {
	case cfg.WhatsAppDriver == "bridge":
		wa, err := sms.NewBridgeSender(cfg.WhatsAppBridgeURL, cfg.WhatsAppBridgeToken, cfg.WhatsAppReceiptTemplate)
		if err != nil {
			log.Fatalf("[FATAL CONFIG] WhatsApp bridge initialization failed: %v", err)
		}
		messages.SetWhatsApp(wa)
		whatsapp = "bridge"
	case cfg.WhatsAppDriver != "disabled" && cfg.WhatsAppAccessToken != "" && cfg.WhatsAppPhoneNumberID != "":
		wa, err := sms.NewWhatsAppSender(cfg.WhatsAppAPIVersion, cfg.WhatsAppPhoneNumberID, cfg.WhatsAppAccessToken,
			cfg.WhatsAppReceiptTemplate, cfg.WhatsAppTemplateLanguage)
		if err != nil {
			log.Fatalf("[FATAL CONFIG] WhatsApp initialization failed: %v", err)
		}
		messages.SetWhatsApp(wa)
		whatsapp = "cloud_api"
	}
	go messages.StartWorker(ctx, 5*time.Second)
	log.Printf("[SMS] driver=%s whatsapp=%s worker started", cfg.SMSDriver, whatsapp)
	return messages
}
