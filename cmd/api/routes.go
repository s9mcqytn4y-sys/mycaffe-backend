package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/config"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/handler"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/midtrans"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/storage"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/realtime"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/service"
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
	invoice    *handler.InvoiceHandler
	offline    *handler.OfflineHandler
	receiptOut *handler.ReceiptDeliveryHandler
	media      *handler.MediaHandler
	governance *handler.GovernanceHandler
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
	governanceService := service.NewGovernanceService(
		db,
		cfg,
		service.WithGovernanceMailer(mailSender, cfg.AppPublicURL),
	)
	paymentService := service.NewPaymentService(
		db,
		paymentRepo,
		orderRepo,
		midtransClient,
		service.WithStoreGatewayResolver(governanceService.ResolveStoreGatewayClientTx),
	)
	h.offline = handler.NewOfflineHandler(service.NewOfflineSyncService(db, orderService, paymentService))
	if cfg.PlatformMidtransServerKey != "" {
		h.midtrans.SetPlatformClient(midtrans.NewClientWithKey(cfg.PlatformMidtransServerKey, cfg.PlatformMidtransIsProduction))
	}
	h.midtrans.SetPaymentService(paymentService)
	h.midtrans.SetGovernanceService(governanceService)
	h.governance = handler.NewGovernanceHandler(governanceService, cfg.PlatformAdminAPIKey)

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
	h.invoice = handler.NewInvoiceHandler(service.NewInvoiceService(db))
	// Invoice reminders at D-3, the due date and D+7 (v0.11.1 PR 11.10, X18).
	go service.NewInvoiceReminderService(db, emailService, messageService).StartWorker(workerCtx, time.Hour)
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
	v1.POST("/payments/midtrans/webhook/:store_slug", h.midtrans.HandleTenantWebhook)

	if h.auth != nil {
		registerStaffAuthRoutes(v1, h)
		registerStoreAndPOSRoutes(v1, h)
		registerTableAndGuestRoutes(v1, h)
		registerKDSAndCatalogRoutes(v1, h)
		if h.governance != nil {
			registerGovernanceRoutes(v1, h)
			registerMobileBootstrapRoutes(v1, h)
		}
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
	pos.GET("/sales-channels", h.auth.RequirePermission(rbac.OrderPOSCreate), h.catalog.ListSalesChannels(true))
	pos.GET("/business-customers", h.auth.RequirePermission(rbac.PaymentTenderDirect), h.invoice.ListChargeableCustomers)
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
	pos.POST("/customers/lookup", h.auth.RequirePermission(rbac.OrderPOSCreate), h.customer.LookupCustomer)
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
	// B2B invoices (v0.11.1 PR 11.9, X18): charged orders billed on invoices paid in instalments.
	invoices := v1.Group("/invoices", h.auth.RequirePermission(rbac.InvoiceManage))
	invoices.GET("/customers", h.invoice.ListBusinessCustomers)
	invoices.POST("/customers", h.invoice.CreateBusinessCustomer)
	invoices.PATCH("/customers/:id", h.invoice.UpdateBusinessCustomer)
	invoices.GET("/uninvoiced-orders", h.invoice.ListUninvoicedOrders)
	invoices.GET("", h.invoice.ListInvoices)
	invoices.POST("", h.invoice.CreateInvoice)
	invoices.GET("/:id", h.invoice.GetInvoice)
	invoices.GET("/:id/pdf", h.invoice.InvoicePDF)
	invoices.POST("/:id/payments", h.invoice.PayInvoice)

	reports := v1.Group("/reports", h.auth.RequirePermission(rbac.ReportFinanceRead))
	reports.GET("/sales", h.report.Sales)
	reports.GET("/summary", h.report.Summary)
	reports.GET("/gross-profit", h.report.GrossProfit)
	reports.GET("/channels", h.report.Channels)

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
	// Payables (v0.11 PR 11.5, X7): one per receipt, paid at once or in instalments.
	inv.GET("/payables", h.inventory.ListPayables)
	inv.POST("/payables/:id/payments", h.inventory.PayPayable)
	// Stock counts and waste (v0.11 PR 11.6, X8): staff record, the owner approves sessions above the limit.
	inv.GET("/stock-sessions", h.inventory.ListStockSessions)
	inv.POST("/stock-sessions/:id/approve", h.inventory.DecideStockSession(true))
	inv.POST("/stock-sessions/:id/reject", h.inventory.DecideStockSession(false))
	count := v1.Group("/inventory", h.auth.RequirePermission(rbac.InventoryCount))
	count.GET("/stock-items", h.inventory.ListStockItems)
	count.POST("/stock-sessions", h.inventory.SubmitStockSession)
	// Promotions (v0.11 PR 11.1, K7): one best promotion per line, taken off the line subtotal.
	cat.GET("/promotions", manage, h.catalog.ListPromotions)
	cat.POST("/promotions", manage, h.catalog.CreatePromotion)
	cat.PATCH("/promotions/:id", manage, h.catalog.UpdatePromotion)
	// Sales channels (v0.11.1 PR 11.7, X12-X15): delivery-app orders typed in at the POS at channel prices.
	cat.GET("/sales-channels", manage, h.catalog.ListSalesChannels(false))
	cat.POST("/sales-channels", manage, h.catalog.CreateSalesChannel)
	cat.PATCH("/sales-channels/:id", manage, h.catalog.UpdateSalesChannel)
	cat.PUT("/sales-channels/:id/prices", manage, h.catalog.SetChannelPrices)
	// Stamp card (v0.11.1 PR 11.8, X16, X17).
	cat.GET("/stamp-program", manage, h.catalog.GetStampProgram)
	cat.PUT("/stamp-program", manage, h.catalog.SetStampProgram)
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

func registerGovernanceRoutes(v1 *gin.RouterGroup, h *handlerSet) {
	// Public SaaS Legal, Pricing, Onboarding + Email OTP & Storefront CMS (ADR-009)
	v1.GET("/public/legal", h.governance.ListPublicLegalDocuments)
	v1.GET("/public/plans", h.governance.ListPublicSubscriptionPlans)
	v1.POST("/public/onboarding/apply", handler.LimitBody(64<<10), h.governance.SubmitOnboardingApplication)
	v1.POST("/public/onboarding/:id/verify-otp", handler.LimitBody(4<<10), h.governance.VerifyOnboardingOTP)
	v1.POST("/public/onboarding/:id/resend-otp", handler.LimitBody(4<<10), h.governance.ResendOnboardingOTP)
	v1.GET("/public/stores/:slug/cms", h.governance.GetPublicStoreCMS)

	// Tenant Owner Self-Service Governance (strictly scoped via WithTenantTx + RLS)
	ownerManage := h.auth.RequirePermission(rbac.StoreSettingsManage)
	storeGov := v1.Group("/store", ownerManage)
	storeGov.GET("/legal/acceptances", h.governance.ListTenantLegalAcceptances)
	storeGov.POST("/legal/:id/accept", h.governance.AcceptLegalDocument)
	storeGov.GET("/kyb", h.governance.GetStoreKYB)
	storeGov.PUT("/kyb", h.governance.UpsertStoreKYB)
	storeGov.GET("/payment-credentials", h.governance.GetStorePaymentCredentials)
	storeGov.PUT("/payment-credentials", h.governance.UpsertStorePaymentCredentials)
	storeGov.GET("/cms", h.governance.GetStoreCMS)
	storeGov.PUT("/cms", h.governance.UpsertStoreCMS)
	storeGov.GET("/notifications", h.governance.GetStoreNotifications)
	storeGov.PUT("/notifications", h.governance.UpsertStoreNotifications)
	storeGov.GET("/network/nodes", h.governance.ListNetworkNodes)
	storeGov.PUT("/network/nodes", h.governance.UpsertNetworkNode)
	storeGov.POST("/network/simulate", h.governance.RunNetworkSimulation)
	storeGov.GET("/network/mikrotik", h.governance.GetStoreMikrotikProfile)
	storeGov.PUT("/network/mikrotik", h.governance.UpsertStoreMikrotikProfile)
	storeGov.POST("/network/mikrotik/generate-rsc", h.governance.GenerateStoreMikrotikRsc)
	storeGov.GET("/subscription/invoices", h.governance.ListStoreSubscriptionInvoices)
	storeGov.POST("/subscription/invoices", h.governance.CreateStoreSubscriptionInvoice)
	storeGov.POST("/clearance", h.governance.RequestAccountClearance)
	storeGov.POST("/clearance/:id/cancel", h.governance.CancelAccountClearance)

	// AzureDevID Platform Superadmin Console (constant-time X-Platform-Admin-Key, fails closed 503 if unset)
	platform := v1.Group("/platform", h.governance.RequirePlatformAdmin())
	platform.GET("/overview", h.governance.PlatformOverview)
	platform.GET("/onboarding", h.governance.ListOnboardingApplications)
	platform.POST("/onboarding/:id/review", h.governance.ReviewOnboardingApplication)
	platform.POST("/onboarding/:id/provision", h.governance.ProvisionOnboardingApplication)
	platform.POST("/tenants/:store_id/kyb-review", h.governance.ReviewStoreKYB)
	platform.POST("/tenants/:store_id/clearance/:id/execute", h.governance.ExecuteTenantClearance)
	platform.GET("/midtrans-readiness", h.governance.MidtransBusinessReviewReadiness)
}

func registerMobileBootstrapRoutes(v1 *gin.RouterGroup, h *handlerSet) {
	// Mobile Native Tablet POS & KDS bootstrap handshake.
	// Requires standard staff bearer authentication (accessible to any valid role).
	mobile := v1.Group("/mobile", h.auth.RequireStaff())
	mobile.GET("/bootstrap", h.governance.MobileBootstrap)
}
