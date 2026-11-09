package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/service"
)

// InvoiceHandler serves business customers and B2B invoices (v0.11.1 PR 11.9, X18).
type InvoiceHandler struct {
	svc *service.InvoiceService
}

func NewInvoiceHandler(svc *service.InvoiceService) *InvoiceHandler {
	return &InvoiceHandler{svc: svc}
}

// ListBusinessCustomers handles GET /api/v1/invoices/customers (invoice.manage).
func (h *InvoiceHandler) ListBusinessCustomers(c *gin.Context) {
	out, err := h.svc.ListBusinessCustomers(c.Request.Context(), currentStaff(c))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"customers": out})
}

// ListChargeableCustomers handles GET /api/v1/pos/business-customers (payment.tender.direct).
func (h *InvoiceHandler) ListChargeableCustomers(c *gin.Context) {
	out, err := h.svc.ListChargeableCustomers(c.Request.Context(), currentStaff(c))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"customers": out})
}

// CreateBusinessCustomer handles POST /api/v1/invoices/customers.
func (h *InvoiceHandler) CreateBusinessCustomer(c *gin.Context) {
	var in service.BusinessCustomerInput
	if !bindJSON(c, &in) {
		return
	}
	out, err := h.svc.CreateBusinessCustomer(c.Request.Context(), currentStaff(c), in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

// UpdateBusinessCustomer handles PATCH /api/v1/invoices/customers/:id.
func (h *InvoiceHandler) UpdateBusinessCustomer(c *gin.Context) {
	id, ok := pathID(c, "id")
	var in service.BusinessCustomerInput
	if !ok || !bindJSON(c, &in) {
		return
	}
	out, err := h.svc.UpdateBusinessCustomer(c.Request.Context(), currentStaff(c), id, in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// ListUninvoicedOrders handles GET /api/v1/invoices/uninvoiced-orders?business_customer_id=.
func (h *InvoiceHandler) ListUninvoicedOrders(c *gin.Context) {
	var customerID *uuid.UUID
	if raw := c.Query("business_customer_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"code": "VALIDATION_FAILED", "error": "Periksa kembali isian yang ditandai.",
				"fields": map[string]string{"business_customer_id": "harus UUID"}})
			return
		}
		customerID = &id
	}
	out, err := h.svc.ListUninvoicedOrders(c.Request.Context(), currentStaff(c), customerID)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"orders": out})
}

// CreateInvoice handles POST /api/v1/invoices.
func (h *InvoiceHandler) CreateInvoice(c *gin.Context) {
	var in service.CreateInvoiceInput
	if !bindJSON(c, &in) {
		return
	}
	out, err := h.svc.CreateInvoice(c.Request.Context(), currentStaff(c), in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

// ListInvoices handles GET /api/v1/invoices?status=open|paid.
func (h *InvoiceHandler) ListInvoices(c *gin.Context) {
	out, err := h.svc.ListInvoices(c.Request.Context(), currentStaff(c), c.Query("status"))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"invoices": out})
}

// GetInvoice handles GET /api/v1/invoices/:id.
func (h *InvoiceHandler) GetInvoice(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	out, err := h.svc.GetInvoice(c.Request.Context(), currentStaff(c), id)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// InvoicePDF handles GET /api/v1/invoices/:id/pdf.
func (h *InvoiceHandler) InvoicePDF(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	pdf, name, err := h.svc.InvoicePDF(c.Request.Context(), currentStaff(c), id)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="`+name+`"`)
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "application/pdf", pdf)
}

// PayInvoice handles POST /api/v1/invoices/:id/payments.
func (h *InvoiceHandler) PayInvoice(c *gin.Context) {
	id, ok := pathID(c, "id")
	var in service.InvoicePaymentInput
	if !ok || !bindJSON(c, &in) {
		return
	}
	out, err := h.svc.PayInvoice(c.Request.Context(), currentStaff(c), id, in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}
