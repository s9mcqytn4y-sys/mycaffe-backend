package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/domain"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/imaging"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/storage"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/repository/postgres"
)

// Media uploads (B3, C4, decision K9): the browser PUTs the file straight to the public media bucket
// with a presigned URL; /complete re-reads it, verifies type, size and dimensions, writes JPEG variants
// without metadata and only then links it as the product image.

const (
	mediaMaxBytes   = 5 << 20
	mediaPresignTTL = 5 * time.Minute
	mediaUploadTTL  = 15 * time.Minute // window to finish the upload and call /complete
	mediaKindImage  = "product_image"
)

// mediaVariantWidths are the rendered widths; the 600 px one becomes products.image_url.
var mediaVariantWidths = []int{200, 600, 1200}

var (
	ErrMediaNotFound         = errors.New("media upload not found")
	ErrMediaTooLarge         = errors.New("media file too large")
	ErrMediaUnsupported      = errors.New("media type not supported")
	ErrMediaDimensions       = errors.New("media dimensions out of range")
	ErrMediaAlreadyCompleted = errors.New("media upload already completed")
	ErrMediaExpired          = errors.New("media upload expired")
	ErrMediaNotUploaded      = errors.New("media file has not been uploaded yet")
	ErrMediaUnavailable      = errors.New("media storage is not configured")
)

type CreateMediaUploadInput struct {
	Kind        string    `json:"kind"`
	ProductID   uuid.UUID `json:"product_id"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
}

type MediaUploadTicket struct {
	ID        uuid.UUID         `json:"id"`
	UploadURL string            `json:"upload_url"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers"`
	ExpiresAt time.Time         `json:"expires_at"`
}

type MediaVariant struct {
	Width  int    `json:"width"`
	Height int    `json:"height"`
	URL    string `json:"url"`
}

type CompletedMediaUpload struct {
	ID        uuid.UUID      `json:"id"`
	ProductID uuid.UUID      `json:"product_id"`
	ImageURL  string         `json:"image_url"`
	Variants  []MediaVariant `json:"variants"`
}

type MediaService struct {
	db      *sqlx.DB
	store   storage.ObjectStore
	sem     chan struct{} // bounds concurrent decodes (a 25 MP image needs ~100 MB)
	nowFunc func() time.Time
}

// NewMediaService works without a store (R2 not configured): every call then answers ErrMediaUnavailable.
func NewMediaService(db *sqlx.DB, store storage.ObjectStore) *MediaService {
	return &MediaService{db: db, store: store, sem: make(chan struct{}, 2), nowFunc: time.Now}
}

