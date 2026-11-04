package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

// ApprovalHandler serves remote owner approval (ADR-007): cashiers ask, owners decide on their phone.
type ApprovalHandler struct {
	svc *service.ApprovalService
}

func NewApprovalHandler(svc *service.ApprovalService) *ApprovalHandler {
	return &ApprovalHandler{svc: svc}
}

// Create handles POST /api/v1/pos/approvals (permission of the requested action, non-owners).
func (h *ApprovalHandler) Create(c *gin.Context) {
	var in service.CreateApprovalInput
	if err := c.ShouldBindJSON(&in); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", "Data permintaan persetujuan tidak valid.")
		return
	}
	res, err := h.svc.Create(c.Request.Context(), currentStaff(c), in)
	if err != nil {
		writeApprovalError(c, err)
		return
	}
	c.JSON(http.StatusCreated, res)
}

// Get handles GET /api/v1/pos/approvals/:id (requester or owner).
func (h *ApprovalHandler) Get(c *gin.Context) {
	id, ok := approvalID(c)
	if !ok {
		return
	}
	res, err := h.svc.Get(c.Request.Context(), currentStaff(c), id)
	if err != nil {
		writeApprovalError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, res)
}

// ListPending handles GET /api/v1/approvals?status=pending (approval.decide).
func (h *ApprovalHandler) ListPending(c *gin.Context) {
	if s := c.DefaultQuery("status", "pending"); s != "pending" {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"code": "VALIDATION_FAILED", "error": "Periksa kembali isian yang ditandai.", "fields": map[string]string{"status": "hanya pending"}})
		return
	}
	items, err := h.svc.ListPending(c.Request.Context(), currentStaff(c))
	if err != nil {
		writeApprovalError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"items": items})
}

// Approve handles POST /api/v1/approvals/:id/approve {pin} (approval.decide).
func (h *ApprovalHandler) Approve(c *gin.Context) {
	id, ok := approvalID(c)
	if !ok {
		return
	}
	var in struct {
		PIN string `json:"pin"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", "Data persetujuan tidak valid.")
		return
	}
	res, err := h.svc.Approve(c.Request.Context(), currentStaff(c), id, in.PIN)
	if err != nil {
		writeApprovalError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// Reject handles POST /api/v1/approvals/:id/reject {note?} (approval.decide).
func (h *ApprovalHandler) Reject(c *gin.Context) {
	id, ok := approvalID(c)
	if !ok {
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&in); err != nil {
			authError(c, http.StatusBadRequest, "INVALID_REQUEST", "Data penolakan tidak valid.")
			return
		}
	}
	res, err := h.svc.Reject(c.Request.Context(), currentStaff(c), id, in.Note)
	if err != nil {
		writeApprovalError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

func approvalID(c *gin.Context) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		authError(c, http.StatusNotFound, "NOT_FOUND", "Permintaan persetujuan tidak ditemukan.")
		return uuid.Nil, false
	}
	return id, true
}

func writeApprovalError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrApprovalNotFound):
		authError(c, http.StatusNotFound, "NOT_FOUND", "Permintaan persetujuan atau datanya tidak ditemukan.")
	case errors.Is(err, service.ErrApprovalSelf):
		authError(c, http.StatusForbidden, "APPROVAL_SELF", "Owner tidak dapat memutuskan permintaannya sendiri.")
	case errors.Is(err, service.ErrApprovalNotNeeded):
		authError(c, http.StatusConflict, "APPROVAL_NOT_NEEDED", "Aksi ini tidak memerlukan persetujuan owner; gunakan PIN Anda sendiri.")
	case errors.Is(err, service.ErrApprovalNotPending):
		authError(c, http.StatusConflict, "APPROVAL_NOT_PENDING", "Permintaan ini sudah diputuskan.")
	case errors.Is(err, service.ErrApprovalExpired):
		authError(c, http.StatusConflict, "APPROVAL_EXPIRED", "Permintaan ini sudah kedaluwarsa. Minta kasir mengirim ulang.")
	case errors.Is(err, service.ErrApprovalLimit):
		authError(c, http.StatusTooManyRequests, "APPROVAL_LIMIT", "Masih ada 3 permintaan yang menunggu. Tunggu keputusan owner.")
	default:
		writeOperationalError(c, err) // validation, forbidden, owner PIN errors and limits, 503
	}
}
