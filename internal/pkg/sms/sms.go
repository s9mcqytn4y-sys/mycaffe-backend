// Package sms delivers text messages (decision V5): one Sender interface with a log driver for
// development/test, an email bridge driver for free real-time OTP delivery over SMTP, and a
// webhook driver for HTTP gateways (Telegram Bot API, WhatsApp Cloud API, Fonnte, Zenziva).
package sms

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/mailer"
)

// ErrPermanent marks a failure that retrying cannot fix (invalid number, rejected sender).
var ErrPermanent = errors.New("permanent sms failure")

// ErrInvalidPhone means the input is not a usable phone number.
var ErrInvalidPhone = errors.New("invalid phone number")

type Message struct {
	To   string // E.164, e.g. +6281234567890
	Body string
}

type Sender interface {
	Send(ctx context.Context, m Message) error
}

// LogSender prints messages instead of sending them. Config allows it only in development and
// test, because OTP bodies are secrets.
type LogSender struct{}

func (LogSender) Send(_ context.Context, m Message) error {
	log.Printf("[SMS LOG DRIVER] to=%s body=%q", m.To, m.Body)
	return nil
}

// EmailSender bridges SMS messages to an email recipient via mailer.Sender.
// This allows 100% free, real-time end-to-end OTP verification in development and demo/staging
// environments without requiring a paid telco SMS gateway.
type EmailSender struct {
	Mailer      mailer.Sender
	RecipientTo string
}

func NewEmailSender(m mailer.Sender, recipientTo string) (*EmailSender, error) {
	to := strings.TrimSpace(recipientTo)
	if m == nil {
		return nil, errors.New("sms email sender requires a non-nil mailer")
	}
	if to == "" {
		return nil, errors.New("sms email sender requires a non-empty recipient email address")
	}
	return &EmailSender{
		Mailer:      m,
		RecipientTo: to,
	}, nil
}

func (s *EmailSender) Send(ctx context.Context, m Message) error {
	phone := strings.TrimSpace(m.To)
	if phone == "" {
		return fmt.Errorf("%w: empty destination phone", ErrPermanent)
	}
	subject := fmt.Sprintf("[MyCaffe SMS Gateway] Pesan untuk %s", phone)
	textBody := fmt.Sprintf("Penerima SMS : %s\nWaktu (UTC)  : %s\n\nIsi Pesan SMS:\n%s\n",
		phone, time.Now().UTC().Format(time.RFC3339), m.Body)
	htmlBody := fmt.Sprintf(
		`<div style="font-family:sans-serif;max-width:520px;padding:16px;border:1px solid #e5e0d8;border-radius:8px;background:#faf7f2;color:#2c1e18;">`+
			`<h3 style="margin-top:0;color:#4a2c1d;">MyCaffe SMS-to-Email Bridge</h3>`+
			`<p style="margin:4px 0;"><strong>Nomor Tujuan:</strong> <code>%s</code></p>`+
			`<div style="margin-top:12px;padding:12px;background:#ffffff;border-left:4px solid #c87d55;border-radius:4px;font-size:15px;white-space:pre-wrap;">%s</div>`+
			`</div>`,
		html.EscapeString(phone),
		html.EscapeString(m.Body),
	)
	err := s.Mailer.Send(ctx, mailer.Message{
		To:      s.RecipientTo,
		Subject: subject,
		Text:    textBody,
		HTML:    htmlBody,
	})
	if err != nil {
		if errors.Is(err, mailer.ErrPermanent) {
			return fmt.Errorf("%w: %v", ErrPermanent, err)
		}
		return err
	}
	return nil
}

// WebhookSender delivers SMS messages via HTTP POST JSON {"to": "+628...", "body": "..."}
// to an external gateway (e.g., Telegram Bot bridge, WhatsApp Cloud API worker, Fonnte, Zenziva).
type WebhookSender struct {
	URL         string
	BearerToken string
	Client      *http.Client
}

func NewWebhookSender(endpointURL, bearerToken string) (*WebhookSender, error) {
	u := strings.TrimSpace(endpointURL)
	if u == "" {
		return nil, errors.New("sms webhook sender requires a non-empty endpoint URL")
	}
	return &WebhookSender{
		URL:         u,
		BearerToken: strings.TrimSpace(bearerToken),
		Client:      &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (w *WebhookSender) Send(ctx context.Context, m Message) error {
	payload, err := json.Marshal(struct {
		To   string `json:"to"`
		Body string `json:"body"`
	}{
		To:   m.To,
		Body: m.Body,
	})
	if err != nil {
		return fmt.Errorf("%w: marshal payload: %v", ErrPermanent, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("%w: create request: %v", ErrPermanent, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if w.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+w.BearerToken)
	}

	client := w.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("sms webhook post failed: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
		return fmt.Errorf("%w: webhook returned status %d", ErrPermanent, resp.StatusCode)
	}
	return fmt.Errorf("sms webhook returned transient status %d", resp.StatusCode)
}

var e164 = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)

// NormalizePhone turns Indonesian inputs (0812…, 62812…, +62 812-…) into E.164 and accepts other
// E.164 numbers as they are.
func NormalizePhone(raw string) (string, error) {
	s := strings.NewReplacer(" ", "", "-", "", "(", "", ")", "", ".", "").Replace(strings.TrimSpace(raw))
	switch {
	case strings.HasPrefix(s, "+"):
	case strings.HasPrefix(s, "62"):
		s = "+" + s
	case strings.HasPrefix(s, "0"):
		s = "+62" + s[1:]
	default:
		return "", ErrInvalidPhone
	}
	if !e164.MatchString(s) || (strings.HasPrefix(s, "+62") && !strings.HasPrefix(s, "+628")) {
		return "", ErrInvalidPhone // Indonesian mobile numbers start with 8 after the country code
	}
	return s, nil
}
