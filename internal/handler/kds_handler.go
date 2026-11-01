package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

// KDSHandler serves the station screens (iteration 5d).
type KDSHandler struct {
	kds *service.KDSService
}

func NewKDSHandler(kds *service.KDSService) *KDSHandler {
	return &KDSHandler{kds: kds}
}

func writeKDSError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrKDSTicketNotFound):
		authError(c, http.StatusNotFound, "NOT_FOUND", "Tiket dapur tidak ditemukan.")
	case errors.Is(err, service.ErrKDSStaleStatus):
		authError(c, http.StatusConflict, "KDS_STALE_STATUS", "Status tiket sudah diubah dari layar lain. Muat ulang.")
	case errors.Is(err, service.ErrKDSTransition):
		authError(c, http.StatusConflict, "KDS_TRANSITION_NOT_ALLOWED", "Perubahan status tiket ini tidak diizinkan.")
	default:
		writeServiceError(c, err)
	}
}

// ListTickets handles GET /api/v1/kds/tickets?station=bar|kitchen.
func (h *KDSHandler) ListTickets(c *gin.Context) {
	tickets, err := h.kds.ListActive(c.Request.Context(), currentStaff(c), c.Query("station"))
	if err != nil {
		writeKDSError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"tickets": tickets})
}

// Transition handles POST /api/v1/kds/tickets/:id/transition {to, expected_status}.
func (h *KDSHandler) Transition(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var in service.KDSTransitionInput
	if !bindJSON(c, &in) {
		return
	}
	ticket, err := h.kds.Transition(c.Request.Context(), currentStaff(c), id, in)
	if err != nil {
		writeKDSError(c, err)
		return
	}
	c.JSON(http.StatusOK, ticket)
}
