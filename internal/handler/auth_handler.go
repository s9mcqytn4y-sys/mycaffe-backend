package handler

import (
	"errors"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

const (
	RefreshCookieName = "__Secure-mc_staff_rt"
	refreshCookiePath = "/api/v1/auth/staff"
	// CSRFHeader must be present on cookie-authenticated calls. A custom header forces a CORS
	// preflight, so cross-site forms cannot replay the SameSite=Strict cookie (ADR-003 D4).
	CSRFHeader = "X-CSRF-Protection"

	maxAuthBodyBytes = 4 * 1024
)

type AuthHandler struct {
	authService *service.AuthService
	limiter     *loginFailureLimiter
	// DefaultStoreSlug fills an omitted store_slug (single-cafe deployments, DEFAULT_STORE_SLUG).
	DefaultStoreSlug string
}

func NewAuthHandler(authService *service.AuthService) *AuthHandler {
	return &AuthHandler{
		authService: authService,
		limiter:     newLoginFailureLimiter(20, 15*time.Minute),
	}
}

type StaffLoginRequest struct {
	StoreSlug string `json:"store_slug"`
	Email     string `json:"email" binding:"required"`
	PIN       string `json:"pin" binding:"required"`
}

var pinFormat = regexp.MustCompile(`^[0-9]{6}$`)

// StaffSessionResponse matches the OpenAPI StaffSessionResponse schema.
type StaffSessionResponse struct {
	AccessToken          string                `json:"access_token"`
	TokenType            string                `json:"token_type"`
	AccessTokenExpiresAt time.Time             `json:"access_token_expires_at"`
	SessionExpiresAt     time.Time             `json:"session_expires_at"`
	Staff                service.StaffIdentity `json:"staff"`
	// PINChangeRequired: the staff member signed in with a temporary PIN and must change it.
	PINChangeRequired bool `json:"pin_change_required"`
}

// StaffMeResponse matches the OpenAPI StaffMeResponse schema.
type StaffMeResponse struct {
	Staff service.StaffIdentity `json:"staff"`
	// Permissions: effective RBAC set; a UI hint only, every request is re-checked server-side.
	Permissions       []string  `json:"permissions"`
	SessionExpiresAt  time.Time `json:"session_expires_at"`
	PINChangeRequired bool      `json:"pin_change_required"`
}

func authError(c *gin.Context, status int, code, message string) {
	c.JSON(status, gin.H{"code": code, "error": message})
}

// Login handles POST /api/v1/auth/staff/login.
// @Summary Staff login (store slug + email + PIN)
// @Tags Auth
// @Accept json
// @Produce json
// @Param body body StaffLoginRequest true "Credentials"
// @Success 200 {object} StaffSessionResponse
// @Failure 400 {object} map[string]interface{}
// @Failure 401 {object} map[string]interface{}
// @Failure 429 {object} map[string]interface{}
// @Failure 503 {object} map[string]interface{}
// @Router /api/v1/auth/staff/login [post]
func (h *AuthHandler) Login(c *gin.Context) {
	ip := c.ClientIP()
	if h.limiter.Blocked(ip) {
		c.Header("Retry-After", "900")
		authError(c, http.StatusTooManyRequests, "RATE_LIMITED", "Terlalu banyak percobaan masuk. Coba lagi dalam 15 menit.")
		return
	}

	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxAuthBodyBytes)
	var req StaffLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil || !pinFormat.MatchString(req.PIN) {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", "Kode toko, email, dan PIN 6 digit wajib diisi.")
		return
	}
	if req.StoreSlug == "" {
		req.StoreSlug = h.DefaultStoreSlug
	}

	tokens, err := h.authService.Login(c.Request.Context(), service.LoginInput{
		StoreSlug: req.StoreSlug,
		Email:     req.Email,
		PIN:       req.PIN,
		UserAgent: c.Request.UserAgent(),
		ClientIP:  ip,
	})
	switch {
	case err == nil:
	case errors.Is(err, service.ErrInvalidCredentials):
		h.limiter.RecordFailure(ip)
		log.Printf("[AUTH] staff login failed request_ip=%s store_slug=%q outcome=invalid_credentials", ip, strings.ToLower(strings.TrimSpace(req.StoreSlug)))
		authError(c, http.StatusUnauthorized, "INVALID_CREDENTIALS", "Kode toko, email, atau PIN salah.")
		return
	case errors.Is(err, service.ErrAuthBusy):
		c.Header("Retry-After", "1")
		authError(c, http.StatusTooManyRequests, "RATE_LIMITED", "Server sedang sibuk. Coba lagi sebentar.")
		return
	default:
		log.Printf("[AUTH] staff login error request_ip=%s: %v", ip, err)
		authError(c, http.StatusServiceUnavailable, "SERVICE_TEMPORARILY_UNAVAILABLE", "Layanan masuk sedang tidak tersedia.")
		return
	}

	h.writeSession(c, tokens)
}

