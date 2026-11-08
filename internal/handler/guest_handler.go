package handler

import (
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/service"
)

const (
	GuestCookieName = "__Secure-mc_guest"
	guestCookiePath = "/api/v1/public"
	guestContextKey = "guest_identity"
)

// GuestHandler serves QR guest sessions, guest orders and the public tracker (iteration 5b).
type GuestHandler struct {
	guests *service.GuestService
	// shortcut: in-process sliding windows (single API instance, ADR-002); see loginFailureLimiter.
	sessionsPerIP  *loginFailureLimiter
	ordersPerGuest *loginFailureLimiter
}

func NewGuestHandler(guests *service.GuestService) *GuestHandler {
	return &GuestHandler{
		guests:         guests,
		sessionsPerIP:  newLoginFailureLimiter(10, 10*time.Minute), // decision QR5
		ordersPerGuest: newLoginFailureLimiter(10, 10*time.Minute),
	}
}

func writeGuestError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrGuestSessionInvalid):
		authError(c, http.StatusUnauthorized, "GUEST_SESSION_EXPIRED", "Sesi pemesanan berakhir. Pindai ulang QR di meja.")
	case errors.Is(err, service.ErrQROrderingPaused):
		authError(c, http.StatusConflict, "QR_ORDERING_PAUSED", "Pemesanan lewat QR sedang dihentikan sementara. Silakan pesan ke kasir.")
	case errors.Is(err, service.ErrStoreClosed):
		authError(c, http.StatusConflict, "STORE_CLOSED", "Toko sedang tutup.")
	case errors.Is(err, service.ErrTableInactive):
		authError(c, http.StatusConflict, "TABLE_INACTIVE", "Meja ini sedang tidak menerima pesanan.")
	case errors.Is(err, service.ErrGuestUnpaidLimit):
		authError(c, http.StatusConflict, "UNPAID_ORDER_LIMIT", "Selesaikan pembayaran pesanan sebelumnya terlebih dahulu.")
	case errors.Is(err, service.ErrGuestOrderNotFound), errors.Is(err, service.ErrTableNotFound):
		authError(c, http.StatusNotFound, "NOT_FOUND", "Data tidak ditemukan.")
	default:
		var validation *service.ValidationError
		if errors.As(err, &validation) {
			writeServiceError(c, err)
			return
		}
		orderError(c, err)
	}
}

func requireCSRF(c *gin.Context) bool {
	if c.GetHeader(CSRFHeader) != "1" {
		authError(c, http.StatusForbidden, "CSRF_HEADER_REQUIRED", "Permintaan ditolak.")
		return false
	}
	return true
}

// StartSession handles POST /api/v1/public/qr/:token/session.
func (h *GuestHandler) StartSession(c *gin.Context) {
	if !requireCSRF(c) {
		return
	}
	ip := c.ClientIP()
	if h.sessionsPerIP.Blocked(ip) {
		authError(c, http.StatusTooManyRequests, "TOO_MANY_REQUESTS", "Terlalu banyak percobaan. Coba lagi beberapa menit lagi.")
		return
	}
	h.sessionsPerIP.RecordFailure(ip)
	cookie, view, err := h.guests.StartSession(c.Request.Context(), c.Param("token"))
	if err != nil {
		writeGuestError(c, err)
		return
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name: GuestCookieName, Value: cookie, Path: guestCookiePath,
		MaxAge: int(time.Until(view.ExpiresAt).Seconds()), Expires: view.ExpiresAt,
		Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode,
	})
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, view)
}

// RequireGuest authenticates the guest cookie; mutating requests also need X-CSRF-Protection: 1.
func (h *GuestHandler) RequireGuest() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodGet && !requireCSRF(c) {
			c.Abort()
			return
		}
		cookie, err := c.Cookie(GuestCookieName)
		if err != nil || cookie == "" {
			writeGuestError(c, service.ErrGuestSessionInvalid)
			c.Abort()
			return
		}
		guest, err := h.guests.Authenticate(c.Request.Context(), cookie)
		if err != nil {
			if !errors.Is(err, service.ErrGuestSessionInvalid) {
				log.Printf("[GUEST] authenticate error request_id=%s: %v", c.GetString(RequestIDHeader), err)
			}
			writeGuestError(c, err)
			c.Abort()
			return
		}
		c.Set(guestContextKey, guest)
		c.Header("Cache-Control", "no-store")
		c.Next()
	}
}

func currentGuest(c *gin.Context) *service.GuestIdentity {
	g, _ := c.Get(guestContextKey)
	guest, _ := g.(*service.GuestIdentity)
	return guest
}

// Session handles GET /api/v1/public/guest/session.
func (h *GuestHandler) Session(c *gin.Context) {
	g := currentGuest(c)
	c.JSON(http.StatusOK, service.GuestSessionView{TableNumber: g.TableNumber, ExpiresAt: g.ExpiresAt})
}

// CreateOrder handles POST /api/v1/public/guest/orders (pay-first, decision T2).
func (h *GuestHandler) CreateOrder(c *gin.Context) {
	g := currentGuest(c)
	key := g.SessionID.String()
	if h.ordersPerGuest.Blocked(key) {
		authError(c, http.StatusTooManyRequests, "TOO_MANY_REQUESTS", "Terlalu banyak percobaan pesanan. Coba lagi beberapa menit lagi.")
		return
	}
	h.ordersPerGuest.RecordFailure(key)
	var in service.GuestOrderInput
	if !bindJSON(c, &in) {
		return
	}
	res, err := h.guests.CreateOrder(c.Request.Context(), g, in)
	if err != nil {
		writeGuestError(c, err)
		return
	}
	status := http.StatusCreated
	if res.IsIdempotentReplay {
		status = http.StatusOK
	}
	c.JSON(status, res)
}

// ListOrders handles GET /api/v1/public/guest/orders (only this device's orders, decision Q6).
func (h *GuestHandler) ListOrders(c *gin.Context) {
	orders, err := h.guests.ListOrders(c.Request.Context(), currentGuest(c))
	if err != nil {
		writeGuestError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"orders": orders})
}

// InitiatePayment handles POST /api/v1/public/guest/orders/:id/payments (dynamic QRIS only).
func (h *GuestHandler) InitiatePayment(c *gin.Context) {
	orderID, ok := pathID(c, "id")
	if !ok {
		return
	}
	var req struct {
		IdempotencyKey string `json:"idempotency_key"`
	}
	if !bindJSON(c, &req) {
		return
	}
	res, err := h.guests.InitiatePayment(c.Request.Context(), currentGuest(c), orderID, req.IdempotencyKey)
	if err != nil {
		if errors.Is(err, service.ErrGuestOrderNotFound) {
			writeGuestError(c, err)
			return
		}
		var validation *service.ValidationError
		if errors.As(err, &validation) {
			writeServiceError(c, err)
			return
		}
		paymentError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// Track handles GET /api/v1/public/orders/track/:token (decision QR6).
func (h *GuestHandler) Track(c *gin.Context) {
	order, err := h.guests.Track(c.Request.Context(), c.Param("token"))
	if err != nil {
		writeGuestError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, order)
}
