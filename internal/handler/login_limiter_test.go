package handler

import (
	"testing"
	"time"
)

func TestLoginFailureLimiterSlidingWindow(t *testing.T) {
	now := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	l := newLoginFailureLimiter(3, 15*time.Minute)
	l.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		if l.Blocked("203.0.113.7") {
			t.Fatalf("blocked after %d failures", i)
		}
		l.RecordFailure("203.0.113.7")
	}
	if !l.Blocked("203.0.113.7") {
		t.Fatal("expected block at the limit")
	}
	if l.Blocked("198.51.100.9") {
		t.Fatal("limit must be per IP")
	}
	now = now.Add(15*time.Minute + time.Second)
	if l.Blocked("203.0.113.7") {
		t.Fatal("failures outside the window must expire")
	}
	if len(l.failures) != 0 {
		t.Fatalf("expired IPs must be dropped, have %d", len(l.failures))
	}
}
