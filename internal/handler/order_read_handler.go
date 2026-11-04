package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

// OrderReadHandler serves the POS order list and detail (v0.7 G-1/G-2, permission order.read.store).
type OrderReadHandler struct {
	svc *service.OrderReadService
}

func NewOrderReadHandler(svc *service.OrderReadService) *OrderReadHandler {
	return &OrderReadHandler{svc: svc}
}

// List handles GET /api/v1/pos/orders?status=&payment_status=&billing_mode=&from=&to=&shift_id=&table_id=&q=&limit=&after=.
func (h *OrderReadHandler) List(c *gin.Context) {
	res, err := h.svc.List(c.Request.Context(), currentStaff(c), service.OrderListFilter{
		Status: c.Query("status"), PaymentStatus: c.Query("payment_status"), BillingMode: c.Query("billing_mode"),
		From: c.Query("from"), To: c.Query("to"), ShiftID: c.Query("shift_id"), TableID: c.Query("table_id"),
		Query: c.Query("q"), Limit: c.Query("limit"), After: c.Query("after"),
	})
	if err != nil {
		writeOrderReadError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, res)
}

// Get handles GET /api/v1/pos/orders/:id.
func (h *OrderReadHandler) Get(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		authError(c, http.StatusNotFound, "NOT_FOUND", "Pesanan tidak ditemukan.")
		return
	}
	res, err := h.svc.Get(c.Request.Context(), currentStaff(c), id)
	if err != nil {
		writeOrderReadError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, res)
}

func writeOrderReadError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrOrderNotFound):
		authError(c, http.StatusNotFound, "NOT_FOUND", "Pesanan tidak ditemukan.")
	case errors.Is(err, service.ErrForbidden):
		authError(c, http.StatusForbidden, "FORBIDDEN", "Anda tidak memiliki izin untuk tindakan ini.")
	default:
		writeReportError(c, err) // 422 VALIDATION_FAILED or a logged 503
	}
}
