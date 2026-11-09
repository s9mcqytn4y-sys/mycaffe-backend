package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/service"
)

// CustomerHandler serves customer phone verification at the POS (decision V4).
type CustomerHandler struct {
	svc *service.CustomerService
}

func NewCustomerHandler(svc *service.CustomerService) *CustomerHandler {
	return &CustomerHandler{svc: svc}
}

// StartVerification handles POST /api/v1/pos/customers/verify {phone, name?} (order.pos.create).
func (h *CustomerHandler) StartVerification(c *gin.Context) {
	var req struct {
		Phone string `json:"phone" binding:"required"`
		Name  string `json:"name"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", "Nomor HP pelanggan wajib diisi.")
		return
	}
	res, err := h.svc.StartVerification(c.Request.Context(), currentStaff(c), req.Phone, req.Name)
	if err != nil {
		otpError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, res)
}

// ConfirmVerification handles POST /api/v1/pos/customers/verify/:id/confirm {code}.
func (h *CustomerHandler) ConfirmVerification(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		authError(c, http.StatusNotFound, "NOT_FOUND", "Verifikasi tidak ditemukan.")
		return
	}
	var req struct {
		Code string `json:"code" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", "Kode OTP wajib diisi.")
		return
	}
	res, err := h.svc.ConfirmVerification(c.Request.Context(), currentStaff(c), id, req.Code)
	if err != nil {
		otpError(c, err)
		return
	}
	c.JSON(http.StatusOK, res)
}

// LookupCustomer handles POST /api/v1/pos/customers/lookup {phone}; a POST keeps the phone number out of URLs and logs.
func (h *CustomerHandler) LookupCustomer(c *gin.Context) {
	var in struct {
		Phone string `json:"phone"`
	}
	if !bindJSON(c, &in) {
		return
	}
	out, err := h.svc.LookupCustomer(c.Request.Context(), currentStaff(c), in.Phone)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
