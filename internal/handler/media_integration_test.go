package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/config"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/handler"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/database"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/rbac"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/storage"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/repository/postgres"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/service"
)

// TestIntegrationMedia covers B3 with an in-memory bucket: presign, verify, JPEG variants without
// EXIF, linking products.image_url, rejects (type spoofing, size, dimensions, double completion,
// expiry, other store's product), RBAC, and that image_url cannot be written through the catalog API.
func TestIntegrationMedia(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_APP_DATABASE_URL"))
	if dsn == "" {
		t.Skip("TEST_APP_DATABASE_URL not set; skipping media integration test")
	}
	db, err := database.NewPostgresDB(&config.Config{DatabaseURL: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	gin.SetMode(gin.TestMode)
	authSvc := service.NewAuthService(db, postgres.NewAuthRepository(), "integration-test-jwt-secret-at-least-32-chars", service.AuthPolicy{
		AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 12 * time.Hour, SessionIdleTimeout: 2 * time.Hour,
		MaxSessions: 3, PINMaxFailures: 5, PINLockout: 15 * time.Minute, MaxConcurrentHashes: 8,
	})
	ah := handler.NewAuthHandler(authSvc)
	bucket := storage.NewMemory()
	mh := handler.NewMediaHandler(service.NewMediaService(db, bucket))
	ch := handler.NewCatalogHandler(service.NewCatalogService(db))

	r := gin.New()
	r.POST("/login", ah.Login)
	media := r.Group("/media", ah.RequirePermission(rbac.CatalogManage))
	media.POST("/uploads", mh.CreateUpload)
	media.POST("/uploads/:id/complete", mh.Complete)
	r.POST("/catalog/products", ah.RequirePermission(rbac.CatalogManage), ch.CreateProduct)
	r.PATCH("/catalog/products/:id", ah.RequirePermission(rbac.CatalogManage), ch.UpdateProduct)

	call := func(method, path, token string, body any) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req := httptest.NewRequest(method, path, &buf)
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	login := func(slug, email string) string {
		t.Helper()
		res := call("POST", "/login", "", map[string]string{"store_slug": slug, "email": email, "pin": "482913"})
		if res.Code != http.StatusOK {
			t.Fatalf("login %s failed: %d: %s", email, res.Code, res.Body.String())
		}
		var body struct {
			AccessToken string `json:"access_token"`
		}
		_ = json.Unmarshal(res.Body.Bytes(), &body)
		return body.AccessToken
	}
	inStore := func(storeID uuid.UUID, fn func(tx *sqlx.Tx) error) {
		t.Helper()
		if err := database.WithTenantTx(ctx, db, storeID, fn); err != nil {
			t.Fatalf("tenant tx failed: %v", err)
		}
	}

	centralID := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	expressID := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	owner := login("central-cafe", "owner.central@mycaffe.local")
	cashier := login("central-cafe", "cashier.central@mycaffe.local")
	newProduct := func(storeID uuid.UUID) uuid.UUID {
		id := uuid.New()
		inStore(storeID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `WITH p AS (INSERT INTO products (id, store_id, name, base_price, is_available) VALUES ($1, $2, $3, 0, true) RETURNING store_id, id)
				INSERT INTO product_variants (store_id, product_id, name, price, is_default) SELECT store_id, id, 'Reguler', 10000, true FROM p`,
				id, storeID, "Foto "+id.String()[:8])
			return err
		})
		return id
	}
	photo := func(w, h int) []byte {
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				img.Set(x, y, color.RGBA{uint8(x), uint8(y), 120, 255})
			}
		}
		var buf bytes.Buffer
		_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
		// A camera-style EXIF block (orientation 1 plus a fake GPS marker) that must not survive.
		exif := append([]byte("Exif\x00\x00II\x2a\x00\x08\x00\x00\x00\x00\x00"), []byte("GPS-SECRET")...)
		seg := []byte{0xFF, 0xE1, byte((len(exif) + 2) >> 8), byte(len(exif) + 2)}
		data := buf.Bytes()
		return append(append(append([]byte{}, data[:2]...), append(seg, exif...)...), data[2:]...)
	}
	type ticket struct {
		ID        uuid.UUID         `json:"id"`
		UploadURL string            `json:"upload_url"`
		Method    string            `json:"method"`
		Headers   map[string]string `json:"headers"`
	}
	start := func(productID uuid.UUID, contentType string, size int) (*httptest.ResponseRecorder, ticket) {
		res := call("POST", "/media/uploads", owner, map[string]any{"kind": "product_image", "product_id": productID, "content_type": contentType, "size": size})
		var tk ticket
		_ = json.Unmarshal(res.Body.Bytes(), &tk)
		return res, tk
	}
	complete := func(id uuid.UUID) *httptest.ResponseRecorder {
		return call("POST", fmt.Sprintf("/media/uploads/%s/complete", id), owner, nil)
	}
	keyOf := func(tk ticket) string { return strings.TrimPrefix(tk.UploadURL, "memory://") }

	t.Run("HappyPath", func(t *testing.T) {
		productID := newProduct(centralID)
		data := photo(1600, 900)
		res, tk := start(productID, "image/jpeg", len(data))
		if res.Code != http.StatusCreated || tk.Method != "PUT" || tk.Headers["Content-Type"] != "image/jpeg" || !strings.HasPrefix(keyOf(tk), "uploads/"+centralID.String()+"/") {
			t.Fatalf("upload ticket wrong: %d %s", res.Code, res.Body.String())
		}
		bucket.Upload(keyOf(tk), data)
		done := complete(tk.ID)
		if done.Code != http.StatusOK {
			t.Fatalf("complete failed: %d: %s", done.Code, done.Body.String())
		}
		var out service.CompletedMediaUpload
		_ = json.Unmarshal(done.Body.Bytes(), &out)
		if len(out.Variants) != 3 || out.Variants[0].Width != 200 || out.Variants[1].Width != 600 || out.Variants[2].Width != 1200 ||
			out.Variants[2].Height != 675 || out.ImageURL != out.Variants[1].URL {
			t.Fatalf("variants wrong: %+v", out)
		}
		for _, v := range out.Variants {
			key := strings.TrimPrefix(v.URL, "https://media.example.test/")
			obj, ct, ok := bucket.Object(key)
			if !ok || ct != "image/jpeg" || bytes.Contains(obj, []byte("Exif")) || bytes.Contains(obj, []byte("GPS-SECRET")) {
				t.Fatalf("variant %s must be a metadata-free JPEG (stored=%v type=%s)", key, ok, ct)
			}
		}
		if _, _, ok := bucket.Object(keyOf(tk)); ok {
			t.Fatal("the raw upload must be deleted after processing")
		}
		var imageURL string
		var audits int
		inStore(centralID, func(tx *sqlx.Tx) error {
			if err := tx.GetContext(ctx, &imageURL, `SELECT COALESCE(image_url, '') FROM products WHERE store_id = $1 AND id = $2`, centralID, productID); err != nil {
				return err
			}
			return tx.GetContext(ctx, &audits, `SELECT COUNT(*) FROM audit_logs WHERE store_id = $1 AND action = 'PRODUCT_IMAGE_CHANGED' AND reference_id = $2`, centralID, productID.String())
		})
		if imageURL != out.ImageURL || audits != 1 {
			t.Fatalf("product image must be linked and audited: url=%q audits=%d", imageURL, audits)
		}
		if res := complete(tk.ID); res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "MEDIA_ALREADY_COMPLETED") {
			t.Fatalf("second complete must be 409, got %d: %s", res.Code, res.Body.String())
		}
	})

	t.Run("Rejects", func(t *testing.T) {
		productID := newProduct(centralID)
		if res, _ := start(productID, "image/gif", 1000); res.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("gif must be 415, got %d", res.Code)
		}
		if res, _ := start(productID, "image/jpeg", 5<<20+1); res.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("over 5 MB must be 413, got %d", res.Code)
		}
		if res, _ := start(newProduct(expressID), "image/jpeg", 1000); res.Code != http.StatusNotFound {
			t.Fatalf("another store's product must be 404, got %d", res.Code)
		}
		if res := call("POST", "/media/uploads", owner, map[string]any{"kind": "avatar", "content_type": "image/jpeg", "size": 1}); res.Code != http.StatusUnprocessableEntity {
			t.Fatalf("unknown kind and missing product must be 422, got %d", res.Code)
		}

		// Declared JPEG, actually PNG: 415 and the object is removed.
		var pngBuf bytes.Buffer
		_ = png.Encode(&pngBuf, image.NewRGBA(image.Rect(0, 0, 300, 300)))
		_, tk := start(productID, "image/jpeg", pngBuf.Len())
		if res := complete(tk.ID); res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), "MEDIA_NOT_UPLOADED") {
			t.Fatalf("complete before upload must be 409 MEDIA_NOT_UPLOADED, got %d", res.Code)
		}
		bucket.Upload(keyOf(tk), pngBuf.Bytes())
		if res := complete(tk.ID); res.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("spoofed type must be 415, got %d: %s", res.Code, res.Body.String())
		}
		if _, _, ok := bucket.Object(keyOf(tk)); ok {
			t.Fatal("a rejected upload must be deleted")
		}

		// Uploaded object bigger than the limit (the declared size lied).
		_, tk = start(productID, "image/jpeg", 1000)
		bucket.Upload(keyOf(tk), make([]byte, 5<<20+1))
		if res := complete(tk.ID); res.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("oversized object must be 413, got %d", res.Code)
		}

		small := photo(150, 150)
		_, tk = start(productID, "image/jpeg", len(small))
		bucket.Upload(keyOf(tk), small)
		if res := complete(tk.ID); res.Code != http.StatusUnprocessableEntity || !strings.Contains(res.Body.String(), "MEDIA_DIMENSIONS") {
			t.Fatalf("150x150 must be 422 MEDIA_DIMENSIONS, got %d", res.Code)
		}

		data := photo(400, 400)
		_, tk = start(productID, "image/jpeg", len(data))
		bucket.Upload(keyOf(tk), data)
		inStore(centralID, func(tx *sqlx.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE media_uploads SET created_at = NOW() - INTERVAL '1 hour', expires_at = NOW() - INTERVAL '1 minute' WHERE store_id = $1 AND id = $2`, centralID, tk.ID)
			return err
		})
		if res := complete(tk.ID); res.Code != http.StatusGone {
			t.Fatalf("expired upload must be 410, got %d", res.Code)
		}
		if res := complete(uuid.New()); res.Code != http.StatusNotFound {
			t.Fatalf("unknown upload must be 404, got %d", res.Code)
		}
	})

	t.Run("RBAC", func(t *testing.T) {
		if res := call("POST", "/media/uploads", cashier, map[string]any{"kind": "product_image", "product_id": newProduct(centralID), "content_type": "image/jpeg", "size": 1000}); res.Code != http.StatusForbidden {
			t.Fatalf("cashier must get 403, got %d", res.Code)
		}
		if res := call("POST", fmt.Sprintf("/media/uploads/%s/complete", uuid.New()), cashier, nil); res.Code != http.StatusForbidden {
			t.Fatalf("cashier must get 403 on complete, got %d", res.Code)
		}
	})

	t.Run("ProductImageURLNotWritable", func(t *testing.T) {
		productID := newProduct(centralID)
		res := call("PATCH", fmt.Sprintf("/catalog/products/%s", productID), owner, map[string]any{"image_url": "https://evil.example/x.jpg"})
		if res.Code != http.StatusUnprocessableEntity || !strings.Contains(res.Body.String(), "image_url") {
			t.Fatalf("PATCH image_url must be 422, got %d: %s", res.Code, res.Body.String())
		}
		res = call("POST", "/catalog/products", owner, map[string]any{"name": "Foto Liar", "image_url": "https://evil.example/x.jpg",
			"variants": []map[string]any{{"name": "Reguler", "price": 10000}}})
		if res.Code != http.StatusUnprocessableEntity || !strings.Contains(res.Body.String(), "image_url") {
			t.Fatalf("POST with image_url must be 422, got %d: %s", res.Code, res.Body.String())
		}
	})
}
