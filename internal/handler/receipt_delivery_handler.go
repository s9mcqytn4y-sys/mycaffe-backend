package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/service"
)

// ReceiptDeliveryHandler sends digital receipts by e-mail, WhatsApp or SMS (v0.9 PR 9.5, W9).
type ReceiptDeliveryHandler struct {
	deliveries *service.ReceiptDeliveryService
}

func NewReceiptDeliveryHandler(deliveries *service.ReceiptDeliveryService) *ReceiptDeliveryHandler {
	return &ReceiptDeliveryHandler{deliveries: deliveries}
}

func writeReceiptDeliveryError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrWhatsAppUnavailable):
		authError(c, http.StatusServiceUnavailable, "WHATSAPP_UNAVAILABLE", "Pengiriman WhatsApp belum diaktifkan. Kirim lewat email.")
	case errors.Is(err, service.ErrSMSUnavailable):
		authError(c, http.StatusServiceUnavailable, "SMS_UNAVAILABLE", "Pengiriman SMS belum diaktifkan. Kirim lewat email.")
	case errors.Is(err, service.ErrReceiptNotPaid):
		authError(c, http.StatusConflict, "RECEIPT_NOT_READY", "Struk tersedia setelah pesanan dibayar.")
	case errors.Is(err, service.ErrReceiptSendLimit):
		authError(c, http.StatusTooManyRequests, "RECEIPT_SEND_LIMIT", "Batas kirim struk tercapai. Coba lagi besok atau tunjukkan struk di layar.")
	case errors.Is(err, service.ErrPhoneNotVerified):
		authError(c, http.StatusUnprocessableEntity, "PHONE_NOT_VERIFIED", "Nomor HP belum diverifikasi. Verifikasi dulu atau kirim lewat email.")
	case errors.Is(err, service.ErrOrderNotFound):
		authError(c, http.StatusNotFound, "NOT_FOUND", "Pesanan tidak ditemukan.")
	case errors.Is(err, service.ErrGuestSessionInvalid):
		writeGuestError(c, err)
	default:
		writeServiceError(c, err)
	}
}

// SendByStaff handles POST /api/v1/pos/orders/:id/receipt/send {channel, to}.
func (h *ReceiptDeliveryHandler) SendByStaff(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var in service.ReceiptSendInput
	if !bindJSON(c, &in) {
		return
	}
	out, err := h.deliveries.SendByStaff(c.Request.Context(), currentStaff(c), id, in)
	if err != nil {
		writeReceiptDeliveryError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, out)
}

// SendByGuest handles POST /api/v1/public/guest/orders/:id/receipt/send (guest cookie + CSRF, own orders).
func (h *ReceiptDeliveryHandler) SendByGuest(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var in service.ReceiptSendInput
	if !bindJSON(c, &in) {
		return
	}
	out, err := h.deliveries.SendByGuest(c.Request.Context(), currentGuest(c), id, in)
	if err != nil {
		writeReceiptDeliveryError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, out)
}
