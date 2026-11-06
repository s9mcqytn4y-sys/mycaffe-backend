package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// W18: the agent caches its printers' settings while online and prints POS tickets on the LAN with them,
// marked OFFLINE, routed by station exactly like the server.
func TestLANFallbackPrintsWithCachedSettings(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/print-agent/printer" || r.Header.Get("Authorization") != "Printer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"station": "kitchen", "format": "escpos", "paper_width_mm": 58, "copies": 1, "footer": ""})
	}))
	addr, prints := fakePrinter(t)
	key := strings.Repeat("k", 32)
	cfg := Config{APIURL: api.URL, Printers: []PrinterConfig{{Name: "Dapur", Token: "tok", Address: addr}},
		LAN: &LANConfig{Key: key, CacheFile: filepath.Join(t.TempDir(), "cache.json")}}
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	a := newAgent(cfg)
	a.refreshConfig(context.Background())
	api.Close() // the internet goes down

	post := func(h http.Handler, auth string, body any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/print", bytes.NewReader(raw))
		req.Header.Set("Authorization", auth)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	ticket := map[string]any{"queue_number": "X1-007", "customer_name": "Rina", "lines": []map[string]any{
		{"name": "Americano", "quantity": 1, "station": "bar"},
		{"name": "Nasi Goreng", "quantity": 2, "station": "kitchen"},
	}}
	if res := post(a.lanHandler(), "Bearer wrong", ticket); res.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong key must be 401, got %d", res.Code)
	}

	// A restart during the outage still prints: the settings come from the cache file.
	restarted := newAgent(cfg)
	restarted.loadCache()
	res := post(restarted.lanHandler(), "Bearer "+key, ticket)
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"printed":["Dapur"]`) {
		t.Fatalf("the kitchen printer must print: %d %s", res.Code, res.Body.String())
	}
	select {
	case got := <-prints:
		out := string(got)
		if !strings.Contains(out, "DAPUR") || !strings.Contains(out, "OFFLINE") || !strings.Contains(out, "Nasi Goreng") || strings.Contains(out, "Americano") {
			t.Fatalf("kitchen ticket must carry only kitchen lines, marked OFFLINE: %q", out)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing reached the printer")
	}

	empty := newAgent(Config{APIURL: "http://x", Printers: cfg.Printers, LAN: &LANConfig{Key: key, CacheFile: filepath.Join(t.TempDir(), "none.json")}})
	if res := post(empty.lanHandler(), "Bearer "+key, ticket); res.Code != http.StatusServiceUnavailable {
		t.Fatalf("without cached settings the agent must say it cannot print, got %d", res.Code)
	}
	if err := (&Config{APIURL: "http://x", Printers: cfg.Printers, LAN: &LANConfig{Key: "short"}}).validate(); err == nil {
		t.Fatal("a short LAN key must be refused")
	}
}
