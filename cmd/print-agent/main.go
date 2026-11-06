// Command print-agent prints the server's print queue on LAN printers (decision W13): it claims
// jobs for each configured printer, sends the bytes to IP:9100 and acknowledges the result.
//
// Usage: print-agent -config print-agent.json
//
//	{
//	  "api_url": "https://demo.abbdullaziz.online",
//	  "poll_seconds": 3,
//	  "printers": [{"name": "Bar", "token": "<agent token>", "address": "192.168.1.50:9100"}]
//	}
//
// The tokens are secrets: keep the file readable only by the account running the agent. On Windows
// the binary also runs as a service (see docs/Setup/PrintAgent.md).
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func loadConfig(path string) (Config, error) {
	var cfg Config
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, err
	}
	return cfg, cfg.validate()
}

func main() {
	path := flag.String("config", "print-agent.json", "path to the agent configuration")
	flag.Parse()
	cfg, err := loadConfig(*path)
	if err != nil {
		log.Fatalf("[PRINT AGENT] config: %v", err)
	}
	a := newAgent(cfg)
	if runAsService(a) {
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("[PRINT AGENT] serving %d printer(s) for %s", len(cfg.Printers), cfg.APIURL)
	a.run(ctx)
}
