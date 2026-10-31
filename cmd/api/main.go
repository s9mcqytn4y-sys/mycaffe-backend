package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/handler"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/mailer"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/midtrans"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/realtime"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

// @title MyCaffe Core POS API
// @version 1.0
// @description Backend API untuk Multi-Tenant QR Ordering, POS, KDS, & TV Queue Display (MyCaffe Platform).
// @BasePath /
func main() {
	cfg, err := config.LoadConfig()
	if err != nil {
		log.Fatalf("[FATAL CONFIG] %v", err)
	}

	// Initialize Sentry error monitoring
	if cfg.SentryDSN != "" && cfg.SentryDSN != "https://your-public-key@o0.ingest.sentry.io/projectId" {
		err := sentry.Init(sentry.ClientOptions{
			Dsn:         cfg.SentryDSN,
			Environment: cfg.SentryEnvironment,
		})
		if err != nil {
			log.Printf("[SENTRY] Initialization failed: %v", err)
		} else {
			defer sentry.Flush(2 * time.Second)
			log.Println("[SENTRY] Initialized successfully")
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

	// Set Gin mode
	if cfg.AppEnv == "production" {
		gin.SetMode(gin.ReleaseMode)
	} else {
		gin.SetMode(gin.DebugMode)
	}

	router := gin.Default()
	// Without this Gin trusts X-Forwarded-For from any peer, so ClientIP() is spoofable.
	// Empty TRUSTED_PROXIES trusts none; set it to the Nginx network CIDR behind the reverse proxy.
	if err := router.SetTrustedProxies(cfg.TrustedProxies); err != nil {
		log.Fatalf("[FATAL CONFIG] invalid TRUSTED_PROXIES: %v", err)
	}
	router.Use(handler.RequestID(), handler.LimitBody(1<<20))

	// Strict Origin-Validated CORS Middleware (No Wildcard + Credentials violation)
	router.Use(func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")

		if cfg.IsOriginAllowed(origin) {
			c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
			c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
			c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Protection, Authorization, Accept, Origin, Cache-Control, X-Requested-With")
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
	})

	// Background worker lifecycle context (stopped cleanly on SIGINT/SIGTERM)
	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()

	// Initialize Realtime Hub with allowed origins
	hub := realtime.NewHub(cfg.AllowedOrigins)
	go hub.RunWithContext(workerCtx)

	// Initialize Midtrans Core API Client
	midtransClient := midtrans.NewClient(cfg)

	// Initialize Handlers
	healthHandler := handler.NewHealthHandler("MyCaffe POS Backend", cfg.AppEnv, db)
	midtransHandler := handler.NewMidtransHandler(midtransClient, hub)
	orderHandler := handler.NewOrderHandler(cfg, midtransClient, hub)
	var authHandler *handler.AuthHandler
	var posHandler *handler.POSHandler
	var catalogHandler *handler.CatalogHandler
	if db != nil {
		authService := service.NewAuthService(db, postgres.NewAuthRepository(), cfg.JWTSecret, service.AuthPolicy{
			AccessTokenTTL:     cfg.StaffAccessTokenTTL,
			RefreshTokenTTL:    cfg.StaffRefreshTokenTTL,
			SessionIdleTimeout: cfg.StaffSessionIdleTimeout,
			MaxSessions:        cfg.StaffMaxSessions,
			PINMaxFailures:     cfg.StaffPINMaxFailures,
			PINLockout:         cfg.StaffPINLockout,
		}, service.WithAuthEmail(newEmailService(cfg, db, workerCtx), cfg.AppPublicURL, ""))
		authHandler = handler.NewAuthHandler(authService)
		authHandler.DefaultStoreSlug = cfg.DefaultStoreSlug

		orderRepo := postgres.NewOrderRepository()
		orderService := service.NewOrderService(db, orderRepo)
		orderHandler.SetOrderService(orderService)

		paymentRepo := postgres.NewPaymentRepository()
		paymentService := service.NewPaymentService(db, paymentRepo, orderRepo, midtransClient)
		midtransHandler.SetPaymentService(paymentService)
		posHandler = handler.NewPOSHandler(orderService, paymentService, service.NewSettingsService(db))
		catalogHandler = handler.NewCatalogHandler(service.NewCatalogService(db))

		go paymentService.StartReconciliationWorker(workerCtx, 30*time.Second, 2*time.Minute, 50)
	}

	// Register Routes
	v1 := router.Group("/api/v1")
	{
		v1.GET("/health", healthHandler.Check)
		v1.POST("/orders", orderHandler.CreateOrder)
		v1.POST("/payments/midtrans/webhook", midtransHandler.HandleWebhook)
		if authHandler != nil {
			staffAuth := v1.Group("/auth/staff")
			staffAuth.POST("/login", authHandler.Login)
			staffAuth.POST("/refresh", authHandler.Refresh)
			staffAuth.POST("/logout", authHandler.Logout)
			staffAuth.GET("/me", authHandler.Me)
			staffAuth.POST("/pin/forgot", authHandler.ForgotPIN)
			staffAuth.POST("/pin/setup", authHandler.SetupPIN)
			staffAuth.POST("/pin/change", authHandler.RequireStaff(), authHandler.ChangePIN)

			owner := v1.Group("/staff", authHandler.RequirePermission(rbac.StaffManage))
			owner.GET("", authHandler.ListStaff)
			owner.POST("", authHandler.InviteStaff)
			owner.POST("/:id/pin-reset", authHandler.SendPINReset)
			owner.POST("/:id/temporary-pin", authHandler.IssueTemporaryPIN)

			store := v1.Group("/store")
			store.GET("/settings", authHandler.RequireStaff(), posHandler.GetSettings)
			store.PATCH("/settings", authHandler.RequirePermission(rbac.StoreSettingsManage), posHandler.UpdateSettings)

			// POS (ADR-003 D8 "staff" class): store and actor come from the access token only.
			pos := v1.Group("/pos")
			pos.POST("/orders", authHandler.RequirePermission(rbac.OrderPOSCreate), posHandler.CreateOrder)
			pos.POST("/orders/quote", authHandler.RequireStaff(), posHandler.QuoteOrder)
			pos.POST("/orders/:id/payments", authHandler.RequireStaff(), posHandler.InitiatePayment)
			pos.GET("/catalog", authHandler.RequireStaff(), catalogHandler.StaffCatalog)

			// Guest menu & quote (public, cacheable catalog; dynamic quote for guest carts).
			v1.GET("/public/stores/:slug/catalog", catalogHandler.PublicCatalog)
			v1.POST("/public/stores/:slug/orders/quote", posHandler.PublicQuoteOrder)

			// Catalog management (catalog C3): owner CRUD; availability toggles follow the station permission.
			manage := authHandler.RequirePermission(rbac.CatalogManage)
			cat := v1.Group("/catalog")
			cat.POST("/categories", manage, catalogHandler.CreateCategory)
			cat.PATCH("/categories/:id", manage, catalogHandler.UpdateCategory)
			cat.DELETE("/categories/:id", manage, catalogHandler.DeleteCategory)
			cat.POST("/products", manage, catalogHandler.CreateProduct)
			cat.PATCH("/products/:id", manage, catalogHandler.UpdateProduct)
			cat.DELETE("/products/:id", manage, catalogHandler.ArchiveProduct)
			cat.PATCH("/products/:id/availability", authHandler.RequireStaff(), catalogHandler.SetAvailability)
			cat.POST("/products/:id/variants", manage, catalogHandler.CreateVariant)
			cat.PATCH("/variants/:id", manage, catalogHandler.UpdateVariant)
			cat.DELETE("/variants/:id", manage, catalogHandler.ArchiveVariant)
			cat.POST("/modifier-groups", manage, catalogHandler.CreateModifierGroup)
			cat.GET("/modifier-groups", manage, catalogHandler.ListModifierGroups)
			cat.GET("/modifier-groups/:id", manage, catalogHandler.GetModifierGroup)
			cat.PATCH("/modifier-groups/:id", manage, catalogHandler.UpdateModifierGroup)
			cat.DELETE("/modifier-groups/:id", manage, catalogHandler.ArchiveModifierGroup)
			cat.POST("/products/:id/modifier-groups", manage, catalogHandler.AttachModifierGroupToProduct)
			cat.DELETE("/products/:id/modifier-groups/:group_id", manage, catalogHandler.DetachModifierGroupFromProduct)
			cat.POST("/modifier-groups/:id/options", manage, catalogHandler.CreateModifierOption)
			cat.PATCH("/modifier-groups/:id/options/:option_id", manage, catalogHandler.UpdateModifierOption)
			cat.DELETE("/modifier-groups/:id/options/:option_id", manage, catalogHandler.ArchiveModifierOption)
			cat.POST("/modifier-options/:id/child-groups", manage, catalogHandler.AttachChildGroupToOption)
			cat.DELETE("/modifier-options/:id/child-groups/:group_id", manage, catalogHandler.DetachChildGroupFromOption)
		}
	}

	// Health check endpoints for Docker / LB probes
	router.GET("/health", healthHandler.Check)
	router.GET("/health/live", healthHandler.Liveness)
	router.GET("/health/ready", healthHandler.Check)

	// WebSocket endpoint for KDS & TV Queue realtime updates (guarded by token/tenant validation)
	router.GET("/ws", orderHandler.HandleWebSocket)

	// Root greeting
	router.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "MyCaffe Core POS Backend API is running.",
			"version": "1.0.0",
			"docs":    "/swagger/index.html",
		})
	})

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

// newEmailService picks the mail driver from config (log for dev/test, SMTP otherwise) and
// starts the outbox worker on the shared worker context.
func newEmailService(cfg *config.Config, db *sqlx.DB, ctx context.Context) *service.EmailService {
	var sender mailer.Sender = mailer.LogSender{}
	if cfg.EmailDriver == "smtp" {
		sender = mailer.NewSMTPSender(mailer.SMTPConfig{
			Host: cfg.SMTPHost, Port: cfg.SMTPPort, Username: cfg.SMTPUsername, Password: cfg.SMTPPassword,
			TLSMode: cfg.SMTPTLSMode, From: cfg.EmailFromEmail, FromName: cfg.EmailFromName,
		})
	}
	emailService := service.NewEmailService(db, sender)
	go emailService.StartWorker(ctx, 10*time.Second)
	log.Printf("[EMAIL] driver=%s worker started", cfg.EmailDriver)
	return emailService
}