// Refresh handles POST /api/v1/auth/staff/refresh (cookie + X-CSRF-Protection: 1).
// @Summary Rotate staff refresh token
// @Tags Auth
// @Produce json
// @Success 200 {object} StaffSessionResponse
// @Failure 401 {object} map[string]interface{}
// @Failure 403 {object} map[string]interface{}
// @Router /api/v1/auth/staff/refresh [post]
func (h *AuthHandler) Refresh(c *gin.Context) {
	if c.GetHeader(CSRFHeader) != "1" {
		authError(c, http.StatusForbidden, "CSRF_HEADER_REQUIRED", "Permintaan ditolak.")
		return
	}
	cookie, err := c.Cookie(RefreshCookieName)
	if err != nil || cookie == "" {
		authError(c, http.StatusUnauthorized, "SESSION_INVALID", "Sesi berakhir. Silakan masuk kembali.")
		return
	}
	tokens, err := h.authService.Refresh(c.Request.Context(), cookie, c.Request.UserAgent(), c.ClientIP())
	if errors.Is(err, service.ErrRefreshRace) {
		// Keep the cookie: the browser already holds the rotated one from the concurrent request.
		authError(c, http.StatusConflict, "REFRESH_RACE", "Sesi baru saja diperbarui. Ulangi permintaan.")
		return
	}
	if errors.Is(err, service.ErrInvalidSession) {
		h.clearRefreshCookie(c)
		authError(c, http.StatusUnauthorized, "SESSION_INVALID", "Sesi berakhir. Silakan masuk kembali.")
		return
	}
	if err != nil {
		log.Printf("[AUTH] staff refresh error request_ip=%s: %v", c.ClientIP(), err)
		authError(c, http.StatusServiceUnavailable, "SERVICE_TEMPORARILY_UNAVAILABLE", "Layanan sesi sedang tidak tersedia.")
		return
	}
	h.writeSession(c, tokens)
}

// Logout handles POST /api/v1/auth/staff/logout (cookie + X-CSRF-Protection: 1).
// @Summary Revoke the current staff session
// @Tags Auth
// @Success 204
// @Failure 403 {object} map[string]interface{}
// @Router /api/v1/auth/staff/logout [post]
func (h *AuthHandler) Logout(c *gin.Context) {
	if c.GetHeader(CSRFHeader) != "1" {
		authError(c, http.StatusForbidden, "CSRF_HEADER_REQUIRED", "Permintaan ditolak.")
		return
	}
	if cookie, err := c.Cookie(RefreshCookieName); err == nil && cookie != "" {
		if err := h.authService.Logout(c.Request.Context(), cookie); err != nil {
			log.Printf("[AUTH] staff logout error request_ip=%s: %v", c.ClientIP(), err)
			authError(c, http.StatusServiceUnavailable, "SERVICE_TEMPORARILY_UNAVAILABLE", "Layanan sesi sedang tidak tersedia.")
			return
		}
	}
	h.clearRefreshCookie(c)
	c.Status(http.StatusNoContent)
}

// Me handles GET /api/v1/auth/staff/me (Authorization: Bearer <access token>).
// @Summary Current staff identity
// @Tags Auth
// @Produce json
// @Success 200 {object} service.StaffIdentity
// @Failure 401 {object} map[string]interface{}
// @Router /api/v1/auth/staff/me [get]
func (h *AuthHandler) Me(c *gin.Context) {
	token, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
	if !ok || token == "" {
		authError(c, http.StatusUnauthorized, "UNAUTHENTICATED", "Silakan masuk terlebih dahulu.")
		return
	}
	authenticated, err := h.authService.Authenticate(c.Request.Context(), token)
	switch {
	case err == nil:
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, StaffMeResponse{
			Staff:             authenticated.Staff,
			Permissions:       rbac.Strings(authenticated.Permissions),
			SessionExpiresAt:  authenticated.SessionExpiresAt,
			PINChangeRequired: authenticated.PINChangeRequired,
		})
	case errors.Is(err, service.ErrSessionStale):
		authError(c, http.StatusUnauthorized, "SESSION_STALE", "Hak akses berubah. Muat ulang sesi.")
	case errors.Is(err, service.ErrInvalidSession):
		authError(c, http.StatusUnauthorized, "SESSION_INVALID", "Sesi tidak valid. Silakan masuk kembali.")
	default:
		log.Printf("[AUTH] staff authenticate error request_ip=%s: %v", c.ClientIP(), err)
		authError(c, http.StatusServiceUnavailable, "SERVICE_TEMPORARILY_UNAVAILABLE", "Layanan sesi sedang tidak tersedia.")
	}
}

func (h *AuthHandler) writeSession(c *gin.Context, tokens *service.SessionTokens) {
	maxAge := int(time.Until(tokens.RefreshExpiresAt).Seconds())
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     RefreshCookieName,
		Value:    tokens.RefreshToken,
		Path:     refreshCookiePath,
		MaxAge:   maxAge,
		Expires:  tokens.RefreshExpiresAt,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, StaffSessionResponse{
		AccessToken:          tokens.AccessToken,
		TokenType:            "Bearer",
		AccessTokenExpiresAt: tokens.AccessExpiresAt,
		SessionExpiresAt:     tokens.RefreshExpiresAt,
		Staff:                tokens.Staff,
		PINChangeRequired:    tokens.PINChangeRequired,
	})
}

func (h *AuthHandler) clearRefreshCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     RefreshCookieName,
		Value:    "",
		Path:     refreshCookiePath,
		MaxAge:   -1,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}
