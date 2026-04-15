package handlers

import (
	"net/http"
	"time"

	"github.com/abottVU/netbox-failover/internal/server/api/jwtutil"
	"github.com/abottVU/netbox-failover/internal/server/api/middleware"
	"github.com/abottVU/netbox-failover/internal/server/crypto"
	"github.com/abottVU/netbox-failover/internal/server/db/queries"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"
)

// AuthHandler handles authentication endpoints.
type AuthHandler struct {
	users         *queries.UserQuerier
	refreshTokens *queries.RefreshTokenQuerier
	jwtSecret     []byte
}

func NewAuthHandler(
	users *queries.UserQuerier,
	refreshTokens *queries.RefreshTokenQuerier,
	jwtSecret []byte,
) *AuthHandler {
	return &AuthHandler{users: users, refreshTokens: refreshTokens, jwtSecret: jwtSecret}
}

type loginRequest struct {
	Username string `json:"username" validate:"required"`
	Password string `json:"password" validate:"required"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"` // seconds
}

// Login godoc
// POST /api/v1/auth/login
func (h *AuthHandler) Login(c echo.Context) error {
	var req loginRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	user, err := h.users.GetByUsername(c.Request().Context(), req.Username)
	if err != nil {
		// Don't reveal whether the user exists
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid credentials")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid credentials")
	}

	accessToken, err := jwtutil.IssueAccess(user.ID, user.Role, h.jwtSecret)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "could not issue token")
	}

	rawRefresh, err := jwtutil.GenerateRefreshToken()
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "could not generate refresh token")
	}

	expiresAt := time.Now().Add(jwtutil.RefreshTokenTTL)
	if err := h.refreshTokens.Create(
		c.Request().Context(),
		user.ID,
		crypto.HashToken(rawRefresh),
		expiresAt,
	); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "could not store refresh token")
	}

	_ = h.users.UpdateLastLogin(c.Request().Context(), user.ID)

	return c.JSON(http.StatusOK, tokenResponse{
		AccessToken:  accessToken,
		RefreshToken: rawRefresh,
		ExpiresIn:    int(jwtutil.AccessTokenTTL.Seconds()),
	})
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token" validate:"required"`
}

// Refresh godoc
// POST /api/v1/auth/refresh
func (h *AuthHandler) Refresh(c echo.Context) error {
	var req refreshRequest
	if err := c.Bind(&req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}

	hash := crypto.HashToken(req.RefreshToken)
	rt, err := h.refreshTokens.GetValid(c.Request().Context(), hash)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid or expired refresh token")
	}

	user, err := h.users.GetByID(c.Request().Context(), rt.UserID)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "user not found")
	}

	accessToken, err := jwtutil.IssueAccess(user.ID, user.Role, h.jwtSecret)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "could not issue token")
	}

	return c.JSON(http.StatusOK, map[string]any{
		"access_token": accessToken,
		"expires_in":   int(jwtutil.AccessTokenTTL.Seconds()),
	})
}

// Logout godoc
// POST /api/v1/auth/logout
func (h *AuthHandler) Logout(c echo.Context) error {
	var req refreshRequest
	if err := c.Bind(&req); err == nil && req.RefreshToken != "" {
		hash := crypto.HashToken(req.RefreshToken)
		_ = h.refreshTokens.Revoke(c.Request().Context(), hash)
	}
	return c.NoContent(http.StatusNoContent)
}

type meResponse struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

// Me godoc
// GET /api/v1/auth/me
func (h *AuthHandler) Me(c echo.Context) error {
	userIDStr, _ := c.Get(middleware.ContextKeyUserID).(string)
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid user context")
	}

	user, err := h.users.GetByID(c.Request().Context(), userID)
	if err != nil {
		return echo.NewHTTPError(http.StatusNotFound, "user not found")
	}

	return c.JSON(http.StatusOK, meResponse{
		ID:       user.ID.String(),
		Username: user.Username,
		Role:     user.Role,
	})
}
