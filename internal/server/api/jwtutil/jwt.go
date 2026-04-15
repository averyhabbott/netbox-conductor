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

const (
	TOTPPendingTTL  = 5 * time.Minute
	TOTPEnrollTTL   = 10 * time.Minute
)

// TOTPPendingClaims is a short-lived token issued after password verification
// when TOTP is enabled. The client must exchange it for full tokens via /auth/totp/verify.
type TOTPPendingClaims struct {
	UserID string `json:"sub"`
	Type   string `json:"typ"` // "totp_pending"
	jwt.RegisteredClaims
}

// IssueTOTPPending creates a 5-minute token asserting that password auth passed
// and only the TOTP code is outstanding.
func IssueTOTPPending(userID uuid.UUID, secret []byte) (string, error) {
	now := time.Now()
	claims := TOTPPendingClaims{
		UserID: userID.String(),
		Type:   "totp_pending",
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(TOTPPendingTTL)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
}

// ParseTOTPPending validates a totp_pending token and returns the embedded user ID.
func ParseTOTPPending(raw string, secret []byte) (uuid.UUID, error) {
	token, err := jwt.ParseWithClaims(raw, &TOTPPendingClaims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return secret, nil
	})
	if err != nil {
		return uuid.Nil, err
	}
	c, ok := token.Claims.(*TOTPPendingClaims)
	if !ok || !token.Valid || c.Type != "totp_pending" {
		return uuid.Nil, fmt.Errorf("invalid totp_pending token")
	}
	return uuid.Parse(c.UserID)
}

// TOTPEnrollClaims carries a pending TOTP secret during the enrollment confirmation step.
type TOTPEnrollClaims struct {
	UserID string `json:"sub"`
	Secret string `json:"sec"` // base32 TOTP secret (not yet committed to DB)
	Type   string `json:"typ"` // "totp_enroll"
	jwt.RegisteredClaims
}

// IssueTOTPEnroll creates a short-lived token embedding the TOTP secret for enrollment confirmation.
func IssueTOTPEnroll(userID uuid.UUID, secret string, signingKey []byte) (string, error) {
	now := time.Now()
	claims := TOTPEnrollClaims{
		UserID: userID.String(),
		Secret: secret,
		Type:   "totp_enroll",
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(TOTPEnrollTTL)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(signingKey)
}

// ParseTOTPEnroll validates a totp_enroll token and returns (userID, secret).
func ParseTOTPEnroll(raw string, signingKey []byte) (uuid.UUID, string, error) {
	token, err := jwt.ParseWithClaims(raw, &TOTPEnrollClaims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return signingKey, nil
	})
	if err != nil {
		return uuid.Nil, "", err
	}
	c, ok := token.Claims.(*TOTPEnrollClaims)
	if !ok || !token.Valid || c.Type != "totp_enroll" {
		return uuid.Nil, "", fmt.Errorf("invalid totp_enroll token")
	}
	uid, err := uuid.Parse(c.UserID)
	return uid, c.Secret, err
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
