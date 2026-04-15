package middleware

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/averyhabbott/netbox-conductor/internal/server/db/queries"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

// Audit returns middleware that writes an audit log entry for every mutating request.
// It runs after the handler so it can capture the response status code.
func Audit(auditQ *queries.AuditQuerier) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			method := c.Request().Method

			// Only audit mutating requests
			if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
				return next(c)
			}

			// Run the handler first
			handlerErr := next(c)

			// Determine outcome
			status := c.Response().Status
			outcome := "success"
			if status >= 400 || handlerErr != nil {
				outcome = "failure"
			}

			// Extract actor
			var actorID *uuid.UUID
			if idStr, ok := c.Get(ContextKeyUserID).(string); ok && idStr != "" {
				if id, err := uuid.Parse(idStr); err == nil {
					actorID = &id
				}
			}

			// Build action string from method + route path template
			action := fmt.Sprintf("%s %s", method, c.Path())

			// Extract target UUID from common path params
			targetType, targetID := extractAuditTarget(c)

			go func() {
				if err := auditQ.Write(context.Background(), queries.WriteAuditParams{
					ActorUserID: actorID,
					Action:      action,
					TargetType:  targetType,
					TargetID:    targetID,
					Outcome:     outcome,
				}); err != nil {
					log.Printf("audit write error: %v", err)
				}
			}()

			return handlerErr
		}
	}
}

// extractAuditTarget pulls the most specific UUID param from the path for audit context.
func extractAuditTarget(c echo.Context) (targetType *string, targetID *uuid.UUID) {
	if nid := c.Param("nid"); nid != "" {
		if id, err := uuid.Parse(nid); err == nil {
			t := "node"
			return &t, &id
		}
	}
	if raw := c.Param("id"); raw != "" {
		if id, err := uuid.Parse(raw); err == nil {
			path := c.Path()
			var t string
			switch {
			case strings.Contains(path, "/clusters"):
				t = "cluster"
			case strings.Contains(path, "/tasks"):
				t = "task"
			default:
				t = "unknown"
			}
			return &t, &id
		}
	}
	return nil, nil
}
