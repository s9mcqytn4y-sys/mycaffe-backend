package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

// OutletCashHandler serves the outlet cash fund (decision V13).
type OutletCashHandler struct {
	svc *service.OutletCashService
}

func NewOutletCashHandler(svc *service.OutletCashService) *OutletCashHandler {
	return &OutletCashHandler{svc: svc}
}

// Summary handles GET /api/v1/pos/outlet-cash (cash.movement.create).
func (h *OutletCashHandler) Summary(c *gin.Context) {
	res, err := h.svc.Summary(c.Request.Context(), currentStaff(c))
	if err != nil {
		writeOutletCashError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, res)
}

// Record handles POST /api/v1/pos/outlet-cash/movements {movement_type, amount, category?, note, drawer_id?, approval_id?}.
func (h *OutletCashHandler) Record(c *gin.Context) {
	var in service.OutletMovementInput
	if err := c.ShouldBindJSON(&in); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", "Data mutasi kas outlet tidak valid.")
		return
	}
	res, err := h.svc.Record(c.Request.Context(), currentStaff(c), in)
	if err != nil {
		writeOutletCashError(c, err)
		return
	}
	c.JSON(http.StatusCreated, res)
}

func writeOutletCashError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrOutletInsufficientFunds):
		authError(c, http.StatusConflict, "OUTLET_INSUFFICIENT_FUNDS", "Saldo kas outlet tidak mencukupi.")
	case errors.Is(err, service.ErrOutletApprovalRequired):
		authError(c, http.StatusConflict, "OUTLET_EXPENSE_APPROVAL_REQUIRED", "Pengeluaran melebihi batas harian. Minta persetujuan owner (outlet_expense).")
	case errors.Is(err, service.ErrNoOpenShift):
		authError(c, http.StatusConflict, "NO_OPEN_SHIFT", "Laci ini tidak punya shift yang terbuka.")
	case errors.Is(err, service.ErrInsufficientDrawerCash):
		authError(c, http.StatusConflict, "INSUFFICIENT_DRAWER_CASH", "Kas di laci tidak mencukupi untuk disetor.")
	default:
		writeOperationalError(c, err) // validation, forbidden, APPROVAL_INVALID, 503
	}
}
