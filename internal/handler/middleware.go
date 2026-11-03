package handler

import (
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/metrics"
)

const RequestIDHeader = "X-Request-ID"

var safeRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{8,64}$`)

// RequestID propagates a well-formed inbound X-Request-ID (e.g. from Nginx) or creates one, and
// echoes it on the response so a cashier's error report can be matched to server logs.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(RequestIDHeader)
		if !safeRequestID.MatchString(id) {
			id = uuid.NewString()
		}
		c.Set(RequestIDHeader, id)
		c.Header(RequestIDHeader, id)
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

		attrs := []any{
			"request_id", c.GetString(RequestIDHeader),
			"method", c.Request.Method,
			"route", route,
			"status", status,
			"latency_ms", elapsed.Milliseconds(),
			"client_ip", c.ClientIP(),
		}
		if staff := currentStaff(c); staff != nil {
			attrs = append(attrs, "store_id", staff.Staff.Store.ID, "staff_id", staff.Staff.UserID)
		}
		level := slog.LevelInfo
		if status >= http.StatusInternalServerError {
			level = slog.LevelError
		}
		slog.Log(c.Request.Context(), level, "http_request", attrs...)
	}
}

// Recover turns a handler panic into a 500 and logs it with the request id and stack.
func Recover() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("panic", "request_id", c.GetString(RequestIDHeader), "route", c.FullPath(), "panic", r, "stack", string(debug.Stack()))
				authError(c, http.StatusInternalServerError, "INTERNAL_ERROR", "Terjadi kesalahan pada server.")
				c.Abort()
			}
		}()
		c.Next()
	}
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
