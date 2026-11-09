package sms

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A message naming its own template must not go out under the configured receipt template.
func TestSendersUseTheMessageTemplate(t *testing.T) {
	var names []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Template any `json:"template"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch v := body.Template.(type) {
		case string:
			names = append(names, v)
		case map[string]any:
			names = append(names, v["name"].(string))
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	bridge, err := NewBridgeSender(srv.URL, "0123456789abcdef0123456789abcdef", "struk_digital")
	if err != nil {
		t.Fatal(err)
	}
	bridge.Client = srv.Client()
	for _, m := range []Message{{To: "+6281234567890", Body: "a"}, {To: "+6281234567890", Body: "a", Template: "tagihan_invoice"}} {
		if err := bridge.Send(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
	if len(names) != 2 || names[0] != "struk_digital" || names[1] != "tagihan_invoice" {
		t.Fatalf("unexpected templates: %v", names)
	}
}
