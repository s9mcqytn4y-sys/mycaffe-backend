package qr

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrInvalidQRToken = errors.New("qr: invalid or corrupted token")
	ErrExpiredQRToken = errors.New("qr: token expired")
	ErrSignatureMismatch = errors.New("qr: signature verification failed")
)

// TablePayload holds decoded data embedded in a signed table QR.
type TablePayload struct {
	StoreID   uuid.UUID `json:"store_id"`
	TableID   uuid.UUID `json:"table_id"`
	TableNo   string    `json:"table_no"`
	ExpiresAt int64     `json:"exp"`
}

// TokenResolver generates and securely validates signed table tokens and store pairing QR codes.
type TokenResolver struct {
	secretKey []byte
}

// NewTokenResolver creates a TokenResolver with an HMAC secret.
func NewTokenResolver(secretKey string) (*TokenResolver, error) {
	key := strings.TrimSpace(secretKey)
	if len(key) < 16 {
		return nil, errors.New("qr token resolver requires a secret of at least 16 characters")
	}
	return &TokenResolver{secretKey: []byte(key)}, nil
}

// GenerateSignedToken creates a URL-safe signed payload: <base64-payload>.<hex-signature>.
func (r *TokenResolver) GenerateSignedToken(storeID, tableID uuid.UUID, tableNo string, ttl time.Duration) (string, error) {
	p := TablePayload{
		StoreID:   storeID,
		TableID:   tableID,
		TableNo:   strings.TrimSpace(tableNo),
		ExpiresAt: time.Now().Add(ttl).Unix(),
	}

	rawJSON, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("marshal table payload: %w", err)
	}

	b64Payload := base64.RawURLEncoding.EncodeToString(rawJSON)
	sig := r.computeSignature(b64Payload)

	return fmt.Sprintf("%s.%s", b64Payload, sig), nil
}

// VerifyAndDecode decodes and cryptographically verifies a signed token string.
func (r *TokenResolver) VerifyAndDecode(signedToken string) (*TablePayload, error) {
	parts := strings.Split(signedToken, ".")
	if len(parts) != 2 {
		return nil, ErrInvalidQRToken
	}

	b64Payload, signatureHex := parts[0], parts[1]
	expectedSig := r.computeSignature(b64Payload)

	if !hmac.Equal([]byte(signatureHex), []byte(expectedSig)) {
		return nil, ErrSignatureMismatch
	}

	rawJSON, err := base64.RawURLEncoding.DecodeString(b64Payload)
	if err != nil {
		return nil, ErrInvalidQRToken
	}

	var p TablePayload
	if err := json.Unmarshal(rawJSON, &p); err != nil {
		return nil, ErrInvalidQRToken
	}

	if p.ExpiresAt > 0 && time.Now().Unix() > p.ExpiresAt {
		return nil, ErrExpiredQRToken
	}

	return &p, nil
}

func (r *TokenResolver) computeSignature(data string) string {
	mac := hmac.New(sha256.New, r.secretKey)
	mac.Write([]byte(data))
	return hex.EncodeToString(mac.Sum(nil))
}
