package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/s9mcqytn4y-sys/mycaffe-backend/internal/pkg/printfmt"
)

// LAN fallback (decision W18): while the internet is down the POS device sends its kitchen ticket straight
// to the agent on the cafe LAN. The agent renders it with the same printfmt code as the server, using the
// printer settings it cached while online, and marks it OFFLINE (the official number follows on sync).

// LANConfig enables the LAN endpoint. Key is shared with the POS devices of this cafe (min 32 chars).
type LANConfig struct {
	Listen    string `json:"listen"`     // default ":8787"
	Key       string `json:"key"`        // sent as "Authorization: Bearer <key>"
	CacheFile string `json:"cache_file"` // printer settings cached for outages; default next to the config
}

func (l *LANConfig) validate() error {
	if len(l.Key) < 32 {
		return errors.New("lan.key must be at least 32 characters")
	}
	if l.Listen == "" {
		l.Listen = ":8787"
	}
	return nil
}

const configRefresh = 5 * time.Minute

// loadCache reads the printer settings saved by an earlier run, so a restart during an outage still prints.
func (a *agent) loadCache() {
	raw, err := os.ReadFile(a.cfg.LAN.CacheFile)
	if err != nil {
		return
	}
	cache := map[string]printfmt.Printer{}
	if json.Unmarshal(raw, &cache) == nil {
		a.mu.Lock()
		a.cache = cache
		a.mu.Unlock()
	}
}

// refreshConfig fetches each printer's settings from the server and saves them when they changed.
func (a *agent) refreshConfig(ctx context.Context) {
	fresh := map[string]printfmt.Printer{}
	for _, p := range a.cfg.Printers {
		cfg, err := a.fetchPrinter(ctx, p)
		if err != nil {
			log.Printf("[PRINT AGENT] %s: settings not refreshed: %v", p.Name, err)
			continue
		}
		fresh[p.Name] = *cfg
	}
	if len(fresh) == 0 {
		return
	}
	a.mu.Lock()
	for name, cfg := range fresh {
		a.cache[name] = cfg
	}
	raw, _ := json.MarshalIndent(a.cache, "", "  ")
	a.mu.Unlock()
	if err := os.WriteFile(a.cfg.LAN.CacheFile, raw, 0o600); err != nil {
		log.Printf("[PRINT AGENT] cannot save printer settings cache: %v", err)
	}
}

func (a *agent) fetchPrinter(ctx context.Context, p PrinterConfig) (*printfmt.Printer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.cfg.APIURL+"/api/v1/print-agent/printer", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Printer "+p.Token)
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, errUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	var cfg printfmt.Printer
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// lanHandler serves POST /print {queue_number, order_number, customer_name, table_number, lines[{..., station}]}.
func (a *agent) lanHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The POS is a web app on another origin; the key, not the origin, is the control.
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Private-Network", "true")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/print" {
			http.NotFound(w, r)
			return
		}
		key, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(key), []byte(a.cfg.LAN.Key)) != 1 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		var t printfmt.Ticket
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&t); err != nil || t.QueueNumber == "" || len(t.Lines) == 0 {
			http.Error(w, `{"error":"queue_number and lines are required"}`, http.StatusBadRequest)
			return
		}
		printed, failed := a.printLAN(r.Context(), t)
		w.Header().Set("Content-Type", "application/json")
		if len(printed) == 0 && len(failed) == 0 {
			w.WriteHeader(http.StatusServiceUnavailable) // no cached settings yet: the agent never reached the server
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"printed": printed, "failed": failed})
	})
}

// printLAN routes the ticket to every printer with cached settings, like the server does.
func (a *agent) printLAN(ctx context.Context, t printfmt.Ticket) ([]string, map[string]string) {
	t.Offline, t.Reprint = true, false
	if t.LocalTime == "" {
		t.LocalTime = time.Now().Format("02/01 15:04")
	}
	printed, failed := []string{}, map[string]string{}
	for _, p := range a.cfg.Printers {
		a.mu.Lock()
		cfg, ok := a.cache[p.Name]
		a.mu.Unlock()
		if !ok {
			continue
		}
		_, sel := printfmt.Select(cfg.Station, t.Lines)
		if len(sel) == 0 {
			continue
		}
		ticket := t
		ticket.Lines = sel
		data, err := printfmt.Render(cfg, ticket)
		if err == nil {
			err = a.send(ctx, p.Address, data)
		}
		if err != nil {
			failed[p.Name] = err.Error()
			continue
		}
		printed = append(printed, p.Name)
	}
	return printed, failed
}

// serveLAN runs the LAN endpoint and the settings refresh until ctx ends.
func (a *agent) serveLAN(ctx context.Context, wg *sync.WaitGroup) {
	a.loadCache()
	srv := &http.Server{Addr: a.cfg.LAN.Listen, Handler: a.lanHandler(), ReadHeaderTimeout: 5 * time.Second}
	wg.Add(2)
	go func() {
		defer wg.Done()
		log.Printf("[PRINT AGENT] LAN fallback on %s", a.cfg.LAN.Listen)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[PRINT AGENT] LAN endpoint stopped: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		for {
			a.refreshConfig(ctx)
			select {
			case <-ctx.Done():
				shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_ = srv.Shutdown(shutdown)
				cancel()
				return
			case <-time.After(configRefresh):
			}
		}
	}()
}
