package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

const (
	loginMaxAttempts = 10           // attempts allowed per window per IP
	loginWindow      = time.Minute  // window duration
)

type loginBucket struct {
	mu       sync.Mutex
	attempts int
	resetAt  time.Time
}

func (b *loginBucket) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if time.Now().After(b.resetAt) {
		b.attempts = 0
		b.resetAt = time.Now().Add(loginWindow)
	}
	b.attempts++
	return b.attempts <= loginMaxAttempts
}

// loginBuckets is a global map of IP → bucket. Buckets are never deleted
// (acceptable for a long-lived server; the map stays small in practice).
var loginBuckets sync.Map // map[string]*loginBucket

// LoginRateLimit limits POST /auth/login to loginMaxAttempts per IP per minute.
// Returns 429 when the limit is exceeded.
func LoginRateLimit() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			ip := c.RealIP()
			val, _ := loginBuckets.LoadOrStore(ip, &loginBucket{
				resetAt: time.Now().Add(loginWindow),
			})
			b := val.(*loginBucket)
			if !b.allow() {
				return echo.NewHTTPError(http.StatusTooManyRequests,
					"too many login attempts — try again in a minute")
			}
			return next(c)
		}
	}
}
