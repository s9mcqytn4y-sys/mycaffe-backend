package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/handler"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/midtrans"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/storage"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/realtime"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

// handlerSet groups all HTTP handlers wired for the API server.
type handlerSet struct {
	health     *handler.HealthHandler
	midtrans   *handler.MidtransHandler
	auth       *handler.AuthHandler
	pos        *handler.POSHandler
	catalog    *handler.CatalogHandler
	shift      *handler.ShiftHandler
	tab        *handler.TabHandler
	table      *handler.TableHandler
	guest      *handler.GuestHandler
	realtime   *handler.RealtimeHandler
	kds        *handler.KDSHandler
	receipt    *handler.ReceiptHandler
	report     *handler.ReportHandler
	orderRead  *handler.OrderReadHandler
	customer   *handler.CustomerHandler
	outletCash *handler.OutletCashHandler
	approval   *handler.ApprovalHandler
	display    *handler.DisplayHandler
	print      *handler.PrintHandler
	device     *handler.DeviceHandler
	inventory  *handler.InventoryHandler
	offline    *handler.OfflineHandler
	receiptOut *handler.ReceiptDeliveryHandler
	media      *handler.MediaHandler
}

// wireHandlers constructs domain repositories, services, background workers, and HTTP handlers.
func wireHandlers(workerCtx context.Context, cfg *config.Config, db *sqlx.DB, hub *realtime.Hub) *handlerSet {
	midtransClient := midtrans.NewClient(cfg)
	h := &handlerSet{
		health:   handler.NewHealthHandler("MyCaffe POS Backend", cfg.AppEnv, db),
		midtrans: handler.NewMidtransHandler(midtransClient, hub),
	}
	if db == nil {
		return h
	}

	mailSender := buildMailSender(cfg)
	emailService := newEmailService(cfg, db, workerCtx, mailSender)
	messageService := newMessageService(cfg, db, workerCtx, mailSender)
	h.customer = handler.NewCustomerHandler(service.NewCustomerService(db, messageService))

	authPolicy := service.AuthPolicy{
		AccessTokenTTL:     cfg.StaffAccessTokenTTL,
		RefreshTokenTTL:    cfg.StaffRefreshTokenTTL,
		SessionIdleTimeout: cfg.StaffSessionIdleTimeout,
		MaxSessions:        cfg.StaffMaxSessions,
		PINMaxFailures:     cfg.StaffPINMaxFailures,
		PINLockout:         cfg.StaffPINLockout,
	}
	authService := service.NewAuthService(
		db,
		postgres.NewAuthRepository(),
		cfg.JWTSecret,
		authPolicy,
		service.WithAuthEmail(emailService, cfg.AppPublicURL, ""),
		service.WithAuthSMS(messageService),
	)
	h.auth = handler.NewAuthHandler(authService)
	h.auth.DefaultStoreSlug = cfg.DefaultStoreSlug

	orderRepo := postgres.NewOrderRepository()
	orderService := service.NewOrderService(db, orderRepo)

	paymentRepo := postgres.NewPaymentRepository()
	paymentService := service.NewPaymentService(db, paymentRepo, orderRepo, midtransClient)
	h.offline = handler.NewOfflineHandler(service.NewOfflineSyncService(db, orderService, paymentService))
	h.midtrans.SetPaymentService(paymentService)

	h.pos = handler.NewPOSHandler(orderService, paymentService, service.NewSettingsService(db))
	operationalService := service.NewOperationalService(
		db,
		orderRepo,
		paymentRepo,
		postgres.NewShiftRepository(),
		postgres.NewAuthRepository(),
		postgres.NewSettingsRepository(),
	)
	operationalService.SetPINPolicy(authPolicy, emailService)
	operationalService.SetGateway(midtransClient) // RF1-a Direct Refund
	h.pos.SetOperational(operationalService)

	h.approval = handler.NewApprovalHandler(service.NewApprovalService(db, operationalService))
	catalogService := service.NewCatalogService(db)
	h.catalog = handler.NewCatalogHandler(catalogService)
	h.inventory = handler.NewInventoryHandler(service.NewInventoryService(db))
	h.device = handler.NewDeviceHandler(service.NewDeviceService(db, catalogService))

	shiftService := service.NewShiftService(db)
	shiftService.SetPINPolicy(authPolicy, emailService)
	h.shift = handler.NewShiftHandler(shiftService)

	h.tab = handler.NewTabHandler(service.NewTabService(db, postgres.NewTabRepository(), orderRepo))
	tableService := service.NewTableService(db)
	h.table = handler.NewTableHandler(tableService)
	// Table timer warnings (v0.9 PR 9.2, W6).
	go tableService.StartTimerWorker(workerCtx, 30*time.Second)
	h.guest = handler.NewGuestHandler(service.NewGuestService(db, orderService, paymentService))
	h.kds = handler.NewKDSHandler(service.NewKDSService(db))
	receiptService := service.NewReceiptService(db, cfg.AppPublicURL)
	h.receipt = handler.NewReceiptHandler(receiptService)
	h.receiptOut = handler.NewReceiptDeliveryHandler(service.NewReceiptDeliveryService(db, receiptService, emailService, messageService))
	h.report = handler.NewReportHandler(service.NewReportService(db))
	h.orderRead = handler.NewOrderReadHandler(service.NewOrderReadService(db))
	h.outletCash = handler.NewOutletCashHandler(service.NewOutletCashService(db))

	mediaStore, err := storage.NewR2(storage.R2Config{
		AccountID:       cfg.R2AccountID,
		AccessKeyID:     cfg.R2AccessKeyID,
		SecretAccessKey: cfg.R2SecretAccessKey,
		Bucket:          cfg.R2BucketName,
		PublicURL:       cfg.R2PublicURL,
	})
	if err != nil {
		log.Printf("[MEDIA] uploads disabled: %v", err) // /media/uploads answers 503 MEDIA_UNAVAILABLE
	}
	h.media = handler.NewMediaHandler(service.NewMediaService(db, mediaStore))

	realtimeService := service.NewRealtimeService(db)
	h.realtime = handler.NewRealtimeHandler(hub, realtimeService)
	h.display = handler.NewDisplayHandler(service.NewDisplayService(db), realtimeService)
	h.print = handler.NewPrintHandler(service.NewPrintService(db))

	// Outbox relay (iteration 5c): LISTEN outbox + 2 s polling, at-least-once, ordered per store.
	go realtimeService.RunRelay(workerCtx, hub)

	// Midtrans 2-phase payment reconciliation worker
	go paymentService.StartReconciliationWorker(workerCtx, 30*time.Second, 2*time.Minute, 50)

	return h
}

