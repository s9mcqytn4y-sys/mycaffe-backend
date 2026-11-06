// Command wa-bridge relays WhatsApp receipts for the demo and development environments through an
// unofficial WhatsApp Web session (whatsmeow), decision W12. Production uses the WhatsApp Cloud API
// driver in the API instead; the API refuses the bridge driver when APP_ENV=production.
//
// Environment: WA_BRIDGE_TOKEN (shared bearer token, at least 32 characters), WA_BRIDGE_ADDR
// (default :8090), WA_BRIDGE_DB (default /data/wa-bridge.db).
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	token := os.Getenv("WA_BRIDGE_TOKEN")
	if len(token) < 32 {
		log.Fatal("[WA BRIDGE] WA_BRIDGE_TOKEN must be set (at least 32 characters)")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	sender, err := newWASender(ctx, getenv("WA_BRIDGE_DB", "/data/wa-bridge.db"))
	if err != nil {
		log.Fatalf("[WA BRIDGE] %v", err)
	}
	defer sender.Close()
	if err := sender.Connect(ctx); err != nil {
		log.Fatalf("[WA BRIDGE] connect: %v", err)
	}

	srv := &http.Server{Addr: getenv("WA_BRIDGE_ADDR", ":8090"), Handler: newHandler(token, sender), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Printf("[WA BRIDGE] listening on %s", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("[WA BRIDGE] %v", err)
	}
}
