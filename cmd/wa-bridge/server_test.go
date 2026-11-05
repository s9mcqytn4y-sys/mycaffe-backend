package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeSender struct {
	ready bool
	err   error
	sent  []string
}

func (f *fakeSender) Ready() bool { return f.ready }
func (f *fakeSender) Send(_ context.Context, phone, text string) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, phone+"|"+text)
	return nil
}

const testToken = "0123456789abcdef0123456789abcdef"

func post(h http.Handler, auth, body string) int {
	req := httptest.NewRequest("POST", "/send", strings.NewReader(body))
	req.Header.Set("Authorization", auth)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w.Code
}

func TestSendHandler(t *testing.T) {
	f := &fakeSender{ready: true}
	h := newHandler(testToken, f)
	ok := `{"to":"+6281234567890","template":"struk_digital","params":["Kafe Uji","ORD-1","https://x/receipts/a"]}`

	if code := post(h, "Bearer wrong", ok); code != http.StatusUnauthorized {
		t.Fatalf("wrong token must be 401, got %d", code)
	}
	if code := post(h, "Bearer "+testToken, `{"to":"0812","template":"text","params":["hi"]}`); code != http.StatusBadRequest {
		t.Fatalf("non-E.164 number must be 400, got %d", code)
	}
	if code := post(h, "Bearer "+testToken, `{"to":"+6281234567890","template":"struk_digital","params":["a"]}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong parameter count must be 422, got %d", code)
	}
	if code := post(h, "Bearer "+testToken, ok); code != http.StatusNoContent {
		t.Fatalf("send must be 204, got %d", code)
	}
	want := "+6281234567890|Terima kasih sudah berbelanja di Kafe Uji. Struk digital pesanan ORD-1 bisa dilihat di https://x/receipts/a. Tautan berlaku 90 hari."
	if len(f.sent) != 1 || f.sent[0] != want {
		t.Fatalf("unexpected message %v", f.sent)
	}

	f.err = ErrNotOnWhatsApp
	if code := post(h, "Bearer "+testToken, ok); code != http.StatusUnprocessableEntity {
		t.Fatalf("a number without WhatsApp must be 422, got %d", code)
	}
	f.err = ErrNotReady
	if code := post(h, "Bearer "+testToken, ok); code != http.StatusServiceUnavailable {
		t.Fatalf("an unpaired bridge must be 503, got %d", code)
	}

	f.ready = false
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("healthz must be 503 until paired, got %d", w.Code)
	}
}