// registerRoutes mounts all API v1 endpoints, health probes, and WebSocket upgrades on router.
func registerRoutes(router *gin.Engine, h *handlerSet) {
	v1 := router.Group("/api/v1")
	v1.GET("/health", h.health.Check)
	v1.POST("/payments/midtrans/webhook", h.midtrans.HandleWebhook)

	if h.auth != nil {
		registerStaffAuthRoutes(v1, h)
		registerStoreAndPOSRoutes(v1, h)
		registerTableAndGuestRoutes(v1, h)
		registerKDSAndCatalogRoutes(v1, h)
	}

	// Health check endpoints for Docker / LB probes
	router.GET("/health", h.health.Check)
	router.GET("/health/live", h.health.Liveness)
	router.GET("/health/ready", h.health.Check)

	// WebSocket (iteration 5c): single-use 30 s ticket from POST /api/v1/pos/ws-ticket or
	// /api/v1/public/guest/ws-ticket; replay with last_seq.
	if h.realtime != nil {
		router.GET("/ws", h.realtime.Connect)
	}

	// Root greeting
	router.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "MyCaffe Core POS Backend API is running.",
			"version": "1.0.0",
			"docs":    "/swagger/index.html",
		})
	})
}

func registerStaffAuthRoutes(v1 *gin.RouterGroup, h *handlerSet) {
	staffAuth := v1.Group("/auth/staff")
	staffAuth.POST("/login", h.auth.Login)
	staffAuth.POST("/refresh", h.auth.Refresh)
	staffAuth.POST("/logout", h.auth.Logout)
	staffAuth.GET("/me", h.auth.Me)
	staffAuth.POST("/pin/forgot", h.auth.ForgotPIN)
	staffAuth.POST("/pin/forgot-sms", h.auth.ForgotPINSMS)
	staffAuth.POST("/pin/reset-sms", h.auth.ResetPINSMS)
	staffAuth.POST("/pin/setup", h.auth.SetupPIN)
	staffAuth.POST("/pin/change", h.auth.RequireStaff(), h.auth.ChangePIN)

	owner := v1.Group("/staff", h.auth.RequirePermission(rbac.StaffManage))
	owner.GET("", h.auth.ListStaff)
	owner.POST("", h.auth.InviteStaff)
	owner.POST("/:id/pin-reset", h.auth.SendPINReset)
	owner.POST("/:id/temporary-pin", h.auth.IssueTemporaryPIN)
	owner.POST("/:id/phone", h.auth.SetStaffPhone)
	owner.POST("/:id/badge", h.auth.IssueBadge)
	owner.POST("/:id/badge/revoke", h.auth.RevokeBadge)
}

