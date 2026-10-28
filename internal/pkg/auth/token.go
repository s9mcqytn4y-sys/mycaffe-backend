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
	ExpectedIssuer   = "kafe-senja-backend"
	ExpectedAudience = "kafe-senja-pos"
)

var ValidRoles = []string{"owner", "manager", "cashier", "barista", "kitchen"}

type CustomClaims struct {
	StoreID string `json:"store_id"`
	UserID  string `json:"user_id"`
	Role    string `json:"role"`
	jwt.RegisteredClaims
}

var (
	ErrTokenMissing    = errors.New("authentication token is missing")
	ErrTokenMalformed  = errors.New("token format is malformed")
	ErrTokenExpired    = errors.New("authentication token has expired")
	ErrTokenInvalid    = errors.New("token signature or claims are invalid")
	ErrStoreIDMissing  = errors.New("token does not contain a valid tenant store_id")
	ErrUserIDMissing   = errors.New("token does not contain a valid user_id")
	ErrRoleInvalid     = errors.New("token does not contain a valid authorized role")
	ErrIssuerInvalid   = errors.New("token issuer is invalid")
	ErrAudienceInvalid = errors.New("token audience is invalid")
	ErrExpMissing      = errors.New("token expiration claim is required")
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
		// Strictly enforce HS256 algorithm
		if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, fmt.Errorf("unexpected signing method alg: %v, strictly require HS256", t.Header["alg"])
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
		return nil, ErrTokenInvalid
	}

	claims, ok := token.Claims.(*CustomClaims)
	if !ok || !token.Valid {
		return nil, ErrTokenInvalid
	}

	// 1. Mandatory Expiration Claim
	if claims.ExpiresAt == nil {
		return nil, ErrExpMissing
	}

	// 2. Issuer Validation
	if claims.Issuer != ExpectedIssuer {
		return nil, ErrIssuerInvalid
	}

	// 3. Audience Validation
	if !slices.Contains(claims.Audience, ExpectedAudience) {
		return nil, ErrAudienceInvalid
	}

	// 4. Store ID UUID Validation
	if claims.StoreID == "" {
		return nil, ErrStoreIDMissing
	}
	if _, err := uuid.Parse(claims.StoreID); err != nil {
		return nil, ErrStoreIDMissing
	}

	// 5. User ID UUID Validation
	if claims.UserID == "" {
		return nil, ErrUserIDMissing
	}
	if _, err := uuid.Parse(claims.UserID); err != nil {
		return nil, ErrUserIDMissing
	}

	// 6. Role Enum Validation
	if !slices.Contains(ValidRoles, claims.Role) {
		return nil, ErrRoleInvalid
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
			Issuer:    ExpectedIssuer,
			Audience:  jwt.ClaimStrings{ExpectedAudience},
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secretKey))
}
