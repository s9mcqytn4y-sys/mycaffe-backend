package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/service"
)

// PrintHandler serves printers (owner), the print agent API ("Authorization: Printer <token>") and
// reprints (v0.9 PR 9.4, W8).
type PrintHandler struct {
	prints *service.PrintService
}

func NewPrintHandler(prints *service.PrintService) *PrintHandler {
	return &PrintHandler{prints: prints}
}

func writePrintError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrPrinterTokenInvalid):
		authError(c, http.StatusUnauthorized, "PRINTER_TOKEN_INVALID", "Token printer tidak valid atau printer nonaktif.")
	case errors.Is(err, service.ErrPrinterNotFound):
		authError(c, http.StatusNotFound, "NOT_FOUND", "Printer tidak ditemukan.")
	case errors.Is(err, service.ErrPrintJobNotFound):
		authError(c, http.StatusNotFound, "PRINT_JOB_NOT_FOUND", "Tugas cetak tidak ditemukan atau sudah selesai.")
	case errors.Is(err, service.ErrNoPrinter):
		authError(c, http.StatusConflict, "PRINTER_NOT_CONFIGURED", "Belum ada printer aktif untuk cetakan ini.")
	case errors.Is(err, service.ErrOrderNotFound):
		authError(c, http.StatusNotFound, "NOT_FOUND", "Pesanan tidak ditemukan.")
	default:
		writeServiceError(c, err)
	}
}

func printerToken(c *gin.Context) string {
	token, _ := strings.CutPrefix(c.GetHeader("Authorization"), "Printer ")
	return strings.TrimSpace(token)
}

// ListPrinters handles GET /api/v1/store/printers (store.settings.manage).
func (h *PrintHandler) ListPrinters(c *gin.Context) {
	printers, err := h.prints.ListPrinters(c.Request.Context(), currentStaff(c))
	if err != nil {
		writePrintError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"printers": printers})
}

// CreatePrinter handles POST /api/v1/store/printers; the agent token is shown once.
func (h *PrintHandler) CreatePrinter(c *gin.Context) {
	var in service.PrinterInput
	if !bindJSON(c, &in) {
		return
	}
	p, err := h.prints.CreatePrinter(c.Request.Context(), currentStaff(c), in)
	if err != nil {
		writePrintError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, p)
}

// UpdatePrinter handles PATCH /api/v1/store/printers/:id.
func (h *PrintHandler) UpdatePrinter(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var in service.PrinterInput
	if !bindJSON(c, &in) {
		return
	}
	p, err := h.prints.UpdatePrinter(c.Request.Context(), currentStaff(c), id, in)
	if err != nil {
		writePrintError(c, err)
		return
	}
	c.JSON(http.StatusOK, p)
}

// RotateToken handles POST /api/v1/store/printers/:id/rotate-token; the new token is shown once.
func (h *PrintHandler) RotateToken(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	p, err := h.prints.RotateToken(c.Request.Context(), currentStaff(c), id)
	if err != nil {
		writePrintError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, p)
}

// Claim handles POST /api/v1/print-agent/jobs/claim: 200 with a job, 204 when nothing is due.
func (h *PrintHandler) Claim(c *gin.Context) {
	job, err := h.prints.Claim(c.Request.Context(), printerToken(c))
	if err != nil {
		writePrintError(c, err)
		return
	}
	if job == nil {
		c.Status(http.StatusNoContent)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, job)
}

type ackRequest struct {
	Status string `json:"status" binding:"required,oneof=printed failed"`
	Error  string `json:"error"`
}

// Ack handles POST /api/v1/print-agent/jobs/:id/ack {status: printed|failed, error}.
func (h *PrintHandler) Ack(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var req ackRequest
	if !bindJSON(c, &req) {
		return
	}
	if err := h.prints.Ack(c.Request.Context(), printerToken(c), id, req.Status == "printed", req.Error); err != nil {
		writePrintError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

type reprintRequest struct {
	Kind string `json:"kind"`
}

// Reprint handles POST /api/v1/pos/orders/:id/reprint {kind: ticket|checker|label}.
func (h *PrintHandler) Reprint(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var req reprintRequest
	if !bindJSON(c, &req) {
		return
	}
	n, err := h.prints.Reprint(c.Request.Context(), currentStaff(c), id, req.Kind)
	if err != nil {
		writePrintError(c, err)
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"jobs": n})
}

// AgentPrinter handles GET /api/v1/print-agent/printer: the printer's settings for the agent's LAN cache.
func (h *PrintHandler) AgentPrinter(c *gin.Context) {
	p, err := h.prints.AgentPrinter(c.Request.Context(), printerToken(c))
	if err != nil {
		writePrintError(c, err)
		return
	}
	c.JSON(http.StatusOK, p)
}
