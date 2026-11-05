package handler

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

// POSHandler serves the authenticated cashier/owner routes (ADR-003 D8 class "staff").
// The store and the acting user always come from the verified access token; request bodies
// carry no store_id or actor id.
type POSHandler struct {
	orders      *service.OrderService
	payments    *service.PaymentService
	settings    *service.SettingsService
	operational *service.OperationalService
}

func NewPOSHandler(orders *service.OrderService, payments *service.PaymentService, settings *service.SettingsService) *POSHandler {
	return &POSHandler{orders: orders, payments: payments, settings: settings}
}

func (h *POSHandler) SetOperational(op *service.OperationalService) {
	h.operational = op
}

type POSCreateOrderRequest struct {
	IdempotencyKey           string                   `json:"idempotency_key" binding:"required,max=100"`
	TableNumber              string                   `json:"table_number" binding:"max=50"`
	CustomerName             string                   `json:"customer_name" binding:"max=100"`
	CustomerPhone            string                   `json:"customer_phone" binding:"max=50"`
	OrderType                domain.OrderType         `json:"order_type" binding:"required"`
	BillingMode              domain.BillingMode       `json:"billing_mode"`
	ExpectedTotal            *int64                   `json:"expected_total,omitempty"`
	ExpectedQuoteFingerprint string                   `json:"expected_quote_fingerprint,omitempty"`
	Notes                    string                   `json:"notes" binding:"max=500"`
	Items                    []service.OrderItemInput `json:"items" binding:"required,min=1,max=100"`
}

// CreateOrder handles POST /api/v1/pos/orders (permission order.pos.create).
func (h *POSHandler) CreateOrder(c *gin.Context) {
	var req POSCreateOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", "Data pesanan tidak lengkap atau tidak valid.")
		return
	}
	actor := currentStaff(c)
	actorID := actor.Staff.UserID
	name := req.CustomerName
	if name == "" {
		name = "Pelanggan"
	}
	res, err := h.orders.CreateOrder(c.Request.Context(), actor.Staff.Store.ID, service.OrderCreationInput{
		IdempotencyKey: req.IdempotencyKey, TableNumber: req.TableNumber, CustomerName: name,
		CustomerPhone: req.CustomerPhone, OrderType: req.OrderType, BillingMode: req.BillingMode,
		ActorUserID: &actorID, ExpectedTotal: req.ExpectedTotal,
		ExpectedQuoteFingerprint: req.ExpectedQuoteFingerprint, Notes: req.Notes, Items: req.Items,
	})
	if err != nil {
		orderError(c, err)
		return
	}
	status := http.StatusCreated
	if res.IsIdempotentReplay {
		status = http.StatusOK
	}
	c.JSON(status, res)
}

type OrderQuoteRequest struct {
	OrderType                domain.OrderType         `json:"order_type" binding:"required"`
	BillingMode              domain.BillingMode       `json:"billing_mode"`
	ExpectedTotal            *int64                   `json:"expected_total,omitempty"`
	ExpectedQuoteFingerprint string                   `json:"expected_quote_fingerprint,omitempty"`
	Items                    []service.OrderItemInput `json:"items" binding:"required,min=1,max=100"`
}

