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
	orders   *service.OrderService
	payments *service.PaymentService
	settings *service.SettingsService
}

func NewPOSHandler(orders *service.OrderService, payments *service.PaymentService, settings *service.SettingsService) *POSHandler {
	return &POSHandler{orders: orders, payments: payments, settings: settings}
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
		CashTenderedAmount: req.CashTenderedAmount, ReconciliationRef: req.ReconciliationRef, CallbackURL: req.CallbackURL,
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
		errors.Is(err, service.ErrReconciliationRefRequired):
		authError(c, http.StatusBadRequest, "INVALID_PAYMENT", err.Error())
	case errors.Is(err, service.ErrUnauthorizedCashierTender):
		authError(c, http.StatusForbidden, "FORBIDDEN", err.Error())
	case errors.Is(err, postgres.ErrOrderNotFound), errors.Is(err, postgres.ErrStoreNotFound):
		authError(c, http.StatusNotFound, "NOT_FOUND", err.Error())
	case errors.Is(err, service.ErrNoOpenShift):
		authError(c, http.StatusConflict, "NO_OPEN_SHIFT", err.Error())
	case errors.Is(err, service.ErrTabNotClosed):
		authError(c, http.StatusConflict, "TAB_NOT_CLOSED", "Tab belum ditutup. Selesaikan dan tutup tab terlebih dahulu sebelum memproses pembayaran.")
	case errors.Is(err, service.ErrPaymentIdempotencyConflict), errors.Is(err, service.ErrSplitAlreadyPaid),
		errors.Is(err, service.ErrOrderNotPayable):
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
