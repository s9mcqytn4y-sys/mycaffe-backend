package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestRedactAttrAndString(t *testing.T) {
	var buf bytes.Buffer
	logger := NewJSONLogger(&buf)
	logger.Info("auth_attempt",
		"pin", "123456",
		"jwt_token", "dummy-test-token",
		"authorization", "Bearer dummy-auth-value",
		"msg_detail", "failed using Mid-server-" + "testvalue",
		"store_id", "store-01",
	)

	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("expected valid json log line, got %v (%q)", err, buf.String())
	}
	if decoded["pin"] != "[REDACTED]" {
		t.Fatalf("expected pin to be redacted, got %v", decoded["pin"])
	}
	if decoded["jwt_token"] != "[REDACTED]" {
		t.Fatalf("expected jwt_token to be redacted, got %v", decoded["jwt_token"])
	}
	if decoded["authorization"] != "[REDACTED]" {
		t.Fatalf("expected authorization to be redacted, got %v", decoded["authorization"])
	}
	if decoded["msg_detail"] != "failed using Mid-server-[REDACTED]" {
		t.Fatalf("expected Midtrans key to be redacted, got %v", decoded["msg_detail"])
	}
	if decoded["store_id"] != "store-01" {
		t.Fatalf("expected store_id preserved, got %v", decoded["store_id"])
	}
	_ = slog.LevelInfo
}

func TestTraceparentRoundTrip(t *testing.T) {
	traceID, spanID, hdr := NewTraceparent()
	parsedTrace, parsedSpan, ok := ParseTraceparent(hdr)
	if !ok {
		t.Fatalf("expected valid generated traceparent %q", hdr)
	}
	if parsedTrace != traceID || parsedSpan != spanID {
		t.Fatalf("mismatch: got (%s, %s), want (%s, %s)", parsedTrace, parsedSpan, traceID, spanID)
	}

	if _, _, ok := ParseTraceparent("00-00000000000000000000000000000000-0000000000000001-01"); ok {
		t.Fatal("expected all-zero traceID to fail validation")
	}
	if _, _, ok := ParseTraceparent("invalid-header"); ok {
		t.Fatal("expected invalid header to fail")
	}
}
