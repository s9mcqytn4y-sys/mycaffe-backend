package handler

import (
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/auth"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

const staffContextKey = "mycaffe.staff"

// RequireStaff authenticates the bearer access token against the database. The trusted identity
// is stored on the request context; handlers must never read store_id or actor ids from the body.
func (h *AuthHandler) RequireStaff() gin.HandlerFunc {
	return func(c *gin.Context) {
		if h.authenticate(c) == nil {
			return
		}
		c.Next()
	}
}

// RequirePermission authenticates like RequireStaff and then requires an RBAC permission computed
// from the database role and store options (ADR-003 D7). The check runs before c.Next(), so the
// handler never executes for a caller without the permission.
func (h *AuthHandler) RequirePermission(p rbac.Permission) gin.HandlerFunc {
	return func(c *gin.Context) {
		staff := h.authenticate(c)
		if staff == nil {
			return
		}
		if !staff.Can(p) {
			authError(c, http.StatusForbidden, "FORBIDDEN", "Peran Anda tidak diizinkan melakukan aksi ini.")
			c.Abort()
			return
		}
		c.Next()
	}
}

// authenticate validates the bearer token, stores the identity on the context, and returns it.
// On failure it writes the response, aborts, and returns nil. It never calls c.Next().
func (h *AuthHandler) authenticate(c *gin.Context) *service.AuthenticatedStaff {
	token, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
	if !ok || token == "" {
		authError(c, http.StatusUnauthorized, "UNAUTHENTICATED", "Silakan masuk terlebih dahulu.")
		c.Abort()
		return nil
	}
	staff, err := h.authService.Authenticate(c.Request.Context(), token)
	switch {
	case err == nil:
		c.Set(staffContextKey, staff)
		return staff
	case errors.Is(err, service.ErrSessionStale):
		authError(c, http.StatusUnauthorized, "SESSION_STALE", "Hak akses berubah. Muat ulang sesi.")
	case errors.Is(err, service.ErrInvalidSession):
		authError(c, http.StatusUnauthorized, "SESSION_INVALID", "Sesi tidak valid. Silakan masuk kembali.")
	default:
		log.Printf("[AUTH] authenticate error request_ip=%s: %v", c.ClientIP(), err)
		authError(c, http.StatusServiceUnavailable, "SERVICE_TEMPORARILY_UNAVAILABLE", "Layanan sesi sedang tidak tersedia.")
	}
	c.Abort()
	return nil
}

func currentStaff(c *gin.Context) *service.AuthenticatedStaff {
	staff, _ := c.Get(staffContextKey)
	s, _ := staff.(*service.AuthenticatedStaff)
	return s
}

// lifecycleError maps account-lifecycle errors to HTTP responses.
func lifecycleError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, auth.ErrPINFormat), errors.Is(err, auth.ErrPINTooWeak):
		authError(c, http.StatusBadRequest, "PIN_REJECTED", err.Error())
	case errors.Is(err, service.ErrInvalidEmail), errors.Is(err, service.ErrInvalidName), errors.Is(err, service.ErrInvalidRole):
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
	case errors.Is(err, service.ErrInvalidAccountLink):
		authError(c, http.StatusBadRequest, "LINK_INVALID", "Tautan tidak valid, sudah dipakai, atau kedaluwarsa. Minta tautan baru.")
	case errors.Is(err, service.ErrWrongCurrentPIN):
		authError(c, http.StatusUnauthorized, "INVALID_CREDENTIALS", "PIN saat ini salah.")
	case errors.Is(err, service.ErrForbidden):
		authError(c, http.StatusForbidden, "FORBIDDEN", "Peran Anda tidak diizinkan melakukan aksi ini.")
	case errors.Is(err, service.ErrStaffNotFound):
		authError(c, http.StatusNotFound, "STAFF_NOT_FOUND", "Staf tidak ditemukan.")
	case errors.Is(err, postgres.ErrEmailTaken):
		authError(c, http.StatusConflict, "EMAIL_TAKEN", "Email sudah dipakai staf lain di toko ini.")
	case errors.Is(err, service.ErrAuthBusy):
		c.Header("Retry-After", "1")
		authError(c, http.StatusTooManyRequests, "RATE_LIMITED", "Server sedang sibuk. Coba lagi sebentar.")
	default:
		log.Printf("[AUTH] account lifecycle error request_ip=%s: %v", c.ClientIP(), err)
		authError(c, http.StatusServiceUnavailable, "SERVICE_TEMPORARILY_UNAVAILABLE", "Layanan akun sedang tidak tersedia.")
	}
}

