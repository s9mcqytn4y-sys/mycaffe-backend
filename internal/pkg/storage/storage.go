// Package storage wraps the public media bucket (Cloudflare R2, S3 API) behind a small interface so the
// media service can be tested with an in-memory store.
package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

var (
	ErrNotFound = errors.New("object not found")
	ErrTooLarge = errors.New("object larger than allowed")
)

// ObjectStore is what the media service needs from a bucket.
type ObjectStore interface {
	// PresignPut returns a URL the browser PUTs the file to, with the headers it must send.
	PresignPut(ctx context.Context, key, contentType string, size int64, ttl time.Duration) (url string, headers map[string]string, err error)
	// Get reads an object, failing with ErrTooLarge beyond maxBytes and ErrNotFound when absent.
	Get(ctx context.Context, key string, maxBytes int64) ([]byte, error)
	Put(ctx context.Context, key, contentType string, body []byte) error
	Delete(ctx context.Context, key string) error
	// PublicURL is the CDN URL of an object in the public bucket.
	PublicURL(key string) string
}

// R2Config is the media bucket configuration (R2_* environment variables).
type R2Config struct {
	AccountID, AccessKeyID, SecretAccessKey, Bucket, PublicURL string
}

type r2Store struct {
	client    *s3.Client
	presign   *s3.PresignClient
	bucket    string
	publicURL string
}

// NewR2 returns the R2 store, or an error when the media bucket is not configured.
func NewR2(cfg R2Config) (ObjectStore, error) {
	if cfg.AccountID == "" || cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" || cfg.Bucket == "" || cfg.PublicURL == "" {
		return nil, errors.New("R2 media bucket is not configured")
	}
	client := s3.New(s3.Options{
		Region:       "auto",
		BaseEndpoint: aws.String(fmt.Sprintf("https://%s.r2.cloudflarestorage.com", cfg.AccountID)),
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		UsePathStyle: true,
	})
	return &r2Store{client: client, presign: s3.NewPresignClient(client), bucket: cfg.Bucket, publicURL: strings.TrimRight(cfg.PublicURL, "/")}, nil
}

func (r *r2Store) PresignPut(ctx context.Context, key, contentType string, size int64, ttl time.Duration) (string, map[string]string, error) {
	req, err := r.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(r.bucket), Key: aws.String(key), ContentType: aws.String(contentType), ContentLength: aws.Int64(size),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", nil, fmt.Errorf("failed to presign upload: %w", err)
	}
	return req.URL, map[string]string{"Content-Type": contentType}, nil
}

func (r *r2Store) Get(ctx context.Context, key string, maxBytes int64) ([]byte, error) {
	out, err := r.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(r.bucket), Key: aws.String(key)})
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "NoSuchKey" || apiErr.ErrorCode() == "NotFound") {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("failed to read object: %w", err)
	}
	defer out.Body.Close()
	data, err := io.ReadAll(io.LimitReader(out.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read object body: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrTooLarge
	}
	return data, nil
}

func (r *r2Store) Put(ctx context.Context, key, contentType string, body []byte) error {
	_, err := r.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(r.bucket), Key: aws.String(key), Body: bytes.NewReader(body),
		ContentType: aws.String(contentType), CacheControl: aws.String("public, max-age=31536000, immutable"),
	})
	if err != nil {
		return fmt.Errorf("failed to write object: %w", err)
	}
	return nil
}

func (r *r2Store) Delete(ctx context.Context, key string) error {
	if _, err := r.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(r.bucket), Key: aws.String(key)}); err != nil {
		return fmt.Errorf("failed to delete object: %w", err)
	}
	return nil
}

func (r *r2Store) PublicURL(key string) string { return r.publicURL + "/" + key }

// Memory is an in-memory ObjectStore for tests; Upload stands in for the browser's presigned PUT.
type Memory struct {
	mu      sync.Mutex
	objects map[string][]byte
	types   map[string]string
}

func NewMemory() *Memory {
	return &Memory{objects: map[string][]byte{}, types: map[string]string{}}
}

func (m *Memory) PresignPut(_ context.Context, key, contentType string, _ int64, _ time.Duration) (string, map[string]string, error) {
	return "memory://" + key, map[string]string{"Content-Type": contentType}, nil
}

func (m *Memory) Upload(key string, body []byte) { _ = m.Put(context.Background(), key, "", body) }

func (m *Memory) Get(_ context.Context, key string, maxBytes int64) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[key]
	if !ok {
		return nil, ErrNotFound
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrTooLarge
	}
	return data, nil
}

func (m *Memory) Put(_ context.Context, key, contentType string, body []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key], m.types[key] = body, contentType
	return nil
}

func (m *Memory) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	delete(m.types, key)
	return nil
}

func (m *Memory) PublicURL(key string) string { return "https://media.example.test/" + key }

// Object returns a stored object and its content type (tests).
func (m *Memory) Object(key string) ([]byte, string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.objects[key]
	return data, m.types[key], ok
}
