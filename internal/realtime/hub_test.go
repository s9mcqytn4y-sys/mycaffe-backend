package realtime_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/s9mcqytn4y-sys/cassy-backend/internal/realtime"
)

func TestHubTenantIsolation(t *testing.T) {
	hub := realtime.NewHub([]string{"http://localhost:3000"})
	go hub.Run()

	// 1. Broadcast with empty StoreID must return an error (prevent cross-tenant leaks)
	err := hub.BroadcastEvent("test.event", "", map[string]string{"msg": "leak"})
	if err == nil {
		t.Errorf("expected BroadcastEvent with empty storeID to return error, got nil")
	}

	// 2. Valid broadcast with storeID must succeed
	err = hub.BroadcastEvent("test.event", "store-123", map[string]string{"msg": "secure"})
	if err != nil {
		t.Errorf("expected BroadcastEvent with valid storeID to succeed, got %v", err)
	}

	// Let hub process event
	time.Sleep(50 * time.Millisecond)
}

func TestHubBroadcastAfterShutdownDoesNotBlock(t *testing.T) {
	hub := realtime.NewHub(nil)
	hub.Shutdown()

	done := make(chan error, 1)
	go func() {
		var err error
		// Overfill the 256-slot buffer: without the stop signal the extra sends block forever.
		for i := 0; i < 300 && err == nil; i++ {
			err = hub.BroadcastEvent("test.event", "store-123", nil)
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected BroadcastEvent to report shutdown once the buffer is full")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("BroadcastEvent blocked after hub shutdown")
	}
}

// serveTestWS admits and serves a subscription described by the query (store, ip, guest), with an
// Alive check controlled by the test.
func serveTestWS(t *testing.T, hub *realtime.Hub, alive *atomic.Bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		sub := realtime.Subscription{
			StoreID: q.Get("store"), Channels: []string{realtime.ChannelPOS}, IP: q.Get("ip"), GuestKey: q.Get("guest"),
			Alive: func(context.Context) (bool, error) { return alive.Load(), nil },
		}
		if err := hub.Admit(sub); err != nil {
			http.Error(w, err.Error(), http.StatusTooManyRequests)
			return
		}
		hub.ServeWS(w, r, sub)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func dialTestWS(t *testing.T, srv *httptest.Server, query string) (*websocket.Conn, int) {
	t.Helper()
	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/?"+query, http.Header{"Origin": {"http://localhost:3000"}})
	if err != nil {
		if resp != nil {
			return nil, resp.StatusCode
		}
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, http.StatusSwitchingProtocols
}

func TestHubConnectionCaps(t *testing.T) {
	hub := realtime.NewHub([]string{"http://localhost:3000"})
	hub.SetLimits(realtime.Limits{PerGuest: 1, PerIP: 2, PerStore: 3}, 0)
	go hub.Run()
	t.Cleanup(hub.Shutdown)
	alive := &atomic.Bool{}
	alive.Store(true)
	srv := serveTestWS(t, hub, alive)

	// expect dials once; an accepted connection is awaited until the hub loop has registered it, so the
	// next cap check sees it (registration is asynchronous to the handshake).
	expect := func(query string, want int) {
		t.Helper()
		store := strings.TrimPrefix(strings.Split(query, "&")[0], "store=")
		before := hub.Connections(store)
		if _, code := dialTestWS(t, srv, query); code != want {
			t.Fatalf("%s: got %d, want %d", query, code, want)
		}
		if want != http.StatusSwitchingProtocols {
			return
		}
		for deadline := time.Now().Add(time.Second); hub.Connections(store) == before; time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("%s: connection was never registered", query)
			}
		}
	}
	expect("store=s1&ip=10.0.0.1&guest=g1", http.StatusSwitchingProtocols)
	expect("store=s1&ip=10.0.0.2&guest=g1", http.StatusTooManyRequests) // per guest session
	expect("store=s1&ip=10.0.0.1", http.StatusSwitchingProtocols)
	expect("store=s1&ip=10.0.0.1", http.StatusTooManyRequests) // per IP
	expect("store=s1&ip=10.0.0.3", http.StatusSwitchingProtocols)
	expect("store=s1&ip=10.0.0.4", http.StatusTooManyRequests) // per store
	expect("store=s2&ip=10.0.0.1", http.StatusSwitchingProtocols)
}

func TestHubClosesConnectionWhenSessionEnds(t *testing.T) {
	hub := realtime.NewHub([]string{"http://localhost:3000"})
	hub.SetLimits(realtime.DefaultLimits, 50*time.Millisecond)
	go hub.Run()
	t.Cleanup(hub.Shutdown)
	alive := &atomic.Bool{}
	alive.Store(true)
	conn, _ := dialTestWS(t, serveTestWS(t, hub, alive), "store=s1&ip=10.0.0.1")

	alive.Store(false)
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("expected the connection to close once the session ended")
	}
}