func registerStoreAndPOSRoutes(v1 *gin.RouterGroup, h *handlerSet) {
	store := v1.Group("/store")
	store.GET("/settings", h.auth.RequireStaff(), h.pos.GetSettings)
	store.PATCH("/settings", h.auth.RequirePermission(rbac.StoreSettingsManage), h.pos.UpdateSettings)

	// POS (ADR-003 D8 "staff" class): store and actor come from the access token only.
	pos := v1.Group("/pos")
	pos.POST("/orders", h.auth.RequirePermission(rbac.OrderPOSCreate), h.pos.CreateOrder)
	pos.POST("/orders/quote", h.auth.RequirePermission(rbac.OrderPOSCreate), h.pos.QuoteOrder)
	pos.POST("/orders/:id/payments", h.auth.RequireStaff(), h.pos.InitiatePayment)
	pos.GET("/catalog", h.auth.RequireStaff(), h.catalog.StaffCatalog)

	// Cash Drawers & Shifts (Iteration 4b)
	pos.POST("/drawers", h.auth.RequirePermission(rbac.CashDrawerManage), h.shift.CreateDrawer)
	pos.GET("/drawers", h.auth.RequireStaff(), h.shift.ListDrawers)
	pos.PATCH("/drawers/:id", h.auth.RequirePermission(rbac.CashDrawerManage), h.shift.UpdateDrawer)

	pos.POST("/shifts", h.auth.RequirePermission(rbac.ShiftOpen), h.shift.OpenShift)
	pos.GET("/shifts/current", h.auth.RequireStaff(), h.shift.GetCurrentShift)
	pos.GET("/outlet-cash", h.auth.RequirePermission(rbac.CashMovementCreate), h.outletCash.Summary)
	pos.POST("/outlet-cash/movements", h.auth.RequirePermission(rbac.CashMovementCreate), h.outletCash.Record)
	pos.POST("/shifts/movements", h.auth.RequirePermission(rbac.CashMovementCreate), h.shift.CreateCashMovement)
	pos.POST("/shifts/:id/close", h.auth.RequirePermission(rbac.ShiftClose), h.shift.CloseShift)
	pos.POST("/shifts/:id/force-close", h.auth.RequirePermission(rbac.ShiftForceClose), h.shift.ForceCloseShift)
	pos.GET("/shifts/:id/x-report", h.auth.RequireStaff(), h.shift.GetXReport)
	pos.GET("/shifts/:id/z-report", h.auth.RequireStaff(), h.shift.GetZReport)

	// Open Tabs, Rounds & Split Bill (Iteration 4c-1 & 4c-2)
	pos.POST("/tabs", h.auth.RequirePermission(rbac.TabManage), h.tab.OpenTab)
	pos.POST("/tabs/:id/rounds", h.auth.RequirePermission(rbac.TabManage), h.tab.SubmitRound)
	pos.GET("/tabs/:id", h.auth.RequireStaff(), h.tab.GetTab)
	pos.POST("/tabs/:id/close", h.auth.RequirePermission(rbac.TabManage), h.tab.CloseTab)
	pos.POST("/tabs/:id/reopen", h.auth.RequirePermission(rbac.TabManage), h.tab.ReopenTab)

	// POS Operational: Void, Refund & Stock Override (Iteration 4d)
	pos.POST("/approvals", h.auth.RequireStaff(), h.approval.Create)
	pos.GET("/approvals/:id", h.auth.RequireStaff(), h.approval.Get)
	approvals := v1.Group("/approvals", h.auth.RequirePermission(rbac.ApprovalDecide))
	approvals.GET("", h.approval.ListPending)
	approvals.POST("/:id/approve", h.approval.Approve)
	approvals.POST("/:id/reject", h.approval.Reject)
	pos.POST("/orders/:id/discount", h.auth.RequirePermission(rbac.OrderDiscountApply), h.pos.ApplyDiscount)
	pos.POST("/orders/:id/reprint", h.auth.RequirePermission(rbac.OrderPOSCreate), h.print.Reprint)
	pos.POST("/orders/:id/receipt/send", h.auth.RequirePermission(rbac.OrderPOSCreate), h.receiptOut.SendByStaff)
	pos.POST("/orders/:id/void", h.auth.RequirePermission(rbac.OrderVoid), h.pos.VoidOrder)
	pos.POST("/orders/:id/payments/:payment_id/refund", h.auth.RequirePermission(rbac.PaymentRefund), h.pos.RefundPayment)
	pos.POST("/products/:id/stock-override", h.auth.RequirePermission(rbac.StockOverride), h.pos.OverrideStock)

	// Receipts (B1, decision R1): snapshot-only JSON or ESC/POS, 90-day digital link.
	pos.POST("/customers/verify", h.auth.RequirePermission(rbac.OrderPOSCreate), h.customer.StartVerification)
	pos.POST("/customers/verify/:id/confirm", h.auth.RequirePermission(rbac.OrderPOSCreate), h.customer.ConfirmVerification)
	pos.GET("/orders", h.auth.RequirePermission(rbac.OrderReadStore), h.orderRead.List)
	pos.GET("/orders/:id", h.auth.RequirePermission(rbac.OrderReadStore), h.orderRead.Get)
	pos.GET("/orders/:id/receipt", h.auth.RequirePermission(rbac.OrderReadStore), h.receipt.GetReceipt)
	pos.POST("/orders/:id/receipt-link", h.auth.RequirePermission(rbac.OrderReadStore), h.receipt.IssueLink)
	v1.GET("/public/receipts/:token", h.receipt.PublicReceipt)
	// Queue TV displays (v0.9 PR 9.3, W7): owner manages tokens; displays read with "Authorization: Display <token>".
	displays := v1.Group("/store/display-tokens", h.auth.RequirePermission(rbac.StoreSettingsManage))
	displays.GET("", h.display.ListTokens)
	displays.POST("", h.display.CreateToken)
	displays.POST("/:id/revoke", h.display.RevokeToken)
	// Print queue (v0.9 PR 9.4, W8): owner configures printers; agents use "Authorization: Printer <token>".
	printers := v1.Group("/store/printers", h.auth.RequirePermission(rbac.StoreSettingsManage))
	printers.GET("", h.print.ListPrinters)
	printers.POST("", h.print.CreatePrinter)
	printers.PATCH("/:id", h.print.UpdatePrinter)
	printers.POST("/:id/rotate-token", h.print.RotateToken)
	agent := v1.Group("/print-agent", handler.LimitBody(4<<10))
	agent.POST("/jobs/claim", h.print.Claim)
	agent.POST("/jobs/:id/ack", h.print.Ack)
	agent.GET("/printer", h.print.AgentPrinter)
	// POS devices (v0.10 PR 10.1, ADR-005): owner registers/revokes; devices use "Authorization: Device <credential>".
	devices := v1.Group("/store/devices", h.auth.RequirePermission(rbac.DeviceManage))
	devices.GET("", h.device.ListDevices)
	devices.POST("", h.device.RegisterDevice)
	devices.PATCH("/:id", h.device.RenameDevice)
	devices.POST("/:id/revoke", h.device.RevokeDevice)
	v1.GET("/device/catalog", h.device.Catalog)
	// Offline order sync (v0.10 PR 10.2): one result per order; rejected orders wait in the owner review queue.
	v1.POST("/device/offline-orders/sync", handler.LimitBody(512<<10), h.offline.Sync)
	offline := v1.Group("/store/offline-submissions", h.auth.RequirePermission(rbac.DeviceManage))
	offline.GET("", h.offline.ListSubmissions)
	offline.POST("/:id/resolve", h.offline.ResolveSubmission)
	v1.GET("/public/queue", h.display.Queue)
	v1.POST("/public/queue/ws-ticket", h.display.IssueTicket)

	// Owner reports (B2): read-only, business day in the store time zone, JSON or CSV.
	reports := v1.Group("/reports", h.auth.RequirePermission(rbac.ReportFinanceRead))
	reports.GET("/sales", h.report.Sales)
	reports.GET("/summary", h.report.Summary)

	// Media uploads (B3, K9): presigned PUT to R2, then verify + JPEG variants + link to the product.
	media := v1.Group("/media", handler.LimitBody(4<<10), h.auth.RequirePermission(rbac.CatalogManage))
	media.POST("/uploads", h.media.CreateUpload)
	media.POST("/uploads/:id/complete", h.media.Complete)
}

