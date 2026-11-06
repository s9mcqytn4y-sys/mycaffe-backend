package sms

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBridgeSender(t *testing.T) {
	var got struct {
		Auth string
		Body struct {
			To       string   `json:"to"`
			Template string   `json:"template"`
			Params   []string `json:"params"`
		}
	}
	status := http.StatusNoContent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Auth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got.Body)
		w.WriteHeader(status)
	}))
	defer srv.Close()

	if _, err := NewBridgeSender(srv.URL, "short", "struk_digital"); err == nil {
		t.Fatal("a short token must be rejected")
	}
	b, err := NewBridgeSender(srv.URL+"/", "0123456789abcdef0123456789abcdef", "struk_digital")
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Send(context.Background(), Message{To: "+6281234567890", Body: "Kafe\nORD-1\nhttps://x/r"}); err != nil {
		t.Fatal(err)
	}
	if got.Auth != "Bearer 0123456789abcdef0123456789abcdef" || got.Body.Template != "struk_digital" || len(got.Body.Params) != 3 || got.Body.To != "+6281234567890" {
		t.Fatalf("unexpected request %+v", got)
	}
	status = http.StatusUnprocessableEntity
	if err := b.Send(context.Background(), Message{To: "+6281234567890", Body: "a"}); !errors.Is(err, ErrPermanent) {
		t.Fatalf("422 (not on WhatsApp) must be permanent, got %v", err)
	}
	status = http.StatusServiceUnavailable
	if err := b.Send(context.Background(), Message{To: "+6281234567890", Body: "a"}); err == nil || errors.Is(err, ErrPermanent) {
		t.Fatalf("503 (bridge not paired) must be retried, got %v", err)
	}
}
