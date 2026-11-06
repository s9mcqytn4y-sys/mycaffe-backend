package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

// PrinterConfig is one printer the agent serves: its agent token (from the owner's printer settings,
// shown once) and its LAN address, usually IP:9100 (raw printing).
type PrinterConfig struct {
	Name    string `json:"name"`
	Token   string `json:"token"`
	Address string `json:"address"`
}

type Config struct {
	APIURL      string          `json:"api_url"`
	PollSeconds int             `json:"poll_seconds"`
	Printers    []PrinterConfig `json:"printers"`
}

func (c *Config) validate() error {
	c.APIURL = strings.TrimRight(strings.TrimSpace(c.APIURL), "/")
	if !strings.HasPrefix(c.APIURL, "https://") && !strings.HasPrefix(c.APIURL, "http://") {
		return errors.New("api_url must be an http(s) URL")
	}
	if c.PollSeconds <= 0 {
		c.PollSeconds = 3
	}
	if len(c.Printers) == 0 {
		return errors.New("at least one printer is required")
	}
	for i, p := range c.Printers {
		if p.Token == "" || p.Address == "" {
			return fmt.Errorf("printer %d needs token and address", i+1)
		}
		if _, _, err := net.SplitHostPort(p.Address); err != nil {
			return fmt.Errorf("printer %d address %q must be host:port", i+1, p.Address)
		}
	}
	return nil
}

type job struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Format   string `json:"format"`
	Payload  string `json:"payload_base64"`
	Attempts int    `json:"attempts"`
}

var errUnauthorized = errors.New("printer token refused (rotated or printer inactive)")

// agent runs one claim -> print -> ack loop per printer.
type agent struct {
	cfg    Config
	client *http.Client
	// send writes raw bytes to the printer; replaceable in tests.
	send func(ctx context.Context, address string, data []byte) error
}

func newAgent(cfg Config) *agent {
	return &agent{cfg: cfg, client: &http.Client{Timeout: 15 * time.Second}, send: sendRaw}
}

// sendRaw prints over a plain TCP connection (port 9100 "raw"/JetDirect printing).
func sendRaw(ctx context.Context, address string, data []byte) error {
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
	_, err = conn.Write(data)
	return err
}

func (a *agent) run(ctx context.Context) {
	done := make(chan struct{})
	for _, p := range a.cfg.Printers {
		go func(p PrinterConfig) {
			a.loop(ctx, p)
			done <- struct{}{}
		}(p)
	}
	for range a.cfg.Printers {
		<-done
	}
}

func (a *agent) loop(ctx context.Context, p PrinterConfig) {
	wait := time.Duration(a.cfg.PollSeconds) * time.Second
	backoff := wait
	for {
		printed, err := a.once(ctx, p)
		switch {
		case err == nil:
			backoff = wait
		case errors.Is(err, errUnauthorized):
			log.Printf("[PRINT AGENT] %s: %v; retrying in 1 minute", p.Name, err)
			backoff = time.Minute
		default:
			log.Printf("[PRINT AGENT] %s: %v", p.Name, err)
			backoff = min(backoff*2, time.Minute)
		}
		if printed && err == nil {
			continue // more jobs may be waiting
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

// once claims at most one job, prints it and acknowledges the result. It reports whether a job was
// handled, so the loop drains the queue without waiting.
func (a *agent) once(ctx context.Context, p PrinterConfig) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.APIURL+"/api/v1/print-agent/jobs/claim", nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Printer "+p.Token)
	resp, err := a.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("claim: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return false, nil
	case http.StatusUnauthorized:
		return false, errUnauthorized
	case http.StatusOK:
	default:
		return false, fmt.Errorf("claim: unexpected status %d", resp.StatusCode)
	}
	var j job
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&j); err != nil {
		return false, fmt.Errorf("claim: decode: %w", err)
	}
	data, err := base64.StdEncoding.DecodeString(j.Payload)
	if err != nil {
		return true, a.ack(ctx, p, j.ID, fmt.Errorf("payload is not base64: %w", err))
	}
	printErr := a.send(ctx, p.Address, data)
	if printErr != nil {
		log.Printf("[PRINT AGENT] %s: job %s (%s) failed: %v", p.Name, j.ID, j.Kind, printErr)
	}
	return true, a.ack(ctx, p, j.ID, printErr)
}

func (a *agent) ack(ctx context.Context, p PrinterConfig, jobID string, printErr error) error {
	body := map[string]string{"status": "printed"}
	if printErr != nil {
		body = map[string]string{"status": "failed", "error": printErr.Error()}
	}
	payload, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.APIURL+"/api/v1/print-agent/jobs/"+jobID+"/ack", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Printer "+p.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		// The server hands the job out again after its 60 s lease, so nothing is lost.
		return fmt.Errorf("ack: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("ack: unexpected status %d", resp.StatusCode)
	}
	return nil
}
