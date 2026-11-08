package mailer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ResendSender delivers transactional email via Resend's REST API.
type ResendSender struct {
	apiKey  string
	from    string
	client  *http.Client
	baseURL string
}

// NewResendSender constructs a ResendSender with the API key and sender identity.
func NewResendSender(apiKey, from string) (*ResendSender, error) {
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return nil, errors.New("resend sender requires a non-empty API key")
	}
	f := strings.TrimSpace(from)
	if f == "" {
		f = "Cassy by AzureDevID <noreply@azuredev.id>"
	}
	return &ResendSender{
		apiKey:  key,
		from:    f,
		client:  &http.Client{Timeout: 15 * time.Second},
		baseURL: "https://api.resend.com/emails",
	}, nil
}

type resendEmailPayload struct {
	From    string            `json:"from"`
	To      []string          `json:"to"`
	Subject string            `json:"subject"`
	HTML    string            `json:"html,omitempty"`
	Text    string            `json:"text,omitempty"`
	ReplyTo string            `json:"reply_to,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Send sends the message through Resend's REST endpoint.
func (r *ResendSender) Send(ctx context.Context, msg Message) error {
	to := strings.TrimSpace(msg.To)
	if to == "" {
		return fmt.Errorf("%w: empty recipient address", ErrPermanent)
	}

	payload := resendEmailPayload{
		From:    r.from,
		To:      []string{to},
		Subject: msg.Subject,
		HTML:    msg.HTML,
		Text:    msg.Text,
		ReplyTo: msg.ReplyTo,
		Headers: map[string]string{
			"X-Entity-Ref-ID": fmt.Sprintf("cassy-%d", time.Now().UnixNano()),
		},
	}

	rawJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal resend payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL, bytes.NewReader(rawJSON))
	if err != nil {
		return fmt.Errorf("create resend request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+r.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Cassy-AzureDevID-Mailer/1.0")

	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("resend http request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
		return fmt.Errorf("%w: resend returned %d: %s", ErrPermanent, resp.StatusCode, string(bodyBytes))
	}

	return fmt.Errorf("resend returned status %d: %s", resp.StatusCode, string(bodyBytes))
}
