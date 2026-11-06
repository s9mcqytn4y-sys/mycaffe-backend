package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
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