func registerTableAndGuestRoutes(v1 *gin.RouterGroup, h *handlerSet) {
	pos := v1.Group("/pos")
	// Tables & table sessions (iteration 5a): table board for staff, CRUD for the owner (QR3).
	pos.GET("/tables", h.auth.RequireStaff(), h.table.ListTables)
	pos.POST("/tables", h.auth.RequirePermission(rbac.TableManage), h.table.CreateTable)
	pos.PATCH("/tables/:id", h.auth.RequirePermission(rbac.TableManage), h.table.UpdateTable)
	pos.POST("/tables/:id/archive", h.auth.RequirePermission(rbac.TableManage), h.table.ArchiveTable)
	pos.POST("/tables/:id/rotate-qr", h.auth.RequirePermission(rbac.TableQRManage), h.table.RotateQR)
	pos.POST("/tables/:id/sessions", h.auth.RequirePermission(rbac.TabManage), h.table.OpenSession)
	pos.POST("/table-sessions/:id/close", h.auth.RequirePermission(rbac.TabManage), h.table.CloseSession)
	pos.POST("/table-sessions/:id/move", h.auth.RequirePermission(rbac.TabManage), h.table.MoveSession)
	pos.POST("/table-sessions/:id/merge", h.auth.RequirePermission(rbac.TabManage), h.table.MergeSession)
	pos.POST("/table-sessions/:id/split", h.auth.RequirePermission(rbac.TabManage), h.table.SplitTable)
	pos.POST("/table-sessions/:id/extend", h.auth.RequirePermission(rbac.TabManage), h.table.ExtendTimer)
	v1.GET("/public/qr/:token", h.table.ResolveQR)

	// QR guest ordering (iteration 5b): pay-first per device (T2), own orders only (Q6).
	v1.POST("/public/qr/:token/session", h.guest.StartSession)
	v1.GET("/public/orders/track/:token", h.guest.Track)
	guest := v1.Group("/public/guest", handler.LimitBody(64<<10), h.guest.RequireGuest())
	guest.GET("/session", h.guest.Session)
	guest.GET("/orders", h.guest.ListOrders)
	guest.POST("/orders", h.guest.CreateOrder)
	guest.POST("/orders/:id/payments", h.guest.InitiatePayment)
	guest.POST("/orders/:id/receipt/send", h.receiptOut.SendByGuest)
	guest.POST("/ws-ticket", h.realtime.IssueGuestTicket)
	pos.POST("/ws-ticket", h.auth.RequireStaff(), h.realtime.IssueStaffTicket)
}

