package auth

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	// TokenTypeStaffAccess separates staff access tokens from future guest/QR tokens (ADR-003 D4).
	TokenTypeStaffAccess = "staff_access"
	// staffKeyID names the signing key. shortcut: single key from JWT_SECRET_KEY, add the
	// STAFF_JWT_KEYS key ring when the first rotation is needed.
	staffKeyID = "k1"
)

var (
	ErrTokenTypeInvalid  = errors.New("token type is not a staff access token")
	ErrSessionIDMissing  = errors.New("token does not contain a valid session id")
	ErrTokenKeyIDInvalid = errors.New("token key id is unknown")
)

// StaffClaims are the claims of an issued staff access token. sub is users.id, sid is auth_sessions.id.
type StaffClaims struct {
	Type      string `json:"typ"`
	StoreID   string `json:"store_id"`
	Role      string `json:"role"`
	SessionID string `json:"sid"`
	jwt.RegisteredClaims
}

// IssueStaffAccessToken signs a short-lived HS256 staff access token.
func IssueStaffAccessToken(secret string, storeID, userID, sessionID uuid.UUID, role string, now time.Time, ttl time.Duration) (string, time.Time, error) {
	if len(secret) < 32 {
		return "", time.Time{}, errors.New("internal configuration error: JWT secret must be at least 32 characters")
	}
	if !slices.Contains(ValidRoles, role) {
		return "", time.Time{}, ErrRoleInvalid
	}
	expiresAt := now.Add(ttl)
	claims := StaffClaims{
		Type:      TokenTypeStaffAccess,
		StoreID:   storeID.String(),
		Role:      role,
		SessionID: sessionID.String(),
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID.String(),
			ID:        uuid.NewString(),
			Issuer:    ExpectedIssuer,
			Audience:  jwt.ClaimStrings{ExpectedAudience},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	token.Header["kid"] = staffKeyID
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to sign staff access token: %w", err)
	}
	return signed, expiresAt, nil
}

// ValidateStaffAccessToken verifies signature, algorithm, key id, purpose, issuer, audience,
// expiry and identifier claims. Session/user liveness is checked by the caller against the database.
func ValidateStaffAccessToken(tokenString, secret string) (*StaffClaims, error) {
	if tokenString == "" {
		return nil, ErrTokenMissing
	}
	if secret == "" {
		return nil, errors.New("internal configuration error: empty JWT secret key")
	}
	token, err := jwt.ParseWithClaims(tokenString, &StaffClaims{}, func(t *jwt.Token) (interface{}, error) {
		if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, fmt.Errorf("unexpected signing method alg: %v", t.Header["alg"])
		}
		if kid, _ := t.Header["kid"].(string); kid != staffKeyID {
			return nil, ErrTokenKeyIDInvalid
		}
		return []byte(secret), nil
	}, jwt.WithExpirationRequired(), jwt.WithIssuer(ExpectedIssuer), jwt.WithAudience(ExpectedAudience))
	if err != nil {
		switch {
		case errors.Is(err, jwt.ErrTokenExpired):
			return nil, ErrTokenExpired
		case errors.Is(err, jwt.ErrTokenMalformed):
			return nil, ErrTokenMalformed
		case errors.Is(err, jwt.ErrTokenRequiredClaimMissing):
			return nil, ErrExpMissing
		case errors.Is(err, jwt.ErrTokenInvalidIssuer):
			return nil, ErrIssuerInvalid
		case errors.Is(err, jwt.ErrTokenInvalidAudience):
			return nil, ErrAudienceInvalid
		}
		return nil, ErrTokenInvalid
	}
	claims, ok := token.Claims.(*StaffClaims)
	if !ok || !token.Valid {
		return nil, ErrTokenInvalid
	}
	if claims.Type != TokenTypeStaffAccess {
		return nil, ErrTokenTypeInvalid
	}
	if _, err := uuid.Parse(claims.StoreID); err != nil {
		return nil, ErrStoreIDMissing
	}
	if _, err := uuid.Parse(claims.Subject); err != nil {
		return nil, ErrUserIDMissing
	}
	if _, err := uuid.Parse(claims.SessionID); err != nil {
		return nil, ErrSessionIDMissing
	}
	if !slices.Contains(ValidRoles, claims.Role) {
		return nil, ErrRoleInvalid
	}
	return claims, nil
}