func (s *MediaService) CreateUpload(ctx context.Context, actor *AuthenticatedStaff, in CreateMediaUploadInput) (*MediaUploadTicket, error) {
	if !actor.Can(rbac.CatalogManage) {
		return nil, ErrForbidden
	}
	if s.store == nil {
		return nil, ErrMediaUnavailable
	}
	f := map[string]string{}
	if in.Kind != mediaKindImage {
		f["kind"] = "harus product_image"
	}
	if in.ProductID == uuid.Nil {
		f["product_id"] = "wajib diisi"
	}
	if in.Size <= 0 {
		f["size"] = "wajib diisi (byte)"
	}
	if len(f) > 0 {
		return nil, &ValidationError{Fields: f}
	}
	switch in.ContentType {
	case "image/jpeg", "image/png", "image/webp":
	default:
		return nil, ErrMediaUnsupported
	}
	if in.Size > mediaMaxBytes {
		return nil, ErrMediaTooLarge
	}

	storeID, id, now := actor.Staff.Store.ID, uuid.New(), s.nowFunc().UTC()
	key := fmt.Sprintf("uploads/%s/%s", storeID, id)
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO media_uploads (id, store_id, kind, product_id, object_key, content_type, size_bytes, created_by, created_at, expires_at)
			SELECT $1, $2, $3, p.id, $5, $6, $7, $8, $9, $10 FROM products p WHERE p.store_id = $2 AND p.id = $4 AND p.archived_at IS NULL`,
			id, storeID, in.Kind, in.ProductID, key, in.ContentType, in.Size, actor.Staff.UserID, now, now.Add(mediaUploadTTL))
		if err != nil {
			return fmt.Errorf("failed to record media upload: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrMediaNotFound
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	url, headers, err := s.store.PresignPut(ctx, key, in.ContentType, in.Size, mediaPresignTTL)
	if err != nil {
		return nil, err
	}
	return &MediaUploadTicket{ID: id, UploadURL: url, Method: "PUT", Headers: headers, ExpiresAt: now.Add(mediaPresignTTL)}, nil
}

// Complete verifies the uploaded object and links its 600 px variant as the product image. A rejected
// file is deleted from the bucket; the upload stays pending until it expires.
func (s *MediaService) Complete(ctx context.Context, actor *AuthenticatedStaff, id uuid.UUID) (*CompletedMediaUpload, error) {
	if !actor.Can(rbac.CatalogManage) {
		return nil, ErrForbidden
	}
	if s.store == nil {
		return nil, ErrMediaUnavailable
	}
	storeID := actor.Staff.Store.ID
	var up struct {
		ProductID   uuid.UUID `db:"product_id"`
		Status      string    `db:"status"`
		ObjectKey   string    `db:"object_key"`
		ContentType string    `db:"content_type"`
		ExpiresAt   time.Time `db:"expires_at"`
	}
	err := database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		err := tx.GetContext(ctx, &up, `SELECT product_id, status, object_key, content_type, expires_at FROM media_uploads WHERE store_id = $1 AND id = $2`, storeID, id)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrMediaNotFound
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	switch {
	case up.Status == "completed":
		return nil, ErrMediaAlreadyCompleted
	case !s.nowFunc().Before(up.ExpiresAt):
		return nil, ErrMediaExpired
	}

	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	data, err := s.store.Get(ctx, up.ObjectKey, mediaMaxBytes)
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return nil, ErrMediaNotUploaded
	case errors.Is(err, storage.ErrTooLarge):
		_ = s.store.Delete(ctx, up.ObjectKey)
		return nil, ErrMediaTooLarge
	case err != nil:
		return nil, err
	}
	variants, err := imaging.Process(data, up.ContentType, mediaVariantWidths)
	if err != nil {
		_ = s.store.Delete(ctx, up.ObjectKey)
		if errors.Is(err, imaging.ErrDimensions) {
			return nil, ErrMediaDimensions
		}
		return nil, ErrMediaUnsupported
	}

	out := &CompletedMediaUpload{ID: id, ProductID: up.ProductID}
	for i, v := range variants {
		key := fmt.Sprintf("products/%s/%s/%d.jpg", storeID, id, mediaVariantWidths[i])
		if err := s.store.Put(ctx, key, "image/jpeg", v.JPEG); err != nil {
			return nil, err
		}
		out.Variants = append(out.Variants, MediaVariant{Width: v.Width, Height: v.Height, URL: s.store.PublicURL(key)})
	}
	out.ImageURL = out.Variants[1].URL
	variantsJSON, _ := json.Marshal(out.Variants)

	now := s.nowFunc().UTC()
	err = database.WithTenantTx(ctx, s.db, storeID, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE media_uploads SET status = 'completed', variants = $3, completed_at = $4
			WHERE store_id = $1 AND id = $2 AND status = 'pending'`, storeID, id, variantsJSON, now)
		if err != nil {
			return fmt.Errorf("failed to complete media upload: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrMediaAlreadyCompleted // a concurrent /complete won
		}
		var previous sql.NullString
		if err := tx.GetContext(ctx, &previous, `UPDATE products p SET image_url = $3, updated_at = $4
			FROM products old WHERE p.store_id = $1 AND p.id = $2 AND old.store_id = p.store_id AND old.id = p.id
			RETURNING old.image_url`, storeID, up.ProductID, out.ImageURL, now); err != nil {
			return fmt.Errorf("failed to link product image: %w", err)
		}
		actorID := actor.Staff.UserID
		meta, _ := json.Marshal(map[string]any{"upload_id": id, "image_url": out.ImageURL, "previous_image_url": previous.String})
		return postgres.InsertAuditLog(ctx, tx, &domain.AuditLog{
			StoreID: storeID, ActorID: &actorID, ActorName: actor.Staff.FullName, Action: "PRODUCT_IMAGE_CHANGED",
			Reason: "Foto produk diperbarui lewat unggah media", ReferenceID: up.ProductID.String(), Metadata: string(meta), CreatedAt: now,
		})
	})
	if err != nil {
		return nil, err
	}
	_ = s.store.Delete(ctx, up.ObjectKey) // the raw upload is no longer needed
	return out, nil
}
