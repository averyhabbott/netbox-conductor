package middleware

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestLoginRateLimitIgnoresForwardedFor(t *testing.T) {
	e := echo.New()
	e.IPExtractor = echo.ExtractIPDirect()
	e.POST("/login", func(c echo.Context) error { return c.NoContent(http.StatusOK) }, LoginRateLimit())

	var last int
	for i := 0; i <= loginMaxAttempts; i++ {
		req := httptest.NewRequest(http.MethodPost, "/login", nil)
		req.RemoteAddr = "192.0.2.7:5555"
		req.Header.Set("X-Forwarded-For", "10.0.0."+strconv.Itoa(i)) // fresh fake IP each time
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		last = rec.Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("attempt %d got %d, want 429", loginMaxAttempts+1, last)
	}
}
