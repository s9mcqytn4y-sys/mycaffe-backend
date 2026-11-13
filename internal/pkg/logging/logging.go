package logging

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"regexp"
	"strings"
)

const (
	TraceparentHeader = "traceparent"
	TraceIDKey        = "trace_id"
	SpanIDKey         = "span_id"
)

var traceparentRegex = regexp.MustCompile(`^00-([0-9a-f]{32})-([0-9a-f]{16})-[0-9a-f]{2}$`)

var sensitiveKeyTokens = []string{
	"password",
	"secret",
	"token",
	"signature",
	"server_key",
	"client_key",
	"api_key",
	"encryption_key",
	"private_key",
	"authorization",
	"cookie",
	"qr_string",
	"otp_code",
	"pin_hash",
	"raw_pin",
	"credential",
}

// NewJSONLogger constructs a structured JSON slog logger with automatic PII and credential redaction.
func NewJSONLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
		ReplaceAttr: RedactAttr,
	}))
}

// RedactAttr masks attribute values whose key or string content matches known secret patterns.
func RedactAttr(_ []string, a slog.Attr) slog.Attr {
	lowerKey := strings.ToLower(strings.TrimSpace(a.Key))
	if lowerKey == "pin" || lowerKey == "otp" {
		return slog.String(a.Key, "[REDACTED]")
	}
	for _, token := range sensitiveKeyTokens {
		if strings.Contains(lowerKey, token) {
			return slog.String(a.Key, "[REDACTED]")
		}
	}

	if a.Value.Kind() == slog.KindString {
		val := a.Value.String()
		if redacted := RedactString(val); redacted != val {
			return slog.String(a.Key, redacted)
		}
	}
	return a
}

// RedactString scrubs bearer tokens and Midtrans server keys from free-form log strings.
func RedactString(val string) string {
	if val == "" {
		return val
	}
	lower := strings.ToLower(val)
	if idx := strings.Index(lower, "bearer "); idx != -1 {
		prefix := val[:idx+len("Bearer ")]
		rest := val[idx+len("Bearer "):]
		if spaceIdx := strings.IndexAny(rest, " \t\r\n"); spaceIdx != -1 {
			val = prefix + "[REDACTED]" + rest[spaceIdx:]
		} else {
			val = prefix + "[REDACTED]"
		}
	}
	for _, keyPrefix := range []string{"SB-Mid-server-", "Mid-server-"} {
		if idx := strings.Index(val, keyPrefix); idx != -1 {
			rest := val[idx+len(keyPrefix):]
			end := len(rest)
			for i, r := range rest {
				if r == ' ' || r == '"' || r == '\'' || r == ',' || r == '\n' || r == '\r' {
					end = i
					break
				}
			}
			val = val[:idx+len(keyPrefix)] + "[REDACTED]" + rest[end:]
		}
	}
	return val
}

// ParseTraceparent parses a W3C Trace Context traceparent header (version 00).
func ParseTraceparent(header string) (traceID, spanID string, ok bool) {
	trimmed := strings.ToLower(strings.TrimSpace(header))
	matches := traceparentRegex.FindStringSubmatch(trimmed)
	if len(matches) != 3 {
		return "", "", false
	}
	if strings.Trim(matches[1], "0") == "" || strings.Trim(matches[2], "0") == "" {
		return "", "", false
	}
	return matches[1], matches[2], true
}

// NewTraceparent generates a valid W3C Trace Context version-00 traceparent header.
func NewTraceparent() (traceID, spanID, header string) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// Deterministic non-zero fallback should never happen on modern OS CSPRNG.
		traceID = "00000000000000000000000000000001"
		spanID = "0000000000000001"
		return traceID, spanID, "00-" + traceID + "-" + spanID + "-01"
	}
	traceID = hex.EncodeToString(raw[:16])
	spanID = hex.EncodeToString(raw[16:24])
	return traceID, spanID, "00-" + traceID + "-" + spanID + "-01"
}
