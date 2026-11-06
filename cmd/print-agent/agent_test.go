package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeServer hands out the queued jobs once and records acknowledgements.
type fakeServer struct {
	mu    sync.Mutex
	jobs  []job
	acks  map[string]map[string]string
	token string
}

func (f *fakeServer) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/print-agent/jobs/claim", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Printer "+f.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.jobs) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		j := f.jobs[0]
		f.jobs = f.jobs[1:]
		_ = json.NewEncoder(w).Encode(j)
	})
	mux.HandleFunc("POST /api/v1/print-agent/jobs/{id}/ack", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.acks[r.PathValue("id")] = body
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

// fakePrinter accepts raw TCP prints like a port 9100 printer.
func fakePrinter(t *testing.T) (string, <-chan []byte) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan []byte, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			data, _ := io.ReadAll(c)
			c.Close()
			got <- data
		}
	}()
	return ln.Addr().String(), got
}

func TestAgentPrintsAndAcknowledges(t *testing.T) {
	payload := []byte("\x1b@A012\nBAR\n")
	f := &fakeServer{token: "tok", acks: map[string]map[string]string{}, jobs: []job{
		{ID: "j1", Kind: "ticket", Format: "escpos", Payload: base64.StdEncoding.EncodeToString(payload), Attempts: 1},
		{ID: "j2", Kind: "ticket", Format: "escpos", Payload: "not base64!", Attempts: 1},
	}}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	addr, prints := fakePrinter(t)

	cfg := Config{APIURL: srv.URL, Printers: []PrinterConfig{{Name: "Bar", Token: "tok", Address: addr}}}
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	a := newAgent(cfg)
	if handled, err := a.once(context.Background(), cfg.Printers[0]); !handled || err != nil {
		t.Fatalf("first job: handled=%v err=%v", handled, err)
	}
	select {
	case got := <-prints:
		if string(got) != string(payload) {
			t.Fatalf("printer received %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing reached the printer")
	}
	if f.acks["j1"]["status"] != "printed" {
		t.Fatalf("job 1 must be acknowledged printed, got %v", f.acks["j1"])
	}
	_, _ = a.once(context.Background(), cfg.Printers[0])
	if f.acks["j2"]["status"] != "failed" || !strings.Contains(f.acks["j2"]["error"], "base64") {
		t.Fatalf("a broken payload must be acknowledged failed, got %v", f.acks["j2"])
	}
	if handled, err := a.once(context.Background(), cfg.Printers[0]); handled || err != nil {
		t.Fatalf("an empty queue must be a quiet no-op, got handled=%v err=%v", handled, err)
	}
}

func TestAgentReportsUnreachablePrinterAndBadToken(t *testing.T) {
	f := &fakeServer{token: "tok", acks: map[string]map[string]string{}, jobs: []job{
		{ID: "j1", Kind: "label", Format: "tspl", Payload: base64.StdEncoding.EncodeToString([]byte("PRINT 1,1\r\n"))},
	}}
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := ln.Addr().String()
	ln.Close() // nothing listens there any more

	a := newAgent(Config{APIURL: srv.URL, PollSeconds: 1, Printers: []PrinterConfig{{Name: "Label", Token: "tok", Address: dead}}})
	_, _ = a.once(context.Background(), a.cfg.Printers[0])
	if f.acks["j1"]["status"] != "failed" {
		t.Fatalf("an unreachable printer must be acknowledged failed (server retries), got %v", f.acks["j1"])
	}
	if _, err := a.once(context.Background(), PrinterConfig{Name: "X", Token: "rotated", Address: dead}); !errors.Is(err, errUnauthorized) {
		t.Fatalf("a refused token must surface errUnauthorized, got %v", err)
	}
}

func TestConfigValidation(t *testing.T) {
	for _, c := range []Config{
		{APIURL: "ftp://x", Printers: []PrinterConfig{{Token: "t", Address: "1.2.3.4:9100"}}},
		{APIURL: "https://x"},
		{APIURL: "https://x", Printers: []PrinterConfig{{Token: "t", Address: "1.2.3.4"}}},
		{APIURL: "https://x", Printers: []PrinterConfig{{Address: "1.2.3.4:9100"}}},
	} {
		if err := c.validate(); err == nil {
			t.Errorf("config %+v must be rejected", c)
		}
	}
}
