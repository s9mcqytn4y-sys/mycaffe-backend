package handler

import (
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/logging"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/metrics"
)

const RequestIDHeader = "X-Request-ID"

var safeRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{8,64}$`)

// SentryClientOptions builds the fail-safe Sentry SDK configuration with Free Plan quota
// protection (10% trace sampling, 100% error capture) and strict PII / credential scrubbing
// (closing Security Risk Register R6).
func SentryClientOptions(dsn, environment string) sentry.ClientOptions {
	return sentry.ClientOptions{
		Dsn:              strings.TrimSpace(dsn),
		Environment:      strings.TrimSpace(environment),
		SampleRate:       1.0,
		TracesSampleRate: 0.1,
		SendDefaultPII:   false,
		AttachStacktrace: true,
		BeforeSend:       ScrubSentryEvent,
	}
}

// ScrubSentryEvent strips sensitive headers, cookies, query strings, and request bodies
// before any event leaves the process for Sentry.
func ScrubSentryEvent(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
	if event == nil {
		return nil
	}
	if event.Request != nil {
		event.Request.Cookies = ""
		event.Request.QueryString = ""
		if event.Request.Data != "" {
			event.Request.Data = "[REDACTED]"
		}
		if event.Request.Headers != nil {
			for k := range event.Request.Headers {
				lower := strings.ToLower(k)
				if lower == "authorization" || lower == "cookie" || lower == "set-cookie" ||
					strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "pin") || strings.Contains(lower, "key") {
					event.Request.Headers[k] = "[REDACTED]"
				}
			}
		}
	}
	return event
}

// RequestID propagates a well-formed inbound X-Request-ID and W3C traceparent header (or creates them),
// echoing both on the response so support, frontend telemetry, Sentry, and server logs correlate cleanly.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(RequestIDHeader)
		if !safeRequestID.MatchString(id) {
			id = uuid.NewString()
		}
		traceID, spanID, ok := logging.ParseTraceparent(c.GetHeader(logging.TraceparentHeader))
		var traceparent string
		if ok {
			traceparent = strings.ToLower(strings.TrimSpace(c.GetHeader(logging.TraceparentHeader)))
		} else {
			traceID, spanID, traceparent = logging.NewTraceparent()
		}
		c.Set(RequestIDHeader, id)
		c.Set(logging.TraceIDKey, traceID)
		c.Set(logging.SpanIDKey, spanID)
		c.Header(RequestIDHeader, id)
		c.Header(logging.TraceparentHeader, traceparent)
		c.Next()
	}
}

// AccessLog writes one JSON line per request and feeds the HTTP metrics. It logs the route template
// (c.FullPath), never the raw path or query, so QR, tracker and WebSocket tickets stay out of logs.
func AccessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		status := c.Writer.Status()
		elapsed := time.Since(start)
		metrics.HTTPRequests.WithLabelValues(c.Request.Method, route, strconv.Itoa(status)).Inc()
		metrics.HTTPDuration.WithLabelValues(c.Request.Method, route).Observe(elapsed.Seconds())

		reqID := c.GetString(RequestIDHeader)
		traceID := c.GetString(logging.TraceIDKey)
		attrs := []any{
			"request_id", reqID,
			"trace_id", traceID,
			"method", c.Request.Method,
			"route", route,
			"status", status,
			"latency_ms", elapsed.Milliseconds(),
			"client_ip", c.ClientIP(),
		}
		var storeIDStr string
		if staff := currentStaff(c); staff != nil {
			storeIDStr = staff.Staff.Store.ID.String()
			attrs = append(attrs, "store_id", staff.Staff.Store.ID, "staff_id", staff.Staff.UserID)
		}
		level := slog.LevelInfo
		if status >= http.StatusInternalServerError {
			level = slog.LevelError
			if len(c.Errors) > 0 {
				captureRequestErrorToSentry(c, reqID, route, storeIDStr, status, c.Errors.Last().Err)
			}
		}
		slog.Log(c.Request.Context(), level, "http_request", attrs...)
	}
}

// Recover turns a handler panic into a 500, logs it with the request id and stack,
// and reports the panic to Sentry with tenant/request tags (R6).
func Recover() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				reqID := c.GetString(RequestIDHeader)
				traceID := c.GetString(logging.TraceIDKey)
				route := c.FullPath()
				if route == "" {
					route = "unmatched"
				}
				slog.Error("panic", "request_id", reqID, "trace_id", traceID, "route", route, "panic", r, "stack", string(debug.Stack()))

				hub := sentry.CurrentHub().Clone()
				hub.ConfigureScope(func(scope *sentry.Scope) {
					scope.SetTag("request_id", reqID)
					if traceID != "" {
						scope.SetTag("trace_id", traceID)
					}
					scope.SetTag("route", route)
					scope.SetTag("method", c.Request.Method)
					if staff := currentStaff(c); staff != nil {
						scope.SetTag("store_id", staff.Staff.Store.ID.String())
					}
				})
				hub.RecoverWithContext(c.Request.Context(), r)

				authError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "Terjadi kesalahan pada server.")
				c.Abort()
			}
		}()
		c.Next()
	}
}

func captureRequestErrorToSentry(c *gin.Context, reqID, route, storeID string, status int, err error) {
	if err == nil {
		return
	}
	hub := sentry.CurrentHub().Clone()
	hub.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetTag("request_id", reqID)
		if traceID := c.GetString(logging.TraceIDKey); traceID != "" {
			scope.SetTag("trace_id", traceID)
		}
		scope.SetTag("route", route)
		scope.SetTag("method", c.Request.Method)
		scope.SetTag("http.status_code", strconv.Itoa(status))
		if storeID != "" {
			scope.SetTag("store_id", storeID)
		}
	})
	hub.CaptureException(err)
}

// LimitBody caps request bodies so a single client cannot make the API buffer arbitrary JSON.
// Routes that need more (future media uploads use presigned URLs, not the API) are unaffected.
func LimitBody(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		}
		c.Next()
	}
}
