package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/service"
)

// OfflineHandler serves offline order sync for registered devices and the owner's review queue
// (v0.10 PR 10.2, ADR-005).
type OfflineHandler struct {
	sync *service.OfflineSyncService
}

func NewOfflineHandler(sync *service.OfflineSyncService) *OfflineHandler {
	return &OfflineHandler{sync: sync}
}

func writeOfflineError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrSubmissionNotFound):
		authError(c, http.StatusNotFound, "NOT_FOUND", "Pesanan offline tidak ditemukan.")
	case errors.Is(err, service.ErrSubmissionResolved):
		authError(c, http.StatusConflict, "SUBMISSION_ALREADY_RESOLVED", "Pesanan offline ini sudah diputuskan.")
	default:
		writeDeviceError(c, err)
	}
}

// Sync handles POST /api/v1/device/offline-orders/sync ("Authorization: Device <credential>").
// Always 200 with one result per order once the device is valid; rejected orders wait for the owner.
func (h *OfflineHandler) Sync(c *gin.Context) {
	var in service.OfflineSyncInput
	if !bindJSON(c, &in) {
		return
	}
	results, err := h.sync.Sync(c.Request.Context(), deviceCredential(c), in)
	if err != nil {
		writeOfflineError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"results": results})
}

// ListSubmissions handles GET /api/v1/store/offline-submissions?status=rejected (device.manage).
func (h *OfflineHandler) ListSubmissions(c *gin.Context) {
	items, err := h.sync.ListSubmissions(c.Request.Context(), currentStaff(c), c.Query("status"))
	if err != nil {
		writeOfflineError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"submissions": items})
}

// ResolveSubmission handles POST /api/v1/store/offline-submissions/:id/resolve {decision, note}.
func (h *OfflineHandler) ResolveSubmission(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var in service.ResolveSubmissionInput
	if !bindJSON(c, &in) {
		return
	}
	res, err := h.sync.ResolveSubmission(c.Request.Context(), currentStaff(c), id, in)
	if err != nil {
		writeOfflineError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}
