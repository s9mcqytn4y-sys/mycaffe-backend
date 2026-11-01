package handler

import (
	"sync"
	"time"
)

// loginFailureLimiter counts failed staff logins per trusted client IP in a sliding window
// (ADR-003 D3, app layer). shortcut: in-process state, valid for the single-VPS topology of
// ADR-002; move to PostgreSQL or Redis if the API ever runs as more than one instance.
type loginFailureLimiter struct {
	mu       sync.Mutex
	limit    int
	window   time.Duration
	failures map[string][]time.Time
	now      func() time.Time
}

func newLoginFailureLimiter(limit int, window time.Duration) *loginFailureLimiter {
	return &loginFailureLimiter{limit: limit, window: window, failures: map[string][]time.Time{}, now: time.Now}
}

// Blocked reports whether ip already reached the failure limit inside the window.
func (l *loginFailureLimiter) Blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.prune(ip)) >= l.limit
}

func (l *loginFailureLimiter) RecordFailure(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures[ip] = append(l.prune(ip), l.now())
	if len(l.failures) > 10000 {
		l.sweep()
	}
}

func (l *loginFailureLimiter) prune(ip string) []time.Time {
	cutoff := l.now().Add(-l.window)
	kept := l.failures[ip][:0]
	for _, t := range l.failures[ip] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.failures, ip)
		return nil
	}
	l.failures[ip] = kept
	return kept
}

// sweep drops idle IPs so a scan from many addresses cannot grow the map without bound.
func (l *loginFailureLimiter) sweep() {
	for ip := range l.failures {
		l.prune(ip)
	}
}
