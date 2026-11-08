package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/service"
)

// CatalogHandler serves menu read models (staff POS and public guest menu) and owner CRUD.
// Error contract shared by CRUD modules:
//
//	400 INVALID_JSON       malformed body
//	401/403                authentication / permission (middleware or service)
//	404 NOT_FOUND          id not in the caller's store
//	409 CONFLICT           uniqueness clash, {"field": "..."}
//	422 VALIDATION_FAILED  {"fields": {"name": "wajib diisi", ...}}
//	503                    infrastructure failure (logged with request id)
type CatalogHandler struct {
	catalog *service.CatalogService
}

func NewCatalogHandler(catalog *service.CatalogService) *CatalogHandler {
	return &CatalogHandler{catalog: catalog}
}

// writeServiceError maps typed service errors to the shared error contract.
func writeServiceError(c *gin.Context, err error) {
	var validation *service.ValidationError
	var conflict *service.ConflictError
	switch {
	case errors.As(err, &validation):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"code": "VALIDATION_FAILED", "error": "Periksa kembali isian yang ditandai.", "fields": validation.Fields})
	case errors.As(err, &conflict):
		c.JSON(http.StatusConflict, gin.H{"code": "CONFLICT", "error": conflict.Message, "field": conflict.Field})
	case errors.Is(err, service.ErrCatalogNotFound):
		authError(c, http.StatusNotFound, "NOT_FOUND", "Data tidak ditemukan.")
	case errors.Is(err, service.ErrForbidden):
		authError(c, http.StatusForbidden, "FORBIDDEN", "Peran Anda tidak diizinkan melakukan aksi ini.")
	case errors.Is(err, service.ErrInvalidSession):
		authError(c, http.StatusUnauthorized, "SESSION_INVALID", "Sesi tidak valid. Silakan masuk kembali.")
	default:
		log.Printf("[API] request_id=%s %s %s error: %v", c.GetString(RequestIDHeader), c.Request.Method, c.FullPath(), err)
		authError(c, http.StatusServiceUnavailable, "SERVICE_TEMPORARILY_UNAVAILABLE", "Layanan sedang bermasalah. Coba lagi.")
	}
}

func bindJSON(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_JSON", "Format data tidak valid.")
		return false
	}
	return true
}

func pathID(c *gin.Context, name string) (uuid.UUID, bool) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil || id == uuid.Nil {
		authError(c, http.StatusNotFound, "NOT_FOUND", "Data tidak ditemukan.")
		return uuid.Nil, false
	}
	return id, true
}

// writeCatalog sends the view with a content ETag (generated_at excluded) and honours If-None-Match.
func writeCatalog(c *gin.Context, view *service.CatalogView, cacheControl string) {
	generated := view.GeneratedAt
	view.GeneratedAt = time.Time{}
	stable, err := json.Marshal(view)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	sum := sha256.Sum256(stable)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	c.Header("ETag", etag)
	c.Header("Cache-Control", cacheControl)
	if c.GetHeader("If-None-Match") == etag {
		c.Status(http.StatusNotModified)
		return
	}
	view.GeneratedAt = generated
	c.JSON(http.StatusOK, view)
}

// StaffCatalog handles GET /api/v1/pos/catalog.
func (h *CatalogHandler) StaffCatalog(c *gin.Context) {
	view, err := h.catalog.StaffCatalog(c.Request.Context(), currentStaff(c))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeCatalog(c, view, "private, no-cache")
}

// PublicCatalog handles GET /api/v1/public/stores/:slug/catalog.
func (h *CatalogHandler) PublicCatalog(c *gin.Context) {
	view, err := h.catalog.PublicCatalog(c.Request.Context(), c.Param("slug"))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	writeCatalog(c, view, "public, max-age=30")
}

