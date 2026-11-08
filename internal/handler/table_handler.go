package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/service"
)

// TableHandler serves tables, table sessions and QR resolution (iteration 5a).
type TableHandler struct {
	tables *service.TableService
}

func NewTableHandler(tables *service.TableService) *TableHandler {
	return &TableHandler{tables: tables}
}

func writeTableError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrTableNotFound):
		authError(c, http.StatusNotFound, "NOT_FOUND", "Meja tidak ditemukan.")
	case errors.Is(err, service.ErrTableSessionOpen):
		authError(c, http.StatusConflict, "TABLE_SESSION_OPEN", "Meja masih memiliki sesi aktif. Kosongkan meja terlebih dahulu.")
	case errors.Is(err, service.ErrTableHasUnpaidOrders):
		authError(c, http.StatusConflict, "TABLE_HAS_UNPAID_ORDERS", "Masih ada pesanan meja ini yang belum dibayar.")
	case errors.Is(err, service.ErrTableInactive):
		authError(c, http.StatusConflict, "TABLE_INACTIVE", "Meja sedang tidak aktif.")
	case errors.Is(err, service.ErrTableSessionNotOpen):
		authError(c, http.StatusConflict, "TABLE_SESSION_NOT_OPEN", "Sesi meja sudah ditutup. Muat ulang papan meja.")
	case errors.Is(err, service.ErrTableTimerOff):
		authError(c, http.StatusConflict, "TABLE_TIMER_OFF", "Meja ini tidak memakai batas waktu.")
	case errors.Is(err, service.ErrTableNotMerged):
		authError(c, http.StatusConflict, "TABLE_NOT_MERGED", "Meja ini tidak tergabung dengan sesi tersebut.")
	default:
		writeServiceError(c, err)
	}
}

// ListTables handles GET /api/v1/pos/tables?section= (any staff).
func (h *TableHandler) ListTables(c *gin.Context) {
	board, err := h.tables.ListTables(c.Request.Context(), currentStaff(c), c.Query("section"))
	if err != nil {
		writeTableError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"tables": board})
}

// CreateTable handles POST /api/v1/pos/tables (table.manage).
func (h *TableHandler) CreateTable(c *gin.Context) {
	var in service.TableInput
	if !bindJSON(c, &in) {
		return
	}
	t, err := h.tables.CreateTable(c.Request.Context(), currentStaff(c), in)
	if err != nil {
		writeTableError(c, err)
		return
	}
	c.JSON(http.StatusCreated, t)
}

// UpdateTable handles PATCH /api/v1/pos/tables/:id (table.manage).
func (h *TableHandler) UpdateTable(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var in service.TableInput
	if !bindJSON(c, &in) {
		return
	}
	t, err := h.tables.UpdateTable(c.Request.Context(), currentStaff(c), id, in)
	if err != nil {
		writeTableError(c, err)
		return
	}
	c.JSON(http.StatusOK, t)
}

// ArchiveTable handles POST /api/v1/pos/tables/:id/archive (table.manage).
func (h *TableHandler) ArchiveTable(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	if err := h.tables.ArchiveTable(c.Request.Context(), currentStaff(c), id); err != nil {
		writeTableError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// RotateQR handles POST /api/v1/pos/tables/:id/rotate-qr (table.qr.manage).
func (h *TableHandler) RotateQR(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	t, err := h.tables.RotateQR(c.Request.Context(), currentStaff(c), id)
	if err != nil {
		writeTableError(c, err)
		return
	}
	c.JSON(http.StatusOK, t)
}

// OpenSession handles POST /api/v1/pos/tables/:id/sessions (tab.manage).
func (h *TableHandler) OpenSession(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	session, err := h.tables.OpenSession(c.Request.Context(), currentStaff(c), id)
	if err != nil {
		writeTableError(c, err)
		return
	}
	c.JSON(http.StatusOK, session)
}

// CloseSession handles POST /api/v1/pos/table-sessions/:id/close (tab.manage).
func (h *TableHandler) CloseSession(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	session, err := h.tables.CloseSession(c.Request.Context(), currentStaff(c), id)
	if err != nil {
		writeTableError(c, err)
		return
	}
	c.JSON(http.StatusOK, session)
}

// ResolveQR handles GET /api/v1/public/qr/:token (public, decision QR1).
func (h *TableHandler) ResolveQR(c *gin.Context) {
	table, err := h.tables.ResolveQR(c.Request.Context(), c.Param("token"))
	if err != nil {
		writeTableError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, table)
}

type moveSessionRequest struct {
	ToTableID uuid.UUID `json:"to_table_id" binding:"required"`
}

type mergeSessionRequest struct {
	SessionID uuid.UUID `json:"session_id" binding:"required"`
}

type splitTableRequest struct {
	TableID uuid.UUID `json:"table_id" binding:"required"`
}

// MoveSession handles POST /api/v1/pos/table-sessions/:id/move (tab.manage, W5).
func (h *TableHandler) MoveSession(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var req moveSessionRequest
	if !bindJSON(c, &req) {
		return
	}
	session, err := h.tables.MoveSession(c.Request.Context(), currentStaff(c), id, req.ToTableID)
	if err != nil {
		writeTableError(c, err)
		return
	}
	c.JSON(http.StatusOK, session)
}

// MergeSession handles POST /api/v1/pos/table-sessions/:id/merge (tab.manage, W5).
func (h *TableHandler) MergeSession(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var req mergeSessionRequest
	if !bindJSON(c, &req) {
		return
	}
	session, err := h.tables.MergeSession(c.Request.Context(), currentStaff(c), id, req.SessionID)
	if err != nil {
		writeTableError(c, err)
		return
	}
	c.JSON(http.StatusOK, session)
}

// SplitTable handles POST /api/v1/pos/table-sessions/:id/split (tab.manage, W5).
func (h *TableHandler) SplitTable(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var req splitTableRequest
	if !bindJSON(c, &req) {
		return
	}
	session, err := h.tables.SplitTable(c.Request.Context(), currentStaff(c), id, req.TableID)
	if err != nil {
		writeTableError(c, err)
		return
	}
	c.JSON(http.StatusOK, session)
}

type extendTimerRequest struct {
	Minutes int `json:"minutes" binding:"required"`
}

// ExtendTimer handles POST /api/v1/pos/table-sessions/:id/extend (tab.manage, W6).
func (h *TableHandler) ExtendTimer(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var req extendTimerRequest
	if !bindJSON(c, &req) {
		return
	}
	session, err := h.tables.ExtendTimer(c.Request.Context(), currentStaff(c), id, req.Minutes)
	if err != nil {
		writeTableError(c, err)
		return
	}
	c.JSON(http.StatusOK, session)
}
