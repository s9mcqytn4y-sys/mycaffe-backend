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
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/midtrans"
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

	// Strict Origin-Validated CORS Middleware (No Wildcard + Credentials violation)
	router.Use(func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")

		if cfg.IsOriginAllowed(origin) {
			c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
			c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
			c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization, Accept, Origin, Cache-Control, X-Requested-With")
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
	if db != nil {
		orderRepo := postgres.NewOrderRepository()
		orderService := service.NewOrderService(db, orderRepo)
		orderHandler.SetOrderService(orderService)

		paymentRepo := postgres.NewPaymentRepository()
		paymentService := service.NewPaymentService(db, paymentRepo, orderRepo, midtransClient)
		orderHandler.SetPaymentService(paymentService)
		midtransHandler.SetPaymentService(paymentService)

		go paymentService.StartReconciliationWorker(workerCtx, 30*time.Second, 2*time.Minute, 50)
	}

	// Register Routes
	v1 := router.Group("/api/v1")
	{
		v1.GET("/health", healthHandler.Check)
		v1.POST("/orders", orderHandler.CreateOrder)
		v1.POST("/orders/:id/payments/initiate", orderHandler.InitiateOrderPayment)
		v1.POST("/payments/midtrans/webhook", midtransHandler.HandleWebhook)
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
		Addr:        ":" + cfg.Port,
		Handler:     router,
		ReadTimeout: 15 * time.Second,
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