func (h *CatalogHandler) CreateCategory(c *gin.Context) {
	var in service.CategoryInput
	if !bindJSON(c, &in) {
		return
	}
	out, err := h.catalog.CreateCategory(c.Request.Context(), currentStaff(c), in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

func (h *CatalogHandler) UpdateCategory(c *gin.Context) {
	id, ok := pathID(c, "id")
	var in service.CategoryInput
	if !ok || !bindJSON(c, &in) {
		return
	}
	out, err := h.catalog.UpdateCategory(c.Request.Context(), currentStaff(c), id, in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *CatalogHandler) DeleteCategory(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	if err := h.catalog.DeleteCategory(c.Request.Context(), currentStaff(c), id); err != nil {
		writeServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *CatalogHandler) CreateProduct(c *gin.Context) {
	var in service.ProductInput
	if !bindJSON(c, &in) {
		return
	}
	out, err := h.catalog.CreateProduct(c.Request.Context(), currentStaff(c), in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

func (h *CatalogHandler) UpdateProduct(c *gin.Context) {
	id, ok := pathID(c, "id")
	var in service.ProductInput
	if !ok || !bindJSON(c, &in) {
		return
	}
	out, err := h.catalog.UpdateProduct(c.Request.Context(), currentStaff(c), id, in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *CatalogHandler) ArchiveProduct(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	if err := h.catalog.ArchiveProduct(c.Request.Context(), currentStaff(c), id); err != nil {
		writeServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

type availabilityRequest struct {
	IsAvailable *bool `json:"is_available"`
}

// SetAvailability handles PATCH /api/v1/catalog/products/:id/availability (station staff or owner).
func (h *CatalogHandler) SetAvailability(c *gin.Context) {
	id, ok := pathID(c, "id")
	var in availabilityRequest
	if !ok || !bindJSON(c, &in) {
		return
	}
	if in.IsAvailable == nil {
		writeServiceError(c, &service.ValidationError{Fields: map[string]string{"is_available": "wajib diisi"}})
		return
	}
	if err := h.catalog.SetAvailability(c.Request.Context(), currentStaff(c), id, *in.IsAvailable); err != nil {
		writeServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *CatalogHandler) CreateVariant(c *gin.Context) {
	id, ok := pathID(c, "id")
	var in service.VariantInput
	if !ok || !bindJSON(c, &in) {
		return
	}
	out, err := h.catalog.CreateVariant(c.Request.Context(), currentStaff(c), id, in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

func (h *CatalogHandler) UpdateVariant(c *gin.Context) {
	id, ok := pathID(c, "id")
	var in service.VariantInput
	if !ok || !bindJSON(c, &in) {
		return
	}
	out, err := h.catalog.UpdateVariant(c.Request.Context(), currentStaff(c), id, in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *CatalogHandler) ArchiveVariant(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	if err := h.catalog.ArchiveVariant(c.Request.Context(), currentStaff(c), id); err != nil {
		writeServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ---------- Modifier groups & options (Iteration 4a, decision P1) ----------

func (h *CatalogHandler) CreateModifierGroup(c *gin.Context) {
	var in service.ModifierGroupInput
	if !bindJSON(c, &in) {
		return
	}
	out, err := h.catalog.CreateModifierGroup(c.Request.Context(), currentStaff(c), in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

func (h *CatalogHandler) GetModifierGroup(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	out, err := h.catalog.GetModifierGroup(c.Request.Context(), currentStaff(c), id)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *CatalogHandler) ListModifierGroups(c *gin.Context) {
	includeArchived := c.Query("include_archived") == "true"
	out, err := h.catalog.ListModifierGroups(c.Request.Context(), currentStaff(c), includeArchived)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *CatalogHandler) UpdateModifierGroup(c *gin.Context) {
	id, ok := pathID(c, "id")
	var in service.ModifierGroupInput
	if !ok || !bindJSON(c, &in) {
		return
	}
	out, err := h.catalog.UpdateModifierGroup(c.Request.Context(), currentStaff(c), id, in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *CatalogHandler) ArchiveModifierGroup(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	forceUnlink := c.Query("force_unlink") == "true"
	if err := h.catalog.ArchiveModifierGroup(c.Request.Context(), currentStaff(c), id, forceUnlink); err != nil {
		writeServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *CatalogHandler) AttachModifierGroupToProduct(c *gin.Context) {
	productID, ok := pathID(c, "id")
	var in service.AttachModifierGroupInput
	if !ok || !bindJSON(c, &in) {
		return
	}
	if err := h.catalog.AttachModifierGroupToProduct(c.Request.Context(), currentStaff(c), productID, in); err != nil {
		writeServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *CatalogHandler) DetachModifierGroupFromProduct(c *gin.Context) {
	productID, ok1 := pathID(c, "id")
	groupID, ok2 := pathID(c, "group_id")
	if !ok1 || !ok2 {
		return
	}
	if err := h.catalog.DetachModifierGroupFromProduct(c.Request.Context(), currentStaff(c), productID, groupID); err != nil {
		writeServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *CatalogHandler) CreateModifierOption(c *gin.Context) {
	groupID, ok := pathID(c, "id")
	var in service.ModifierOptionInput
	if !ok || !bindJSON(c, &in) {
		return
	}
	out, err := h.catalog.CreateModifierOption(c.Request.Context(), currentStaff(c), groupID, in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

func (h *CatalogHandler) UpdateModifierOption(c *gin.Context) {
	groupID, ok1 := pathID(c, "id")
	optionID, ok2 := pathID(c, "option_id")
	var in service.ModifierOptionInput
	if !ok1 || !ok2 || !bindJSON(c, &in) {
		return
	}
	out, err := h.catalog.UpdateModifierOption(c.Request.Context(), currentStaff(c), groupID, optionID, in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

func (h *CatalogHandler) ArchiveModifierOption(c *gin.Context) {
	groupID, ok1 := pathID(c, "id")
	optionID, ok2 := pathID(c, "option_id")
	if !ok1 || !ok2 {
		return
	}
	if err := h.catalog.ArchiveModifierOption(c.Request.Context(), currentStaff(c), groupID, optionID); err != nil {
		writeServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *CatalogHandler) AttachChildGroupToOption(c *gin.Context) {
	optionID, ok := pathID(c, "id")
	var in service.AttachChildGroupInput
	if !ok || !bindJSON(c, &in) {
		return
	}
	if err := h.catalog.AttachChildGroupToOption(c.Request.Context(), currentStaff(c), optionID, in); err != nil {
		writeServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *CatalogHandler) DetachChildGroupFromOption(c *gin.Context) {
	optionID, ok1 := pathID(c, "id")
	childGroupID, ok2 := pathID(c, "group_id")
	if !ok1 || !ok2 {
		return
	}
	if err := h.catalog.DetachChildGroupFromOption(c.Request.Context(), currentStaff(c), optionID, childGroupID); err != nil {
		writeServiceError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ListPromotions handles GET /api/v1/catalog/promotions (catalog.manage).
func (h *CatalogHandler) ListPromotions(c *gin.Context) {
	out, err := h.catalog.ListPromotions(c.Request.Context(), currentStaff(c))
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"promotions": out})
}

// CreatePromotion handles POST /api/v1/catalog/promotions (K7).
func (h *CatalogHandler) CreatePromotion(c *gin.Context) {
	var in service.PromotionInput
	if !bindJSON(c, &in) {
		return
	}
	out, err := h.catalog.CreatePromotion(c.Request.Context(), currentStaff(c), in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

// UpdatePromotion handles PATCH /api/v1/catalog/promotions/:id; is_active=false ends a promotion.
func (h *CatalogHandler) UpdatePromotion(c *gin.Context) {
	id, ok := pathID(c, "id")
	var in service.PromotionInput
	if !ok || !bindJSON(c, &in) {
		return
	}
	out, err := h.catalog.UpdatePromotion(c.Request.Context(), currentStaff(c), id, in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

type bundleSlotsRequest struct {
	Slots []service.BundleSlotInput `json:"slots"`
}

// SetBundleSlots handles PUT /api/v1/catalog/products/:id/bundle-slots: replaces the parts of a bundle (X4).
func (h *CatalogHandler) SetBundleSlots(c *gin.Context) {
	id, ok := pathID(c, "id")
	var req bundleSlotsRequest
	if !ok || !bindJSON(c, &req) {
		return
	}
	out, err := h.catalog.SetBundleSlots(c.Request.Context(), currentStaff(c), id, req.Slots)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"slots": out})
}

// ListSalesChannels handles GET /api/v1/catalog/sales-channels (owner, all) and GET /api/v1/pos/sales-channels
// (staff, active only, with prices for the POS).
func (h *CatalogHandler) ListSalesChannels(activeOnly bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		out, err := h.catalog.ListSalesChannels(c.Request.Context(), currentStaff(c), activeOnly)
		if err != nil {
			writeServiceError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"sales_channels": out})
	}
}

// CreateSalesChannel handles POST /api/v1/catalog/sales-channels (X12).
func (h *CatalogHandler) CreateSalesChannel(c *gin.Context) {
	var in service.SalesChannelInput
	if !bindJSON(c, &in) {
		return
	}
	out, err := h.catalog.CreateSalesChannel(c.Request.Context(), currentStaff(c), in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusCreated, out)
}

// UpdateSalesChannel handles PATCH /api/v1/catalog/sales-channels/:id; is_active=false retires a channel.
func (h *CatalogHandler) UpdateSalesChannel(c *gin.Context) {
	id, ok := pathID(c, "id")
	var in service.SalesChannelInput
	if !ok || !bindJSON(c, &in) {
		return
	}
	out, err := h.catalog.UpdateSalesChannel(c.Request.Context(), currentStaff(c), id, in)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}

// SetChannelPrices handles PUT /api/v1/catalog/sales-channels/:id/prices (X13).
func (h *CatalogHandler) SetChannelPrices(c *gin.Context) {
	id, ok := pathID(c, "id")
	var in struct {
		Prices []service.ChannelPriceInput `json:"prices"`
	}
	if !ok || !bindJSON(c, &in) {
		return
	}
	out, err := h.catalog.SetChannelPrices(c.Request.Context(), currentStaff(c), id, in.Prices)
	if err != nil {
		writeServiceError(c, err)
		return
	}
	c.JSON(http.StatusOK, out)
}
