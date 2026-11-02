package main

import (
	"context"
	"database/sql"
	"log"
	"log/slog"
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
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/metrics"
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
	// JSON logs; slog.SetDefault also routes every existing log.Printf through this handler.
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

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

	router := gin.New()
	router.Use(handler.Recover(), handler.AccessLog())
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
	var authHandler *handler.AuthHandler
	var posHandler *handler.POSHandler
	var catalogHandler *handler.CatalogHandler
	var shiftHandler *handler.ShiftHandler
	var tabHandler *handler.TabHandler
	var tableHandler *handler.TableHandler
	var guestHandler *handler.GuestHandler
	var realtimeHandler *handler.RealtimeHandler
	var kdsHandler *handler.KDSHandler
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

		paymentRepo := postgres.NewPaymentRepository()
		paymentService := service.NewPaymentService(db, paymentRepo, orderRepo, midtransClient)
		midtransHandler.SetPaymentService(paymentService)
		posHandler = handler.NewPOSHandler(orderService, paymentService, service.NewSettingsService(db))
		operationalService := service.NewOperationalService(db, orderRepo, paymentRepo, postgres.NewShiftRepository(), postgres.NewAuthRepository(), postgres.NewSettingsRepository())
		posHandler.SetOperational(operationalService)
		catalogHandler = handler.NewCatalogHandler(service.NewCatalogService(db))
		shiftHandler = handler.NewShiftHandler(service.NewShiftService(db))
		tabHandler = handler.NewTabHandler(service.NewTabService(db, postgres.NewTabRepository(), orderRepo))
		tableHandler = handler.NewTableHandler(service.NewTableService(db))
		guestHandler = handler.NewGuestHandler(service.NewGuestService(db, orderService, paymentService))
		kdsHandler = handler.NewKDSHandler(service.NewKDSService(db))
		realtimeService := service.NewRealtimeService(db)
		realtimeHandler = handler.NewRealtimeHandler(hub, realtimeService)
		// Outbox relay (iteration 5c): LISTEN outbox + 2 s polling, at-least-once, ordered per store.
		go realtimeService.RunRelay(workerCtx, hub)

		go paymentService.StartReconciliationWorker(workerCtx, 30*time.Second, 2*time.Minute, 50)
	}

	// Register Routes
	v1 := router.Group("/api/v1")
	{
		v1.GET("/health", healthHandler.Check)
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
			pos.POST("/orders/quote", authHandler.RequirePermission(rbac.OrderPOSCreate), posHandler.QuoteOrder)
			pos.POST("/orders/:id/payments", authHandler.RequireStaff(), posHandler.InitiatePayment)
			pos.GET("/catalog", authHandler.RequireStaff(), catalogHandler.StaffCatalog)

			// Cash Drawers & Shifts (Iteration 4b)
			pos.POST("/drawers", authHandler.RequirePermission(rbac.CashDrawerManage), shiftHandler.CreateDrawer)
			pos.GET("/drawers", authHandler.RequireStaff(), shiftHandler.ListDrawers)
			pos.PATCH("/drawers/:id", authHandler.RequirePermission(rbac.CashDrawerManage), shiftHandler.UpdateDrawer)

			pos.POST("/shifts", authHandler.RequirePermission(rbac.ShiftOpen), shiftHandler.OpenShift)
			pos.GET("/shifts/current", authHandler.RequireStaff(), shiftHandler.GetCurrentShift)
			pos.POST("/shifts/movements", authHandler.RequirePermission(rbac.CashMovementCreate), shiftHandler.CreateCashMovement)
			pos.POST("/shifts/:id/close", authHandler.RequirePermission(rbac.ShiftClose), shiftHandler.CloseShift)
			pos.POST("/shifts/:id/force-close", authHandler.RequirePermission(rbac.ShiftForceClose), shiftHandler.ForceCloseShift)
			pos.GET("/shifts/:id/x-report", authHandler.RequireStaff(), shiftHandler.GetXReport)
			pos.GET("/shifts/:id/z-report", authHandler.RequireStaff(), shiftHandler.GetZReport)

			// Open Tabs, Rounds & Split Bill (Iteration 4c-1 & 4c-2)
			pos.POST("/tabs", authHandler.RequirePermission(rbac.TabManage), tabHandler.OpenTab)
			pos.POST("/tabs/:id/rounds", authHandler.RequirePermission(rbac.TabManage), tabHandler.SubmitRound)
			pos.GET("/tabs/:id", authHandler.RequireStaff(), tabHandler.GetTab)
			pos.POST("/tabs/:id/close", authHandler.RequirePermission(rbac.TabManage), tabHandler.CloseTab)
			pos.POST("/tabs/:id/reopen", authHandler.RequirePermission(rbac.TabManage), tabHandler.ReopenTab)

			// POS Operational: Void, Refund & Stock Override (Iteration 4d)
			pos.POST("/orders/:id/void", authHandler.RequirePermission(rbac.OrderVoid), posHandler.VoidOrder)
			pos.POST("/orders/:id/payments/:payment_id/refund", authHandler.RequirePermission(rbac.PaymentRefund), posHandler.RefundPayment)
			pos.POST("/products/:id/stock-override", authHandler.RequirePermission(rbac.StockOverride), posHandler.OverrideStock)

			// Tables & table sessions (iteration 5a): table board for staff, CRUD for the owner (QR3).
			pos.GET("/tables", authHandler.RequireStaff(), tableHandler.ListTables)
			pos.POST("/tables", authHandler.RequirePermission(rbac.TableManage), tableHandler.CreateTable)
			pos.PATCH("/tables/:id", authHandler.RequirePermission(rbac.TableManage), tableHandler.UpdateTable)
			pos.POST("/tables/:id/archive", authHandler.RequirePermission(rbac.TableManage), tableHandler.ArchiveTable)
			pos.POST("/tables/:id/rotate-qr", authHandler.RequirePermission(rbac.TableQRManage), tableHandler.RotateQR)
			pos.POST("/tables/:id/sessions", authHandler.RequirePermission(rbac.TabManage), tableHandler.OpenSession)
			pos.POST("/table-sessions/:id/close", authHandler.RequirePermission(rbac.TabManage), tableHandler.CloseSession)
			v1.GET("/public/qr/:token", tableHandler.ResolveQR)

			// QR guest ordering (iteration 5b): pay-first per device (T2), own orders only (Q6).
			v1.POST("/public/qr/:token/session", guestHandler.StartSession)
			v1.GET("/public/orders/track/:token", guestHandler.Track)
			guest := v1.Group("/public/guest", handler.LimitBody(64<<10), guestHandler.RequireGuest())
			guest.GET("/session", guestHandler.Session)
			guest.GET("/orders", guestHandler.ListOrders)
			guest.POST("/orders", guestHandler.CreateOrder)
			guest.POST("/orders/:id/payments", guestHandler.InitiatePayment)
			guest.POST("/ws-ticket", realtimeHandler.IssueGuestTicket)
			pos.POST("/ws-ticket", authHandler.RequireStaff(), realtimeHandler.IssueStaffTicket)

			// KDS (iteration 5d): station permissions are checked per ticket in the service.
			kds := v1.Group("/kds", authHandler.RequireStaff())
			kds.GET("/tickets", kdsHandler.ListTickets)
			kds.POST("/tickets/:id/transition", kdsHandler.Transition)

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

	// WebSocket (iteration 5c): single-use 30 s ticket from POST /api/v1/pos/ws-ticket or
	// /api/v1/public/guest/ws-ticket; replay with last_seq.
	if realtimeHandler != nil {
		router.GET("/ws", realtimeHandler.Connect)
	}

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
