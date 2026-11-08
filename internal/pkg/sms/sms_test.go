package sms_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/mailer"
	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/sms"
)

type captureMailer struct {
	last mailer.Message
	err  error
}

func (c *captureMailer) Send(_ context.Context, m mailer.Message) error {
	c.last = m
	return c.err
}

func TestNormalizePhone(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"0812-3456-7890", "+6281234567890", false},
		{"6281234567890", "+6281234567890", false},
		{"+62 812 3456 7890", "+6281234567890", false},
		{"+62215551234", "", true}, // landline not starting with +628
		{"12345", "", true},
	}
	for _, tc := range cases {
		got, err := sms.NormalizePhone(tc.in)
		if (err != nil) != tc.wantErr {
			t.Fatalf("NormalizePhone(%q) err=%v, wantErr=%v", tc.in, err, tc.wantErr)
		}
		if got != tc.want {
			t.Fatalf("NormalizePhone(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEmailSender(t *testing.T) {
	cm := &captureMailer{}
	sender, err := sms.NewEmailSender(cm, "owner@abbdullaziz.online")
	if err != nil {
		t.Fatalf("NewEmailSender: %v", err)
	}
	err = sender.Send(context.Background(), sms.Message{
		To:   "+6281234567890",
		Body: "Kode OTP MyCaffe Anda: 482910",
	})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if cm.last.To != "owner@abbdullaziz.online" {
		t.Fatalf("unexpected recipient: %q", cm.last.To)
	}
	if !strings.Contains(cm.last.Subject, "+6281234567890") || !strings.Contains(cm.last.Text, "482910") {
		t.Fatalf("unexpected email content: subject=%q text=%q", cm.last.Subject, cm.last.Text)
	}

	rawMIME, err := mailer.BuildMIME("noreply@abbdullaziz.online", "MyCaffe POS", cm.last, time.Now().UTC())
	if err != nil {
		t.Fatalf("BuildMIME failed: %v", err)
	}
	if !strings.Contains(string(rawMIME), "MyCaffe SMS-to-Email Bridge") || !strings.Contains(string(rawMIME), "482910") {
		t.Fatalf("MIME output missing SMS bridge content: %s", string(rawMIME))
	}

	cm.err = mailer.ErrPermanent
	if err := sender.Send(context.Background(), sms.Message{To: "+6281234567890", Body: "test"}); !errors.Is(err, sms.ErrPermanent) {
		t.Fatalf("expected permanent error propagation, got %v", err)
	}
}

func TestWebhookSender(t *testing.T) {
	var gotAuth, gotTo, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var payload struct {
			To   string `json:"to"`
			Body string `json:"body"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		gotTo = payload.To
		gotBody = payload.Body
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	sender, err := sms.NewWebhookSender(srv.URL, "secret-token-123")
	if err != nil {
		t.Fatalf("NewWebhookSender: %v", err)
	}
	if err := sender.Send(context.Background(), sms.Message{To: "+6281299998888", Body: "OTP 991122"}); err != nil {
		t.Fatalf("WebhookSender.Send: %v", err)
	}
	if gotAuth != "Bearer secret-token-123" || gotTo != "+6281299998888" || gotBody != "OTP 991122" {
		t.Fatalf("unexpected webhook request: auth=%q to=%q body=%q", gotAuth, gotTo, gotBody)
	}
}