type ForgotPINRequest struct {
	StoreSlug string `json:"store_slug"`
	Email     string `json:"email" binding:"required"`
}

// ForgotPIN handles POST /api/v1/auth/staff/pin/forgot. Always 202 so it cannot be used to
// discover which emails exist.
func (h *AuthHandler) ForgotPIN(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxAuthBodyBytes)
	var req ForgotPINRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", "Kode toko dan email wajib diisi.")
		return
	}
	if req.StoreSlug == "" {
		req.StoreSlug = h.DefaultStoreSlug
	}
	if err := h.authService.RequestPINReset(c.Request.Context(), req.StoreSlug, req.Email); err != nil {
		lifecycleError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"message": "Jika email terdaftar, tautan untuk membuat PIN baru sudah dikirim. Periksa kotak masuk Anda."})
}

type SetupPINRequest struct {
	Token string `json:"token" binding:"required"`
	PIN   string `json:"pin" binding:"required"`
}

// SetupPIN handles POST /api/v1/auth/staff/pin/setup (activation or reset link).
func (h *AuthHandler) SetupPIN(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxAuthBodyBytes)
	var req SetupPINRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", "Token dan PIN wajib diisi.")
		return
	}
	result, err := h.authService.SetPINWithToken(c.Request.Context(), req.Token, req.PIN)
	if err != nil {
		lifecycleError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"message": "PIN berhasil dibuat. Silakan masuk.", "store_slug": result.StoreSlug, "email": result.Email})
}

type ChangePINRequest struct {
	CurrentPIN string `json:"current_pin" binding:"required"`
	NewPIN     string `json:"new_pin" binding:"required"`
}

// ChangePIN handles POST /api/v1/auth/staff/pin/change (any signed-in staff).
func (h *AuthHandler) ChangePIN(c *gin.Context) {
	var req ChangePINRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", "PIN lama dan PIN baru wajib diisi.")
		return
	}
	if err := h.authService.ChangePIN(c.Request.Context(), currentStaff(c), req.CurrentPIN, req.NewPIN); err != nil {
		lifecycleError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ListStaff handles GET /api/v1/staff (owner).
func (h *AuthHandler) ListStaff(c *gin.Context) {
	staff, err := h.authService.ListStaff(c.Request.Context(), currentStaff(c))
	if err != nil {
		lifecycleError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"staff": staff})
}

type InviteStaffRequest struct {
	FullName string          `json:"full_name" binding:"required"`
	Email    string          `json:"email" binding:"required"`
	Role     domain.UserRole `json:"role" binding:"required"`
}

// InviteStaff handles POST /api/v1/staff (owner): creates the account and emails an activation link.
func (h *AuthHandler) InviteStaff(c *gin.Context) {
	var req InviteStaffRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", "Nama, email, dan peran wajib diisi.")
		return
	}
	staff, err := h.authService.InviteStaff(c.Request.Context(), currentStaff(c), service.InviteStaffInput{
		FullName: req.FullName, Email: req.Email, Role: req.Role,
	})
	if err != nil {
		lifecycleError(c, err)
		return
	}
	c.JSON(http.StatusCreated, staff)
}

func staffIDParam(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil || id == uuid.Nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", "ID staf tidak valid.")
		return uuid.Nil, false
	}
	return id, true
}

// SendPINReset handles POST /api/v1/staff/:id/pin-reset (owner).
func (h *AuthHandler) SendPINReset(c *gin.Context) {
	id, ok := staffIDParam(c)
	if !ok {
		return
	}
	if err := h.authService.SendPINResetLink(c.Request.Context(), currentStaff(c), id); err != nil {
		lifecycleError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"message": "Tautan atur ulang PIN dikirim ke email staf."})
}

type TemporaryPINRequest struct {
	PIN string `json:"pin" binding:"required"`
}

