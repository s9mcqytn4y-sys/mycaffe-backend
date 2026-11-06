package handler

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

// InventoryHandler serves ingredients, recipes and product unit costs (v0.11 PR 11.3, inventory.manage).
type InventoryHandler struct {
	inventory *service.InventoryService
}

func NewInventoryHandler(inventory *service.InventoryService) *InventoryHandler {
	return &InventoryHandler{inventory: inventory}
}

// ListIngredients handles GET /api/v1/inventory/ingredients?low=true.
func (h *InventoryHandler) ListIngredients(c *gin.Context) {
	out, err := h.inventory.ListIngredients(c.Request.Context(), currentStaff(c), c.Query("low") == "true")
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ingredients": out})
}

// CreateIngredient handles POST /api/v1/inventory/ingredients.
func (h *InventoryHandler) CreateIngredient(c *gin.Context) {
	var in service.IngredientInput
	if !bindJSON(c, &in) {
		return
	}
	out, err := h.inventory.CreateIngredient(c.Request.Context(), currentStaff(c), in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

// UpdateIngredient handles PATCH /api/v1/inventory/ingredients/:id.
func (h *InventoryHandler) UpdateIngredient(c *gin.Context) {
	id, ok := pathID(c, "id")
	var in service.IngredientInput
	if !ok || !bindJSON(c, &in) {
		return
	}
	out, err := h.inventory.UpdateIngredient(c.Request.Context(), currentStaff(c), id, in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// GetRecipe handles GET /api/v1/inventory/recipes/:kind/:id (kind: variant | option).
func (h *InventoryHandler) GetRecipe(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	out, err := h.inventory.GetRecipe(c.Request.Context(), currentStaff(c), c.Param("kind"), id)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

type recipeRequest struct {
	Items []service.RecipeItem `json:"items"`
}

// SetRecipe handles PUT /api/v1/inventory/recipes/:kind/:id {items: [{ingredient_id, quantity}]}.
func (h *InventoryHandler) SetRecipe(c *gin.Context) {
	id, ok := pathID(c, "id")
	var req recipeRequest
	if !ok || !bindJSON(c, &req) {
		return
	}
	out, err := h.inventory.SetRecipe(c.Request.Context(), currentStaff(c), c.Param("kind"), id, req.Items)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"items": out})
}

type unitCostRequest struct {
	UnitCost int64 `json:"unit_cost"`
}

// SetProductUnitCost handles PUT /api/v1/inventory/products/:id/unit-cost (ready-to-serve products, X9).
func (h *InventoryHandler) SetProductUnitCost(c *gin.Context) {
	id, ok := pathID(c, "id")
	var req unitCostRequest
	if !ok || !bindJSON(c, &req) {
		return
	}
	if err := h.inventory.SetProductUnitCost(c.Request.Context(), currentStaff(c), id, req.UnitCost); err != nil {
		writeServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ListSuppliers handles GET /api/v1/inventory/suppliers.
func (h *InventoryHandler) ListSuppliers(c *gin.Context) {
	out, err := h.inventory.ListSuppliers(c.Request.Context(), currentStaff(c))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"suppliers": out})
}

// CreateSupplier handles POST /api/v1/inventory/suppliers.
func (h *InventoryHandler) CreateSupplier(c *gin.Context) {
	var in service.SupplierInput
	if !bindJSON(c, &in) {
		return
	}
	out, err := h.inventory.CreateSupplier(c.Request.Context(), currentStaff(c), in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

// UpdateSupplier handles PATCH /api/v1/inventory/suppliers/:id.
func (h *InventoryHandler) UpdateSupplier(c *gin.Context) {
	id, ok := pathID(c, "id")
	var in service.SupplierInput
	if !ok || !bindJSON(c, &in) {
		return
	}
	out, err := h.inventory.UpdateSupplier(c.Request.Context(), currentStaff(c), id, in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// ListPurchaseOrders handles GET /api/v1/inventory/purchase-orders?status=.
func (h *InventoryHandler) ListPurchaseOrders(c *gin.Context) {
	out, err := h.inventory.ListPurchaseOrders(c.Request.Context(), currentStaff(c), c.Query("status"))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"purchase_orders": out})
}

// CreatePurchaseOrder handles POST /api/v1/inventory/purchase-orders (draft).
func (h *InventoryHandler) CreatePurchaseOrder(c *gin.Context) {
	var in service.PurchaseOrderInput
	if !bindJSON(c, &in) {
		return
	}
	out, err := h.inventory.CreatePurchaseOrder(c.Request.Context(), currentStaff(c), in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

// GetPurchaseOrder handles GET /api/v1/inventory/purchase-orders/:id.
func (h *InventoryHandler) GetPurchaseOrder(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	out, err := h.inventory.GetPurchaseOrder(c.Request.Context(), currentStaff(c), id)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// UpdatePurchaseOrder handles PATCH /api/v1/inventory/purchase-orders/:id (draft only).
func (h *InventoryHandler) UpdatePurchaseOrder(c *gin.Context) {
	id, ok := pathID(c, "id")
	var in service.PurchaseOrderInput
	if !ok || !bindJSON(c, &in) {
		return
	}
	out, err := h.inventory.UpdatePurchaseOrder(c.Request.Context(), currentStaff(c), id, in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// TransitionPurchaseOrder handles POST /api/v1/inventory/purchase-orders/:id/{send,cancel,close}.
func (h *InventoryHandler) TransitionPurchaseOrder(action string) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := pathID(c, "id")
		if !ok {
			return
		}
		out, err := h.inventory.TransitionPurchaseOrder(c.Request.Context(), currentStaff(c), id, action)
		if err != nil {
			writeServiceError(c, err)
			return
		}
		c.JSON(http.StatusOK, out)
	}
}

type receiveRequest struct {
	Lines []service.ReceiveLineInput `json:"lines"`
	Note  string                     `json:"note"`
}

// ReceivePurchaseOrder handles POST /api/v1/inventory/purchase-orders/:id/receipts.
func (h *InventoryHandler) ReceivePurchaseOrder(c *gin.Context) {
	id, ok := pathID(c, "id")
	var req receiveRequest
	if !ok || !bindJSON(c, &req) {
		return
	}
	out, err := h.inventory.ReceivePurchaseOrder(c.Request.Context(), currentStaff(c), id, req.Lines, req.Note)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

// DirectPurchase handles POST /api/v1/inventory/purchases (no PO).
func (h *InventoryHandler) DirectPurchase(c *gin.Context) {
	var in service.DirectPurchaseInput
	if !bindJSON(c, &in) {
		return
	}
	out, err := h.inventory.DirectPurchase(c.Request.Context(), currentStaff(c), in)
	if err != nil {
		writePayableError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

// ListReceipts handles GET /api/v1/inventory/receipts?purchase_order_id=.
func (h *InventoryHandler) ListReceipts(c *gin.Context) {
	var poID *uuid.UUID
	if raw := c.Query("purchase_order_id"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			authError(c, http.StatusBadRequest, "INVALID_ID", "purchase_order_id tidak valid.")
			return
		}
		poID = &id
	}
	out, err := h.inventory.ListReceipts(c.Request.Context(), currentStaff(c), poID)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"receipts": out})
}

func writePayableError(c *gin.Context, err error) {
	if errors.Is(err, service.ErrOutletInsufficientFunds) {
		authError(c, http.StatusConflict, "OUTLET_INSUFFICIENT_FUNDS", "Saldo kas outlet tidak cukup.")
		return
	}
	writeServiceError(c, err)
}

// ListPayables handles GET /api/v1/inventory/payables?status=open|paid.
func (h *InventoryHandler) ListPayables(c *gin.Context) {
	out, err := h.inventory.ListPayables(c.Request.Context(), currentStaff(c), c.Query("status"))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"payables": out})
}

// PayPayable handles POST /api/v1/inventory/payables/:id/payments {amount, method, reference, note}.
func (h *InventoryHandler) PayPayable(c *gin.Context) {
	id, ok := pathID(c, "id")
	var in service.PayPayableInput
	if !ok || !bindJSON(c, &in) {
		return
	}
	out, err := h.inventory.PayPayable(c.Request.Context(), currentStaff(c), id, in)
	if err != nil {
		writePayableError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}
