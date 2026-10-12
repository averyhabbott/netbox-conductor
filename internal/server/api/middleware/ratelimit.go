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

// loginBuckets is a global map of route+IP → bucket. Expired buckets are swept
// every few minutes so the map can't grow without bound.
var (
	loginBuckets   sync.Map // map[string]*loginBucket
	loginSweepOnce sync.Once
)

func sweepLoginBuckets() {
	for range time.Tick(5 * time.Minute) {
		now := time.Now()
		loginBuckets.Range(func(k, v any) bool {
			b := v.(*loginBucket)
			b.mu.Lock()
			expired := now.After(b.resetAt)
			b.mu.Unlock()
			if expired {
				loginBuckets.Delete(k)
			}
			return true
		})
	}
}

// LoginRateLimit limits each route it's attached to (login, TOTP verify) to
// loginMaxAttempts per client IP per minute; each route has its own budget.
// Returns 429 when the limit is exceeded. The IP is the TCP peer address
// (router sets ExtractIPDirect), so X-Forwarded-For can't fake a fresh IP.
func LoginRateLimit() echo.MiddlewareFunc {
	loginSweepOnce.Do(func() { go sweepLoginBuckets() })
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			key := c.Path() + "|" + c.RealIP()
			val, _ := loginBuckets.LoadOrStore(key, &loginBucket{
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