// IssueTemporaryPIN handles POST /api/v1/staff/:id/temporary-pin (owner; staff without email).
func (h *AuthHandler) IssueTemporaryPIN(c *gin.Context) {
	id, ok := staffIDParam(c)
	if !ok {
		return
	}
	var req TemporaryPINRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", "PIN sementara wajib diisi.")
		return
	}
	if err := h.authService.IssueTemporaryPIN(c.Request.Context(), currentStaff(c), id, req.PIN); err != nil {
		lifecycleError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// otpError maps the SMS one-time-code errors (decision V4) and falls back to lifecycleError.
func otpError(c *gin.Context, err error) {
	var conflict *service.ConflictError
	var validation *service.ValidationError
	switch {
	case errors.Is(err, service.ErrSMSUnavailable):
		authError(c, http.StatusServiceUnavailable, "SMS_UNAVAILABLE", "Pengiriman SMS belum diaktifkan di toko ini.")
	case errors.Is(err, service.ErrOTPInvalid):
		authError(c, http.StatusBadRequest, "OTP_INVALID", "Kode OTP salah atau kedaluwarsa. Minta kode baru.")
	case errors.Is(err, service.ErrOTPRateLimited):
		c.Header("Retry-After", "60")
		authError(c, http.StatusTooManyRequests, "OTP_RATE_LIMITED", "Tunggu sebentar sebelum meminta kode OTP lagi.")
	case errors.Is(err, service.ErrInvalidPhone):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"code": "VALIDATION_FAILED", "error": "Periksa kembali isian yang ditandai.", "fields": map[string]string{"phone": "nomor HP tidak valid (contoh 081234567890)"}})
	case errors.As(err, &validation):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"code": "VALIDATION_FAILED", "error": "Periksa kembali isian yang ditandai.", "fields": validation.Fields})
	case errors.As(err, &conflict):
		c.JSON(http.StatusConflict, gin.H{"code": "CONFLICT", "error": conflict.Message, "field": conflict.Field})
	case errors.Is(err, service.ErrVerificationNotFound):
		authError(c, http.StatusNotFound, "NOT_FOUND", "Verifikasi tidak ditemukan.")
	default:
		lifecycleError(c, err)
	}
}

// smsBudget caps public SMS requests per client IP: each one costs money (shared with login failures).
func (h *AuthHandler) smsBudget(c *gin.Context) bool {
	ip := c.ClientIP()
	if h.limiter.Blocked(ip) {
		c.Header("Retry-After", "900")
		authError(c, http.StatusTooManyRequests, "RATE_LIMITED", "Terlalu banyak percobaan. Coba lagi nanti.")
		return false
	}
	h.limiter.RecordFailure(ip)
	return true
}

// ForgotPINSMS handles POST /api/v1/auth/staff/pin/forgot-sms. Always 202 (unless SMS is off) so it
// cannot be used to find staff phone numbers.
func (h *AuthHandler) ForgotPINSMS(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxAuthBodyBytes)
	var req struct {
		StoreSlug string `json:"store_slug"`
		Phone     string `json:"phone" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", "Kode toko dan nomor HP wajib diisi.")
		return
	}
	if !h.smsBudget(c) {
		return
	}
	if req.StoreSlug == "" {
		req.StoreSlug = h.DefaultStoreSlug
	}
	if err := h.authService.RequestPINResetSMS(c.Request.Context(), req.StoreSlug, req.Phone); err != nil {
		otpError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"message": "Jika nomor HP terdaftar, kode OTP untuk membuat PIN baru sudah dikirim lewat SMS."})
}

// ResetPINSMS handles POST /api/v1/auth/staff/pin/reset-sms {store_slug, phone, code, pin}.
func (h *AuthHandler) ResetPINSMS(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxAuthBodyBytes)
	var req struct {
		StoreSlug string `json:"store_slug"`
		Phone     string `json:"phone" binding:"required"`
		Code      string `json:"code" binding:"required"`
		PIN       string `json:"pin" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", "Nomor HP, kode OTP, dan PIN baru wajib diisi.")
		return
	}
	if !h.smsBudget(c) {
		return
	}
	if req.StoreSlug == "" {
		req.StoreSlug = h.DefaultStoreSlug
	}
	res, err := h.authService.ResetPINWithSMS(c.Request.Context(), req.StoreSlug, req.Phone, req.Code, req.PIN)
	if err != nil {
		otpError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// SetStaffPhone handles POST /api/v1/staff/:id/phone {phone} (owner).
func (h *AuthHandler) SetStaffPhone(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		authError(c, http.StatusNotFound, "STAFF_NOT_FOUND", "Staf tidak ditemukan.")
		return
	}
	var req struct {
		Phone string `json:"phone" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", "Nomor HP wajib diisi.")
		return
	}
	phone, err := h.authService.SetStaffPhone(c.Request.Context(), currentStaff(c), id, req.Phone)
	if err != nil {
		otpError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"id": id, "phone": phone})
}
