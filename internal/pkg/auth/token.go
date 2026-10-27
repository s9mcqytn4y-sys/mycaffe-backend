package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type CustomClaims struct {
	StoreID string `json:"store_id"`
	UserID  string `json:"user_id"`
	Role    string `json:"role"`
	jwt.RegisteredClaims
}

var (
	ErrTokenMissing   = errors.New("authentication token is missing")
	ErrTokenMalformed = errors.New("token format is malformed")
	ErrTokenExpired   = errors.New("authentication token has expired")
	ErrTokenInvalid   = errors.New("token signature or claims are invalid")
	ErrStoreIDMissing = errors.New("token does not contain a valid tenant store_id")
)

// ValidateToken cryptographically verifies a JWT token and returns authenticated claims
func ValidateToken(tokenString, secretKey string) (*CustomClaims, error) {
	if tokenString == "" {
		return nil, ErrTokenMissing
	}
	if secretKey == "" {
		return nil, errors.New("internal configuration error: empty JWT secret key")
	}

	token, err := jwt.ParseWithClaims(tokenString, &CustomClaims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secretKey), nil
	})

	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrTokenExpired
		}
		if errors.Is(err, jwt.ErrTokenMalformed) {
			return nil, ErrTokenMalformed
		}
		return nil, fmt.Errorf("%w: %v", ErrTokenInvalid, err)
	}

	claims, ok := token.Claims.(*CustomClaims)
	if !ok || !token.Valid {
		return nil, ErrTokenInvalid
	}

	if claims.StoreID == "" {
		return nil, ErrStoreIDMissing
	}

	if _, err := uuid.Parse(claims.StoreID); err != nil {
		return nil, fmt.Errorf("invalid store_id in token claims: %w", err)
	}

	return claims, nil
}

// GenerateTestToken generates a valid signed JWT for testing and ticket validation
func GenerateTestToken(storeID, userID, role, secretKey string, duration time.Duration) (string, error) {
	claims := CustomClaims{
		StoreID: storeID,
		UserID:  userID,
		Role:    role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(duration)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "kafe-senja-backend",
			Audience:  jwt.ClaimStrings{"kafe-senja-pos"},
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secretKey))
}
