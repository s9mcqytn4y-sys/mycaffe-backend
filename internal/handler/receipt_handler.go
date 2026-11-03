package handler

import (
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

// ReceiptHandler serves B1 receipts: structured JSON or ESC/POS bytes for staff, and the public
// 90-day digital receipt.
type ReceiptHandler struct {
	svc *service.ReceiptService
}

func NewReceiptHandler(svc *service.ReceiptService) *ReceiptHandler {
	return &ReceiptHandler{svc: svc}
}

// GetReceipt handles GET /api/v1/pos/orders/:id/receipt?kind=sale|refund|void&payment_id=&format=json|escpos&width=58|80.
// An ESC/POS sale receipt issues a fresh digital receipt link and prints it as a QR code.
func (h *ReceiptHandler) GetReceipt(c *gin.Context) {
	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		authError(c, http.StatusBadRequest, "INVALID_ID", "ID pesanan tidak valid.")
		return
	}
	kind := c.DefaultQuery("kind", service.ReceiptKindSale)
	format := c.DefaultQuery("format", "json")
	width, _ := strconv.Atoi(c.DefaultQuery("width", "58"))
	fields := map[string]string{}
	var paymentID *uuid.UUID
	switch kind {
	case service.ReceiptKindSale, service.ReceiptKindVoid:
	case service.ReceiptKindRefund:
		id, err := uuid.Parse(c.Query("payment_id"))
		if err != nil {
			fields["payment_id"] = "wajib diisi untuk struk refund"
		}
		paymentID = &id
	default:
		fields["kind"] = "harus sale, refund, atau void"
	}
	if format != "json" && format != "escpos" {
		fields["format"] = "harus json atau escpos"
	}
	if width != 58 && width != 80 {
		fields["width"] = "harus 58 atau 80"
	}
	if len(fields) > 0 {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"code": "VALIDATION_FAILED", "error": "Periksa kembali isian yang ditandai.", "fields": fields})
		return
	}

	actor := currentStaff(c)
	receipt, err := h.svc.Build(c.Request.Context(), actor.Staff.Store.ID, orderID, kind, paymentID)
	if err != nil {
		writeReceiptError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	if format == "json" {
		c.JSON(http.StatusOK, receipt)
		return
	}
	qrURL := ""
	if kind == service.ReceiptKindSale {
		link, err := h.svc.IssueLink(c.Request.Context(), actor, orderID)
		if err != nil {
			writeReceiptError(c, err)
			return
		}
		qrURL = link.URL
	}
	out, err := service.RenderESCPOS(receipt, width, qrURL)
	if err != nil {
		writeReceiptError(c, err)
		return
	}
	c.Data(http.StatusOK, "application/octet-stream", out)
}

// IssueLink handles POST /api/v1/pos/orders/:id/receipt-link.
func (h *ReceiptHandler) IssueLink(c *gin.Context) {
	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		authError(c, http.StatusBadRequest, "INVALID_ID", "ID pesanan tidak valid.")
		return
	}
	link, err := h.svc.IssueLink(c.Request.Context(), currentStaff(c), orderID)
	if err != nil {
		writeReceiptError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, link)
}

// PublicReceipt handles GET /api/v1/public/receipts/:token (no customer or staff names).
func (h *ReceiptHandler) PublicReceipt(c *gin.Context) {
	receipt, err := h.svc.Public(c.Request.Context(), c.Param("token"))
	if err != nil {
		writeReceiptError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, receipt)
}

func writeReceiptError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrReceiptNotFound):
		authError(c, http.StatusNotFound, "NOT_FOUND", "Pesanan atau pembayaran tidak ditemukan.")
	case errors.Is(err, service.ErrInvalidReceiptLink):
		authError(c, http.StatusNotFound, "RECEIPT_LINK_INVALID", "Tautan struk tidak valid atau sudah kedaluwarsa.")
	case errors.Is(err, service.ErrReceiptNotAvailable):
		authError(c, http.StatusConflict, "RECEIPT_NOT_AVAILABLE", "Struk jenis ini tidak tersedia untuk pesanan tersebut.")
	default:
		log.Printf("[API] request_id=%s %s %s error: %v", c.GetString(RequestIDHeader), c.Request.Method, c.FullPath(), err)
		authError(c, http.StatusServiceUnavailable, "SERVICE_TEMPORARILY_UNAVAILABLE", "Layanan sedang bermasalah. Coba lagi.")
	}
}