// QuoteOrder handles POST /api/v1/pos/orders/quote (staff POS quote).
func (h *POSHandler) QuoteOrder(c *gin.Context) {
	var req OrderQuoteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", "Data quote tidak lengkap atau tidak valid.")
		return
	}
	actor := currentStaff(c)
	actorID := actor.Staff.UserID
	res, err := h.orders.QuoteOrder(c.Request.Context(), actor.Staff.Store.ID, service.OrderQuoteInput{
		OrderType:                req.OrderType,
		BillingMode:              req.BillingMode,
		ExpectedTotal:            req.ExpectedTotal,
		ExpectedQuoteFingerprint: req.ExpectedQuoteFingerprint,
		ActorUserID:              &actorID,
		Items:                    req.Items,
	})
	if err != nil {
		orderError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// PublicQuoteOrder handles POST /api/v1/public/stores/:slug/orders/quote (guest ordering quote).
func (h *POSHandler) PublicQuoteOrder(c *gin.Context) {
	slug := strings.ToLower(strings.TrimSpace(c.Param("slug")))
	if slug == "" {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", "Store slug wajib diisi.")
		return
	}
	var req OrderQuoteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", "Data quote tidak lengkap atau tidak valid.")
		return
	}
	storeID, active, err := h.orders.ResolveStoreBySlug(c.Request.Context(), slug)
	if err != nil || !active {
		authError(c, http.StatusNotFound, "NOT_FOUND", "Toko tidak ditemukan atau tidak aktif.")
		return
	}
	res, err := h.orders.QuoteOrder(c.Request.Context(), storeID, service.OrderQuoteInput{
		OrderType:                req.OrderType,
		BillingMode:              req.BillingMode,
		ExpectedTotal:            req.ExpectedTotal,
		ExpectedQuoteFingerprint: req.ExpectedQuoteFingerprint,
		Items:                    req.Items,
	})
	if err != nil {
		orderError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

func orderError(c *gin.Context, err error) {
	var quoteErr *service.QuoteChangedError
	switch {
	case errors.As(err, &quoteErr):
		resp := gin.H{
			"code":           "QUOTE_CHANGED",
			"error":          "Total harga atau rincian pesanan telah berubah dari quote sebelumnya.",
			"expected_total": quoteErr.ExpectedTotal,
			"actual_total":   quoteErr.ActualTotal,
			"quote":          quoteErr.Quote,
		}
		if quoteErr.ExpectedFingerprint != "" || quoteErr.ActualFingerprint != "" {
			resp["expected_fingerprint"] = quoteErr.ExpectedFingerprint
			resp["actual_fingerprint"] = quoteErr.ActualFingerprint
		}
		c.JSON(http.StatusConflict, resp)
	case errors.Is(err, service.ErrInvalidOrderInput), errors.Is(err, service.ErrInvalidModifierSelection),
		errors.Is(err, service.ErrInvalidSubAddonSelection), errors.Is(err, service.ErrUnsupportedBillingMode):
		authError(c, http.StatusBadRequest, "INVALID_ORDER", err.Error())
	case errors.Is(err, service.ErrProductNotFound), errors.Is(err, service.ErrCrossTenantAccess):
		authError(c, http.StatusNotFound, "NOT_FOUND", err.Error())
	case errors.Is(err, service.ErrProductUnavailable), errors.Is(err, service.ErrInsufficientStock):
		authError(c, http.StatusConflict, "UNAVAILABLE", err.Error())
	case errors.Is(err, service.ErrIdempotencyConflict):
		authError(c, http.StatusConflict, "IDEMPOTENCY_CONFLICT", err.Error())
	case errors.Is(err, service.ErrUnauthorizedBillingMode):
		authError(c, http.StatusForbidden, "FORBIDDEN", err.Error())
	case errors.Is(err, service.ErrStoreInactive):
		authError(c, http.StatusConflict, "STORE_INACTIVE", err.Error())
	default:
		log.Printf("[POS] create order error request_ip=%s: %v", c.ClientIP(), err)
		authError(c, http.StatusServiceUnavailable, "SERVICE_TEMPORARILY_UNAVAILABLE", "Gagal membuat pesanan. Coba lagi.")
	}
}

type POSInitiatePaymentRequest struct {
	SplitID            *uuid.UUID           `json:"split_id,omitempty"`
	IdempotencyKey     string               `json:"idempotency_key" binding:"required,max=100"`
	Method             domain.PaymentMethod `json:"method" binding:"required"`
	Bank               string               `json:"bank,omitempty"`
	CashTenderedAmount int64                `json:"cash_tendered_amount,omitempty"`
	Amount             *int64               `json:"amount,omitempty"`
	ReconciliationRef  string               `json:"reconciliation_ref,omitempty" binding:"max=100"`
	CallbackURL        string               `json:"callback_url,omitempty" binding:"max=500"`
	DrawerID           *uuid.UUID           `json:"drawer_id,omitempty"`
}

// InitiatePayment handles POST /api/v1/pos/orders/:id/payments. Direct tender (cash, static QRIS,
// EDC) needs payment.tender.direct; gateway methods need payment.initiate.gateway.
func (h *POSHandler) InitiatePayment(c *gin.Context) {
	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil || orderID == uuid.Nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", "ID pesanan tidak valid.")
		return
	}
	var req POSInitiatePaymentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", "Data pembayaran tidak lengkap atau tidak valid.")
		return
	}
	actor := currentStaff(c)
	needed := rbac.PaymentInitiateGateway
	switch req.Method {
	case domain.PaymentMethodCash, domain.PaymentMethodStaticQRIS, domain.PaymentMethodEDC:
		needed = rbac.PaymentTenderDirect
	}
	if !actor.Can(needed) {
		authError(c, http.StatusForbidden, "FORBIDDEN", "Peran Anda tidak diizinkan menerima pembayaran ini.")
		return
	}
	actorID := actor.Staff.UserID
	res, err := h.payments.InitiatePayment(c.Request.Context(), actor.Staff.Store.ID, service.InitiatePaymentInput{
		OrderID: orderID, SplitID: req.SplitID, IdempotencyKey: req.IdempotencyKey, Method: req.Method, Bank: req.Bank,
		CashTenderedAmount: req.CashTenderedAmount, Amount: req.Amount, ReconciliationRef: req.ReconciliationRef, CallbackURL: req.CallbackURL,
		ActorUserID: &actorID,
		DrawerID:    req.DrawerID,
	})
	if err != nil {
		paymentError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

func paymentError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrPaymentIdempotencyKeyRequired), errors.Is(err, service.ErrUnsupportedPaymentMethod),
		errors.Is(err, service.ErrPaymentMethodNotEnabled), errors.Is(err, service.ErrInsufficientCashTendered),
		errors.Is(err, service.ErrReconciliationRefRequired), errors.Is(err, service.ErrSplitIDRequired),
		errors.Is(err, service.ErrSplitIDNotAllowed):
		authError(c, http.StatusBadRequest, "INVALID_PAYMENT", err.Error())
	case errors.Is(err, service.ErrInvalidTenderAmount):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"code": "VALIDATION_FAILED", "error": "Periksa kembali isian yang ditandai.", "fields": map[string]string{"amount": "harus lebih dari 0 dan tidak melebihi sisa tagihan"}})
	case errors.Is(err, service.ErrGatewayMustPayRemaining):
		authError(c, http.StatusConflict, "GATEWAY_AMOUNT_MUST_BE_REMAINING", "QRIS dan pembayaran gateway harus melunasi seluruh sisa tagihan.")
	case errors.Is(err, service.ErrUnauthorizedCashierTender):
		authError(c, http.StatusForbidden, "FORBIDDEN", err.Error())
	case errors.Is(err, postgres.ErrOrderNotFound), errors.Is(err, postgres.ErrStoreNotFound):
		authError(c, http.StatusNotFound, "NOT_FOUND", err.Error())
	case errors.Is(err, service.ErrNoOpenShift):
		authError(c, http.StatusConflict, "NO_OPEN_SHIFT", err.Error())
	case errors.Is(err, service.ErrTabNotClosed):
		authError(c, http.StatusConflict, "TAB_NOT_CLOSED", "Tab belum ditutup. Selesaikan dan tutup tab terlebih dahulu sebelum memproses pembayaran.")
	case errors.Is(err, service.ErrSplitPaymentPending):
		authError(c, http.StatusConflict, "SPLIT_PAYMENT_PENDING", "Split bill memiliki transaksi pembayaran yang sedang gantung (pending). Batalkan atau tunggu kedaluwarsa.")
	case errors.Is(err, service.ErrSplitAlreadyPaid):
		authError(c, http.StatusConflict, "SPLIT_ALREADY_PAID", "Split pembayaran ini sudah lunas.")
	case errors.Is(err, service.ErrPaymentIdempotencyConflict), errors.Is(err, service.ErrOrderNotPayable):
		authError(c, http.StatusConflict, "PAYMENT_CONFLICT", err.Error())
	default:
		log.Printf("[POS] initiate payment error request_ip=%s: %v", c.ClientIP(), err)
		authError(c, http.StatusServiceUnavailable, "SERVICE_TEMPORARILY_UNAVAILABLE", "Gagal memproses pembayaran. Coba lagi.")
	}
}

