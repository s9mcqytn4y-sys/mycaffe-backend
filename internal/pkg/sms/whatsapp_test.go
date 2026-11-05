package sms

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWhatsAppSender(t *testing.T) {
	var got struct {
		Path, Auth string
		Body       map[string]any
	}
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Path, got.Auth = r.URL.Path, r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&got.Body)
		w.WriteHeader(status)
	}))
	defer srv.Close()

	if _, err := NewWhatsAppSender("v21.0", "", "token", "struk_digital", "id"); err == nil {
		t.Fatal("a missing phone number id must be rejected")
	}
	w, err := NewWhatsAppSender("v21.0", "1234", "secret-token", "struk_digital", "id")
	if err != nil {
		t.Fatal(err)
	}
	w.BaseURL = srv.URL
	if err := w.Send(context.Background(), Message{To: "+6281234567890", Body: "Kafe Uji\nORD-1\nhttps://x/receipts/abc"}); err != nil {
		t.Fatal(err)
	}
	if got.Path != "/v21.0/1234/messages" || got.Auth != "Bearer secret-token" {
		t.Fatalf("unexpected request %s %s", got.Path, got.Auth)
	}
	tmpl := got.Body["template"].(map[string]any)
	params := tmpl["components"].([]any)[0].(map[string]any)["parameters"].([]any)
	if got.Body["to"] != "6281234567890" || tmpl["name"] != "struk_digital" || tmpl["language"].(map[string]any)["code"] != "id" ||
		len(params) != 3 || params[2].(map[string]any)["text"] != "https://x/receipts/abc" {
		t.Fatalf("unexpected payload %+v", got.Body)
	}

	status = http.StatusBadRequest
	if err := w.Send(context.Background(), Message{To: "+6281234567890", Body: "a"}); !errors.Is(err, ErrPermanent) {
		t.Fatalf("4xx must be permanent, got %v", err)
	}
	status = http.StatusTooManyRequests
	if err := w.Send(context.Background(), Message{To: "+6281234567890", Body: "a"}); err == nil || errors.Is(err, ErrPermanent) {
		t.Fatalf("429 must be transient, got %v", err)
	}
}