func registerKDSAndCatalogRoutes(v1 *gin.RouterGroup, h *handlerSet) {
	// KDS (iteration 5d): station permissions are checked per ticket in the service.
	kds := v1.Group("/kds", h.auth.RequireStaff())
	kds.GET("/tickets", h.kds.ListTickets)
	kds.POST("/tickets/:id/transition", h.kds.Transition)

	// Guest menu & quote (public, cacheable catalog; dynamic quote for guest carts).
	v1.GET("/public/stores/:slug/catalog", h.catalog.PublicCatalog)
	v1.POST("/public/stores/:slug/orders/quote", h.pos.PublicQuoteOrder)

	// Catalog management (catalog C3): owner CRUD; availability toggles follow the station permission.
	manage := h.auth.RequirePermission(rbac.CatalogManage)
	cat := v1.Group("/catalog")
	cat.POST("/categories", manage, h.catalog.CreateCategory)
	cat.PATCH("/categories/:id", manage, h.catalog.UpdateCategory)
	cat.DELETE("/categories/:id", manage, h.catalog.DeleteCategory)
	cat.POST("/products", manage, h.catalog.CreateProduct)
	cat.PATCH("/products/:id", manage, h.catalog.UpdateProduct)
	cat.DELETE("/products/:id", manage, h.catalog.ArchiveProduct)
	cat.PUT("/products/:id/bundle-slots", manage, h.catalog.SetBundleSlots)
	cat.PATCH("/products/:id/availability", h.auth.RequireStaff(), h.catalog.SetAvailability)
	cat.POST("/products/:id/variants", manage, h.catalog.CreateVariant)
	cat.PATCH("/variants/:id", manage, h.catalog.UpdateVariant)
	cat.DELETE("/variants/:id", manage, h.catalog.ArchiveVariant)
	// Ingredients and recipes (v0.11 PR 11.3, K2): deducted when an order is paid, COGS per line (X10).
	inv := v1.Group("/inventory", h.auth.RequirePermission(rbac.InventoryManage))
	inv.GET("/ingredients", h.inventory.ListIngredients)
	inv.POST("/ingredients", h.inventory.CreateIngredient)
	inv.PATCH("/ingredients/:id", h.inventory.UpdateIngredient)
	inv.GET("/recipes/:kind/:id", h.inventory.GetRecipe)
	inv.PUT("/recipes/:kind/:id", h.inventory.SetRecipe)
	inv.PUT("/products/:id/unit-cost", h.inventory.SetProductUnitCost)
	// Purchasing (v0.11 PR 11.4, X7): suppliers, POs with staged receipts, direct purchases, moving-average cost.
	inv.GET("/suppliers", h.inventory.ListSuppliers)
	inv.POST("/suppliers", h.inventory.CreateSupplier)
	inv.PATCH("/suppliers/:id", h.inventory.UpdateSupplier)
	inv.GET("/purchase-orders", h.inventory.ListPurchaseOrders)
	inv.POST("/purchase-orders", h.inventory.CreatePurchaseOrder)
	inv.GET("/purchase-orders/:id", h.inventory.GetPurchaseOrder)
	inv.PATCH("/purchase-orders/:id", h.inventory.UpdatePurchaseOrder)
	inv.POST("/purchase-orders/:id/send", h.inventory.TransitionPurchaseOrder("send"))
	inv.POST("/purchase-orders/:id/cancel", h.inventory.TransitionPurchaseOrder("cancel"))
	inv.POST("/purchase-orders/:id/close", h.inventory.TransitionPurchaseOrder("close"))
	inv.POST("/purchase-orders/:id/receipts", h.inventory.ReceivePurchaseOrder)
	inv.POST("/purchases", h.inventory.DirectPurchase)
	inv.GET("/receipts", h.inventory.ListReceipts)
	// Promotions (v0.11 PR 11.1, K7): one best promotion per line, taken off the line subtotal.
	cat.GET("/promotions", manage, h.catalog.ListPromotions)
	cat.POST("/promotions", manage, h.catalog.CreatePromotion)
	cat.PATCH("/promotions/:id", manage, h.catalog.UpdatePromotion)
	cat.POST("/modifier-groups", manage, h.catalog.CreateModifierGroup)
	cat.GET("/modifier-groups", manage, h.catalog.ListModifierGroups)
	cat.GET("/modifier-groups/:id", manage, h.catalog.GetModifierGroup)
	cat.PATCH("/modifier-groups/:id", manage, h.catalog.UpdateModifierGroup)
	cat.DELETE("/modifier-groups/:id", manage, h.catalog.ArchiveModifierGroup)
	cat.POST("/products/:id/modifier-groups", manage, h.catalog.AttachModifierGroupToProduct)
	cat.DELETE("/products/:id/modifier-groups/:group_id", manage, h.catalog.DetachModifierGroupFromProduct)
	cat.POST("/modifier-groups/:id/options", manage, h.catalog.CreateModifierOption)
	cat.PATCH("/modifier-groups/:id/options/:option_id", manage, h.catalog.UpdateModifierOption)
	cat.DELETE("/modifier-groups/:id/options/:option_id", manage, h.catalog.ArchiveModifierOption)
	cat.POST("/modifier-options/:id/child-groups", manage, h.catalog.AttachChildGroupToOption)
	cat.DELETE("/modifier-options/:id/child-groups/:group_id", manage, h.catalog.DetachChildGroupFromOption)
}