// GetSettings handles GET /api/v1/store/settings (any signed-in staff; POS needs the thresholds).
func (h *POSHandler) GetSettings(c *gin.Context) {
	settings, err := h.settings.Get(c.Request.Context(), currentStaff(c))
	if err != nil {
		lifecycleError(c, err)
		return
	}
	c.JSON(http.StatusOK, settings)
}

// UpdateSettings handles PATCH /api/v1/store/settings (permission store.settings.manage).
func (h *POSHandler) UpdateSettings(c *gin.Context) {
	var patch service.StoreSettingsPatch
	if err := c.ShouldBindJSON(&patch); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", "Format pengaturan tidak valid.")
		return
	}
	settings, err := h.settings.Update(c.Request.Context(), currentStaff(c), patch)
	if errors.Is(err, service.ErrInvalidSettings) {
		authError(c, http.StatusBadRequest, "INVALID_SETTINGS", err.Error())
		return
	}
	if err != nil {
		lifecycleError(c, err)
		return
	}
	c.JSON(http.StatusOK, settings)
}

// VoidOrder handles POST /pos/orders/:id/void (permission order.void).
func (h *POSHandler) VoidOrder(c *gin.Context) {
	if h.operational == nil {
		authError(c, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Layanan operasional belum aktif.")
		return
	}
	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		authError(c, http.StatusBadRequest, "INVALID_ID", "ID pesanan tidak valid.")
		return
	}
	var in service.VoidOrderInput
	if err := c.ShouldBindJSON(&in); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", "Data pembatalan pesanan tidak valid.")
		return
	}
	res, err := h.operational.VoidOrder(c.Request.Context(), currentStaff(c), orderID, in)
	if err != nil {
		writeOperationalError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// ApplyDiscount handles POST /api/v1/pos/orders/:id/discount (order.discount.apply).
func (h *POSHandler) ApplyDiscount(c *gin.Context) {
	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		authError(c, http.StatusNotFound, "NOT_FOUND", "Pesanan tidak ditemukan.")
		return
	}
	var in service.ApplyDiscountInput
	if err := c.ShouldBindJSON(&in); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", "Data diskon tidak valid.")
		return
	}
	res, err := h.operational.ApplyDiscount(c.Request.Context(), currentStaff(c), orderID, in)
	if err != nil {
		writeOperationalError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// RefundPayment handles POST /pos/orders/:id/payments/:payment_id/refund (permission payment.refund).
func (h *POSHandler) RefundPayment(c *gin.Context) {
	if h.operational == nil {
		authError(c, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Layanan operasional belum aktif.")
		return
	}
	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		authError(c, http.StatusBadRequest, "INVALID_ID", "ID pesanan tidak valid.")
		return
	}
	paymentID, err := uuid.Parse(c.Param("payment_id"))
	if err != nil {
		authError(c, http.StatusBadRequest, "INVALID_ID", "ID pembayaran tidak valid.")
		return
	}
	var in service.RefundPaymentInput
	if err := c.ShouldBindJSON(&in); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", "Data pengembalian dana (refund) tidak valid.")
		return
	}
	res, err := h.operational.RefundPayment(c.Request.Context(), currentStaff(c), orderID, paymentID, in)
	if err != nil {
		writeOperationalError(c, err)
		return
	}
	if res.RefundStatus != "" {
		c.JSON(http.StatusAccepted, res) // gateway refund waiting for bank confirmation (RF1-f)
		return
	}
	c.JSON(http.StatusOK, res)
}

