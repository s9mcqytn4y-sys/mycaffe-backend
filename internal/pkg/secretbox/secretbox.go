// Package secretbox provides authenticated AES-256-GCM envelope encryption with tenant-bound
// Associated Authenticated Data (AAD) for BYOK payment, SMTP, and WhatsApp credentials.
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

var (
	ErrKeyEmpty        = errors.New("credential encryption key cannot be empty")
	ErrKeyTooShort     = errors.New("credential encryption key must be at least 32 bytes (64 hex chars, 44 base64 chars, or >=32 char passphrase)")
	ErrPlaintextEmpty  = errors.New("plaintext secret cannot be empty")
	ErrCiphertextShort = errors.New("ciphertext is shorter than AES-GCM nonce + tag")
	ErrDecryptFailed   = errors.New("credential decryption or AAD authentication failed")
)

// Box encrypts and decrypts secrets using AES-256-GCM with per-encryption random 96-bit nonces
// and mandatory Associated Authenticated Data (e.g. "store:<uuid>:midtrans_server_key") to prevent
// cross-tenant ciphertext transplantation.
type Box struct {
	key [32]byte
}

// New constructs a Box from a 64-char hex key, base64-encoded 32-byte key, or >=32-byte passphrase.
func New(rawKey string) (*Box, error) {
	trimmed := strings.TrimSpace(rawKey)
	if trimmed == "" {
		return nil, ErrKeyEmpty
	}

	var derived [32]byte
	if len(trimmed) == 64 {
		if b, err := hex.DecodeString(trimmed); err == nil && len(b) == 32 {
			copy(derived[:], b)
			return &Box{key: derived}, nil
		}
	}
	if b, err := base64.StdEncoding.DecodeString(trimmed); err == nil && len(b) == 32 {
		copy(derived[:], b)
		return &Box{key: derived}, nil
	}
	if len(trimmed) < 32 {
		return nil, ErrKeyTooShort
	}
	derived = sha256.Sum256([]byte(trimmed))
	return &Box{key: derived}, nil
}

// NewDevFallback returns a deterministic development/test Box when rawKey is empty outside staging/production.
func NewDevFallback(rawKey, appEnv string) (*Box, error) {
	if strings.TrimSpace(rawKey) != "" {
		return New(rawKey)
	}
	env := strings.ToLower(strings.TrimSpace(appEnv))
	if env == "production" || env == "staging" {
		return nil, fmt.Errorf("%w in %s environment", ErrKeyEmpty, env)
	}
	return New("mycaffe-dev-only-deterministic-32byte-credential-key!")
}

// Seal encrypts plaintext with AES-256-GCM bound to aad (e.g. storeID + field purpose).
func (b *Box) Seal(plaintext string, aad string) ([]byte, error) {
	if b == nil {
		return nil, ErrKeyEmpty
	}
	if strings.TrimSpace(plaintext) == "" {
		return nil, ErrPlaintextEmpty
	}
	block, err := aes.NewCipher(b.key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), []byte(aad))
	return sealed, nil
}

// Open decrypts and authenticates ciphertext using the exact same aad.
func (b *Box) Open(ciphertext []byte, aad string) (string, error) {
	if b == nil {
		return "", ErrKeyEmpty
	}
	block, err := aes.NewCipher(b.key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	minLen := gcm.NonceSize() + gcm.Overhead()
	if len(ciphertext) < minLen {
		return "", ErrCiphertextShort
	}
	nonce := ciphertext[:gcm.NonceSize()]
	payload := ciphertext[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, payload, []byte(aad))
	if err != nil {
		return "", ErrDecryptFailed
	}
	return string(plain), nil
}

// MaskSecret produces a non-sensitive display hint (e.g. "SB-Mid-****qb6g") without exposing the secret.
func MaskSecret(secret string) string {
	s := strings.TrimSpace(secret)
	if s == "" {
		return ""
	}
	if len(s) <= 8 {
		return "****"
	}
	prefixLen := 6
	if len(s) < 14 {
		prefixLen = 3
	}
	return s[:prefixLen] + "****" + s[len(s)-4:]
}
