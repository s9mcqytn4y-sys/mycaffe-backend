package handler

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/realtime"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

// RealtimeHandler issues single-use WebSocket tickets and upgrades GET /ws (iteration 5c). Access
// tokens never travel in the query string (ADR-003 F3); the ticket is single-use and lives 30 s.
type RealtimeHandler struct {
	hub *realtime.Hub
	svc *service.RealtimeService
}

func NewRealtimeHandler(hub *realtime.Hub, svc *service.RealtimeService) *RealtimeHandler {
	return &RealtimeHandler{hub: hub, svc: svc}
}

// IssueStaffTicket handles POST /api/v1/pos/ws-ticket (channels follow the staff member's RBAC).
func (h *RealtimeHandler) IssueStaffTicket(c *gin.Context) {
	ticket, err := h.svc.IssueStaffTicket(c.Request.Context(), currentStaff(c))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, ticket)
}

// IssueGuestTicket handles POST /api/v1/public/guest/ws-ticket (the guest's own orders only).
func (h *RealtimeHandler) IssueGuestTicket(c *gin.Context) {
	ticket, err := h.svc.IssueGuestTicket(c.Request.Context(), currentGuest(c))
	if err != nil {
		writeGuestError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, ticket)
}

// Connect handles GET /ws?ticket=...&last_seq=N. Without last_seq nothing is replayed.
func (h *RealtimeHandler) Connect(c *gin.Context) {
	ticket := c.Query("ticket")
	if ticket == "" {
		authError(c, http.StatusUnauthorized, "WS_TICKET_REQUIRED", "Tiket koneksi realtime wajib diisi.")
		return
	}
	storeID, channels, err := h.svc.Redeem(c.Request.Context(), ticket)
	if err != nil {
		if !errors.Is(err, service.ErrInvalidWSTicket) {
			log.Printf("[REALTIME] redeem error request_id=%s: %v", c.GetString(RequestIDHeader), err)
		}
		authError(c, http.StatusUnauthorized, "WS_TICKET_INVALID", "Tiket koneksi realtime tidak valid atau kedaluwarsa.")
		return
	}
	lastSeq := int64(-1)
	if v, err := strconv.ParseInt(c.Query("last_seq"), 10, 64); err == nil && v >= 0 {
		lastSeq = v
	}
	h.hub.ServeWS(c.Writer, c.Request, realtime.Subscription{
		StoreID:  storeID.String(),
		Channels: channels,
		Replay: func() ([]*realtime.EventMessage, error) {
			// The request context ends once the connection is hijacked, so replay gets its own deadline.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return h.svc.Replay(ctx, storeID, channels, lastSeq)
		},
	})
}