// OverrideStock handles POST /pos/products/:id/stock-override (permission stock.override).
func (h *POSHandler) OverrideStock(c *gin.Context) {
	if h.operational == nil {
		authError(c, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "Layanan operasional belum aktif.")
		return
	}
	productID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		authError(c, http.StatusBadRequest, "INVALID_ID", "ID produk tidak valid.")
		return
	}
	var in service.StockOverrideInput
	if err := c.ShouldBindJSON(&in); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", "Data penyesuaian stok tidak valid.")
		return
	}
	res, err := h.operational.OverrideStock(c.Request.Context(), currentStaff(c), productID, in)
	if err != nil {
		writeOperationalError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

func writeOperationalError(c *gin.Context, err error) {
	var validation *service.ValidationError
	switch {
	case errors.As(err, &validation):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"code":   "VALIDATION_FAILED",
			"error":  "Periksa kembali isian yang ditandai.",
			"fields": validation.Fields,
		})
	case errors.Is(err, service.ErrGatewayRefundUnsupported):
		authError(c, http.StatusConflict, "GATEWAY_REFUND_UNSUPPORTED", "Pembayaran ini tidak bisa direfund lewat penyedia pembayaran. Gunakan refund tunai, kas outlet, atau transfer.")
	case errors.Is(err, service.ErrGatewayPartialDisabled):
		authError(c, http.StatusConflict, "GATEWAY_PARTIAL_REFUND_DISABLED", "Refund sebagian lewat penyedia pembayaran belum diaktifkan. Gunakan refund manual.")
	case errors.Is(err, service.ErrGatewayRefundPending):
		authError(c, http.StatusConflict, "GATEWAY_REFUND_PENDING", "Refund lewat penyedia pembayaran masih menunggu konfirmasi bank.")
	case errors.Is(err, service.ErrGatewayRefundDenied):
		authError(c, http.StatusConflict, "GATEWAY_REFUND_DENIED", "Bank menolak refund ini. Gunakan refund tunai, kas outlet, atau transfer.")
	case errors.Is(err, service.ErrOrderNotDiscountable):
		authError(c, http.StatusConflict, "ORDER_NOT_DISCOUNTABLE", "Diskon hanya untuk pesanan pay-first yang belum dibayar dan belum punya diskon.")
	case errors.Is(err, service.ErrRefundExceedsPayment):
		authError(c, http.StatusConflict, "REFUND_EXCEEDS_PAYMENT", "Nilai item yang direfund melebihi sisa pembayaran ini. Refund dari pembayaran lain.")
	case errors.Is(err, service.ErrRefundLinesNotAllowed):
		authError(c, http.StatusConflict, "REFUND_LINES_NOT_ALLOWED", "Refund per item hanya untuk tagihan yang sudah lunas dan bukan split per item.")
	case errors.Is(err, service.ErrOutletInsufficientFunds):
		authError(c, http.StatusConflict, "OUTLET_INSUFFICIENT_FUNDS", "Saldo kas outlet tidak mencukupi.")
	case errors.Is(err, service.ErrApprovalInvalid):
		authError(c, http.StatusConflict, "APPROVAL_INVALID", "Persetujuan owner tidak cocok, sudah dipakai, atau kedaluwarsa. Minta persetujuan baru.")
	case errors.Is(err, service.ErrOwnerPINRequired):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"code":  "OWNER_PIN_REQUIRED",
			"error": "Total transaksi melebihi batas toleransi toko. Wajib menyertakan PIN Owner.",
		})
	case errors.Is(err, service.ErrInvalidOwnerPIN):
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":  "INVALID_OWNER_PIN",
			"error": "PIN Owner salah atau akun pemilik toko terkunci.",
		})
	case errors.Is(err, service.ErrInvalidStaffPIN):
		c.JSON(http.StatusUnauthorized, gin.H{
			"code":  "INVALID_STAFF_PIN",
			"error": "PIN staf kasir tidak valid.",
		})
	case errors.Is(err, service.ErrInsufficientCashInDrawer):
		c.JSON(http.StatusConflict, gin.H{
			"code":  "INSUFFICIENT_DRAWER_CASH",
			"error": "Saldo kas fisik di laci saat ini tidak mencukupi untuk memproses refund tunai.",
		})
	case errors.Is(err, service.ErrNoOpenShift):
		c.JSON(http.StatusConflict, gin.H{
			"code":  "NO_OPEN_SHIFT",
			"error": "Kasir tidak memiliki sesi shift terbuka di laci kas untuk refund tunai.",
		})
	case errors.Is(err, service.ErrOrderHasPayments):
		c.JSON(http.StatusConflict, gin.H{
			"code":  "ORDER_HAS_PAYMENTS",
			"error": "Pesanan sudah memiliki pembayaran (lunas, sebagian, atau masih diproses). Gunakan refund, bukan void.",
		})
	case errors.Is(err, service.ErrForbidden):
		authError(c, http.StatusForbidden, "FORBIDDEN", "Peran Anda tidak diizinkan melakukan aksi ini.")
	case errors.Is(err, service.ErrRefundWindowExceeded):
		c.JSON(http.StatusConflict, gin.H{
			"code":  "REFUND_WINDOW_EXCEEDED",
			"error": "Pembayaran ini sudah melewati batas waktu refund toko dan tidak bisa direfund di sistem.",
		})
	case errors.Is(err, service.ErrPINLocked):
		c.JSON(http.StatusTooManyRequests, gin.H{
			"code":  "PIN_LOCKED",
			"error": "Akun terkunci sementara karena terlalu banyak PIN salah. Coba lagi nanti.",
		})
	case errors.Is(err, service.ErrPINAttemptsExceeded):
		c.JSON(http.StatusTooManyRequests, gin.H{
			"code":  "PIN_ATTEMPTS_EXCEEDED",
			"error": "Terlalu banyak percobaan PIN Owner yang salah. Coba lagi dalam 15 menit.",
		})
	case errors.Is(err, service.ErrOwnerPINStoreLimit):
		c.JSON(http.StatusTooManyRequests, gin.H{
			"code":  "OWNER_PIN_STORE_LIMIT",
			"error": "Persetujuan PIN Owner diblokir sementara karena terlalu banyak PIN salah di toko ini. Owner sudah diberi tahu lewat email.",
		})
	case errors.Is(err, service.ErrProductNotTracked):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"code":  "PRODUCT_NOT_TRACKED",
			"error": "Stok produk ini belum dilacak. Kirim enable_tracking: true untuk mulai melacak stoknya.",
		})
	case errors.Is(err, service.ErrOrderAlreadyCancelled):
		c.JSON(http.StatusConflict, gin.H{
			"code":  "ORDER_ALREADY_CANCELLED",
			"error": "Pesanan sudah dalam status dibatalkan.",
		})
	case errors.Is(err, service.ErrPaymentAlreadyRefunded):
		c.JSON(http.StatusConflict, gin.H{
			"code":  "PAYMENT_ALREADY_REFUNDED",
			"error": "Pembayaran ini sudah pernah di-refund.",
		})
	case errors.Is(err, service.ErrOrderNotRefundable):
		c.JSON(http.StatusConflict, gin.H{
			"code":  "NOT_REFUNDABLE",
			"error": "Transaksi tidak dalam status yang dapat di-refund.",
		})
	case errors.Is(err, postgres.ErrOrderNotFound), errors.Is(err, postgres.ErrPaymentNotFound), errors.Is(err, service.ErrProductNotFound):
		c.JSON(http.StatusNotFound, gin.H{
			"code":  "NOT_FOUND",
			"error": "Data pesanan atau pembayaran tidak ditemukan.",
		})
	default:
		log.Printf("[pos] operational error: %v", err)
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"code":  "SERVICE_TEMPORARILY_UNAVAILABLE",
			"error": "Gagal memproses aksi operasional. Silakan coba kembali.",
		})
	}
}
