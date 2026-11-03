package handler

import (
	"errors"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

// MediaHandler serves B3 product image uploads (presigned PUT to R2, then verify + variants).
type MediaHandler struct {
	svc *service.MediaService
}

func NewMediaHandler(svc *service.MediaService) *MediaHandler {
	return &MediaHandler{svc: svc}
}

// CreateUpload handles POST /api/v1/media/uploads.
func (h *MediaHandler) CreateUpload(c *gin.Context) {
	var in service.CreateMediaUploadInput
	if err := c.ShouldBindJSON(&in); err != nil {
		authError(c, http.StatusBadRequest, "INVALID_REQUEST", "Data unggahan tidak valid.")
		return
	}
	ticket, err := h.svc.CreateUpload(c.Request.Context(), currentStaff(c), in)
	if err != nil {
		writeMediaError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusCreated, ticket)
}

// Complete handles POST /api/v1/media/uploads/:id/complete.
func (h *MediaHandler) Complete(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		authError(c, http.StatusBadRequest, "INVALID_ID", "ID unggahan tidak valid.")
		return
	}
	done, err := h.svc.Complete(c.Request.Context(), currentStaff(c), id)
	if err != nil {
		writeMediaError(c, err)
		return
	}
	c.JSON(http.StatusOK, done)
}

func writeMediaError(c *gin.Context, err error) {
	var validation *service.ValidationError
	switch {
	case errors.As(err, &validation):
		c.JSON(http.StatusUnprocessableEntity, gin.H{"code": "VALIDATION_FAILED", "error": "Periksa kembali isian yang ditandai.", "fields": validation.Fields})
	case errors.Is(err, service.ErrForbidden):
		authError(c, http.StatusForbidden, "FORBIDDEN", "Peran Anda tidak diizinkan melakukan aksi ini.")
	case errors.Is(err, service.ErrMediaNotFound):
		authError(c, http.StatusNotFound, "NOT_FOUND", "Produk atau unggahan tidak ditemukan.")
	case errors.Is(err, service.ErrMediaTooLarge):
		authError(c, http.StatusRequestEntityTooLarge, "MEDIA_TOO_LARGE", "Ukuran file maksimal 5 MB.")
	case errors.Is(err, service.ErrMediaUnsupported):
		authError(c, http.StatusUnsupportedMediaType, "MEDIA_UNSUPPORTED", "File harus gambar JPEG, PNG, atau WebP yang sesuai dengan jenis yang dikirim.")
	case errors.Is(err, service.ErrMediaDimensions):
		authError(c, http.StatusUnprocessableEntity, "MEDIA_DIMENSIONS", "Gambar minimal 200 x 200 piksel dan maksimal 25 megapiksel.")
	case errors.Is(err, service.ErrMediaAlreadyCompleted):
		authError(c, http.StatusConflict, "MEDIA_ALREADY_COMPLETED", "Unggahan ini sudah selesai diproses.")
	case errors.Is(err, service.ErrMediaNotUploaded):
		authError(c, http.StatusConflict, "MEDIA_NOT_UPLOADED", "File belum diunggah ke penyimpanan.")
	case errors.Is(err, service.ErrMediaExpired):
		authError(c, http.StatusGone, "MEDIA_UPLOAD_EXPIRED", "Waktu unggahan habis. Mulai unggah ulang.")
	case errors.Is(err, service.ErrMediaUnavailable):
		authError(c, http.StatusServiceUnavailable, "MEDIA_UNAVAILABLE", "Penyimpanan media belum dikonfigurasi.")
	default:
		log.Printf("[API] request_id=%s %s %s error: %v", c.GetString(RequestIDHeader), c.Request.Method, c.FullPath(), err)
		authError(c, http.StatusServiceUnavailable, "SERVICE_TEMPORARILY_UNAVAILABLE", "Layanan sedang bermasalah. Coba lagi.")
	}
}
