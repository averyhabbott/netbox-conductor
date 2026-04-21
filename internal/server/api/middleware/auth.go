package middleware

import (
	"errors"
	"net/http"
	"strings"

	"github.com/averyhabbott/netbox-conductor/internal/server/api/jwtutil"
	"github.com/labstack/echo/v4"
)

const (
	ContextKeyUserID   = "user_id"
	ContextKeyUsername = "username"
	ContextKeyRole     = "user_role"
)

// JWT validates the Authorization: Bearer <token> header and populates
// echo.Context with the user ID and role claims.
func JWT(secret []byte) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			header := c.Request().Header.Get("Authorization")
			if !strings.HasPrefix(header, "Bearer ") {
				return echo.NewHTTPError(http.StatusUnauthorized, "missing bearer token")
			}
			raw := strings.TrimPrefix(header, "Bearer ")

			claims, err := jwtutil.ParseAccess(raw, secret)
			if err != nil {
				return echo.NewHTTPError(http.StatusUnauthorized, "invalid token")
			}

			c.Set(ContextKeyUserID, claims.UserID)
			c.Set(ContextKeyUsername, claims.Username)
			c.Set(ContextKeyRole, claims.Role)
			return next(c)
		}
	}
}

// RequireRole returns middleware that enforces a minimum role level.
// Order: admin > operator > viewer
func RequireRole(minRole string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			role, ok := c.Get(ContextKeyRole).(string)
			if !ok {
				return echo.NewHTTPError(http.StatusForbidden, "no role claim")
			}
			if !roleAtLeast(role, minRole) {
				return echo.NewHTTPError(http.StatusForbidden, "insufficient permissions")
			}
			return next(c)
		}
	}
}

func roleAtLeast(have, need string) bool {
	rank := map[string]int{"viewer": 1, "operator": 2, "admin": 3}
	return rank[have] >= rank[need]
}

// ErrUnauthorized is a sentinel for auth failures.
var ErrUnauthorized = errors.New("unauthorized")
