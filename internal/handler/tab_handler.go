package handler

import (
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

type TabHandler struct {
	tab *service.TabService
}

func NewTabHandler(tab *service.TabService) *TabHandler {
	return &TabHandler{tab: tab}
}

type OpenTabRequest struct {
	IdempotencyKey      string                   `json:"idempotency_key" binding:"required,max=100"`
	TableNumber         string                   `json:"table_number" binding:"max=50"`
	CustomerName        string                   `json:"customer_name" binding:"max=100"`
	CustomerPhone       string                   `json:"customer_phone" binding:"max=50"`
	TableID             *uuid.UUID               `json:"table_id,omitempty"`
	AllowDuplicateLabel bool                     `json:"allow_duplicate_label"`
	Notes               string                   `json:"notes" binding:"max=500"`
	InitialItems        []service.OrderItemInput `json:"initial_items,omitempty" binding:"omitempty,max=100"`
}

func (h *TabHandler) OpenTab(c *gin.Context) {
	staff := currentStaff(c)
	if staff == nil {
		authError(c, http.StatusUnauthorized, "UNAUTHENTICATED", "Autentikasi diperlukan.")
		return
	}

	var req OpenTabRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", "Data pembukaan tab tidak lengkap atau tidak valid.")
		return
	}

	res, err := h.tab.OpenTab(c.Request.Context(), staff, service.OpenTabInput{
		IdempotencyKey:      req.IdempotencyKey,
		TableNumber:         req.TableNumber,
		CustomerName:        req.CustomerName,
		CustomerPhone:       req.CustomerPhone,
		TableID:             req.TableID,
		AllowDuplicateLabel: req.AllowDuplicateLabel,
		Notes:               req.Notes,
		InitialItems:        req.InitialItems,
	})
	if err != nil {
		writeTabServiceError(c, err)
		return
	}

	status := http.StatusCreated
	if res.IsIdempotentReplay {
		status = http.StatusOK
	}
	c.JSON(status, res)
}

type SubmitRoundRequest struct {
	IdempotencyKey string                   `json:"idempotency_key" binding:"required,max=100"`
	Notes          string                   `json:"notes" binding:"max=500"`
	Items          []service.OrderItemInput `json:"items" binding:"required,min=1,max=100"`
}

func (h *TabHandler) SubmitRound(c *gin.Context) {
	staff := currentStaff(c)
	if staff == nil {
		authError(c, http.StatusUnauthorized, "UNAUTHENTICATED", "Autentikasi diperlukan.")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		authError(c, http.StatusBadRequest, "INVALID_ID", "ID tab tidak valid.")
		return
	}

	var req SubmitRoundRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", "Data ronde tidak valid atau item kosong.")
		return
	}

	res, err := h.tab.SubmitRound(c.Request.Context(), staff, orderID, service.SubmitRoundInput{
		IdempotencyKey: req.IdempotencyKey,
		Notes:          req.Notes,
		Items:          req.Items,
	})
	if err != nil {
		writeTabServiceError(c, err)
		return
	}

	status := http.StatusCreated
	if res.IsIdempotentReplay {
		status = http.StatusOK
	}
	c.JSON(status, res)
}

func (h *TabHandler) GetTab(c *gin.Context) {
	staff := currentStaff(c)
	if staff == nil {
		authError(c, http.StatusUnauthorized, "UNAUTHENTICATED", "Autentikasi diperlukan.")
		return
	}

	orderID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		authError(c, http.StatusBadRequest, "INVALID_ID", "ID tab tidak valid.")
		return
	}

	res, err := h.tab.GetTabDetail(c.Request.Context(), staff, orderID)
	if err != nil {
		writeTabServiceError(c, err)
		return
	}

	c.JSON(http.StatusOK, res)
}

func writeTabServiceError(c *gin.Context, err error) {
	var tabConflict *service.TabConflictError
	var validation *service.ValidationError
	switch {
	case errors.As(err, &tabConflict):
		c.JSON(http.StatusConflict, gin.H{
			"code":              "TAB_LABEL_IN_USE",
			"error":             tabConflict.Message,
			"existing_order_id": tabConflict.ExistingOrderID.String(),
		})
	case errors.Is(err, service.ErrIdempotencyConflict):
		authError(c, http.StatusConflict, "IDEMPOTENCY_CONFLICT", err.Error())
	case errors.Is(err, service.ErrTabNotOpen):
		authError(c, http.StatusConflict, "TAB_NOT_OPEN", "Tab tidak dalam status terbuka atau sudah ditutup.")
	case errors.Is(err, service.ErrEmptyRoundItems):
		authError(c, http.StatusBadRequest, "EMPTY_ROUND_ITEMS", "Ronde pesanan harus memiliki minimal satu item.")
	case errors.Is(err, service.ErrInvalidTabInput), errors.Is(err, service.ErrInvalidOrderInput):
		authError(c, http.StatusBadRequest, "INVALID_INPUT", err.Error())
	case errors.Is(err, service.ErrInsufficientStock):
		authError(c, http.StatusUnprocessableEntity, "INSUFFICIENT_STOCK", err.Error())
	case errors.Is(err, service.ErrProductNotFound):
		authError(c, http.StatusNotFound, "PRODUCT_NOT_FOUND", err.Error())
	case errors.Is(err, service.ErrProductUnavailable):
		authError(c, http.StatusUnprocessableEntity, "PRODUCT_UNAVAILABLE", err.Error())
	case errors.Is(err, service.ErrForbidden):
		authError(c, http.StatusForbidden, "FORBIDDEN", "Peran Anda tidak diizinkan mengelola tab.")
	case errors.Is(err, service.ErrInvalidSession):
		authError(c, http.StatusUnauthorized, "SESSION_INVALID", "Sesi tidak valid. Silakan masuk kembali.")
	case errors.Is(err, service.ErrTabNotFound), errors.Is(err, postgres.ErrOrderNotFound):
		authError(c, http.StatusNotFound, "NOT_FOUND", "Tab tidak ditemukan.")
	case errors.As(err, &validation):
		c.JSON(http.StatusUnprocessableEntity, gin.H{
			"code":   "VALIDATION_FAILED",
			"error":  "Periksa kembali isian pesanan yang ditandai.",
			"fields": validation.Fields,
		})
	default:
		log.Printf("[TAB] request_id=%s %s %s error: %v", c.GetString(RequestIDHeader), c.Request.Method, c.FullPath(), err)
		authError(c, http.StatusServiceUnavailable, "SERVICE_TEMPORARILY_UNAVAILABLE", fmt.Sprintf("Gagal memproses operasi tab: %v", err))
	}
}
