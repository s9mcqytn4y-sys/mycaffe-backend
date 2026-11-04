package handler

import (
	"errors"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

type ShiftHandler struct {
	shift *service.ShiftService
}

func NewShiftHandler(shift *service.ShiftService) *ShiftHandler {
	return &ShiftHandler{shift: shift}
}

func writeShiftServiceError(c *gin.Context, err error) {
	var validation *service.ValidationError
	var conflict *service.ConflictError
	switch {
	case errors.As(err, &validation):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"code": "VALIDATION_FAILED", "error": "Periksa kembali isian yang ditandai.", "fields": validation.Fields})
	case errors.As(err, &conflict):
		c.JSON(http.StatusConflict, gin.H{"code": "CONFLICT", "error": conflict.Message, "field": conflict.Field})
	case errors.Is(err, service.ErrVarianceExceedsTolerance):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"code": "VARIANCE_EXCEEDS_TOLERANCE", "error": "Selisih kas melebihi batas toleransi toko. Memerlukan PIN Owner dan alasan selisih."})
	case errors.Is(err, service.ErrApprovalInvalid):
		authError(c, http.StatusConflict, "APPROVAL_INVALID", "Persetujuan owner tidak cocok, sudah dipakai, atau kedaluwarsa. Minta persetujuan baru.")
	case errors.Is(err, service.ErrInvalidOwnerPIN):
		c.JSON(http.StatusUnauthorized, gin.H{"code": "INVALID_OWNER_PIN", "error": "PIN Owner yang dimasukkan tidak valid."})
	case errors.Is(err, service.ErrOwnerPINStoreLimit):
		c.JSON(http.StatusTooManyRequests, gin.H{"code": "OWNER_PIN_STORE_LIMIT", "error": "Persetujuan PIN Owner diblokir sementara karena terlalu banyak PIN salah di toko ini. Owner sudah diberi tahu lewat email."})
	case errors.Is(err, service.ErrPINLocked), errors.Is(err, service.ErrPINAttemptsExceeded):
		c.JSON(http.StatusTooManyRequests, gin.H{"code": "PIN_LOCKED", "error": "Terlalu banyak PIN Owner salah. Coba lagi dalam 15 menit."})
	case errors.Is(err, service.ErrInsufficientDrawerCash):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"code": "INSUFFICIENT_DRAWER_CASH", "error": "Saldo kas di laci tidak mencukupi untuk mutasi pengeluaran ini."})
	case errors.Is(err, service.ErrDrawerConflict):
		c.JSON(http.StatusConflict, gin.H{"code": "DRAWER_CONFLICT", "error": "Laci kas masih memiliki sesi shift aktif."})
	case errors.Is(err, service.ErrShiftConflict):
		c.JSON(http.StatusConflict, gin.H{"code": "SHIFT_CONFLICT", "error": "Sesi shift tidak dalam status terbuka atau data tidak valid."})
	case errors.Is(err, service.ErrForbidden):
		authError(c, http.StatusForbidden, "FORBIDDEN", "Peran Anda tidak diizinkan melakukan aksi ini.")
	case errors.Is(err, service.ErrInvalidSession):
		authError(c, http.StatusUnauthorized, "SESSION_INVALID", "Sesi tidak valid. Silakan masuk kembali.")
	case errors.Is(err, service.ErrCatalogNotFound):
		authError(c, http.StatusNotFound, "NOT_FOUND", "Data laci atau shift tidak ditemukan.")
	default:
		log.Printf("[API] request_id=%s %s %s error: %v", c.GetString(RequestIDHeader), c.Request.Method, c.FullPath(), err)
		authError(c, http.StatusServiceUnavailable, "SERVICE_TEMPORARILY_UNAVAILABLE", "Layanan sedang bermasalah. Coba lagi.")
	}
}

// CreateDrawer handles POST /pos/drawers (owner only)
func (h *ShiftHandler) CreateDrawer(c *gin.Context) {
	staff := currentStaff(c)
	if staff == nil {
		authError(c, http.StatusUnauthorized, "UNAUTHENTICATED", "Autentikasi diperlukan.")
		return
	}
	var in service.DrawerInput
	if !bindJSON(c, &in) {
		return
	}
	drawer, err := h.shift.CreateDrawer(c.Request.Context(), staff, in)
	if err != nil {
		writeShiftServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, drawer)
}

// ListDrawers handles GET /pos/drawers
func (h *ShiftHandler) ListDrawers(c *gin.Context) {
	staff := currentStaff(c)
	if staff == nil {
		authError(c, http.StatusUnauthorized, "UNAUTHENTICATED", "Autentikasi diperlukan.")
		return
	}
	drawers, err := h.shift.ListDrawers(c.Request.Context(), staff)
	if err != nil {
		writeShiftServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"drawers": drawers})
}

