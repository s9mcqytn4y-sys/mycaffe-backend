package sms

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// BridgeSender sends WhatsApp template messages through cmd/wa-bridge (whatsmeow), for demo and
// development only (decision W12). Same contract as WhatsAppSender: Message.Body holds the template
// parameters one per line; the bridge renders the approved template text (W14).
type BridgeSender struct {
	URL      string
	Token    string
	Template string
	Client   *http.Client
}

func NewBridgeSender(url, token, template string) (*BridgeSender, error) {
	b := &BridgeSender{URL: strings.TrimRight(strings.TrimSpace(url), "/"), Token: strings.TrimSpace(token),
		Template: strings.TrimSpace(template), Client: &http.Client{Timeout: 20 * time.Second}}
	if b.URL == "" || len(b.Token) < 32 || b.Template == "" {
		return nil, errors.New("whatsapp bridge needs a URL, a token of at least 32 characters and a template")
	}
	return b, nil
}

func (b *BridgeSender) Send(ctx context.Context, m Message) error {
	payload, err := json.Marshal(map[string]any{"to": m.To, "template": cmp.Or(m.Template, b.Template), "params": strings.Split(m.Body, "\n")})
	if err != nil {
		return fmt.Errorf("%w: marshal payload: %v", ErrPermanent, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.URL+"/send", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("%w: create request: %v", ErrPermanent, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.Token)
	resp, err := b.Client.Do(req)
	if err != nil {
		return fmt.Errorf("whatsapp bridge post failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests:
		return fmt.Errorf("%w: whatsapp bridge returned status %d: %s", ErrPermanent, resp.StatusCode, bytes.TrimSpace(body))
	}
	return fmt.Errorf("whatsapp bridge returned transient status %d", resp.StatusCode)
}
