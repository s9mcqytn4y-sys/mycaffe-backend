package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

// DisplayHandler serves queue TV displays (v0.9 PR 9.3, W7): owner token management and the public
// queue, authenticated with "Authorization: Display <token>".
type DisplayHandler struct {
	displays *service.DisplayService
	realtime *service.RealtimeService
}

func NewDisplayHandler(displays *service.DisplayService, realtime *service.RealtimeService) *DisplayHandler {
	return &DisplayHandler{displays: displays, realtime: realtime}
}

func writeDisplayError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrDisplayTokenInvalid):
		authError(c, http.StatusUnauthorized, "DISPLAY_TOKEN_INVALID", "Layar ini belum terdaftar atau sudah dicabut. Minta kode baru ke owner.")
	case errors.Is(err, service.ErrDisplayTokenNotFound):
		authError(c, http.StatusNotFound, "NOT_FOUND", "Layar tidak ditemukan.")
	case errors.Is(err, service.ErrDisplayTokenLimit):
		authError(c, http.StatusConflict, "DISPLAY_TOKEN_LIMIT", "Maksimal 10 layar aktif. Cabut layar yang tidak dipakai.")
	default:
		writeServiceError(c, err)
	}
}

func displayToken(c *gin.Context) string {
	token, ok := strings.CutPrefix(c.GetHeader("Authorization"), "Display ")
	if !ok {
		return ""
	}
	return strings.TrimSpace(token)
}

type createDisplayRequest struct {
	Name string `json:"name"`
}

// CreateToken handles POST /api/v1/store/display-tokens (store.settings.manage); the token is shown once.
func (h *DisplayHandler) CreateToken(c *gin.Context) {
	var req createDisplayRequest
	if !bindJSON(c, &req) {
		return
	}
	token, err := h.displays.CreateToken(c.Request.Context(), currentStaff(c), req.Name)
	if err != nil {
		writeDisplayError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, token)
}

// ListTokens handles GET /api/v1/store/display-tokens (store.settings.manage).
func (h *DisplayHandler) ListTokens(c *gin.Context) {
	tokens, err := h.displays.ListTokens(c.Request.Context(), currentStaff(c))
	if err != nil {
		writeDisplayError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"display_tokens": tokens})
}

// RevokeToken handles POST /api/v1/store/display-tokens/:id/revoke (store.settings.manage).
func (h *DisplayHandler) RevokeToken(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	if err := h.displays.RevokeToken(c.Request.Context(), currentStaff(c), id); err != nil {
		writeDisplayError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// Queue handles GET /api/v1/public/queue (display token).
func (h *DisplayHandler) Queue(c *gin.Context) {
	view, err := h.displays.Queue(c.Request.Context(), displayToken(c))
	if err != nil {
		writeDisplayError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, view)
}

// IssueTicket handles POST /api/v1/public/queue/ws-ticket: a single-use ticket for channel "queue".
func (h *DisplayHandler) IssueTicket(c *gin.Context) {
	storeID, tokenID, err := h.displays.Authenticate(c.Request.Context(), displayToken(c))
	if err != nil {
		writeDisplayError(c, err)
		return
	}
	ticket, err := h.realtime.IssueDisplayTicket(c.Request.Context(), storeID, tokenID)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, ticket)
}