// UpdateDrawer handles PATCH /pos/drawers/:id (owner only)
func (h *ShiftHandler) UpdateDrawer(c *gin.Context) {
	staff := currentStaff(c)
	if staff == nil {
		authError(c, http.StatusUnauthorized, "UNAUTHENTICATED", "Autentikasi diperlukan.")
		return
	}
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		authError(c, http.StatusBadRequest, "INVALID_ID", "ID laci tidak valid.")
		return
	}
	var in service.DrawerInput
	if !bindJSON(c, &in) {
		return
	}
	drawer, err := h.shift.UpdateDrawer(c.Request.Context(), staff, id, in)
	if err != nil {
		writeShiftServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, drawer)
}

// OpenShift handles POST /pos/shifts
func (h *ShiftHandler) OpenShift(c *gin.Context) {
	staff := currentStaff(c)
	if staff == nil {
		authError(c, http.StatusUnauthorized, "UNAUTHENTICATED", "Autentikasi diperlukan.")
		return
	}
	var in service.OpenShiftInput
	if !bindJSON(c, &in) {
		return
	}
	res, err := h.shift.OpenShift(c.Request.Context(), staff, in)
	if err != nil {
		writeShiftServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, res)
}

// GetCurrentShift handles GET /pos/shifts/current
func (h *ShiftHandler) GetCurrentShift(c *gin.Context) {
	staff := currentStaff(c)
	if staff == nil {
		authError(c, http.StatusUnauthorized, "UNAUTHENTICATED", "Autentikasi diperlukan.")
		return
	}
	var drawerID *uuid.UUID
	if q := c.Query("drawer_id"); q != "" {
		if id, err := uuid.Parse(q); err == nil {
			drawerID = &id
		}
	}
	shift, err := h.shift.GetCurrentShift(c.Request.Context(), staff, drawerID)
	if err != nil {
		writeShiftServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, shift)
}

// CreateCashMovement handles POST /pos/shifts/movements
func (h *ShiftHandler) CreateCashMovement(c *gin.Context) {
	staff := currentStaff(c)
	if staff == nil {
		authError(c, http.StatusUnauthorized, "UNAUTHENTICATED", "Autentikasi diperlukan.")
		return
	}
	var in service.CreateCashMovementInput
	if !bindJSON(c, &in) {
		return
	}
	res, err := h.shift.CreateCashMovement(c.Request.Context(), staff, in)
	if err != nil {
		writeShiftServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, res)
}

// CloseShift handles POST /pos/shifts/:id/close
func (h *ShiftHandler) CloseShift(c *gin.Context) {
	staff := currentStaff(c)
	if staff == nil {
		authError(c, http.StatusUnauthorized, "UNAUTHENTICATED", "Autentikasi diperlukan.")
		return
	}
	shiftID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		authError(c, http.StatusBadRequest, "INVALID_ID", "ID shift tidak valid.")
		return
	}
	var in service.CloseShiftInput
	if !bindJSON(c, &in) {
		return
	}
	res, err := h.shift.CloseShift(c.Request.Context(), staff, shiftID, in)
	if err != nil {
		writeShiftServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// ForceCloseShift handles POST /pos/shifts/:id/force-close (owner only)
func (h *ShiftHandler) ForceCloseShift(c *gin.Context) {
	staff := currentStaff(c)
	if staff == nil {
		authError(c, http.StatusUnauthorized, "UNAUTHENTICATED", "Autentikasi diperlukan.")
		return
	}
	shiftID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		authError(c, http.StatusBadRequest, "INVALID_ID", "ID shift tidak valid.")
		return
	}
	var in service.ForceCloseShiftInput
	if !bindJSON(c, &in) {
		return
	}
	report, err := h.shift.ForceCloseShift(c.Request.Context(), staff, shiftID, in)
	if err != nil {
		writeShiftServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, report)
}

// GetXReport handles GET /pos/shifts/:id/x-report
func (h *ShiftHandler) GetXReport(c *gin.Context) {
	staff := currentStaff(c)
	if staff == nil {
		authError(c, http.StatusUnauthorized, "UNAUTHENTICATED", "Autentikasi diperlukan.")
		return
	}
	shiftID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		authError(c, http.StatusBadRequest, "INVALID_ID", "ID shift tidak valid.")
		return
	}
	report, err := h.shift.GetXReport(c.Request.Context(), staff, shiftID)
	if err != nil {
		writeShiftServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, report)
}

// GetZReport handles GET /pos/shifts/:id/z-report
func (h *ShiftHandler) GetZReport(c *gin.Context) {
	staff := currentStaff(c)
	if staff == nil {
		authError(c, http.StatusUnauthorized, "UNAUTHENTICATED", "Autentikasi diperlukan.")
		return
	}
	shiftID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		authError(c, http.StatusBadRequest, "INVALID_ID", "ID shift tidak valid.")
		return
	}
	report, err := h.shift.GetZReport(c.Request.Context(), staff, shiftID)
	if err != nil {
		writeShiftServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, report)
}
