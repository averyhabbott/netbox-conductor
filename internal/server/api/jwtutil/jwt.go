package jwtutil

import (
	"crypto/rand"
	"fmt"
	"io"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	AccessTokenTTL  = 15 * time.Minute
	RefreshTokenTTL = 7 * 24 * time.Hour
	refreshTokenLen = 48 // bytes
)

// Claims holds the custom JWT claims for access tokens.
type Claims struct {
	UserID string `json:"sub"`
	Role   string `json:"role"`
	jwt.RegisteredClaims
}

// IssueAccess creates a signed HS256 access token.
func IssueAccess(userID uuid.UUID, role string, secret []byte) (string, error) {
	now := time.Now()
	claims := Claims{
		UserID: userID.String(),
		Role:   role,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(AccessTokenTTL)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(secret)
}

// ParseAccess validates and parses an access token.
func ParseAccess(raw string, secret []byte) (*Claims, error) {
	token, err := jwt.ParseWithClaims(raw, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return secret, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}
	return claims, nil
}

// GenerateRefreshToken returns a cryptographically random hex-encoded refresh token.
func GenerateRefreshToken() (string, error) {
	raw := make([]byte, refreshTokenLen)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", fmt.Errorf("generating refresh token: %w", err)
	}
	// hex encode for safe storage in cookies/headers
	return fmt.Sprintf("%x", raw), nil
}
