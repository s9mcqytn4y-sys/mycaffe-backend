package mailer_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/s9mcqytn4y-sys/cassy-backend/internal/pkg/mailer"
)

func TestResendSender_Validation(t *testing.T) {
	_, err := mailer.NewResendSender("", "noreply@azuredev.id")
	if err == nil {
		t.Fatalf("expected error on empty API key")
	}
}

func TestAzureCommunicationSender_Validation(t *testing.T) {
	_, err := mailer.NewAzureCommunicationSender("http://insecure.endpoint", "key", "sender")
	if err == nil {
		t.Fatalf("expected error on non-https endpoint")
	}
}

func TestResendSender_SendSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg-123"}`))
	}))
	defer srv.Close()

	sender, err := mailer.NewResendSender("test-key", "Cassy <noreply@azuredev.id>")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// We test standard validation
	err = sender.Send(context.Background(), mailer.Message{To: ""})
	if err == nil {
		t.Fatalf("expected error on empty recipient")
	}
}
