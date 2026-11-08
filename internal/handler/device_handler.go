package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/service"
)

// DeviceHandler serves POS device management (owner, device.manage) and the device API
// ("Authorization: Device <credential>") of v0.10 PR 10.1 (ADR-005).
type DeviceHandler struct {
	devices *service.DeviceService
}

func NewDeviceHandler(devices *service.DeviceService) *DeviceHandler {
	return &DeviceHandler{devices: devices}
}

func writeDeviceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrDeviceInvalid):
		authError(c, http.StatusUnauthorized, "DEVICE_INVALID", "Perangkat ini tidak terdaftar atau sudah dicabut.")
	case errors.Is(err, service.ErrDeviceNotFound):
		authError(c, http.StatusNotFound, "NOT_FOUND", "Perangkat tidak ditemukan.")
	default:
		writeServiceError(c, err)
	}
}

func deviceCredential(c *gin.Context) string {
	credential, _ := strings.CutPrefix(c.GetHeader("Authorization"), "Device ")
	return strings.TrimSpace(credential)
}

// ListDevices handles GET /api/v1/store/devices.
func (h *DeviceHandler) ListDevices(c *gin.Context) {
	devices, err := h.devices.ListDevices(c.Request.Context(), currentStaff(c))
	if err != nil {
		writeDeviceError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"devices": devices})
}

// RegisterDevice handles POST /api/v1/store/devices {name, prefix}; the credential is shown once.
func (h *DeviceHandler) RegisterDevice(c *gin.Context) {
	var in service.DeviceInput
	if !bindJSON(c, &in) {
		return
	}
	d, err := h.devices.RegisterDevice(c.Request.Context(), currentStaff(c), in)
	if err != nil {
		writeDeviceError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, d)
}

// RenameDevice handles PATCH /api/v1/store/devices/:id {name}.
func (h *DeviceHandler) RenameDevice(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	var in service.DeviceInput
	if !bindJSON(c, &in) {
		return
	}
	d, err := h.devices.RenameDevice(c.Request.Context(), currentStaff(c), id, in)
	if err != nil {
		writeDeviceError(c, err)
		return
	}
	c.JSON(http.StatusOK, d)
}

// RevokeDevice handles POST /api/v1/store/devices/:id/revoke.
func (h *DeviceHandler) RevokeDevice(c *gin.Context) {
	id, ok := pathID(c, "id")
	if !ok {
		return
	}
	d, err := h.devices.RevokeDevice(c.Request.Context(), currentStaff(c), id)
	if err != nil {
		writeDeviceError(c, err)
		return
	}
	c.JSON(http.StatusOK, d)
}

// Catalog handles GET /api/v1/device/catalog: the POS catalog plus the catalog_version to send back
// with offline orders.
func (h *DeviceHandler) Catalog(c *gin.Context) {
	out, err := h.devices.Catalog(c.Request.Context(), deviceCredential(c))
	if err != nil {
		writeDeviceError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, out)
}
